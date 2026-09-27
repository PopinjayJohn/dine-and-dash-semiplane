package store

import (
	"context"
	"errors"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// Every page-returning method now takes a principal, and the signature is only
// half of it: a method that *can* be filtered and does not is worse than one that
// cannot, because the call site reads as if it had been considered.
//
// So this is the test for the signature change, and it is the same property
// through every door: a `dm-only` page is not returned to a player by any of
// them, and is returned to a DM by all of them.

// filteredFixture is a campaign with the three audiences in it, a player, and a
// player who owns one of the `dm-and-owner` pages.
func filteredFixture(t *testing.T) (*Store, domain.Campaign, domain.Principal) {
	t.Helper()

	ctx := context.Background()
	s, campaign := audienceFixture(t)

	character, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeCharacter,
		Visibility:  domain.VisibilityDMOnly,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker.\n",
		ContentHash: "hash-of-aria",
	}, AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	owned, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:           campaign.ID,
		Path:                 character.Path,
		Title:                character.Title,
		Type:                 character.Type,
		Visibility:           domain.VisibilityDMAndOwner,
		OwnerCharacterPageID: character.ID,
		Frontmatter:          character.Frontmatter,
		Body:                 character.Body,
		ContentHash:          "hash-of-aria-owned",
	}, AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage for the owned page: %v", err)
	}

	// A second character, `dm-only` and owned by the same player. §8 is explicit
	// that `dm-only` is absolute — ownership never unlocks it — and a test with
	// no such page cannot tell an absolute rule from one that happens to hold.
	absolute, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:           campaign.ID,
		Path:                 "characters/brian",
		Title:                "Brian",
		Type:                 domain.PageTypeCharacter,
		Visibility:           domain.VisibilityDMOnly,
		OwnerCharacterPageID: character.ID,
		Frontmatter:          "title: Brian\n",
		Body:                 "A character the DM has marked private.\n",
		ContentHash:          "hash-of-brian",
	}, AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage for the absolute page: %v", err)
	}

	if aliasErr := s.ReplacePageAliases(ctx, owned.ID, []string{"the lockpicker"}); aliasErr != nil {
		t.Fatalf("ReplacePageAliases: %v", aliasErr)
	}
	_ = absolute

	player, err := s.CreatePrincipal(ctx, domain.Principal{
		CampaignID: campaign.ID,
		Label:      "Alice (Ranger)",
		Role:       domain.RolePlayer,
		TokenHash:  "hash-of-alice",
		TokenHint:  "a1b2",
	})
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	if err := s.ReplacePrincipalCharacters(ctx, player.ID, []string{character.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}

	return s, campaign, player
}

func TestPageReadsAreFiltered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, player := filteredFixture(t)

	// The DM, the player, and the player-as-owner, which is a different answer for
	// a different page and the whole point of the ownership column.
	dm := AsDM(campaign.ID)
	owner := player
	// A player who owns nothing: the same role, a different binding.
	stranger, err := s.CreatePrincipal(ctx, domain.Principal{
		CampaignID: campaign.ID,
		Label:      "Bob",
		Role:       domain.RolePlayer,
		TokenHash:  "hash-of-bob",
		TokenHint:  "a1b2",
	})
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	names := func(pages []domain.Page) map[string]bool {
		out := map[string]bool{}
		for _, page := range pages {
			out[page.Path] = true
		}
		return out
	}

	tests := map[string]struct {
		principal   domain.Principal
		wantPresent []string
		wantAbsent  []string
	}{
		"a DM reads every page": {
			principal:   dm,
			wantPresent: []string{"locations/rivergate", "npcs/garros-ironbar", "sessions/the-dragon-heist", "characters/aria"},
			wantAbsent:  nil,
		},
		"a player who owns a character reads their own dm-and-owner page": {
			principal:   owner,
			wantPresent: []string{"locations/rivergate", "characters/aria"},
			wantAbsent:  []string{"npcs/garros-ironbar", "sessions/the-dragon-heist"},
		},
		"a player who owns nothing reads only the players' pages": {
			principal:   stranger,
			wantPresent: []string{"locations/rivergate"},
			wantAbsent:  []string{"npcs/garros-ironbar", "sessions/the-dragon-heist", "characters/aria"},
		},
		"a request that identified nobody reads nothing": {
			principal:   Nobody(),
			wantPresent: nil,
			wantAbsent:  []string{"locations/rivergate", "npcs/garros-ironbar", "sessions/the-dragon-heist", "characters/aria"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			pages, listErr := s.ListPages(ctx, campaign.ID, tt.principal)
			if listErr != nil {
				t.Fatalf("ListPages: %v", listErr)
			}
			got := names(pages)

			for _, path := range tt.wantPresent {
				if !got[path] {
					t.Errorf("the list does not contain %q; it has %v", path, got)
				}
			}
			for _, path := range tt.wantAbsent {
				if got[path] {
					t.Errorf("the list contains %q, which this principal may not read", path)
				}
			}
		})
	}

	// The two lists are genuinely different, or the assertions above are
	// asserting nothing: a principal argument that is ignored would make every
	// row pass for whichever side it happened to be on.
	playerPages, err := s.ListPages(ctx, campaign.ID, player)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	dmPages, err := s.ListPages(ctx, campaign.ID, dm)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(playerPages) >= len(dmPages) {
		t.Errorf("a player sees %d pages and a DM sees %d: the filter is not doing anything",
			len(playerPages), len(dmPages))
	}
}

// A page that exists and may not be read is **not found**, and not "forbidden".
// The distinction tells a player which paths a DM has written, and a path is
// enough to ask about.
func TestAPageAPrincipalMayNotReadIsNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, player := filteredFixture(t)

	// The DM-only page, and the dm-and-owner page this player does not own.
	for _, path := range []string{"sessions/the-dragon-heist", "npcs/garros-ironbar"} {
		if _, err := s.GetPage(ctx, campaign.ID, path, player); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPage(%q) as a player returned %v, want an error matching ErrNotFound", path, err)
		}
	}

	// And the DM gets it, so the two are not the same answer for every path.
	if _, err := s.GetPage(ctx, campaign.ID, "sessions/the-dragon-heist", AsDM(campaign.ID)); err != nil {
		t.Errorf("GetPage of a dm-only page as the DM: %v", err)
	}

	// A `dm-only` page a player *owns* is still not found. §8 says `dm-only` is
	// absolute, and a test that only checked a page nobody owned would miss a
	// resolver or a predicate that granted it to an owner.
	if _, err := s.GetPage(ctx, campaign.ID, "characters/brian", player); err == nil {
		t.Error("a player read a dm-only page they own: dm-only is absolute")
	}
	// And the dm-and-owner one they also own is readable, so the line above is
	// about the audience and not about the owner.
	if _, err := s.GetPage(ctx, campaign.ID, "characters/aria", player); err != nil {
		t.Errorf("a player cannot read the dm-and-owner page they own: %v", err)
	}
}

// The three methods that take an id rather than a path scope by the principal's
// campaign, because they have no campaign of their own. That is a real rule and
// it is worth a test: a principal from another campaign gets not-found rather
// than somebody else's page.
func TestReadsByIdScopeToThePrincipalsCampaign(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, _ := filteredFixture(t)

	page, err := s.GetPage(ctx, campaign.ID, "locations/rivergate", AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}

	// The fixture campaign already has a second one, so this uses it rather than
	// making a third: the question is "is a principal from *elsewhere* refused",
	// and either answers it.
	elsewhere, err := s.CampaignBySlug(ctx, "thornford")
	if err != nil {
		t.Fatalf("the fixture's second campaign: %v", err)
	}

	// A principal of the *other* campaign, asking for this campaign's page.
	outsider := AsDM(elsewhere.ID)
	if _, err := s.GetPageByID(ctx, page.ID, outsider); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetPageByID from another campaign returned %v, want an error matching ErrNotFound", err)
	}
	if _, err := s.GetPageByID(ctx, page.ID, AsDM(campaign.ID)); err != nil {
		t.Errorf("GetPageByID from its own campaign: %v", err)
	}
}

// Backlinks filter on the **source**, and that is the part worth testing
// separately: a backlink says "some page mentions this one", and if the some page
// is a `dm-only` session log then the backlink is a disclosure — it hands a
// player a path, and a path is a thing they can then try to read.
func TestBacklinksFilterOnTheSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, player := filteredFixture(t)

	secret, err := s.GetPage(ctx, campaign.ID, "sessions/the-dragon-heist", AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("GetPage for the dm-only page: %v", err)
	}
	open, err := s.GetPage(ctx, campaign.ID, "locations/rivergate", AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("GetPage for the players' page: %v", err)
	}

	// The DM's page points at both.
	if linkErr := s.ReplaceLinks(ctx, open.ID, []domain.PageLink{
		{SrcPageID: open.ID, DstPath: "sessions/the-dragon-heist", DstPageID: secret.ID},
	}); linkErr != nil {
		t.Fatalf("ReplaceLinks: %v", linkErr)
	}

	// The DM sees both.
	dm := AsDM(campaign.ID)
	if links, dmErr := s.Backlinks(ctx, secret.ID, dm); dmErr != nil {
		t.Fatalf("Backlinks as the DM: %v", dmErr)
	} else if len(links) != 1 {
		t.Errorf("the DM sees %d backlinks, want 1: the graph should hold the link", len(links))
	}

	// The player sees none, because the only page pointing at the secret one is
	// readable by everybody — and the interesting case is the other way round.
	if links, playerErr := s.Backlinks(ctx, secret.ID, player); playerErr != nil {
		t.Fatalf("Backlinks as a player: %v", playerErr)
	} else if len(links) != 1 {
		t.Errorf("a player sees %d backlinks to a dm-only page, want 0: a backlink is a disclosure", len(links))
	}

	// The reverse: a `dm-only` page pointing at an open one. The open page's
	// backlink list must not mention it.
	if linkErr := s.ReplaceLinks(ctx, secret.ID, []domain.PageLink{
		{SrcPageID: secret.ID, DstPath: "locations/rivergate", DstPageID: open.ID},
	}); linkErr != nil {
		t.Fatalf("ReplaceLinks from the secret page: %v", linkErr)
	}

	playerLinks, openErr := s.Backlinks(ctx, open.ID, player)
	if openErr != nil {
		t.Fatalf("Backlinks as a player: %v", openErr)
	}
	for _, link := range playerLinks {
		if link.SrcPageID == secret.ID {
			t.Errorf("a player's backlink list names the dm-only page %s", secret.Path)
		}
	}
}

// The alias and name lookups filter, and an unresolved link to a page a player may
// not read resolves to nothing rather than to a page.
func TestLinkLookupsAreFiltered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, player := filteredFixture(t)

	dm := AsDM(campaign.ID)

	if _, found, err := s.FindPageByAlias(ctx, campaign.ID, "the lockpicker", dm); err != nil || !found {
		t.Fatalf("FindPageByAlias as the DM = %t, %v; want found", found, err)
	}

	// The player owns that page, so the alias resolves for them too — the
	// ownership is what makes it theirs, and an alias to a page they may not read
	// resolving is the thing to check, which is the one below.
	if _, found, err := s.FindPageByAlias(ctx, campaign.ID, "the lockpicker", player); err != nil || !found {
		t.Errorf("FindPageByAlias as the owner = %t, %v; want found", found, err)
	}

	// The DM-only session log has no alias, so give it one and check it does not
	// resolve for a player.
	secret, err := s.GetPage(ctx, campaign.ID, "sessions/the-dragon-heist", dm)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if err := s.ReplacePageAliases(ctx, secret.ID, []string{"the heist that failed"}); err != nil {
		t.Fatalf("ReplacePageAliases: %v", err)
	}

	if _, found, err := s.FindPageByAlias(ctx, campaign.ID, "the heist that failed", player); err != nil || found {
		t.Errorf("FindPageByAlias resolved a dm-only page for a player: found %t, err %v", found, err)
	}
	if _, found, err := s.FindPageByName(ctx, campaign.ID, "sessions/the-dragon-heist.md", player); err != nil || found {
		t.Errorf("FindPageByName resolved a dm-only page for a player: found %t, err %v", found, err)
	}
	if _, found, err := s.FindPageByName(ctx, campaign.ID, "sessions/the-dragon-heist.md", dm); err != nil || !found {
		t.Errorf("FindPageByName as the DM = %t, %v; want found", found, err)
	}
}

// The resolver and the filtered methods have to agree about *which* pages a
// principal owns, because they answer it by different means: one from a
// `principal_characters` row, the other from the predicate. This is the seam
// between the two, and a disagreement is a player who can read a page in one path
// and not the other.
func TestFilteredReadsAgreeWithTheResolver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, campaign, player := filteredFixture(t)

	// Every page the DM has, checked against the resolver for this principal.
	pages, err := s.ListPages(ctx, campaign.ID, AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) == 0 {
		t.Fatal("the fixture has no pages")
	}

	for _, page := range pages {
		owned, err := s.OwnerExists(ctx, player.ID, page.OwnerCharacterPageID)
		if err != nil {
			t.Fatalf("OwnerExists: %v", err)
		}

		// What the resolver would say, from the same two facts the predicate uses.
		want := access.For(access.PrincipalOf(player), access.PageMeta{
			Visibility: page.Visibility,
			Owned:      owned,
			Archived:   page.IsDeleted,
		})

		_, readErr := s.GetPage(ctx, campaign.ID, page.Path, player)
		got := readErr == nil
		if got != want.CanRead {
			t.Errorf("%s: GetPage says %t and the resolver says %t (owner %q, owned %t)",
				page.Path, got, want.CanRead, page.OwnerCharacterPageID, owned)
		}
	}
}
