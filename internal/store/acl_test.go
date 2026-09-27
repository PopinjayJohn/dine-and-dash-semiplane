package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
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
		}, AsDM(campaign.ID))
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
	}, AsDM(campaign.ID))
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
	}, AsDM(campaign.ID)); err != nil {
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

	// Both principals are *of this campaign*, which is now part of the answer
	// rather than an assumption: the read scope asks whether the caller is a
	// principal of the campaign it was asked about, so a fixture principal with
	// no campaign is a principal that reads nothing.
	dm := domain.Principal{ID: "dm-1", CampaignID: campaign.ID, Role: domain.RoleDM}
	player := domain.Principal{ID: "player-1", CampaignID: campaign.ID, Role: domain.RolePlayer}
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
		// Nothing. This case used to expect the `players` page, and the
		// expectation was the bug: the first clause of the audience test was
		// `p.visibility = 'players'` on its own, which does not look at the role
		// before it answers true, so a request that identified nobody read the
		// whole campaign. It now needs to be a player to be a player.
		"a principal with no role reads nothing": {
			as:   unknown,
			want: nil,
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

	// Both principals are *of this campaign*, which is now part of the answer
	// rather than an assumption: the read scope asks whether the caller is a
	// principal of the campaign it was asked about, so a fixture principal with
	// no campaign is a principal that reads nothing.
	dm := domain.Principal{ID: "dm-1", CampaignID: campaign.ID, Role: domain.RoleDM}
	player := domain.Principal{ID: "player-1", CampaignID: campaign.ID, Role: domain.RolePlayer}
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

// The ownership branch is the binding table, correlated on the page.
//
// It was `1 = 0` for two milestones, because no principal owned anything, and the
// test then asserted that it was *still* `1 = 0` — which is the only way to catch
// somebody tidying away a fail-closed branch. Now that it is a real clause the
// equivalent test is the behaviour: a bound player reads their own `dm-and-owner`
// page and an unbound one does not, and the subquery is correlated on the page
// rather than on the campaign.
//
// The correlation is the part worth asserting separately, because the version
// without it is shorter and admits every player to every `dm-and-owner` page in
// their campaign — which is a disclosure, and one that a player with any character
// page at all would find.
func TestOwnershipBranchIsTheBindingTable(t *testing.T) {
	t.Parallel()

	// Both scopes consult it, and the secret scope consults it twice: once for
	// "may read the page" and once for "may see its secrets", which ADR 0007 makes
	// different answers for a `players` page.
	if !strings.Contains(aclAudience, aclOwnership) {
		t.Errorf("the audience test does not consult the ownership test:\n%s", aclAudience)
	}
	if !strings.Contains(aclSecretScope, aclOwnership) {
		t.Errorf("the secret scope does not consult the ownership test:\n%s", aclSecretScope)
	}
	if got, want := strings.Count(aclSecretScope, aclOwnership), 2; got != want {
		t.Errorf("the secret scope consults the ownership test %d times, want %d: "+
			"the audience test and the secret test are different questions", got, want)
	}

	// Correlated on the page's *owner*, which is the character and not the page.
	// The version that correlated on `p.id` said a player may read the one file
	// they are bound to and not the notes underneath it.
	if !strings.Contains(aclOwnership, "pc.character_page_id = p.owner_character_page_id") {
		t.Errorf("the ownership test is not correlated on the page's owner: %q\n"+
			"a binding is between a principal and a character, and every page under "+
			"that character belongs to the same people", aclOwnership)
	}

	// And the two correlations that would be a disclosure instead of a missing
	// feature. Both are one word away from the real one, which is why they are
	// named here rather than left to be noticed.
	for _, leak := range []string{
		// "does this principal own *anything* in this campaign"
		`pc.principal_id = ? AND pc.character_page_id = p.campaign_id`,
		// "does this principal own this page" -- the pre-M7 correlation
		`pc.principal_id = ? AND pc.character_page_id = p.id`,
	} {
		if strings.Contains(aclOwnership, leak) {
			t.Errorf("the ownership test contains %q, which admits more than it should: %q",
				leak, aclOwnership)
		}
	}
	if strings.Contains(aclOwnership, "1 = 0") {
		t.Error("the ownership test still has the placeholder in it: no principal owns a page any more")
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

// A `dm-and-owner` page belongs to the principal it is bound to, and to nobody
// else. This is the case the two-milestone placeholder was standing in for, and it
// is the one that decides whether a player can see their own character's secrets —
// which is the whole point of the private index existing.
func TestABoundPrincipalReadsTheirOwnCharacterPage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign := audienceFixture(t)
	character := characterPage(t, s, campaign.ID)

	// Real principal rows, because a binding's foreign key is to one: an id with
	// no row behind it is not a principal, and a predicate that admitted a
	// principal who does not exist would be a predicate answering a question
	// nothing asked.
	owner := principalRow(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")
	stranger := principalRow(t, s, campaign.ID, "Bob", "hash-of-bob")

	// Before the binding, an owner-to-be sees only the players' page.
	if visible := scopePaths(t, s, readable(campaign.ID, owner)); len(visible) != 1 {
		t.Fatalf("an unbound principal can read %v, want only the players' page", visible)
	}

	if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{character.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}

	// After it, the owner.
	if visible := scopePaths(t, s, readable(campaign.ID, owner)); !slices.Contains(visible, "characters/aria") {
		t.Errorf("the owner cannot read their own character page: %v", visible)
	}

	// And the stranger cannot, which is the half a campaign-wide subquery gets
	// wrong.
	if visible := scopePaths(t, s, readable(campaign.ID, stranger)); slices.Contains(visible, "characters/aria") {
		t.Errorf("an unbound principal can read another player's character page: %v", visible)
	}

	// A DM can, because a DM reads everything.
	dm := domain.Principal{ID: "principal-dm-in-the-acl-fixture", CampaignID: campaign.ID, Role: domain.RoleDM}
	if visible := scopePaths(t, s, readable(campaign.ID, dm)); !slices.Contains(visible, "characters/aria") {
		t.Errorf("the DM cannot read a dm-and-owner page: %v", visible)
	}

	// And unbinding takes the page away again on the next read, which is the
	// property the binding being a *replace* exists for.
	if err := s.ReplacePrincipalCharacters(ctx, owner.ID, nil); err != nil {
		t.Fatalf("unbinding: %v", err)
	}
	if visible := scopePaths(t, s, readable(campaign.ID, owner)); slices.Contains(visible, "characters/aria") {
		t.Errorf("the owner can still read the character page they were unbound from: %v", visible)
	}
}

// The private index is reachable by an owner, and only by an owner. This is the
// payoff of the binding table: until it existed the private index was readable by
// the DM and by nobody else, so a player's own character page's secrets were
// findable by nobody at all.
func TestABoundPrincipalReadsTheirOwnSecretText(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign := audienceFixture(t)
	character := characterPage(t, s, campaign.ID)

	owner := principalRow(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")
	stranger := principalRow(t, s, campaign.ID, "Bob", "hash-of-bob")

	// Nobody but a DM before the binding, which is the state the milestone shipped
	// in and the reason this test is worth writing.
	if secrets := scopePaths(t, s, readableWithSecrets(campaign.ID, owner)); len(secrets) != 0 {
		t.Fatalf("an unbound principal can read secret text: %v", secrets)
	}

	if err := s.ReplacePageIndex(ctx, IndexEntry{
		PageID:     character.ID,
		Title:      character.Title,
		BodyPublic: "A lockpicker who owes money.\n",
		SecretText: "Aria is the toll-collector's sister.",
	}); err != nil {
		t.Fatalf("ReplacePageIndex: %v", err)
	}

	if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{character.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}

	query := mustSearchQuery(t, "sister")

	found, err := s.SearchSecrets(ctx, campaign.ID, owner, query, 10)
	if err != nil {
		t.Fatalf("SearchSecrets as the owner: %v", err)
	}
	if len(found) != 1 || found[0].Path != "characters/aria" {
		t.Errorf("the owner cannot find their own character's secret: %+v", found)
	}

	// And the stranger cannot, which is the property that makes this safe to ship.
	theirs, err := s.SearchSecrets(ctx, campaign.ID, stranger, query, 10)
	if err != nil {
		t.Fatalf("SearchSecrets as a stranger: %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("an unbound principal found secret text: %+v", theirs)
	}
}

// principalRow is a stored principal, which is what a binding points at.
func principalRow(t *testing.T, s *Store, campaignID, label, tokenHash string) domain.Principal {
	t.Helper()

	stored, err := s.CreatePrincipal(context.Background(), domain.Principal{
		CampaignID: campaignID,
		Label:      label,
		Role:       domain.RolePlayer,
		TokenHash:  tokenHash,
		TokenHint:  "a1b2",
	})
	if err != nil {
		t.Fatalf("CreatePrincipal(%q): %v", label, err)
	}
	return stored
}

// characterPage is a `dm-and-owner` page, which is the only audience the ownership
// test has any bearing on.
func characterPage(t *testing.T, s *Store, campaignID string) domain.Page {
	t.Helper()

	// A character page is its own owner, which is what makes a `dm-and-owner`
	// character page readable by the player bound to it rather than by nobody.
	// The id is not known before the insert, so it goes in afterwards.
	character, err := s.UpsertPage(context.Background(), domain.Page{
		CampaignID:  campaignID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeCharacter,
		Visibility:  domain.VisibilityDMAndOwner,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker who owes the toll-collector money.\n",
		ContentHash: "hash-of-aria",
	}, AsDM(campaignID))
	if err != nil {
		t.Fatalf("UpsertPage for the character page: %v", err)
	}

	owned, err := s.UpsertPage(context.Background(), domain.Page{
		CampaignID:           campaignID,
		Path:                 character.Path,
		Title:                character.Title,
		Type:                 character.Type,
		Visibility:           character.Visibility,
		OwnerCharacterPageID: character.ID,
		Frontmatter:          character.Frontmatter,
		Body:                 character.Body,
		ContentHash:          character.ContentHash + "-owned",
		RendererVersion:      character.RendererVersion,
	}, AsDM(campaignID))
	if err != nil {
		t.Fatalf("UpsertPage for the owned character page: %v", err)
	}
	return owned
}

// mustSearchQuery parses a query for the cases above, which are about who can see
// the result rather than about the language.
func mustSearchQuery(t *testing.T, input string) search.Query {
	t.Helper()

	parsed, err := search.Parse(input)
	if err != nil {
		t.Fatalf("Parse(%q): %v", input, err)
	}
	return parsed
}
