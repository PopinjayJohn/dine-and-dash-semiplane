package store_test

import (
	"context"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestReplaceLinksReplacesRatherThanAppends(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	source := mustCreatePage(t, s, c.ID)

	garros := page(c.ID)
	garros.Path = "npcs/garros-ironbar"
	target, upsertErr := s.UpsertPage(ctx, garros)
	if upsertErr != nil {
		t.Fatalf("UpsertPage: %v", upsertErr)
	}

	first := []domain.PageLink{
		{DstPath: "npcs/garros-ironbar", DstPageID: target.ID, Kind: domain.LinkKindEmbed},
		{DstPath: "locations/not-written-yet", Kind: domain.LinkKindLink},
	}
	if err := s.ReplaceLinks(ctx, source.ID, first); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	// Replacing with a shorter list is how a link the DM deleted from the
	// text leaves the graph. An append-only graph would keep it forever.
	second := []domain.PageLink{
		{DstPath: "npcs/garros-ironbar", DstPageID: target.ID, Kind: domain.LinkKindLink},
	}
	if err := s.ReplaceLinks(ctx, source.ID, second); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	links, linksErr := s.LinksFrom(ctx, source.ID)
	if linksErr != nil {
		t.Fatalf("LinksFrom: %v", linksErr)
	}
	if len(links) != 1 {
		t.Fatalf("the page has %d links, want 1: %+v", len(links), links)
	}
	if links[0].DstPath != "npcs/garros-ironbar" || links[0].Kind != domain.LinkKindLink {
		t.Errorf("the surviving link is %+v, want a plain link to npcs/garros-ironbar", links[0])
	}
}

func TestReplaceLinksWithNothingClearsTheGraph(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	source := mustCreatePage(t, s, c.ID)

	if err := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: "npcs/garros-ironbar", Kind: domain.LinkKindLink},
	}); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	// A page that no longer mentions anything has no links, and saying so by
	// passing an empty list is how the sync engine says it.
	if err := s.ReplaceLinks(ctx, source.ID, nil); err != nil {
		t.Fatalf("ReplaceLinks with nothing: %v", err)
	}

	links, err := s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("the page has %d links, want none", len(links))
	}
	if links == nil {
		t.Error("LinksFrom returned nil for a page with no links, want an empty slice")
	}
}

func TestReplaceLinksIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	source := mustCreatePage(t, s, c.ID)

	links := []domain.PageLink{
		{DstPath: "npcs/garros-ironbar", Kind: domain.LinkKindEmbed},
		{DstPath: "locations/rivergate", Kind: domain.LinkKindLink},
	}

	for range 2 {
		if err := s.ReplaceLinks(ctx, source.ID, links); err != nil {
			t.Fatalf("ReplaceLinks: %v", err)
		}
	}

	got, err := s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("running the same replacement twice left %d links, want 2", len(got))
	}
}

func TestReplaceLinksIsOrderedByDestination(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	source := mustCreatePage(t, s, c.ID)

	// Deliberately in the wrong order, with a duplicate path, so the ordering
	// and the primary key are both exercised.
	if err := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: "sessions/2026-02-14-dragon-heist", Kind: domain.LinkKindLink},
		{DstPath: "npcs/garros-ironbar", Kind: domain.LinkKindEmbed},
		{DstPath: "locations/rivergate", Kind: domain.LinkKindLink},
		{DstPath: "npcs/garros-ironbar", Kind: domain.LinkKindLink},
	}); err == nil {
		t.Error("ReplaceLinks accepted the same destination path twice: the graph has one edge per source and path")
	}

	if err := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: "sessions/2026-02-14-dragon-heist", Kind: domain.LinkKindLink},
		{DstPath: "npcs/garros-ironbar", Kind: domain.LinkKindEmbed},
		{DstPath: "locations/rivergate", Kind: domain.LinkKindLink},
	}); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	links, err := s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}

	want := []string{"locations/rivergate", "npcs/garros-ironbar", "sessions/2026-02-14-dragon-heist"}
	if len(links) != len(want) {
		t.Fatalf("the page has %d links, want %d", len(links), len(want))
	}
	for i, path := range want {
		if links[i].DstPath != path {
			t.Errorf("link %d goes to %q, want %q: the order is part of the contract", i, links[i].DstPath, path)
		}
	}
}

func TestBacklinksAndLinksToPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	town := mustCreatePage(t, s, c.ID)
	tavern := page(c.ID)
	tavern.Path = "locations/the-drowned-hound"
	tavernStore, upsertErr := s.UpsertPage(ctx, tavern)
	if upsertErr != nil {
		t.Fatalf("UpsertPage: %v", upsertErr)
	}

	// One source with two links: one to a page that exists, one to a page the
	// DM has not written yet.
	if err := s.ReplaceLinks(ctx, tavernStore.ID, []domain.PageLink{
		{DstPath: town.Path, DstPageID: town.ID, Kind: domain.LinkKindLink},
		{DstPath: "npcs/not-written-yet", Kind: domain.LinkKindEmbed},
	}); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	backlinks, err := s.Backlinks(ctx, town.ID)
	if err != nil {
		t.Fatalf("Backlinks: %v", err)
	}
	if len(backlinks) != 1 {
		t.Fatalf("the town has %d backlinks, want 1", len(backlinks))
	}
	if backlinks[0].SrcPageID != tavernStore.ID {
		t.Errorf("the backlink comes from %s, want %s", backlinks[0].SrcPageID, tavernStore.ID)
	}

	// The unresolved link is in the graph, and the path query finds it. This
	// is the case that matters: a DM writes [[the toll-collector]] long
	// before there is a page for it, and the wiki has to know.
	pending, err := s.LinksToPath(ctx, "npcs/not-written-yet")
	if err != nil {
		t.Fatalf("LinksToPath: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("the unwritten path has %d links, want 1", len(pending))
	}
	if pending[0].Resolved() {
		t.Errorf("the link to an unwritten path is marked resolved, and it has no destination page")
	}
	if pending[0].Kind != domain.LinkKindEmbed {
		t.Errorf("the link kind is %q, want %q", pending[0].Kind, domain.LinkKindEmbed)
	}
}

func TestReplaceLinksRefusesWhatDomainRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*domain.PageLink)
		wantErr string
	}{
		{
			name:    "a link with no destination is refused",
			mutate:  func(l *domain.PageLink) { l.DstPath = "" },
			wantErr: "link destination path is required",
		},
		{
			name:    "an unknown kind is refused",
			mutate:  func(l *domain.PageLink) { l.Kind = "mention" },
			wantErr: "is not one of link, embed",
		},
		{
			name: "a link that claims a different source page is refused",
			mutate: func(l *domain.PageLink) {
				l.SrcPageID = "some-other-page"
			},
			wantErr: "claims to come from page",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := newStore(t)
			c := mustCreateCampaign(t, s)
			source := mustCreatePage(t, s, c.ID)

			l := domain.PageLink{DstPath: "npcs/garros-ironbar", Kind: domain.LinkKindLink}
			tt.mutate(&l)

			assertErrorContains(t, s.ReplaceLinks(ctx, source.ID, []domain.PageLink{l}), tt.wantErr)

			// A refused replacement leaves the graph as it was, rather than
			// half-replaced.
			links, err := s.LinksFrom(ctx, source.ID)
			if err != nil {
				t.Fatalf("LinksFrom: %v", err)
			}
			if len(links) != 0 {
				t.Errorf("a refused replacement left %d links behind: %+v", len(links), links)
			}
		})
	}
}

// TestReplaceLinksIsAllOrNothing is the reason the whole replacement is one
// transaction: a graph with fewer edges than either the file or the previous
// state is a difference the next reindex cannot explain.
func TestReplaceLinksIsAllOrNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	source := mustCreatePage(t, s, c.ID)

	if err := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: "npcs/garros-ironbar", Kind: domain.LinkKindLink},
		{DstPath: "locations/rivergate", Kind: domain.LinkKindEmbed},
	}); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	// The second link has a destination page that does not exist, so the
	// foreign key refuses the insert and the whole replacement is undone.
	err := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: "npcs/not-written-yet", Kind: domain.LinkKindLink},
		{DstPath: "locations/does-not-exist", DstPageID: "no-such-page", Kind: domain.LinkKindLink},
	})
	if err == nil {
		t.Fatal("ReplaceLinks accepted a link to a page that does not exist")
	}

	links, err := s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("after a refused replacement the page has %d links, want the 2 it had before", len(links))
	}
	if links[0].DstPath != "locations/rivergate" || links[1].DstPath != "npcs/garros-ironbar" {
		t.Errorf("the links are %+v, want the two that were there before the refused replacement", links)
	}
}

func TestLinksFromAnUnknownPage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)

	links, err := s.LinksFrom(ctx, "no-such-page")
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("an unknown page has %d links, want none", len(links))
	}

	backlinks, err := s.Backlinks(ctx, "no-such-page")
	if err != nil {
		t.Fatalf("Backlinks: %v", err)
	}
	if len(backlinks) != 0 {
		t.Errorf("an unknown page has %d backlinks, want none", len(backlinks))
	}
}
