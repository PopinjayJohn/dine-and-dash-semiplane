package store_test

import (
	"context"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// A campaign with the names a wiki link might use, in the shapes that make the
// resolution rules non-obvious.
func lookupFixture(t *testing.T) (context.Context, *store.Store, domain.Campaign) {
	t.Helper()

	ctx := context.Background()
	s := newStore(t)

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

func TestFindPageByAlias(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := lookupFixture(t)

	// The awkward pair: two pages whose aliases differ only in case, which is
	// why alias matching is exact. And a duplicate alias across two pages, which
	// a vault can contain and which the resolution order breaks by path.
	pages := []struct {
		path    string
		aliases []string
	}{
		{path: "locations/rivergate", aliases: []string{"the toll town", "Flussport"}},
		{path: "npcs/garros-ironbar", aliases: []string{"the toll-collector", "Rear; the Toll"}},
		{path: "npcs/vel", aliases: []string{"rear; the toll"}},
		{path: "sessions/one", aliases: []string{"shared"}},
		{path: "locations/two", aliases: []string{"shared"}},
	}

	var rivergateID string
	for _, p := range pages {
		stored, err := s.UpsertPage(ctx, domain.Page{
			CampaignID:  campaign.ID,
			Path:        p.path,
			Title:       p.path,
			Type:        domain.PageTypeNote,
			ContentHash: "hash-of-" + p.path,
		})
		if err != nil {
			t.Fatalf("UpsertPage(%q): %v", p.path, err)
		}
		if p.path == "locations/rivergate" {
			rivergateID = stored.ID
		}
		if err := s.ReplacePageAliases(ctx, stored.ID, p.aliases); err != nil {
			t.Fatalf("ReplacePageAliases(%q): %v", p.path, err)
		}
	}

	tests := map[string]struct {
		alias string
		want  string
		found bool
	}{
		"an alias as written":               {alias: "the toll town", want: "locations/rivergate", found: true},
		"an alias that is not a path":       {alias: "Flussport", want: "locations/rivergate", found: true},
		"an alias with punctuation":         {alias: "the toll-collector", want: "npcs/garros-ironbar", found: true},
		"the two cases are two pages":       {alias: "Rear; the Toll", want: "npcs/garros-ironbar", found: true},
		"the lower-case spelling":           {alias: "rear; the toll", want: "npcs/vel", found: true},
		"a duplicate alias is a path order": {alias: "shared", want: "locations/two", found: true},
		"an alias nobody has":               {alias: "the drowned hound", want: "", found: false},
		"a partial alias is not an alias":   {alias: "toll", want: "", found: false},
		"a path is not an alias":            {alias: "locations/rivergate", want: "", found: false},
		"an empty alias is nothing":         {alias: "", want: "", found: false},
		"whitespace is trimmed":             {alias: "  Flussport  ", want: "locations/rivergate", found: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, found, err := s.FindPageByAlias(ctx, campaign.ID, tt.alias)
			if err != nil {
				t.Fatalf("FindPageByAlias(%q): %v", tt.alias, err)
			}
			if found != tt.found {
				t.Fatalf("FindPageByAlias(%q) found = %t, want %t", tt.alias, found, tt.found)
			}
			if found && got.Path != tt.want {
				t.Errorf("FindPageByAlias(%q) = %q, want %q", tt.alias, got.Path, tt.want)
			}
		})
	}

	// The page still answers to its own name, because its name is a property
	// of its path rather than something the caller supplies.
	targets, err := s.PageTargets(ctx, rivergateID)
	if err != nil {
		t.Fatalf("PageTargets: %v", err)
	}
	if got := targets["name"]; len(got) != 1 || got[0] != "rivergate" {
		t.Errorf("the page's own name target is %v, want [rivergate]", got)
	}
}

func TestFindPageByName(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := lookupFixture(t)

	// Two files with the same stem in different folders, a name with
	// punctuation, and a name with a non-ASCII letter -- which is the case
	// SQLite's LOWER() would get wrong and Go's strings.ToLower does not.
	for _, path := range []string{
		"locations/rivergate",
		"locations/nested/rivergate",
		"npcs/the-drowned-hound",
		"locations/Ölbach",
	} {
		stored, err := s.UpsertPage(ctx, domain.Page{
			CampaignID:  campaign.ID,
			Path:        path,
			Title:       path,
			Type:        domain.PageTypeNote,
			ContentHash: "hash-of-" + path,
		})
		if err != nil {
			t.Fatalf("UpsertPage(%q): %v", path, err)
		}
		if err := s.ReplacePageAliases(ctx, stored.ID, nil); err != nil {
			t.Fatalf("ReplacePageTargets(%q): %v", path, err)
		}
	}

	tests := map[string]struct {
		name  string
		want  string
		found bool
	}{
		"a stem as written":                {name: "rivergate", want: "locations/nested/rivergate", found: true},
		"a stem in another case":           {name: "RIVERGATE", want: "locations/nested/rivergate", found: true},
		"a stem with punctuation":          {name: "the-drowned-hound", want: "npcs/the-drowned-hound", found: true},
		"a stem with a non-ASCII letter":   {name: "ölbach", want: "locations/Ölbach", found: true},
		"a whole path finds the same page": {name: "locations/rivergate", want: "locations/nested/rivergate", found: true},
		"a name with the extension":        {name: "rivergate.md", want: "locations/nested/rivergate", found: true},
		"a stem nobody has":                {name: "thornford", want: "", found: false},
		"a partial stem is not a name":     {name: "rive", want: "", found: false},
		"an empty name":                    {name: "", want: "", found: false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, found, err := s.FindPageByName(ctx, campaign.ID, tt.name)
			if err != nil {
				t.Fatalf("FindPageByName(%q): %v", tt.name, err)
			}
			if found != tt.found {
				t.Fatalf("FindPageByName(%q) found = %t, want %t", tt.name, found, tt.found)
			}
			if found && got.Path != tt.want {
				t.Errorf("FindPageByName(%q) = %q, want %q", tt.name, got.Path, tt.want)
			}
		})
	}
}

func TestReplacePageTargetsReplaces(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := lookupFixture(t)

	stored, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash",
	})
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	if aliasErr := s.ReplacePageAliases(ctx, stored.ID, []string{"the toll town", "Flussport"}); aliasErr != nil {
		t.Fatalf("ReplacePageAliases: %v", aliasErr)
	}

	// A DM renames the page, and the aliases the file used to have have to go
	// with it: an append would leave them reachable for ever.
	if aliasErr := s.ReplacePageAliases(ctx, stored.ID, []string{"the bridge town"}); aliasErr != nil {
		t.Fatalf("ReplacePageAliases: %v", aliasErr)
	}

	targets, err := s.PageTargets(ctx, stored.ID)
	if err != nil {
		t.Fatalf("PageTargets: %v", err)
	}

	if got := targets["alias"]; len(got) != 1 || got[0] != "the bridge town" {
		t.Errorf("the page answers to %v, want only the new alias", got)
	}
	if got := targets["name"]; len(got) != 1 || got[0] != "rivergate" {
		t.Errorf("the page's own name target is %v, want [rivergate]: a name target is not the caller's to replace", got)
	}
}

func TestReplacePageTargetsDeduplicatesAndTrims(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := lookupFixture(t)

	stored, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash",
	})
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	if aliasErr := s.ReplacePageAliases(ctx, stored.ID, []string{"Flussport", "  Flussport  ", "", "   "}); aliasErr != nil {
		t.Fatalf("ReplacePageAliases: %v", aliasErr)
	}

	targets, err := s.PageTargets(ctx, stored.ID)
	if err != nil {
		t.Fatalf("PageTargets: %v", err)
	}
	if got := targets["alias"]; len(got) != 1 || got[0] != "Flussport" {
		t.Errorf("the aliases are %v, want one trimmed copy of Flussport", got)
	}
}

func TestArchivedPagesAnswerToNothing(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := lookupFixture(t)

	stored, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash",
	})
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	if err := s.ReplacePageAliases(ctx, stored.ID, []string{"the toll town"}); err != nil {
		t.Fatalf("ReplacePageTargets: %v", err)
	}

	if _, found, err := s.FindPageByName(ctx, campaign.ID, "rivergate"); err != nil || !found {
		t.Fatalf("the page answers to its own name before it is archived (found = %t, %v)", found, err)
	}

	if err := s.DeletePage(ctx, stored.ID); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	// An archived page is gone from every read, and a link that resolves to it
	// would be a link to a page nobody can open.
	if _, found, err := s.FindPageByName(ctx, campaign.ID, "rivergate"); err != nil || found {
		t.Errorf("an archived page answers to its own name (found = %t, %v)", found, err)
	}
	if _, found, err := s.FindPageByAlias(ctx, campaign.ID, "the toll town"); err != nil || found {
		t.Errorf("an archived page answers to its alias (found = %t, %v)", found, err)
	}
}

func TestLookupsArePerCampaign(t *testing.T) {
	t.Parallel()

	ctx, s, first := lookupFixture(t)

	second, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "rivergate-county",
		Name:     "Rivergate County",
		VaultDir: "vault/rivergate-county",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	for _, campaign := range []domain.Campaign{first, second} {
		stored, upsertErr := s.UpsertPage(ctx, domain.Page{
			CampaignID:  campaign.ID,
			Path:        "locations/rivergate",
			Title:       "Rivergate",
			Type:        domain.PageTypeLocation,
			ContentHash: "hash-of-" + campaign.ID,
		})
		if upsertErr != nil {
			t.Fatalf("UpsertPage: %v", upsertErr)
		}
		if aliasErr := s.ReplacePageAliases(ctx, stored.ID, []string{"the toll town"}); aliasErr != nil {
			t.Fatalf("ReplacePageAliases: %v", aliasErr)
		}
	}

	// The same alias in two campaigns is two pages, and each campaign resolves
	// to its own: a wiki link is resolved within a campaign, because a DM has
	// two of them open in two tabs.
	one, _, err := s.FindPageByAlias(ctx, first.ID, "the toll town")
	if err != nil {
		t.Fatalf("FindPageByAlias: %v", err)
	}
	if one.CampaignID != first.ID {
		t.Errorf("the first campaign's alias resolved to a page of campaign %s", one.CampaignID)
	}
}

// TestASecondUpsertOfTheSamePathKeepsTheRow is the rule the name target depends
// on, and the one a caller gets wrong quietly: an upsert on
// (campaign_id, path) keeps the *original* row's id, because that id is what
// the revisions and the inbound links point at, so a caller that supplies a
// different one has it ignored.
func TestASecondUpsertOfTheSamePathKeepsTheRow(t *testing.T) {
	t.Parallel()

	ctx, s, campaign := lookupFixture(t)

	first, err := s.UpsertPage(ctx, domain.Page{
		ID:          "the-original-id",
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash-one",
	})
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	if first.ID != "the-original-id" {
		t.Fatalf("the first upsert stored id %q, want the one it was given", first.ID)
	}

	// A second write of the same path, with a different id and new content.
	second, err := s.UpsertPage(ctx, domain.Page{
		ID:          "a-different-id",
		CampaignID:  campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate, after the flood",
		Type:        domain.PageTypeLocation,
		ContentHash: "hash-two",
	})
	if err != nil {
		t.Fatalf("UpsertPage again: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("the second upsert stored id %q, want the row's own %q", second.ID, first.ID)
	}
	if second.ContentHash != "hash-two" {
		t.Errorf("the second upsert did not update the content: %q", second.ContentHash)
	}

	// The name target belongs to the row, so it is still there and still one
	// row, and it points at the id the page actually has.
	targets, err := s.PageTargets(ctx, first.ID)
	if err != nil {
		t.Fatalf("PageTargets: %v", err)
	}
	if got := targets["name"]; len(got) != 1 || got[0] != "rivergate" {
		t.Errorf("the page's name targets are %v, want [rivergate]", got)
	}

	// And the superseded id owns nothing, which is the other half: a name
	// target recorded against an id no page has would be a link that resolves
	// to nothing.
	orphaned, err := s.PageTargets(ctx, "a-different-id")
	if err != nil {
		t.Fatalf("PageTargets: %v", err)
	}
	if len(orphaned["name"]) != 0 {
		t.Errorf("the ignored id owns the name targets %v, want none", orphaned["name"])
	}
}
