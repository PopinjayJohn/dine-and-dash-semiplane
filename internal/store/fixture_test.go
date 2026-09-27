package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// Fixtures for the store's public API tests. The clock and the id sequence are
// fixed, so a failure message that shows a row shows the same row every run.

// testTime is an instant from the spec's example vault. The step means two
// writes in one test get two different timestamps, which is what makes
// "the second write stamped a later time" assertable.
var testTime = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

// newStore opens a migrated store on a temporary database file.
func newStore(t *testing.T) *store.Store {
	t.Helper()

	return newStoreWithClock(t, clock.NewFixed(testTime, time.Minute))
}

// newStoreWithClock opens a store with a clock the test supplies, for the tests
// that need to know exactly which instant a row was stamped with. A standing
// clock is the right one for those: it stamps every row with the same instant,
// so the expectation is a literal rather than a count of how many times the
// clock has been read.
func newStoreWithClock(t *testing.T, clk clock.Clock) *store.Store {
	t.Helper()

	return newStoreAt(t, filepath.Join(t.TempDir(), "campaigns.db"), clk)
}

func newStoreAt(t *testing.T, path string, clk clock.Clock) *store.Store {
	t.Helper()

	s, err := store.Open(context.Background(), path, store.Options{
		Clock: clk,
		IDGen: idgen.NewSequence("id"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	})

	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	return s
}

// campaign is a complete, valid campaign with no id and no timestamps, which is
// how a caller hands one to the store.
func campaign() domain.Campaign {
	return domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	}
}

// page is a complete, valid page belonging to campaignID.
func page(campaignID string) domain.Page {
	return domain.Page{
		CampaignID:      campaignID,
		Path:            "locations/rivergate",
		Title:           "Rivergate",
		Type:            domain.PageTypeLocation,
		Frontmatter:     "title: Rivergate\ntype: location\ndm_notes: kept verbatim\n",
		Body:            "A fortified town at the confluence of the [[Blackwater]].\n",
		ContentHash:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		RendererVersion: 1,
	}
}

// mustCreateCampaign stores a campaign and returns it as stored, failing the
// test if it cannot.
func mustCreateCampaign(t *testing.T, s *store.Store) domain.Campaign {
	t.Helper()

	c, err := s.CreateCampaign(context.Background(), campaign())
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	return c
}

// mustCreatePage stores a page and returns it as stored, failing the test if it
// cannot.
func mustCreatePage(t *testing.T, s *store.Store, campaignID string) domain.Page {
	t.Helper()

	p, err := s.UpsertPage(context.Background(), page(campaignID), store.AsDM(campaignID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	return p
}

// newStoreTB is newStore for a benchmark, which is a *testing.B and not a
// *testing.T. The two differ only in what they report, so the helper takes the
// interface and both get their own temp directory and their own cleanup.
func newStoreTB(tb testing.TB) *store.Store {
	tb.Helper()

	s, err := store.Open(context.Background(), filepath.Join(tb.TempDir(), "campaigns.db"), store.Options{
		Clock: clock.NewFixed(testTime, time.Minute),
		IDGen: idgen.NewSequence("id"),
	})
	if err != nil {
		tb.Fatalf("store.Open: %v", err)
	}
	tb.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			tb.Errorf("closing the store: %v", closeErr)
		}
	})

	if _, err := s.Migrate(context.Background()); err != nil {
		tb.Fatalf("migrating: %v", err)
	}
	return s
}

// mustCreateCampaignTB is mustCreateCampaign for a benchmark.
func mustCreateCampaignTB(tb testing.TB, s *store.Store) domain.Campaign {
	tb.Helper()

	c, err := s.CreateCampaign(context.Background(), campaign())
	if err != nil {
		tb.Fatalf("CreateCampaign: %v", err)
	}
	return c
}
