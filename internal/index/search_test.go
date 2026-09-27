package index_test

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// This is the whole of M5 end to end, from a file on disk to a search result: a
// file the DM wrote, a sync, and a query typed as the language. Every other test
// in this milestone checks one link of that chain, and the chain is where a
// decision made in one place quietly fails to reach another — the secret text
// being extracted and then never indexed is exactly that kind of failure, and no
// unit test in either package would have noticed.
func TestSyncMakesTheVaultSearchable(t *testing.T) {
	t.Parallel()

	const canary = "IlithyaMarrowCanary"

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	writeVaultFile(t, vaultDir, "locations/rivergate.md", `---
title: Rivergate
aliases: [the toll town, Flussport]
tags: [location, hub]
type: location
visibility: players
---

A fortified town at the confluence of the [[Blackwater]].

> [!SECRET] The toll-collector's real name
> Captain Vell is actually **`+canary+`**, sworn to the Umbral Court.
`)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	campaign := syncer.Campaign().ID
	dm := domain.Principal{ID: "principal-dm", CampaignID: campaign, Role: domain.RoleDM}
	player := domain.Principal{ID: "principal-player", CampaignID: campaign, Role: domain.RolePlayer}

	tests := map[string]struct {
		input string
		who   domain.Principal
		want  []string
	}{
		// Two, and not one: the Drowned Hound links to `[[rivergate]]`, and a
		// wiki link's display text is indexed — so a page is findable by the names
		// it uses as well as the names it has. That is the same sentence a reader
		// would write in it, and a page that mentions a place is findable by that
		// place.
		"a word a page uses as well as one it has": {
			input: "rivergate", who: player,
			want: []string{"locations/rivergate", "locations/the-drowned-hound"},
		},
		"an alias": {
			input: "Flussport", who: player,
			want: []string{"locations/rivergate"},
		},
		"a tag as a filter": {
			input: "tag:hub", who: player,
			want: []string{"locations/rivergate"},
		},
		"the type as a filter, which the fixture vault has two of": {
			input: "type:location", who: player,
			want: []string{"locations/rivergate", "locations/the-drowned-hound"},
		},
		// Prose is findable, and this is the assertion that says so. It was
		// `want: nil` until access control landed, with a comment saying the
		// feature was missing in the safe direction; the word lives in the page's
		// body, the body is in the public index, and the public search's rows are
		// filtered by the read predicate — so a principal who may read the page
		// may find it by its prose, and one who may not gets nothing.
		"a word in the public body": {
			input: "confluence", who: player,
			want: []string{"locations/rivergate"},
		},
		// The secret is, for the DM, because which text is secret is a parsing
		// question and the parser already answers it.
		"the DM finds the secret by its content": {
			input: canary, who: dm,
			want: []string{"locations/rivergate"},
		},
		"and the player does not": {
			input: canary, who: player,
			want: nil,
		},
		"a player cannot reach the private index with a filter either": {
			input: "tag:hub " + canary, who: player,
			want: nil,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			hits, err := search.Run(ctx, syncer.Store(), campaign, tt.who, tt.input, search.DefaultLimit)
			if err != nil {
				t.Fatalf("searching for %q: %v", tt.input, err)
			}

			paths := make([]string, 0, len(hits))
			for _, hit := range hits {
				paths = append(paths, hit.Path)
			}

			if len(paths) != len(tt.want) {
				t.Fatalf("searching for %q returned %v, want %v", tt.input, paths, tt.want)
			}
			for i := range tt.want {
				if paths[i] != tt.want[i] {
					t.Errorf("hit %d is %q, want %q", i, paths[i], tt.want[i])
				}
			}
		})
	}
}

// A page whose file is gone is not findable, even though its row is still there
// and recoverable. A search that names an archived page is a page a player cannot
// open, and the error they get from opening it is a 404 that says the page does
// not exist, which is a different answer from "you may not see it".
func TestArchivedPagesAreNotSearchable(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")
	const file = "locations/old-rivergate.md"

	writeVaultFile(t, vaultDir, file,
		"---\ntitle: The Old Rivergate\nvisibility: players\n---\n\nA town that is not there.\n")

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("the first Sync: %v", err)
	}

	player := domain.Principal{ID: "principal-player", CampaignID: syncer.Campaign().ID, Role: domain.RolePlayer}
	if hits, err := search.Run(ctx, syncer.Store(), syncer.Campaign().ID, player,
		`"Old Rivergate"`, search.DefaultLimit); err != nil {
		t.Fatalf("searching before the archive: %v", err)
	} else if len(hits) == 0 {
		t.Fatal("the page is not findable before it is archived, so the assertion below proves nothing")
	}

	removeVaultFile(t, filepath.Join(vaultDir, file))
	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("the second Sync: %v", err)
	}
	if !slices.Contains(report.Archived, "locations/old-rivergate") {
		t.Fatalf("the sync did not report the page as archived: %+v", report)
	}

	hits, err := search.Run(ctx, syncer.Store(), syncer.Campaign().ID, player,
		`"Old Rivergate"`, search.DefaultLimit)
	if err != nil {
		t.Fatalf("searching after the archive: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("an archived page is still findable: %+v", hits)
	}

	// The row survives, which is the other half of the claim: the archive is
	// recoverable and only the findability went. GetPage will not return it,
	// because that is what an archive means to every read, and the report is how
	// a caller learns the row is still there.
	if report.Changed() == 0 {
		t.Error("the archive reported no change at all")
	}
}

// The settled check covers the search rows, so a page whose file has not changed
// is not rewritten -- and a page whose *index* has drifted is, even though
// nothing in the vault moved.
//
// The second half is the one that matters. An index that can be written but never
// compared rots in place, and the symptom is a search that quietly returns stale
// results for ever; the only defence is that a sync notices.
func TestSyncSettlesOnTheSearchRows(t *testing.T) {
	t.Parallel()

	ctx, syncer, _ := newSyncer(t)

	// The fixture vault, indexed once.
	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("the first Sync: %v", err)
	}

	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("the second Sync: %v", err)
	}
	if report.Changed() != 0 {
		t.Errorf("an unchanged vault was written to: %+v", report)
	}

	// Now damage one page's search rows behind the sync's back, the way a bug or
	// a hand-typed session would. Nothing in the vault moved, and every hash
	// still matches, so a check that only compared hashes would see nothing.
	page, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "locations/rivergate", store.AsDM(syncer.Campaign().ID))
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if deleteErr := syncer.Store().DeletePageIndex(ctx, page.ID); deleteErr != nil {
		t.Fatalf("DeletePageIndex: %v", deleteErr)
	}

	repaired, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("the third Sync: %v", err)
	}
	if repaired.Changed() == 0 {
		t.Error("a sync over a vault whose search rows were deleted wrote nothing: the index rots in place")
	}

	player := domain.Principal{ID: "principal-player", CampaignID: syncer.Campaign().ID, Role: domain.RolePlayer}
	hits, err := search.Run(ctx, syncer.Store(), syncer.Campaign().ID, player, "rivergate", search.DefaultLimit)
	if err != nil {
		t.Fatalf("searching: %v", err)
	}
	if len(hits) == 0 {
		t.Error("the page is not findable after the repair, so the row was deleted rather than rewritten")
	}
}

// The redaction, end to end, from a file on disk. This is the case that has been
// "a missing feature, in the safe direction" since M5, and it is a
// forbidden-substring assertion rather than a check that some hits came back: the
// failure a search can have is not returning a secret, it is *finding* one.
func TestThePublicIndexHoldsNoSecretWord(t *testing.T) {
	t.Parallel()

	const canary = "IlithyaMarrowCanary"

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	// A `players` page with a secret on it: every player may read the page, and
	// none of them may read the secret. This is the case the two-index split
	// exists for, and the one ADR 0009 uses as its example.
	writeVaultFile(t, vaultDir, "locations/rivergate.md", `---
title: Rivergate
visibility: players
---

A fortified town at the confluence of the [[Blackwater]]. The toll-collector is
known by another name entirely.

> [!SECRET] The toll-collector's real name
> Captain Vell is actually **`+canary+`**, sworn to the Umbral Court.
`)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	player := domain.Principal{ID: "principal-player", CampaignID: syncer.Campaign().ID, Role: domain.RolePlayer}
	dm := domain.Principal{ID: "principal-dm", CampaignID: syncer.Campaign().ID, Role: domain.RoleDM}

	// A player may read the page, so it comes back — and its prose is searchable.
	hits, err := search.Run(ctx, syncer.Store(), syncer.Campaign().ID, player, "confluence", search.DefaultLimit)
	if err != nil {
		t.Fatalf("searching for prose: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("searching for a word in the public body returned %d hits, want 1: %+v", len(hits), hits)
	}

	// The secret's word is findable by nobody through the **public** path, and
	// that is the assertion: `search.Run` fuses both indexes, so a hit the DM
	// gets there is the private index working. Asking Run about the public index
	// is asking the wrong question, so this asks the public index directly.
	store_ := syncer.Store()
	for who, principal := range map[string]domain.Principal{"a player": player, "the DM": dm} {
		for _, query := range []string{canary, "ilithyamarrowcanary", `"` + canary + `"`} {
			found, publicErr := store_.SearchPublic(ctx, syncer.Campaign().ID, principal, mustParse(t, query), search.DefaultLimit)
			if publicErr != nil {
				t.Fatalf("searching the public index for %q as %s: %v", query, who, publicErr)
			}
			for _, hit := range found {
				assertNoCanary(t, "the public index, "+who, hit)
			}
			if len(found) != 0 {
				t.Errorf("the public index found %+v for the secret as %s", found, who)
			}
		}
	}

	// And through the whole of `Run`, a player finds nothing — which is the other
	// half, because the private index is filtered too.
	for _, query := range []string{canary, "ilithyamarrowcanary"} {
		found, runErr := search.Run(ctx, store_, syncer.Campaign().ID, player, query, search.DefaultLimit)
		if runErr != nil {
			t.Fatalf("searching for %q as a player: %v", query, runErr)
		}
		if len(found) != 0 {
			t.Errorf("a player found the secret: %+v", found)
		}
	}

	// The DM finds it through the private index, which is what makes the assertions
	// above mean something: an index with no secret text in it would also pass them,
	// and so would a private index the private scope refused.
	found, err := search.Run(ctx, store_, syncer.Campaign().ID, dm, canary, search.DefaultLimit)
	if err != nil {
		t.Fatalf("searching for the secret as the DM: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("the DM cannot find their own secret by content, so the assertions above prove nothing")
	}
	if !strings.Contains(found[0].Snippet, canary) {
		t.Errorf("the DM's excerpt is %q, want it to contain the secret", found[0].Snippet)
	}
}

// mustParse parses a query for the assertions above, which are about who can see
// the result and not about the language.
func mustParse(t *testing.T, input string) search.Query {
	t.Helper()

	parsed, err := search.Parse(input)
	if err != nil {
		t.Fatalf("Parse(%q): %v", input, err)
	}
	return parsed
}

// assertNoCanary is a forbidden-substring assertion over every field a hit carries,
// because an excerpt is the field that leaks and a title is the field that would
// leak a private page's existence.
func assertNoCanary(t *testing.T, what string, hit search.Hit) {
	t.Helper()

	for field, value := range map[string]string{
		"path":    hit.Path,
		"title":   hit.Title,
		"snippet": hit.Snippet,
		"id":      hit.PageID,
	} {
		if strings.Contains(value, "IlithyaMarrowCanary") {
			t.Errorf("%s leaked the secret in the %s: %q", what, field, value)
		}
	}
}
