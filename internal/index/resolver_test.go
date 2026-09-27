package index_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

func TestResolverUsesObsidiansOrder(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := resolverFixture(t)
	r := index.NewResolver(s, campaign.ID)

	// The fixtures, so the order is visible in the test rather than in the
	// implementation: a page, an alias of it, a second page with the same file
	// name in a different folder, and a page in another campaign with the same
	// everything.
	town, upsertErr := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash-town",
	}, store.AsDM(campaign.ID))
	if upsertErr != nil {
		t.Fatalf("UpsertPage: %v", upsertErr)
	}
	if aliasErr := s.ReplacePageAliases(ctx, town.ID, []string{"the toll town", "Flussport"}); aliasErr != nil {
		t.Fatalf("ReplacePageAliases: %v", aliasErr)
	}

	if _, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/nested/rivergate",
		Title:       "Rivergate, the lower one",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash-nested",
	}, store.AsDM(campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	tests := map[string]struct {
		target  string
		heading string
		want    string
		found   bool
	}{
		"an exact path, which is the first and the only unambiguous one": {
			target: "locations/nested/rivergate", want: "locations/nested/rivergate", found: true,
		},
		"an alias": {
			target: "the toll town", want: "locations/rivergate", found: true,
		},
		"a file name, which is the last resort and case-insensitive": {
			target: "RIVERGATE", want: "locations/nested/rivergate", found: true,
		},
		"a path with a fragment still resolves to the page": {
			target: "locations/rivergate#the-bridges", heading: "the-bridges",
			want: "locations/rivergate", found: true,
		},
		"a heading on its own names no page": {
			target: "#the-bridges", want: "", found: false,
		},
		"a name nobody answers to": {
			target: "thornford", want: "", found: false,
		},
		"an empty target": {
			target: "", want: "", found: false,
		},
		"whitespace around a target": {
			target: "  the toll town  ", want: "locations/rivergate", found: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, found, resolveErr := r.Resolve(ctx, tt.target, tt.heading)
			if resolveErr != nil {
				t.Fatalf("Resolve(%q): %v", tt.target, resolveErr)
			}
			if found != tt.found {
				t.Fatalf("Resolve(%q) found = %t, want %t", tt.target, found, tt.found)
			}
			if found && got.Path != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.target, got.Path, tt.want)
			}
		})
	}
}

// TestAResolverIsForOneCampaign is the reason the resolver is a per-campaign
// object rather than a function with a campaign parameter: a DM has two
// campaigns open in two tabs, and `[[rivergate]` means a different page in
// each.
func TestAResolverIsForOneCampaign(t *testing.T) {
	t.Parallel()

	ctx, s, first := resolverFixture(t)

	second, campaignErr := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "rivergate-county",
		Name:     "Rivergate County",
		VaultDir: "vault/rivergate-county",
	})
	if campaignErr != nil {
		t.Fatalf("CreateCampaign: %v", campaignErr)
	}

	for _, campaign := range []domain.Campaign{first, second} {
		if _, upsertErr := s.UpsertPage(ctx, domain.Page{
			CampaignID:  campaign.ID,
			Path:        "locations/rivergate",
			Title:       "Rivergate of " + campaign.Slug.String(),
			Type:        domain.PageTypeLocation,
			ContentHash: "hash-" + campaign.Slug.String(),
		}, store.AsDM(campaign.ID)); upsertErr != nil {
			t.Fatalf("UpsertPage: %v", upsertErr)
		}
	}

	one, found, err := index.NewResolver(s, first.ID).Resolve(ctx, "locations/rivergate", "")
	if err != nil || !found {
		t.Fatalf("Resolve in the first campaign: found = %t, %v", found, err)
	}
	two, found, err := index.NewResolver(s, second.ID).Resolve(ctx, "locations/rivergate", "")
	if err != nil || !found {
		t.Fatalf("Resolve in the second campaign: found = %t, %v", found, err)
	}

	if one.Path != two.Path {
		t.Fatalf("the two campaigns resolved to different paths: %q and %q", one.Path, two.Path)
	}
	if one.Title == two.Title {
		t.Errorf("both campaigns resolved to the same page title %q, so the resolver ignored its campaign", one.Title)
	}
}

// TestTheResolverIsWhatTheRendererAsksFor: the renderer's own interface, the one
// M3 shipped with nothing behind it, is satisfied by this. A compile-time
// assertion in the package says it too; this is the one that fails when the two
// drift.
func TestTheResolverIsWhatTheRendererAsksFor(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := resolverFixture(t)

	town, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash",
	}, store.AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	if aliasErr := s.ReplacePageAliases(ctx, town.ID, []string{"the toll town"}); aliasErr != nil {
		t.Fatalf("ReplacePageAliases: %v", aliasErr)
	}

	result, err := render.NewWithLinks(index.NewResolver(s, campaign.ID)).Render(ctx, render.Page{
		Campaign:    campaign.Slug.String(),
		Path:        "npcs/garros-ironbar",
		Body:        "Ask [[the toll town]] about the coin.\n",
		ContentHash: "hash-of-the-page",
	}, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// The link resolved, so the HTML has an href to the page's own path and not
	// the alias the DM wrote.
	if want := `href="/c/blackwater/locations/rivergate"`; !contains(result.HTML, want) {
		t.Errorf("the rendered HTML does not contain %q:\n%s", want, result.HTML)
	}
	if contains(result.HTML, "unresolved") {
		t.Errorf("a link the index knows is rendered unresolved:\n%s", result.HTML)
	}
	if !contains(result.HTML, ">the toll town<") {
		t.Errorf("the link does not show what the DM wrote:\n%s", result.HTML)
	}
}

// TestAnUnusableResolverResolvesNothing: a nil resolver, or one with no
// campaign, is a page rendered before the index exists. Every link is
// unresolved and nothing panics.
func TestAnUnusableResolverResolvesNothing(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := resolverFixture(t)
	_ = campaign

	if _, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash",
	}, store.AsDM(campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	for name, resolver := range map[string]render.LinkResolver{
		"a nil resolver":              nil,
		"a resolver with no campaign": index.NewResolver(s, ""),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result, err := render.NewWithLinks(resolver).Render(ctx, render.Page{
				Campaign:    campaign.Slug.String(),
				Path:        "npcs/garros-ironbar",
				Body:        "See [[locations/rivergate]].\n",
				ContentHash: "hash-of-the-page",
			}, render.Decision{})
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			if !contains(result.HTML, "unresolved") {
				t.Errorf("the link is not marked unresolved:\n%s", result.HTML)
			}
			if contains(result.HTML, "href=") {
				t.Errorf("an unresolvable link has an href:\n%s", result.HTML)
			}
		})
	}
}

func resolverFixture(t *testing.T) (context.Context, *store.Store, domain.Campaign) {
	t.Helper()

	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "campaigns.db"), store.Options{
		Clock: clock.NewFixed(time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC), time.Minute),
		IDGen: idgen.NewSequence("id"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, migrateErr := s.Migrate(ctx); migrateErr != nil {
		t.Fatalf("Migrate: %v", migrateErr)
	}

	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	return ctx, s, campaign
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && strings.Contains(haystack, needle)
}

// newStore opens and migrates a store in a data directory, the way `wiki sync`
// will: the database beside the vault, not inside it.
func newStore(t *testing.T, root string) *store.Store {
	t.Helper()

	s, err := store.Open(context.Background(), filepath.Join(root, "campaigns.db"), store.Options{
		Clock: clock.NewFixed(time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC), time.Minute),
		IDGen: idgen.NewSequence("id"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, migrateErr := s.Migrate(context.Background()); migrateErr != nil {
		t.Fatalf("Migrate: %v", migrateErr)
	}

	return s
}
