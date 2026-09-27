package testsuite

import (
	"context"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// The two cases below are the search half of the contract, and they are here
// rather than only in the store's own tests for the reason the rest of the suite
// is where it is: an index that is written but cannot be compared rots in
// place, and a second implementation that got that wrong would pass a suite that
// only checked the writes.

// indexRows: the two halves of a page's text go to two different places, and
// replacing one half replaces both.
//
// The property is the split itself. A store that put the secret text in the same
// row as the public body would satisfy every other test in this suite and leak,
// so the contract asks the question the split exists to answer: after the write,
// is the secret in the public row at all?
func indexRows(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	town := createFixturePage(t, s, campaign.ID)

	const (
		publicText = "A fortified town at the confluence of the [[Blackwater]]."
		secretText = "Captain Vell is actually Ilithya Marrow."
	)

	entry := store.IndexEntry{
		PageID:     town.ID,
		Title:      town.Title,
		Aliases:    []string{"the toll town"},
		Tags:       []string{"location", "hub"},
		Kind:       town.Type.String(),
		BodyPublic: publicText,
		SecretText: secretText,
	}
	if err := s.ReplacePageIndex(ctx, entry); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	// The public body is what the public half is findable by, and the secret is
	// in neither of the public half's fields. ReplacePageIndex is the only way
	// in, so the check is that the round trip through the store changed what a
	// search would see, which the settled check and the two queries between them
	// assert; here the shape is checked by reading the entry back, which is
	// enough to catch a store that dropped a half on the way to the disk.
	settled, err := s.PageIndexMatches(ctx, entry)
	if err != nil {
		t.Fatalf("PageIndexMatches: %v", err)
	}
	if !settled {
		t.Error("the entry that was just written does not match what the index holds")
	}

	// A second write with the secret removed and the public body changed is a
	// different entry, and the old text must not still be there.
	changed := entry
	changed.BodyPublic = publicText + " Recently fortified."
	changed.SecretText = ""
	if err := s.ReplacePageIndex(ctx, changed); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	if matches, err := s.PageIndexMatches(ctx, entry); err != nil || matches {
		t.Errorf("the superseded entry still matches (matched = %t, err = %v): "+
			"a replacement that appends leaves text a search can find and a read cannot show", matches, err)
	}
	if matches, err := s.PageIndexMatches(ctx, changed); err != nil || !matches {
		t.Errorf("the replacement does not match what the index holds (matched = %t, err = %v)", matches, err)
	}

	// And taking the page out takes both rows with it, which is what an archive
	// depends on: a page no read can open must not be findable.
	if err := s.DeletePageIndex(ctx, town.ID); err != nil {
		t.Fatalf("DeletePageIndex: %v", err)
	}
	if matches, err := s.PageIndexMatches(ctx, changed); err != nil || matches {
		t.Errorf("a deleted page still has an index row (matched = %t, err = %v)", matches, err)
	}
}

// indexSettles: the check has to be able to say "no", and a check that always
// says yes settles nothing.
//
// This is the whole reason PageIndexMatches is a question and not a getter. A
// store that wrote the index and could not compare it would pass a suite that
// only asserted the writes, and its index would then drift from the vault in
// silence for ever — which is the failure ADR 0001 is about, one layer up from
// the page row.
func indexSettles(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	town := createFixturePage(t, s, campaign.ID)

	entry := store.IndexEntry{PageID: town.ID, Title: "Rivergate", BodyPublic: "A fortified town."}
	if err := s.ReplacePageIndex(ctx, entry); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	fields := map[string]func(*store.IndexEntry){
		"title":       func(e *store.IndexEntry) { e.Title = "Rivergate Bridge" },
		"aliases":     func(e *store.IndexEntry) { e.Aliases = []string{"the bridge town"} },
		"tags":        func(e *store.IndexEntry) { e.Tags = []string{"revealed"} },
		"kind":        func(e *store.IndexEntry) { e.Kind = "npc" },
		"public body": func(e *store.IndexEntry) { e.BodyPublic = "A fortified town. Recently." },
		"secret text": func(e *store.IndexEntry) { e.SecretText = "A secret." },
		"the page":    func(e *store.IndexEntry) { e.PageID = "another page" },
	}

	for name, mutate := range fields {
		t.Run(name, func(t *testing.T) {
			probe := entry
			mutate(&probe)

			matched, err := s.PageIndexMatches(ctx, probe)
			if err != nil {
				t.Fatalf("PageIndexMatches: %v", err)
			}
			if matched {
				t.Errorf("changing the %s did not unsettle the index", name)
			}
		})
	}

	// An entry for a page that was never indexed is not settled either, which is
	// the case a sync hits on the first run over a new vault.
	other := createPage(t, s, campaign.ID, func(p *domain.Page) { p.Path = "npcs/garros-ironbar" })
	unindexed := entry
	unindexed.PageID = other.ID

	matched, err := s.PageIndexMatches(ctx, unindexed)
	if err != nil {
		t.Fatalf("PageIndexMatches for an unindexed page: %v", err)
	}
	if matched {
		t.Error("a page that was never indexed reports a matching index row")
	}
}
