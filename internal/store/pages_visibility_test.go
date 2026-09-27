package store_test

import (
	"context"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// A page's audience is a column now, and a column is a promise. These are the
// two answers it can give: what the caller said, or `players` when the caller
// said nothing. There is no third, and in particular there is no "the value that
// was in the row last time" — a page that stops declaring an audience becomes a
// players' page, which is what the frontmatter default means and what the column
// default means, and a reader that disagreed with both would be a reader that
// quietly widens an audience.
func TestUpsertPageRecordsTheAudience(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		given domain.Visibility
		want  domain.Visibility
	}{
		"players":        {given: domain.VisibilityPlayers, want: domain.VisibilityPlayers},
		"dm-only":        {given: domain.VisibilityDMOnly, want: domain.VisibilityDMOnly},
		"dm-and-owner":   {given: domain.VisibilityDMAndOwner, want: domain.VisibilityDMAndOwner},
		"nothing at all": {given: "", want: domain.VisibilityPlayers},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := newStore(t)
			c := mustCreateCampaign(t, s)

			given := page(c.ID)
			given.Visibility = tt.given

			stored, err := s.UpsertPage(ctx, given, store.AsDM(c.ID))
			if err != nil {
				t.Fatalf("UpsertPage: %v", err)
			}
			if stored.Visibility != tt.want {
				t.Fatalf("a page written with no audience came back as %q, want %q",
					stored.Visibility, tt.want)
			}
			if stored.Audience() != tt.want {
				t.Errorf("Audience() = %q, want %q", stored.Audience(), tt.want)
			}

			// And the same answer comes back by path, by id, and in a list,
			// because a column that is only right on one read path is a column
			// somebody will use the other way round.
			byPath, err := s.GetPage(ctx, c.ID, stored.Path, store.AsDM(c.ID))
			if err != nil {
				t.Fatalf("GetPage: %v", err)
			}
			if byPath.Visibility != tt.want {
				t.Errorf("GetPage returned the audience %q, want %q", byPath.Visibility, tt.want)
			}

			byID, err := s.GetPageByID(ctx, stored.ID, store.AsDM(c.ID))
			if err != nil {
				t.Fatalf("GetPageByID: %v", err)
			}
			if byID.Visibility != tt.want {
				t.Errorf("GetPageByID returned the audience %q, want %q", byID.Visibility, tt.want)
			}

			pages, err := s.ListPages(ctx, c.ID, store.AsDM(c.ID))
			if err != nil {
				t.Fatalf("ListPages: %v", err)
			}
			if len(pages) != 1 || pages[0].Visibility != tt.want {
				t.Errorf("ListPages returned %+v, want the one page with the audience %q", pages, tt.want)
			}
		})
	}
}

// Changing a page's audience is an update to the row at that path, and it does
// not move the page. The id stays, because revisions and inbound links point at
// it, and a change of audience that changed the id would break every link to the
// page in the same write that made it safer.
func TestUpsertPageReplacesTheAudienceInPlace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	open := mustCreatePage(t, s, c.ID)
	if open.Visibility != domain.VisibilityPlayers {
		t.Fatalf("the fixture page starts as %q, want %q", open.Visibility, domain.VisibilityPlayers)
	}

	closed := page(c.ID)
	closed.Visibility = domain.VisibilityDMOnly
	updated, err := s.UpsertPage(ctx, closed, store.AsDM(c.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	if updated.ID != open.ID {
		t.Errorf("changing the audience gave id %q, want the row's id %q", updated.ID, open.ID)
	}
	if updated.Visibility != domain.VisibilityDMOnly {
		t.Errorf("the audience is %q, want %q", updated.Visibility, domain.VisibilityDMOnly)
	}
	if !updated.CreatedAt.Equal(open.CreatedAt) {
		t.Errorf("created_at moved from %v to %v: an audience change is an update",
			open.CreatedAt, updated.CreatedAt)
	}
}
