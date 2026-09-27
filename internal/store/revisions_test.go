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

func revision(pageID string) domain.PageRevision {
	return domain.PageRevision{
		PageID:      pageID,
		Markdown:    "---\ntitle: Rivergate\n---\n\nA fortified town.\n",
		ContentHash: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		Message:     "tolls are too high",
	}
}

func TestAppendRevisionNumbersFromOne(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	p := mustCreatePage(t, s, c.ID)

	// A page with no revisions has none, rather than a revision zero: a
	// revision zero would be a thing that never happened.
	none, err := s.ListRevisions(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a new page has %d revisions, want none", len(none))
	}

	for want := 1; want <= 3; want++ {
		stored, appendErr := s.AppendRevision(ctx, revision(p.ID))
		if appendErr != nil {
			t.Fatalf("AppendRevision: %v", appendErr)
		}
		if stored.Rev != want {
			t.Errorf("revision number = %d, want %d", stored.Rev, want)
		}
	}

	revisions, err := s.ListRevisions(ctx, p.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revisions) != 3 {
		t.Fatalf("the page has %d revisions, want 3", len(revisions))
	}
	for i, r := range revisions {
		if r.Rev != i+1 {
			t.Errorf("revision %d in the list is numbered %d, want %d: the order is part of the contract", i, r.Rev, i+1)
		}
		if r.PageID != p.ID {
			t.Errorf("revision %d belongs to page %s, want %s", i, r.PageID, p.ID)
		}
	}
}

func TestAppendRevisionKeepsWhatTheCallerSupplied(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// A standing clock, so the stamps are literals.
	s := newStoreWithClock(t, clock.NewFixed(testTime, 0))
	c := mustCreateCampaign(t, s)
	p := mustCreatePage(t, s, c.ID)

	want := revision(p.ID)
	want.ID = "revision-1"
	want.Rev = 7
	want.AuthorPrincipalID = "principal-1"
	want.CreatedAt = testTime.Add(-time.Hour)

	stored, err := s.AppendRevision(ctx, want)
	if err != nil {
		t.Fatalf("AppendRevision: %v", err)
	}
	if stored != want {
		t.Errorf("AppendRevision stored %+v, want %+v", stored, want)
	}

	read, err := s.GetRevision(ctx, p.ID, 7)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if read != want {
		t.Errorf("GetRevision returned %+v, want %+v", read, want)
	}
}

func TestAppendRevisionWithoutAnAuthorOrAMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	p := mustCreatePage(t, s, c.ID)

	// A file edited in Obsidian has no author as far as this application is
	// concerned, and an autosave has nothing to say about itself. Both are
	// real states, and both have to survive the round trip as absent rather
	// than as an empty string that looks like a value.
	r := revision(p.ID)
	r.AuthorPrincipalID = ""
	r.Message = ""

	stored, err := s.AppendRevision(ctx, r)
	if err != nil {
		t.Fatalf("AppendRevision: %v", err)
	}
	if stored.AuthorPrincipalID != "" || stored.Message != "" {
		t.Errorf("stored author %q and message %q, want both empty", stored.AuthorPrincipalID, stored.Message)
	}

	read, err := s.GetRevision(ctx, p.ID, stored.Rev)
	if err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
	if read != stored {
		t.Errorf("GetRevision returned %+v, want %+v", read, stored)
	}
}

func TestAppendRevisionRefusesWhatDomainRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*domain.PageRevision)
		wantErr string
	}{
		{name: "a valid revision", mutate: func(*domain.PageRevision) {}},
		{
			name:    "a revision with no page is refused",
			mutate:  func(r *domain.PageRevision) { r.PageID = "" },
			wantErr: "revision page ID is required",
		},
		{
			name:    "a revision with no hash is refused",
			mutate:  func(r *domain.PageRevision) { r.ContentHash = "" },
			wantErr: "revision content hash is required",
		},
		{
			name:    "a negative revision number is refused",
			mutate:  func(r *domain.PageRevision) { r.Rev = -1 },
			wantErr: "revision number: -1, must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := newStore(t)
			c := mustCreateCampaign(t, s)
			p := mustCreatePage(t, s, c.ID)

			r := revision(p.ID)
			tt.mutate(&r)

			_, err := s.AppendRevision(ctx, r)
			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("AppendRevision: %v", err)
			}
		})
	}
}

// TestAppendRevisionForAnUnknownPageIsRefused is the foreign key doing its job:
// a revision that belongs to no page would be a row nothing can ever reach.
func TestAppendRevisionForAnUnknownPageIsRefused(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)

	_, err := s.AppendRevision(ctx, revision("no-such-page"))
	if err == nil {
		t.Fatal("AppendRevision accepted a revision for a page that does not exist")
	}
	if errors.Is(err, store.ErrNotFound) {
		t.Errorf("AppendRevision returned %v, which reports not found: a missing row and a broken row are different problems", err)
	}
}

func TestGetRevisionNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	p := mustCreatePage(t, s, c.ID)

	if _, err := s.AppendRevision(ctx, revision(p.ID)); err != nil {
		t.Fatalf("AppendRevision: %v", err)
	}

	for _, rev := range []int{0, 2, 99} {
		if _, err := s.GetRevision(ctx, p.ID, rev); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetRevision(%d) returned %v, want an error matching ErrNotFound", rev, err)
		}
	}
}

// TestRevisionsAreNumberedPerPage is the reason the next number is read inside
// the write: a second page must not shift the first page's numbering.
func TestRevisionsAreNumberedPerPage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	first := mustCreatePage(t, s, c.ID)
	second := page(c.ID)
	second.Path = "npcs/garros-ironbar"
	secondOther, upsertErr := s.UpsertPage(ctx, second, store.AsDM(c.ID))
	if upsertErr != nil {
		t.Fatalf("UpsertPage: %v", upsertErr)
	}

	for range 2 {
		if _, err := s.AppendRevision(ctx, revision(first.ID)); err != nil {
			t.Fatalf("AppendRevision: %v", err)
		}
	}

	stored, err := s.AppendRevision(ctx, revision(secondOther.ID))
	if err != nil {
		t.Fatalf("AppendRevision: %v", err)
	}
	if stored.Rev != 1 {
		t.Errorf("the second page's first revision is numbered %d, want 1", stored.Rev)
	}
}
