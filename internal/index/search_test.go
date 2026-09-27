package index_test

import (
	"path/filepath"
	"slices"
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

	dm := domain.Principal{ID: "principal-dm", Role: domain.RoleDM}
	player := domain.Principal{ID: "principal-player", Role: domain.RolePlayer}
	campaign := syncer.Campaign().ID

	tests := map[string]struct {
		input string
		who   domain.Principal
		want  []string
	}{
		"the title": {
			input: "rivergate", who: player,
			want: []string{"locations/rivergate"},
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
		// The body is not in the public index yet, and this is the shape of that:
		// a missing feature, named in a test so that the day it is filled in
		// somebody sees which assertion has to move.
		"a word in the public body is not findable yet": {
			input: "confluence", who: player,
			want: nil,
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

	player := domain.Principal{ID: "principal-player", Role: domain.RolePlayer}
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

	player := domain.Principal{ID: "principal-player", Role: domain.RolePlayer}
	hits, err := search.Run(ctx, syncer.Store(), syncer.Campaign().ID, player, "rivergate", search.DefaultLimit)
	if err != nil {
		t.Fatalf("searching: %v", err)
	}
	if len(hits) == 0 {
		t.Error("the page is not findable after the repair, so the row was deleted rather than rewritten")
	}
}
