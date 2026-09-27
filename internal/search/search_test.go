package search_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
)

// TestRRFMergeOrdering is the named test in ADR 0009, and the corpus is the one
// the ADR asks for: exactly one secret, one public page that mentions it, and one
// dm-only page that mentions it. The corpus is small on purpose. The question is
// which of the three survive and in what order, and that does not get more
// interesting with more pages.
func TestRRFMergeOrdering(t *testing.T) {
	t.Parallel()

	// The three pages, and what each index says about them for the two-word query
	// "Ilithya Marrow".
	//
	//   - the town page is `players` and holds the secret, so it is in both
	//     lists: its title matches the public index and its secret matches the
	//     private one.
	//   - the session log is `dm-only` and mentions the name in its public body,
	//     so it is in the public list and not in the private one.
	//   - the Drowned Hound mentions neither and is in neither.
	public := []search.Hit{
		{PageID: "id-town", Path: "locations/rivergate", Title: "Rivergate", Rank: 1,
			Snippet: "A fortified town at the confluence of the [[Blackwater]]."},
		{PageID: "id-session", Path: "sessions/the-dragon-heist", Title: "The Dragon Heist", Rank: 2,
			Snippet: "The fourth is still out there."},
	}
	private := []search.Hit{
		{PageID: "id-town", Path: "locations/rivergate", Title: "Rivergate", Rank: 1,
			Snippet: "Captain Vell is actually Ilithya Marrow.", FromSecrets: true},
	}

	got := search.Fuse(public, private)

	if len(got) != 2 {
		t.Fatalf("the fusion returned %d hits, want 2: %+v", len(got), got)
	}

	// The town page first, because it is in both lists. That is the property
	// RRF is here for: 1/61 from the public list plus 1/61 from the private one
	// beats 1/62 from the public list alone, whichever page that was.
	if got[0].PageID != "id-town" {
		t.Errorf("the first hit is %q, want the page that matched in both indexes", got[0].PageID)
	}

	// And the scores say why, to the precision a test can check.
	wantTown := 1.0/(search.K+1) + 1.0/(search.K+1)
	wantSession := 1.0 / (search.K + 2)
	if math.Abs(got[0].Score-wantTown) > 1e-12 {
		t.Errorf("the first hit scores %v, want %v", got[0].Score, wantTown)
	}
	if math.Abs(got[1].Score-wantSession) > 1e-12 {
		t.Errorf("the second hit scores %v, want %v", got[1].Score, wantSession)
	}

	// The private excerpt won, because a match inside a secret is the more
	// specific answer — and because the page is in the private list at all, which
	// means the stricter scope admitted this principal.
	if !got[0].FromSecrets {
		t.Error("the fused hit is not marked as having matched a secret")
	}
	if got[0].Snippet != "Captain Vell is actually Ilithya Marrow." {
		t.Errorf("the fused excerpt is %q, want the one from the private index", got[0].Snippet)
	}
	// The page that only matched publicly is not marked, and keeps its own
	// excerpt.
	if got[1].FromSecrets {
		t.Error("the second hit is marked as having matched a secret")
	}
	if got[1].Snippet != "The fourth is still out there." {
		t.Errorf("the second excerpt is %q, want the one from the public index", got[1].Snippet)
	}

	// Ranks are renumbered, because rank 2 of two lists is not rank 2 of one.
	for i, hit := range got {
		if hit.Rank != i+1 {
			t.Errorf("hit %d has rank %d, want %d", i, hit.Rank, i+1)
		}
	}
}

// The fusion has to be total: every page that was in a list comes back exactly
// once, whatever happened to it on the way in.
func TestFuseKeepsEveryPageOnce(t *testing.T) {
	t.Parallel()

	public := []search.Hit{
		{PageID: "a", Rank: 1}, {PageID: "b", Rank: 2}, {PageID: "c", Rank: 3},
	}
	private := []search.Hit{
		{PageID: "c", Rank: 1, FromSecrets: true}, {PageID: "d", Rank: 2, FromSecrets: true},
	}

	got := search.Fuse(public, private)

	ids := make([]string, 0, len(got))
	for _, hit := range got {
		ids = append(ids, hit.PageID)
	}
	slices.Sort(ids)
	if want := []string{"a", "b", "c", "d"}; !slices.Equal(ids, want) {
		t.Errorf("the fusion returned %v, want each of %v exactly once", ids, want)
	}
}

// The cases a fusion can get wrong that are not about scores.
func TestFuse(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		lists [][]search.Hit
		want  []string
	}{
		"nothing at all": {
			lists: nil,
			want:  []string{},
		},
		"one empty list": {
			lists: [][]search.Hit{{}},
			want:  []string{},
		},
		"one list is its own order": {
			lists: [][]search.Hit{{{PageID: "a", Rank: 1}, {PageID: "b", Rank: 2}, {PageID: "c", Rank: 3}}},
			want:  []string{"a", "b", "c"},
		},
		"a list with no ranks is taken in its own order": {
			// Rather than treating every entry as rank 1, which would make the
			// whole list tie for first and the result order arbitrary.
			lists: [][]search.Hit{{{PageID: "a"}, {PageID: "b"}, {PageID: "c"}}},
			want:  []string{"a", "b", "c"},
		},
		"a page in both lists beats a first in one": {
			lists: [][]search.Hit{
				{{PageID: "first-only", Rank: 1}, {PageID: "both", Rank: 2}},
				{{PageID: "both", Rank: 1}},
			},
			want: []string{"both", "first-only"},
		},
		"three lists are added, not averaged": {
			lists: [][]search.Hit{
				{{PageID: "a", Rank: 5}, {PageID: "b", Rank: 6}},
				{{PageID: "a", Rank: 5}, {PageID: "b", Rank: 6}},
				{{PageID: "a", Rank: 5}, {PageID: "b", Rank: 6}},
			},
			// Both are in all three lists, so the sum decides and the tie-break
			// does: a was ahead in every one of them.
			want: []string{"a", "b"},
		},
		"a tie keeps the order the pages arrived in": {
			// Two pages at the same ranks in the same lists, which is a genuine
			// tie. The list order decides, not the id: two runs of one search
			// must not reorder, and the ids are not something a reader can see.
			lists: [][]search.Hit{{{PageID: "zzz", Rank: 1}, {PageID: "aaa", Rank: 2}}},
			want:  []string{"zzz", "aaa"},
		},
		"the same page twice in one list counts twice": {
			// A store that returned a page twice would otherwise have a page that
			// outranks everything, so this is the fusion's version of "do not
			// trust the input blindly" — the count is summed, which is visible.
			lists: [][]search.Hit{{{PageID: "a", Rank: 1}, {PageID: "b", Rank: 2}, {PageID: "a", Rank: 1}}},
			want:  []string{"a", "b"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := search.Fuse(tt.lists...)
			if len(got) != len(tt.want) {
				t.Fatalf("the fusion returned %d hits, want %d: %+v", len(got), len(tt.want), got)
			}
			for i, hit := range got {
				if hit.PageID != tt.want[i] {
					t.Errorf("hit %d is %q, want %q (whole list %+v)", i, hit.PageID, tt.want[i], idsOf(got))
				}
			}
		})
	}
}

// The K constant is a published default, and a test that pins its value is a test
// that says "this was a decision" rather than "this is whatever the code says".
// 60 is also the value every other RRF implementation uses, which is the whole
// reason for taking it.
func TestSmoothingConstantIsThePublishedDefault(t *testing.T) {
	t.Parallel()

	if search.K != 60 {
		t.Errorf("K = %d, want 60: it is the published RRF default and it is not tuned here", search.K)
	}

	// And the shape it gives: a hit ten ranks down keeps most of a first's
	// worth, which is the property the comment claims.
	top := 1.0 / (search.K + 1)
	deep := 1.0 / (search.K + 10)
	if ratio := deep / top; ratio < 0.8 || ratio > 0.95 {
		t.Errorf("rank 10 is worth %.3f of rank 1, want between 0.8 and 0.95", ratio)
	}
}

// Run is the entry point, and the tests below drive it through a fake Searcher.
// That is the point of the interface: the fusion, the limits and the empty-query
// rule are all testable with no database in the process, which is what makes them
// cheap enough to test exhaustively.
func TestRun(t *testing.T) {
	t.Parallel()

	corpus := []search.Hit{
		{PageID: "id-town", Path: "locations/rivergate", Title: "Rivergate", Rank: 1},
		{PageID: "id-hound", Path: "locations/the-drowned-hound", Title: "The Drowned Hound", Rank: 2},
	}
	secrets := []search.Hit{
		{PageID: "id-town", Path: "locations/rivergate", Title: "Rivergate", Rank: 1, FromSecrets: true},
	}

	tests := map[string]struct {
		input string
		limit int

		wantPublic  int
		wantSecrets int
		wantResults []string
	}{
		// The fusion, not either query, decides this list: the town page is in
		// both indexes and outranks the Hound, which is only in one.
		"a search runs both indexes": {
			input: "rivergate", limit: search.DefaultLimit,
			wantPublic: 1, wantSecrets: 1,
			wantResults: []string{"id-town", "id-hound"},
		},
		"an empty box runs neither": {
			input: "", limit: search.DefaultLimit,
			wantResults: []string{},
		},
		"whitespace is an empty box": {
			input: "  \t ", limit: search.DefaultLimit,
			wantResults: []string{},
		},
		"a filter with no value runs neither": {
			input: "tag:", limit: search.DefaultLimit,
			wantResults: []string{},
		},
		"a limit above the result count changes nothing": {
			input: "town", limit: 5,
			wantPublic: 1, wantSecrets: 1,
			wantResults: []string{"id-town", "id-hound"},
		},
		"no limit means the default": {
			input: "town", limit: 0,
			wantPublic: 1, wantSecrets: 1,
			wantResults: []string{"id-town", "id-hound"},
		},
		"more results than asked for are cut": {
			input: "town", limit: 1,
			wantPublic: 1, wantSecrets: 1,
			wantResults: []string{"id-town"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			backend := &fakeSearcher{public: corpus, secrets: secrets}
			got, err := search.Run(context.Background(), backend, "campaign-1", searchableDM(), tt.input, tt.limit)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if backend.publicCalls != tt.wantPublic {
				t.Errorf("the public index was searched %d times, want %d", backend.publicCalls, tt.wantPublic)
			}
			if backend.secretCalls != tt.wantSecrets {
				t.Errorf("the private index was searched %d times, want %d", backend.secretCalls, tt.wantSecrets)
			}
			if !slices.Equal(idsOf(got), tt.wantResults) {
				t.Errorf("Run returned %v, want %v", idsOf(got), tt.wantResults)
			}
		})
	}
}

// A query the language refuses is an error, not an empty result, and the error
// is the one the language gave rather than something the search invented.
func TestRunRefusesAQueryItCannotRead(t *testing.T) {
	t.Parallel()

	backend := &fakeSearcher{}

	_, err := search.Run(context.Background(), backend, "campaign-1", searchableDM(), "is:plyers", 0)
	if !errors.Is(err, search.ErrQuery) {
		t.Fatalf("Run returned %v, want an error matching ErrQuery", err)
	}
	if backend.publicCalls != 0 || backend.secretCalls != 0 {
		t.Error("Run queried an index with a query it had already refused")
	}
}

// A failure in either query is the caller's, with the index named. A search that
// quietly dropped the private half because it errored would answer a DM's question
// with a list that looks complete.
func TestRunReportsWhichIndexFailed(t *testing.T) {
	t.Parallel()

	t.Run("the public index", func(t *testing.T) {
		t.Parallel()

		backend := &fakeSearcher{publicErr: errors.New("the public index is unhappy")}
		_, err := search.Run(context.Background(), backend, "campaign-1", searchableDM(), "town", 0)
		if err == nil || !strings.Contains(err.Error(), "public") {
			t.Fatalf("Run returned %v, want an error naming the public index", err)
		}
	})

	t.Run("the private index", func(t *testing.T) {
		t.Parallel()

		backend := &fakeSearcher{secretsErr: errors.New("the private index is unhappy")}
		_, err := search.Run(context.Background(), backend, "campaign-1", searchableDM(), "town", 0)
		if err == nil || !strings.Contains(err.Error(), "private") {
			t.Fatalf("Run returned %v, want an error naming the private index", err)
		}
	})
}

func searchableDM() domain.Principal {
	return domain.Principal{ID: "principal-dm", Role: domain.RoleDM}
}

func idsOf(hits []search.Hit) []string {
	ids := make([]string, 0, len(hits))
	for _, hit := range hits {
		ids = append(ids, hit.PageID)
	}
	return ids
}

// fakeSearcher is the Searcher, answered from a fixture.
type fakeSearcher struct {
	public      []search.Hit
	secrets     []search.Hit
	publicErr   error
	secretsErr  error
	publicCalls int
	secretCalls int

	// asked records what the queries were told, so a test can check the parse
	// reached the store rather than being dropped.
	asked []search.Query
}

func (f *fakeSearcher) SearchPublic(_ context.Context, _ string, _ domain.Principal, q search.Query, _ int) ([]search.Hit, error) {
	f.publicCalls++
	f.asked = append(f.asked, q)
	if f.publicErr != nil {
		return nil, f.publicErr
	}
	return f.public, nil
}

func (f *fakeSearcher) SearchSecrets(_ context.Context, _ string, _ domain.Principal, q search.Query, _ int) ([]search.Hit, error) {
	f.secretCalls++
	f.asked = append(f.asked, q)
	if f.secretsErr != nil {
		return nil, f.secretsErr
	}
	return f.secrets, nil
}
