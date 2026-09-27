package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// These tests are in the package rather than beside it because they read the two
// FTS tables directly, and a test that reached the index through a search could
// not tell which half of it a term came from — which is the whole of ADR 0009.

// The three methods these cover are worth having because the index is *settled*,
// not merely *written*. An index that can be written but not compared is an index
// that rots in place, and nothing in a wiki notices that for months.

func TestReplacePageIndexWritesBothRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _, page := storeWithPage(t)

	const (
		publicText = "A fortified town at the confluence of the [[Blackwater]]."
		secretText = "Captain Vell is actually Ilithya Marrow."
	)

	if err := s.ReplacePageIndex(ctx, IndexEntry{
		PageID:     page.ID,
		Title:      page.Title,
		BodyPublic: publicText,
		SecretText: secretText,
	}); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	// The public row holds the public body and none of the secret, in any column.
	public := mustIndexRow(t, s, publicIndex, page.ID)
	if public["body"] != publicText {
		t.Errorf("the public index body is %q, want %q", public["body"], publicText)
	}
	for column, value := range public {
		if strings.Contains(value, "Ilithya") {
			t.Errorf("the secret reached the public index, in column %q: %q", column, value)
		}
	}

	// And the private row holds the secret.
	private := mustIndexRow(t, s, secretIndex, page.ID)
	if private["secret_text"] != secretText {
		t.Errorf("the private index text is %q, want %q", private["secret_text"], secretText)
	}
}

// A page with no secret is still in the private index, with nothing in it. The
// alternative is no row at all, which makes "which pages are indexed" and "which
// pages have secrets" two questions with two answers, and the second is the kind a
// DM asks mid-session.
func TestReplacePageIndexWritesAnEmptyPrivateRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _, page := storeWithPage(t)

	if err := s.ReplacePageIndex(ctx, IndexEntry{
		PageID:     page.ID,
		BodyPublic: "A fortified town.",
	}); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	row, found := indexRow(t, s, secretIndex, page.ID)
	if !found {
		t.Fatal("a page with no secret has no row in the private index")
	}
	if row["secret_text"] != "" {
		t.Errorf("the private row holds %q, want nothing", row["secret_text"])
	}
}

// The public body is recorded on the row as well as in the index, and the two are
// written together. A row whose column disagrees with its index row is a row
// nobody can audit, and the column is what a reader asks when they want to know
// what the public index was built from.
func TestReplacePageIndexRecordsThePublicBody(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _, page := storeWithPage(t)

	// Before anything is indexed the column is empty, and empty is the safe
	// value: a page nobody has worked out the public half of must not have its
	// whole body sitting in the column the public index is fed from.
	var recorded string
	if err := s.read.QueryRowContext(ctx, `SELECT body_public FROM pages WHERE id = ?`, page.ID).
		Scan(&recorded); err != nil {
		t.Fatalf("reading body_public: %v", err)
	}
	if recorded != "" {
		t.Errorf("body_public is %q before anything was indexed, want the empty string", recorded)
	}

	const publicText = "A fortified town."
	if err := s.ReplacePageIndex(ctx, IndexEntry{PageID: page.ID, BodyPublic: publicText}); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	if err := s.read.QueryRowContext(ctx, `SELECT body_public FROM pages WHERE id = ?`, page.ID).
		Scan(&recorded); err != nil {
		t.Fatalf("reading body_public: %v", err)
	}
	if recorded != publicText {
		t.Errorf("body_public is %q, want %q", recorded, publicText)
	}
}

// The settled check is why PageIndexMatches exists rather than a getter, and
// these are the answers it has to give.
func TestPageIndexMatches(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, page := storeWithPage(t)

	entry := IndexEntry{
		PageID:     page.ID,
		Title:      "Rivergate",
		Aliases:    []string{"the toll town", "Flussport"},
		Tags:       []string{"location", "hub"},
		Kind:       "location",
		BodyPublic: "A fortified town.",
		SecretText: "Captain Vell is Ilithya Marrow.",
	}
	if err := s.ReplacePageIndex(ctx, entry); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	tests := map[string]struct {
		mutate func(*IndexEntry)
		want   bool
	}{
		"the entry that was written matches":   {mutate: func(*IndexEntry) {}, want: true},
		"a changed title does not match":       {mutate: func(e *IndexEntry) { e.Title = "Rivergate Bridge" }},
		"a changed alias does not match":       {mutate: func(e *IndexEntry) { e.Aliases = []string{"the bridge town"} }},
		"a removed tag does not match":         {mutate: func(e *IndexEntry) { e.Tags = []string{"location"} }},
		"a changed kind does not match":        {mutate: func(e *IndexEntry) { e.Kind = "npc" }},
		"a changed public body does not match": {mutate: func(e *IndexEntry) { e.BodyPublic = "A fortified town. Recently." }},
		"a changed secret does not match":      {mutate: func(e *IndexEntry) { e.SecretText = "Something else." }},
		"an emptied secret does not match":     {mutate: func(e *IndexEntry) { e.SecretText = "" }},
		"a changed page does not match":        {mutate: func(e *IndexEntry) { e.PageID = "some other page" }},
		// The joined form is what is compared, so a tag holding a space and two
		// tags that spell it are the same row. Reporting a difference there
		// would make every page with a multi-word tag look unsettled for ever.
		"one tag holding a space is the same row": {
			mutate: func(e *IndexEntry) { e.Tags = []string{"location hub"} }, want: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			probe := entry
			tt.mutate(&probe)

			got, err := s.PageIndexMatches(ctx, probe)
			if err != nil {
				t.Fatalf("PageIndexMatches: %v", err)
			}
			if got != tt.want {
				t.Errorf("PageIndexMatches said %t, want %t", got, tt.want)
			}
		})
	}

	// A page the index has never heard of is not settled, and neither is one
	// whose rows have been taken away.
	other := domain.Page{
		CampaignID: campaign.ID,
		Path:       "npcs/garros-ironbar",
		Title:      "Garros Ironbar",
		Type:       domain.PageTypeNPC,
		Body:       "A dwarf in irons.",
		// The hash is checked by the store and nothing else, so a fixed string is
		// enough; a test that computed one would be testing sha256.
		ContentHash: "hash-of-garros",
	}
	stored, err := s.UpsertPage(ctx, other)
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	unindexed := entry
	unindexed.PageID = stored.ID
	unindexed.Title = "Garros Ironbar"
	unindexed.Aliases = nil
	unindexed.Tags = nil
	unindexed.Kind = "npc"
	unindexed.BodyPublic = ""
	unindexed.SecretText = ""

	matched, err := s.PageIndexMatches(ctx, unindexed)
	if err != nil {
		t.Fatalf("PageIndexMatches for an unindexed page: %v", err)
	}
	if matched {
		t.Error("a page that was never indexed reports a matching index row")
	}
}

// Writing the same entry twice settles, and a page whose rows were taken away is
// not settled. Those are the sync engine's two reasons to write.
func TestPageIndexMatchesIsStableAndNoticesDeletion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _, page := storeWithPage(t)

	entry := IndexEntry{PageID: page.ID, Title: "Rivergate", BodyPublic: "A town."}

	for range 2 {
		if err := s.ReplacePageIndex(ctx, entry); err != nil {
			t.Fatalf("ReplacePageIndex: %v", err)
		}
		matched, err := s.PageIndexMatches(ctx, entry)
		if err != nil {
			t.Fatalf("PageIndexMatches: %v", err)
		}
		if !matched {
			t.Fatal("writing the same entry twice did not settle")
		}
	}

	if err := s.DeletePageIndex(ctx, page.ID); err != nil {
		t.Fatalf("DeletePageIndex: %v", err)
	}

	matched, err := s.PageIndexMatches(ctx, entry)
	if err != nil {
		t.Fatalf("PageIndexMatches after deletion: %v", err)
	}
	if matched {
		t.Error("a page whose index rows were deleted still reports a match")
	}

	// The rows really are gone, in both indexes rather than one of them.
	for _, table := range []string{publicIndex, secretIndex} {
		if _, found := indexRow(t, s, table, page.ID); found {
			t.Errorf("page %s is still in %s after DeletePageIndex", page.ID, table)
		}
	}
}

// An entry with no page is refused rather than written against a row that is not
// there. A blank page id in an INSERT is a row about nothing, and it is the kind
// of row a later `DELETE FROM pages_fts WHERE page_id = ”` would clear.
func TestReplacePageIndexNeedsAPage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)

	if err := s.ReplacePageIndex(ctx, IndexEntry{Title: "Orphan"}); err == nil {
		t.Error("an index entry with no page was accepted by ReplacePageIndex")
	}
	if _, err := s.PageIndexMatches(ctx, IndexEntry{}); err == nil {
		t.Error("an index entry with no page was accepted by PageIndexMatches")
	}
}

// The tokenizer is part of the migration, and this is the assertion that keeps
// it there. `unicode61 remove_diacritics 2` is what makes `Rivergate` find
// `Rivergåte`: a DM typing an accented name is not a failed search, and the
// failure is invisible, because the answer is an empty result set that looks the
// same as "no page mentions that".
//
// It is tested by matching rather than by reading the schema, because the
// behaviour is the thing the clause was written for and a test that read the
// DDL back would pass against a database the table was never built in.
func TestSearchIndexTokenizerFoldsDiacritics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, _, page := storeWithPage(t)

	if err := s.ReplacePageIndex(ctx, IndexEntry{
		PageID:     page.ID,
		Title:      "Rivergåte",
		BodyPublic: "A fortified town on the Thörn.",
	}); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	for _, match := range []string{"rivergate", `"rivergåte"`, "thörn", `"fortified town"`} {
		var found string
		err := s.read.QueryRowContext(ctx,
			`SELECT page_id FROM pages_fts WHERE pages_fts MATCH ?`, match).Scan(&found)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				t.Errorf("MATCH %s found nothing: the tokenizer does not fold the way the migration says", match)
				continue
			}
			t.Fatalf("MATCH %s: %v", match, err)
		}
		if found != page.ID {
			t.Errorf("MATCH %s found page %s, want %s", match, found, page.ID)
		}
	}

	// A phrase is a phrase: the two words have to be adjacent, in that order, or
	// `"town fortified"` is a search for a thing nobody wrote. The query builder
	// relies on this, and so does a DM who quoted two words to be precise.
	var found string
	err := s.read.QueryRowContext(ctx,
		`SELECT page_id FROM pages_fts WHERE pages_fts MATCH ?`, `"town fortified"`).Scan(&found)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf(`MATCH "town fortified" returned %q and %v, want no rows: a phrase is not a bag of words`, found, err)
	}
}

// Fixtures.

// newTestStore opens a migrated store on a temporary file.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(context.Background(), t.TempDir()+"/campaigns.db", Options{
		Clock: fixedClock(),
		IDGen: sequenceIDs(),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("closing the store: %v", closeErr)
		}
	})

	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return s
}

// storeWithPage is a store with one campaign and one page in it, which is what
// every test here needs before it has anything to index.
func storeWithPage(t *testing.T) (*Store, domain.Campaign, domain.Page) {
	t.Helper()

	ctx := context.Background()
	s := newTestStore(t)

	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	page, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		Frontmatter: "title: Rivergate\n",
		Body:        "A fortified town at the confluence of the [[Blackwater]].\n",
		// The hash is checked by the store and nothing else, so a fixed string
		// is enough here; a test that computed one would be testing sha256.
		ContentHash: "hash-of-rivergate",
	})
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	return s, campaign, page
}

// indexRow reads one page's row out of one FTS table, and reports whether it was
// there. The table name is spliced into the SQL because every caller passes one
// of the two constants in this package, which is the same bargain the store's own
// index queries make.
func indexRow(t *testing.T, s *Store, table, pageID string) (map[string]string, bool) {
	t.Helper()

	columns := indexColumns[table]

	row := s.read.QueryRowContext(context.Background(),
		`SELECT `+strings.Join(columns, ", ")+` FROM `+table+` WHERE page_id = ?`, pageID)

	scanned := make([]string, len(columns))
	destinations := make([]any, len(columns))
	for i := range scanned {
		destinations[i] = &scanned[i]
	}

	if err := row.Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		t.Fatalf("reading the %s row for page %s: %v", table, pageID, err)
	}

	values := make(map[string]string, len(columns))
	for i, column := range columns {
		values[column] = scanned[i]
	}
	return values, true
}

// mustIndexRow is indexRow for the cases where the row has to be there.
func mustIndexRow(t *testing.T, s *Store, table, pageID string) map[string]string {
	t.Helper()

	row, found := indexRow(t, s, table, pageID)
	if !found {
		t.Fatalf("page %s has no row in %s", pageID, table)
	}
	return row
}
