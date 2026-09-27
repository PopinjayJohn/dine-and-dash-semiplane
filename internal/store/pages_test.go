package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

func TestUpsertPageStoresEveryField(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStoreWithClock(t, clock.NewFixed(testTime, 0))
	c := mustCreateCampaign(t, s)

	stored, err := s.UpsertPage(ctx, page(c.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	// Everything a reindex has to reproduce: the frontmatter with a key this
	// application does not understand, the body, the hash of the whole file
	// and the renderer the row was built against.
	want := page(c.ID)
	want.ID = stored.ID
	want.CreatedAt = testTime
	want.UpdatedAt = testTime
	// An audience the caller left out comes back as `players`, filled in the
	// same way a blank id and blank timestamps are. The value is the column's
	// default and the frontmatter's default, and returning the row as stored is
	// worth more than returning the row as asked for.
	want.Visibility = domain.VisibilityPlayers

	if stored != want {
		t.Errorf("UpsertPage stored %+v, want %+v", stored, want)
	}
	if stored.Frontmatter != want.Frontmatter {
		t.Errorf("frontmatter = %q, want %q: a key the application does not understand must survive a round trip", stored.Frontmatter, want.Frontmatter)
	}
	if stored.IsDeleted {
		t.Error("a page that was never archived reads back as deleted")
	}

	read, err := s.GetPage(ctx, c.ID, "locations/rivergate")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if read != stored {
		t.Errorf("GetPage returned %+v, want %+v", read, stored)
	}
}

func TestUpsertPageRefusesWhatDomainRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*domain.Page)
		wantErr string
	}{
		{name: "a valid page", mutate: func(*domain.Page) {}},
		{
			name:    "an empty path is refused",
			mutate:  func(p *domain.Page) { p.Path = "" },
			wantErr: "page path is required",
		},
		{
			name:    "an empty title is refused",
			mutate:  func(p *domain.Page) { p.Title = "" },
			wantErr: "page title is required",
		},
		{
			name:    "a missing content hash is refused",
			mutate:  func(p *domain.Page) { p.ContentHash = "" },
			wantErr: "page content hash is required",
		},
		{
			name:    "a page with no campaign is refused",
			mutate:  func(p *domain.Page) { p.CampaignID = "" },
			wantErr: "page campaign ID is required",
		},
		{
			// A fourth audience is refused rather than defaulted. A level the
			// application invents at write time is a level the read predicate
			// does not know, and a page nobody can be shown is better than a
			// page everybody can.
			name:    "an invented visibility level is refused",
			mutate:  func(p *domain.Page) { p.Visibility = domain.Visibility("everyone") },
			wantErr: "page visibility",
		},
		{
			name:    "a misspelt visibility level is refused",
			mutate:  func(p *domain.Page) { p.Visibility = domain.Visibility("plyers") },
			wantErr: "page visibility",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := newStore(t)
			c := mustCreateCampaign(t, s)

			p := page(c.ID)
			tt.mutate(&p)

			_, err := s.UpsertPage(ctx, p)
			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("UpsertPage: %v", err)
			}
		})
	}
}

func TestUpsertPageReplacesTheRowAtTheSamePath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	first := mustCreatePage(t, s, c.ID)

	edited := first
	edited.Title = "Rivergate, before the flood"
	edited.Type = domain.PageTypeNote
	edited.Frontmatter = "title: Rivergate, before the flood\ndm_notes: rewritten\n"
	edited.Body = "A fortified town, half under water.\n"
	edited.ContentHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	edited.RendererVersion = 2

	// A different id for a path that already exists: the path is the
	// identity, so the stored id is the one the revisions and the links
	// already point at.
	edited.ID = "a-different-id"

	// A zero timestamp is how the sync engine asks for the store to stamp the
	// write, which is what a reindex does: it has a file, not a moment.
	edited.UpdatedAt = time.Time{}

	second, err := s.UpsertPage(ctx, edited)
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("id = %q, want the id already in use %q: revisions and links point at it", second.ID, first.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at = %v, want it unchanged at %v", second.CreatedAt, first.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("updated_at = %v, want something after %v", second.UpdatedAt, first.UpdatedAt)
	}
	if second.Title != edited.Title || second.Body != edited.Body || second.ContentHash != edited.ContentHash {
		t.Errorf("UpsertPage stored %+v, want the edited content", second)
	}
	if second.RendererVersion != 2 {
		t.Errorf("renderer_version = %d, want 2", second.RendererVersion)
	}

	// One row, not two: a reindex that inserted a second row for the same
	// path would make the page tree depend on which row came back first.
	pages, err := s.ListPages(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 1 {
		t.Errorf("the campaign has %d pages, want 1", len(pages))
	}
}

func TestUpsertPageIsIdempotentForUnchangedContent(t *testing.T) {
	t.Parallel()

	s := newStore(t)
	c := mustCreateCampaign(t, s)

	first := mustCreatePage(t, s, c.ID)
	again := mustCreatePage(t, s, c.ID)

	if again.ID != first.ID {
		t.Errorf("id = %q, want the id from the first write %q", again.ID, first.ID)
	}
	if !again.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("created_at = %v, want it unchanged at %v", again.CreatedAt, first.CreatedAt)
	}
	if again.ContentHash != first.ContentHash {
		t.Errorf("content hash = %q, want %q", again.ContentHash, first.ContentHash)
	}
}

func TestDeletePageArchivesRatherThanRemoves(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	p := mustCreatePage(t, s, c.ID)

	if err := s.DeletePage(ctx, p.ID); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	if _, err := s.GetPage(ctx, c.ID, p.Path); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetPage after archiving returned %v, want an error matching ErrNotFound", err)
	}
	if _, err := s.GetPageByID(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetPageByID after archiving returned %v, want an error matching ErrNotFound", err)
	}

	pages, err := s.ListPages(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("ListPages returned %d pages after archiving the only one", len(pages))
	}

	// The row is still there, which is what makes an archive recoverable: the
	// same path written again keeps the original creation time and id.
	restored, err := s.UpsertPage(ctx, page(c.ID))
	if err != nil {
		t.Fatalf("UpsertPage after archiving: %v", err)
	}
	if restored.ID != p.ID {
		t.Errorf("id = %q, want the archived row's id %q: archiving deleted the row", restored.ID, p.ID)
	}
	if !restored.CreatedAt.Equal(p.CreatedAt) {
		t.Errorf("created_at = %v, want the archived row's %v", restored.CreatedAt, p.CreatedAt)
	}
}

func TestDeletePageNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	p := mustCreatePage(t, s, c.ID)

	if err := s.DeletePage(ctx, p.ID); err != nil {
		t.Fatalf("the first DeletePage: %v", err)
	}

	// Archiving twice is a no-op, and saying so is more useful than claiming
	// it worked: the page is already gone from every read.
	if err := s.DeletePage(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("archiving an archived page returned %v, want an error matching ErrNotFound", err)
	}
	if err := s.DeletePage(ctx, "no-such-page"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("archiving an unknown page returned %v, want an error matching ErrNotFound", err)
	}
}

func TestListPagesIsOrderedByPath(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	for _, path := range []string{"sessions/2026-02-14-dragon-heist", "characters/aria", "locations/rivergate"} {
		p := page(c.ID)
		p.Path = path
		if _, err := s.UpsertPage(ctx, p); err != nil {
			t.Fatalf("UpsertPage(%q): %v", path, err)
		}
	}

	pages, err := s.ListPages(ctx, c.ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}

	want := []string{"characters/aria", "locations/rivergate", "sessions/2026-02-14-dragon-heist"}
	if len(pages) != len(want) {
		t.Fatalf("ListPages returned %d pages, want %d", len(pages), len(want))
	}
	for i, path := range want {
		if pages[i].Path != path {
			t.Errorf("page %d is %q, want %q: the order is part of the contract", i, pages[i].Path, path)
		}
	}
}

func TestPagesOfOneCampaignDoNotLeakIntoAnother(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)

	first, err := domain.NewSlug("blackwater")
	if err != nil {
		t.Fatalf("the test fixture has an invalid slug: %v", err)
	}
	second, err := domain.NewSlug("rivergate")
	if err != nil {
		t.Fatalf("the test fixture has an invalid slug: %v", err)
	}

	one := mustCreate(t, s, first)
	two := mustCreate(t, s, second)

	mustCreatePage(t, s, one.ID)
	mustCreatePage(t, s, two.ID)

	onePages, err := s.ListPages(ctx, one.ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(onePages) != 1 {
		t.Errorf("the first campaign has %d pages, want 1", len(onePages))
	}

	for _, p := range onePages {
		if p.CampaignID != one.ID {
			t.Errorf("a page of campaign %s came back from campaign %s", p.CampaignID, one.ID)
		}
	}

	// The same path in two campaigns is two pages, not one.
	if onePages[0].ID == mustCreatePage(t, s, two.ID).ID {
		t.Error("the same path in two campaigns produced one row")
	}
}

func mustCreate(t *testing.T, s *store.Store, slug domain.Slug) domain.Campaign {
	t.Helper()

	c, err := s.CreateCampaign(context.Background(), domain.Campaign{
		Slug:     slug,
		Name:     slug.String(),
		VaultDir: "vault/" + slug.String(),
	})
	if err != nil {
		t.Fatalf("CreateCampaign(%q): %v", slug, err)
	}
	return c
}
