package store

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The read predicate is the riskiest four lines in this package, and the tests
// below are the reason it is four lines and not four copies. They run the actual
// SQL the application runs, over a fixture with one page in each of the three
// audiences, and assert who sees what — because a predicate that is correct on
// paper and wrong in the database is a disclosure, and reading the constant is
// not a way to find that out.

// audienceFixture is one campaign holding one page in each audience, plus an
// archived page, so that every term of the predicate has something to act on.
func audienceFixture(t *testing.T) (*Store, domain.Campaign) {
	t.Helper()

	ctx := context.Background()
	s := newTestStore(t)

	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	for path, visibility := range map[string]domain.Visibility{
		"locations/rivergate":       domain.VisibilityPlayers,
		"npcs/garros-ironbar":       domain.VisibilityDMAndOwner,
		"sessions/the-dragon-heist": domain.VisibilityDMOnly,
	} {
		stored, upsertErr := s.UpsertPage(ctx, domain.Page{
			CampaignID:  campaign.ID,
			Path:        path,
			Title:       path,
			Type:        domain.PageTypeNote,
			Visibility:  visibility,
			Frontmatter: "title: " + path + "\n",
			Body:        "Some prose about " + path + ".\n",
			ContentHash: "hash-of-" + path,
		})
		if upsertErr != nil {
			t.Fatalf("UpsertPage(%q): %v", path, upsertErr)
		}
		_ = stored
	}

	// An archived page, which no read may return whatever its audience is.
	stored, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "locations/old-rivergate",
		Title:       "The old Rivergate",
		Type:        domain.PageTypeNote,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: the old Rivergate\n",
		Body:        "Demolished.\n",
		ContentHash: "hash-of-old-rivergate",
	})
	if err != nil {
		t.Fatalf("UpsertPage for the archived page: %v", err)
	}
	if deleteErr := s.DeletePage(ctx, stored.ID); deleteErr != nil {
		t.Fatalf("DeletePage: %v", deleteErr)
	}

	// A second campaign with a page in it, so a scope that forgot the campaign
	// term has something to leak.
	other, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "thornford",
		Name:     "Thornford",
		VaultDir: "vault/thornford",
	})
	if err != nil {
		t.Fatalf("CreateCampaign for the second campaign: %v", err)
	}
	if _, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  other.ID,
		Path:        "locations/rivergate",
		Title:       "Somebody else's Rivergate",
		Type:        domain.PageTypeNote,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: somebody else's Rivergate\n",
		Body:        "Not this campaign's town.\n",
		ContentHash: "hash-of-their-rivergate",
	}); err != nil {
		t.Fatalf("UpsertPage for the other campaign: %v", err)
	}

	return s, campaign
}

// readablePaths runs the read scope as the application runs it and returns the
// paths it admits.
func readablePaths(t *testing.T, s *Store, campaignID string, as domain.Principal) []string {
	t.Helper()

	return scopePaths(t, s, readable(campaignID, as))
}

// readableWithSecretsPaths is readablePaths through the stricter scope.
func readableWithSecretsPaths(t *testing.T, s *Store, campaignID string, as domain.Principal) []string {
	t.Helper()

	return scopePaths(t, s, readableWithSecrets(campaignID, as))
}

func scopePaths(t *testing.T, s *Store, sc scope) []string {
	t.Helper()

	// The shape every page-returning query in this package has: the page table
	// aliased as `p`, the predicate as the whole WHERE clause. Building it here
	// rather than calling a store method is the point — the scope is the unit
	// under test, and it is the unit the invariant is about.
	query := `SELECT p.path FROM pages p WHERE ` + sc.where + ` ORDER BY p.path` //nolint:gosec // sc.where is a constant from acl.go

	rows, err := s.read.QueryContext(context.Background(), query, sc.args...)
	if err != nil {
		t.Fatalf("running the scope: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatalf("scanning a path: %v", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the scope's rows: %v", err)
	}
	return paths
}

// The two roles, and what each may read. A player is an unbound one: there are no
// bindings yet, so the `dm-and-owner` page belongs to nobody and is nobody's to
// read. That is the fail-closed half, and it is the case most likely to be
// "fixed" into the wrong answer by somebody who wants a green test.
func TestReadScopeAdmits(t *testing.T) {
	t.Parallel()

	s, campaign := audienceFixture(t)

	dm := domain.Principal{ID: "dm-1", Role: domain.RoleDM}
	player := domain.Principal{ID: "player-1", Role: domain.RolePlayer}
	// A principal the application has never heard of: no role, no id. The zero
	// value has to be the safe one, because a caller that forgot to look a
	// principal up gets this rather than a panic.
	unknown := domain.Principal{}

	tests := map[string]struct {
		as   domain.Principal
		want []string
	}{
		"a DM reads every page in the campaign": {
			as: dm,
			want: []string{
				"locations/rivergate",
				"npcs/garros-ironbar",
				"sessions/the-dragon-heist",
			},
		},
		"a player reads the players' pages and nothing else": {
			as:   player,
			want: []string{"locations/rivergate"},
		},
		"a principal with no role reads only the players' pages": {
			as:   unknown,
			want: []string{"locations/rivergate"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := readablePaths(t, s, campaign.ID, tt.as)
			if !equalStrings(got, tt.want) {
				t.Errorf("the read scope admitted %v, want %v", got, tt.want)
			}
		})
	}
}

// The stricter scope, which is what the private index is read through. A DM may
// find their own secrets; nobody else may, and the reason a player cannot is not
// that the page is unreadable — it is often perfectly readable — but that the
// secret is not theirs to see.
func TestReadScopeWithSecretsAdmits(t *testing.T) {
	t.Parallel()

	s, campaign := audienceFixture(t)

	dm := domain.Principal{ID: "dm-1", Role: domain.RoleDM}
	player := domain.Principal{ID: "player-1", Role: domain.RolePlayer}
	unknown := domain.Principal{}

	tests := map[string]struct {
		as   domain.Principal
		want []string
	}{
		"a DM reads every page's secrets": {
			as: dm,
			want: []string{
				"locations/rivergate",
				"npcs/garros-ironbar",
				"sessions/the-dragon-heist",
			},
		},
		"a player reads no page's secrets":    {as: player, want: nil},
		"a principal with no role reads none": {as: unknown, want: nil},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := readableWithSecretsPaths(t, s, campaign.ID, tt.as)
			if !equalStrings(got, tt.want) {
				t.Errorf("the secret scope admitted %v, want %v", got, tt.want)
			}
		})
	}
}

// Every placeholder in a scope has an argument, and every argument has a
// placeholder. A predicate with a spare `?` is a driver error at run time; a
// predicate with a spare argument is a driver error too, and both arrive on a
// request a player made rather than in a test that happened to use the same
// query.
func TestScopesHaveOneArgumentPerPlaceholder(t *testing.T) {
	t.Parallel()

	tests := map[string]scope{
		"readable":           readable("campaign-1", domain.Principal{Role: domain.RolePlayer}),
		"readable as a DM":   readable("campaign-1", domain.Principal{Role: domain.RoleDM}),
		"readable with none": readable("campaign-1", domain.Principal{}),
		"with secrets":       readableWithSecrets("campaign-1", domain.Principal{Role: domain.RolePlayer}),
	}

	for name, sc := range tests {
		t.Run(name, func(t *testing.T) {
			if got, want := strings.Count(sc.where, "?"), len(sc.args); got != want {
				t.Errorf("the scope has %d placeholders and %d arguments:\n%s",
					got, want, sc.where)
			}
		})
	}
}

// The ownership branch is present and false, and that is a property rather than
// a placeholder to be filled in later. A predicate that quietly drops the branch
// while the table it needs is still missing widens the audience of every
// `dm-and-owner` page in every campaign, and the test that would have caught it
// is a page written after the branch was removed.
func TestOwnershipBranchFailsClosed(t *testing.T) {
	t.Parallel()

	if !strings.Contains(aclAudience, aclOwnership) {
		t.Errorf("the audience test does not consult the ownership test:\n%s", aclAudience)
	}
	if !strings.Contains(aclSecretScope, aclOwnership) {
		t.Errorf("the secret scope does not consult the ownership test:\n%s", aclSecretScope)
	}
	if aclOwnership != "1 = 0" {
		t.Errorf("the ownership test is %q, want %q while no principal owns a page", aclOwnership, "1 = 0")
	}
}

// The audience test names the two levels that admit somebody, and does not name
// `dm-only` at all.
//
// The absence is the point. `dm-only` is absolute: no clause of this predicate
// can admit it to a player, so the only way to write it down would be to name it
// and then exclude it, and an exclusion somebody can delete is not a control. A
// level whose spelling drifted from the column's CHECK constraint would match
// nothing, which is why the two that are named are checked by name.
func TestAudienceTestNamesTheLevelsThatAdmit(t *testing.T) {
	t.Parallel()

	for _, level := range []domain.Visibility{
		domain.VisibilityPlayers,
		domain.VisibilityDMAndOwner,
	} {
		if !strings.Contains(aclAudience, "'"+level.String()+"'") {
			t.Errorf("the audience test does not mention the %q level:\n%s", level, aclAudience)
		}
	}

	if strings.Contains(aclAudience, "'"+domain.VisibilityDMOnly.String()+"'") {
		t.Errorf("the audience test names %q, which is the one level no clause of it may admit:\n%s",
			domain.VisibilityDMOnly, aclAudience)
	}
}

// equalStrings compares two path lists, treating nil and empty as the same thing
// so that a table can say "nothing" either way.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
