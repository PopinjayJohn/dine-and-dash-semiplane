package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

func TestCreateCampaignFillsInWhatTheCallerLeftBlank(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// A clock that stands still, so the stamps are literals.
	s := newStoreWithClock(t, clock.NewFixed(testTime, 0))

	stored, err := s.CreateCampaign(ctx, campaign())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	if stored.ID != "id-1" {
		t.Errorf("id = %q, want the first id from the generator (%q)", stored.ID, "id-1")
	}
	if stored.CreatedAt != testTime {
		t.Errorf("created_at = %v, want the clock's first instant %v", stored.CreatedAt, testTime)
	}
	if stored.UpdatedAt != testTime {
		t.Errorf("updated_at = %v, want the clock's first instant %v", stored.UpdatedAt, testTime)
	}
	if stored.System != domain.DefaultSystem {
		t.Errorf("system = %q, want the default %q", stored.System, domain.DefaultSystem)
	}

	// And the row reads back exactly as it was returned, timestamps
	// included. A store that returned the argument rather than the row would
	// pass a comparison against the argument and fail this one.
	read, err := s.GetCampaign(ctx, stored.ID)
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	if read != stored {
		t.Errorf("GetCampaign returned %+v, want %+v", read, stored)
	}
}

func TestCreateCampaignRefusesWhatDomainRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*domain.Campaign)
		wantErr string
	}{
		{name: "a valid campaign", mutate: func(*domain.Campaign) {}},
		{
			name:    "an invalid slug is refused",
			mutate:  func(c *domain.Campaign) { c.Slug = "../../etc" },
			wantErr: "campaign slug",
		},
		{
			name:    "an empty name is refused",
			mutate:  func(c *domain.Campaign) { c.Name = "" },
			wantErr: "campaign name is required",
		},
		{
			name:    "an empty vault directory is refused",
			mutate:  func(c *domain.Campaign) { c.VaultDir = "" },
			wantErr: "campaign vault directory is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := campaign()
			tt.mutate(&c)

			stored, err := newStore(t).CreateCampaign(context.Background(), c)
			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("CreateCampaign: %v", err)
			}
			if stored.Slug != c.Slug {
				t.Errorf("stored slug = %q, want %q", stored.Slug, c.Slug)
			}
		})
	}
}

func TestCreateCampaignRefusesDuplicates(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		second domain.Campaign
	}{
		"a slug that is taken": {
			second: domain.Campaign{
				Slug:     "blackwater",
				Name:     "Another Blackwater",
				VaultDir: "vault/other",
			},
		},
		"an id that is taken": {
			second: domain.Campaign{
				ID:       "id-1",
				Slug:     "rivergate",
				Name:     "Rivergate",
				VaultDir: "vault/rivergate",
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			s := newStore(t)
			mustCreateCampaign(t, s)

			_, err := s.CreateCampaign(ctx, tt.second)
			if !errors.Is(err, store.ErrConflict) {
				t.Errorf("CreateCampaign returned %v, want an error matching ErrConflict", err)
			}
		})
	}
}

func TestCampaignBySlug(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	created := mustCreateCampaign(t, s)

	found, err := s.CampaignBySlug(ctx, created.Slug)
	if err != nil {
		t.Fatalf("CampaignBySlug: %v", err)
	}
	if found != created {
		t.Errorf("CampaignBySlug returned %+v, want %+v", found, created)
	}

	_, err = s.CampaignBySlug(ctx, "no-such-campaign")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CampaignBySlug for an unknown slug returned %v, want an error matching ErrNotFound", err)
	}
}

func TestGetCampaignNotFound(t *testing.T) {
	t.Parallel()

	_, err := newStore(t).GetCampaign(context.Background(), "no-such-campaign")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetCampaign returned %v, want an error matching ErrNotFound", err)
	}
}

func TestListCampaignsIsOrderedBySlug(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)

	// Nothing at all is an empty list rather than nil, so a caller rendering
	// a page of campaigns does not have to tell "none yet" from "failed".
	empty, err := s.ListCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListCampaigns on an empty store: %v", err)
	}
	if empty == nil {
		t.Error("ListCampaigns returned nil for an empty store, want an empty slice")
	}

	for _, name := range []string{"rivergate", "blackwater", "thornford"} {
		slug, slugErr := domain.NewSlug(name)
		if slugErr != nil {
			t.Fatalf("the test fixture has an invalid slug: %v", slugErr)
		}
		c := domain.Campaign{Slug: slug, Name: name, VaultDir: "vault/" + name}
		if _, createErr := s.CreateCampaign(ctx, c); createErr != nil {
			t.Fatalf("CreateCampaign(%q): %v", name, createErr)
		}
	}

	got, err := s.ListCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}

	want := []domain.Slug{"blackwater", "rivergate", "thornford"}
	if len(got) != len(want) {
		t.Fatalf("ListCampaigns returned %d campaigns, want %d", len(got), len(want))
	}
	for i, slug := range want {
		if got[i].Slug != slug {
			t.Errorf("campaign %d is %q, want %q: the order is part of the contract", i, got[i].Slug, slug)
		}
	}
}

func TestUpdateCampaign(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	created := mustCreateCampaign(t, s)

	// A second write has to be a later instant, or "updated_at moved" proves
	// nothing.
	renamed := created
	renamed.Name = "The Blackwater, take two"
	renamed.System = "homebrew"
	renamed.VaultDir = "vault/blackwater-2"

	updated, updateErr := s.UpdateCampaign(ctx, renamed)
	if updateErr != nil {
		t.Fatalf("UpdateCampaign: %v", updateErr)
	}

	if updated.Name != renamed.Name || updated.System != "homebrew" || updated.VaultDir != renamed.VaultDir {
		t.Errorf("UpdateCampaign returned %+v, want the new name, system and vault directory", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("updated_at = %v, want something after %v", updated.UpdatedAt, created.UpdatedAt)
	}
	if !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("created_at = %v, want it unchanged at %v", updated.CreatedAt, created.CreatedAt)
	}
	if updated.Slug != created.Slug {
		t.Errorf("slug = %q, want it unchanged at %q: a campaign's identity never moves", updated.Slug, created.Slug)
	}
	if updated.ID != created.ID {
		t.Errorf("id = %q, want it unchanged at %q", updated.ID, created.ID)
	}

	read, err := s.GetCampaign(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	if read != updated {
		t.Errorf("GetCampaign returned %+v, want %+v", read, updated)
	}
}

func TestUpdateCampaignNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)

	_, err := s.UpdateCampaign(ctx, domain.Campaign{
		ID:       "no-such-campaign",
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("UpdateCampaign returned %v, want an error matching ErrNotFound", err)
	}
}

func TestStoreWritesSurviveAReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "campaigns.db")

	first := newStoreAt(t, path, clock.NewFixed(testTime, time.Minute))
	campaignStored := mustCreateCampaign(t, first)
	pageStored := mustCreatePage(t, first, campaignStored.ID)
	if closeErr := first.Close(); closeErr != nil {
		t.Fatalf("closing the first store: %v", closeErr)
	}

	// A second store over the same file, with a different id sequence, so a
	// row that somehow came from the first store's memory cannot pass.
	second, err := store.Open(ctx, path, store.Options{
		Clock: clock.NewFixed(testTime, time.Hour),
		IDGen: idgen.NewSequence("other"),
	})
	if err != nil {
		t.Fatalf("reopening the store: %v", err)
	}
	defer func() {
		if closeErr := second.Close(); closeErr != nil {
			t.Errorf("closing the reopened store: %v", closeErr)
		}
	}()

	got, err := second.GetPage(ctx, campaignStored.ID, pageStored.Path, store.AsDM(campaignStored.ID))
	if err != nil {
		t.Fatalf("reading a page back after reopening: %v", err)
	}
	if got != pageStored {
		t.Errorf("the page read back as %+v, want %+v", got, pageStored)
	}
}
