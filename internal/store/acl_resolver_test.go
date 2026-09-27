package store

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// TestStoreReadPredicateMatchesResolver is the named test from §14, and it is
// the one this project has been deferring since M5: the SQL read predicate and
// the Go resolver answer the same question about the same matrix, and a
// disagreement is a leak in whichever direction it goes.
//
// The two are two implementations of one rule and that is not an accident. The
// predicate has to be SQL because a `WHERE` clause that filtered in Go would have
// to load every page in the campaign to decide which to return. The resolver has
// to be Go because the rights matrix is 36 cells and a matrix you can only
// exercise through a database is a matrix whose unexercised cells are the
// interesting ones. So there are two, and the answer to "which one is right" is
// that they are both required to say the same thing and this test is what makes
// that true.
//
// The direction of a disagreement matters and both are failures:
//
//   - the predicate admits and the resolver refuses: a page a principal may not
//     read reaches a response, and every other control in the project is then
//     defending a building with an unlocked door;
//   - the predicate refuses and the resolver admits: a feature is missing and
//     somebody files a bug. Annoying, and safe.
//
// The test is worth more than the code it checks, because it is the only place
// where the matrix, the column default, the `ON DELETE SET NULL`, the
// `archived` rule and the SQL's own NULL handling are all in one place and
// compared against the thing the documentation says.
func TestStoreReadPredicateMatchesResolver(t *testing.T) {
	t.Parallel()

	// The matrix again, from the other side. Same 36 cells, and this time the
	// answer comes out of SQLite rather than out of `For`, so the two are
	// genuinely independent — a table of expectations written once and used by
	// both tests would test that they agree with the table.
	type cell struct {
		role       domain.Role
		visibility domain.Visibility
		owned      bool
		archived   bool
	}

	var cells []cell
	for _, role := range []domain.Role{domain.RoleDM, domain.RolePlayer, ""} {
		for _, visibility := range []domain.Visibility{
			domain.VisibilityDMOnly, domain.VisibilityDMAndOwner, domain.VisibilityPlayers,
		} {
			for _, owned := range []bool{false, true} {
				for _, archived := range []bool{false, true} {
					cells = append(cells, cell{role: role, visibility: visibility, owned: owned, archived: archived})
				}
			}
		}
	}
	// 3 roles x 3 audiences x 2 owned x 2 archived. The extra role against §8's
	// two is the one the matrix does not have: a request that identified nobody.
	// It is the row most likely to be forgotten, and it is the one whose absence
	// would let a session that was not found be read as a player.
	if len(cells) != 36 {
		t.Fatalf("the matrix has %d cells, want 36: 3 roles x 3 audiences x 2 owned x 2 archived", len(cells))
	}

	for _, c := range cells {
		t.Run(cellName(c.role, c.visibility, c.owned, c.archived), func(t *testing.T) {
			t.Parallel()

			fixture := buildCell(t, c.role, c.visibility, c.owned, c.archived)
			s, campaign, page, principal := fixture.store, fixture.campaign, fixture.page, fixture.principal

			// What Go says.
			want := access.For(access.PrincipalOf(principal), access.PageMeta{
				Visibility: c.visibility,
				Owned:      c.owned,
				Archived:   c.archived,
			})

			// What SQL says, twice: through the read scope, and through the
			// stricter secret scope, because the two share the audience test and
			// the two disagreeing is a different and equally bad failure.
			sqlRead := scopeAdmits(t, s, readable(campaign.ID, principal), page.ID)
			sqlSecrets := scopeAdmits(t, s, readableWithSecrets(campaign.ID, principal), page.ID)

			if sqlRead != want.CanRead {
				t.Errorf("the read predicate says %t and the resolver says %t", sqlRead, want.CanRead)
			}

			// The secret scope is the audience test AND "is this a DM or an
			// owner", so for a page this principal may read the two must agree,
			// and for a page it may not read the secret scope must say no
			// whatever the audience says.
			if want.CanRead && sqlSecrets != want.CanSeeSecrets {
				t.Errorf("the secret predicate says %t and the resolver says %t, on a page both say may be read",
					sqlSecrets, want.CanSeeSecrets)
			}
			if !want.CanRead && sqlSecrets {
				t.Error("the secret predicate admits a page the read predicate does not")
			}
		})
	}
}

// cellFixture is one matrix cell, built: a campaign, a page in that state, a
// principal in that role, and — where the cell owns — a binding between them.
//
// One function rather than three, because "owned" is a fact about three things
// at once: the page's audience, the page's `owner_character_page_id`, and a row
// in `principal_characters`. Any two of the three without the third is a
// fixture that looks like the cell and is not it, and a test that compares the
// resolver with the SQL over a fixture that is not the cell compares them over
// nothing.
type matrixCell struct {
	store     *Store
	campaign  domain.Campaign
	page      domain.Page
	principal domain.Principal
}

func buildCell(t *testing.T, role domain.Role, visibility domain.Visibility, owned, archived bool) matrixCell {
	t.Helper()

	ctx := context.Background()
	s, campaign := audienceFixture(t)

	// The character page. A character page is its *own* owner, which is what
	// makes a binding to it mean "this principal owns this page".
	character, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeCharacter,
		Visibility:  visibility,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker.\n",
		ContentHash: "hash-of-aria",
	}, AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage for the character page: %v", err)
	}

	page := character
	if owned {
		page, err = s.UpsertPage(ctx, domain.Page{
			CampaignID:           campaign.ID,
			Path:                 character.Path,
			Title:                character.Title,
			Type:                 character.Type,
			Visibility:           character.Visibility,
			OwnerCharacterPageID: character.ID,
			Frontmatter:          character.Frontmatter,
			Body:                 character.Body,
			ContentHash:          "hash-of-aria-owned",
		}, AsDM(campaign.ID))
		if err != nil {
			t.Fatalf("UpsertPage for the owned page: %v", err)
		}
	}

	// A principal with no role is not a row this store will hold: Validate
	// refuses it, and the `role` column's CHECK refuses it again. That is the
	// right answer twice over — an unidentified request should not have a
	// principal, and a principal with no role would be one the resolver has to
	// classify.
	//
	// So the no-role cell is a *value* handed to the predicate rather than a row
	// looked up, which is exactly how the real code reaches it: a session that
	// could not be found produces a zero `domain.Principal`, and that value goes
	// straight into the scope.
	principal := domain.Principal{}
	persistent := role != ""

	if persistent {
		principal, err = s.CreatePrincipal(ctx, domain.Principal{
			CampaignID: campaign.ID,
			Label:      "the principal of this cell",
			Role:       role,
			TokenHash:  "hash-of-" + string(role),
			TokenHint:  "a1b2",
		})
		if err != nil {
			t.Fatalf("CreatePrincipal(%q): %v", role, err)
		}
	}

	if owned && persistent {
		// The binding is to the character page, which is the character: every
		// page under it belongs to the same people, and the predicate finds them
		// by comparing against the owner's id rather than the page's own.
		if err := s.ReplacePrincipalCharacters(ctx, principal.ID, []string{character.ID}); err != nil {
			t.Fatalf("ReplacePrincipalCharacters: %v", err)
		}
	}

	if archived {
		if err := s.DeletePage(ctx, page.ID); err != nil {
			t.Fatalf("DeletePage: %v", err)
		}
	}

	return matrixCell{store: s, campaign: campaign, page: page, principal: principal}
}

// scopeAdmits runs a scope for one page and reports whether it admits it.
func scopeAdmits(t *testing.T, s *Store, sc scope, pageID string) bool {
	t.Helper()

	const query = `SELECT 1 FROM pages p WHERE p.id = ? AND (` + `WHERE_PLACEHOLDER` + `)`

	rows, err := s.read.QueryContext(context.Background(),
		strings.Replace(query, "WHERE_PLACEHOLDER", sc.where, 1), append([]any{pageID}, sc.args...)...)
	if err != nil {
		t.Fatalf("running the scope: %v", err)
	}
	defer func() { _ = rows.Close() }()

	admitted := rows.Next()
	if err := rows.Err(); err != nil {
		t.Fatalf("reading the scope's rows: %v", err)
	}
	return admitted
}

func cellName(role domain.Role, visibility domain.Visibility, owned, archived bool) string {
	name := string(role)
	if name == "" {
		name = "no-role"
	}

	name += "/" + string(visibility)
	if owned {
		name += "/owned"
	}
	if archived {
		name += "/archived"
	}
	return name
}

// The disclosure the comparison test found, pinned on its own so that removing
// the conjunct from the audience test is a failure in one named place.
//
// The first clause of the predicate used to be `p.visibility = 'players'`, which
// is true for a page and does not mention the principal at all. A request that
// identified nobody — a session that could not be found, a cookie that was never
// sent — produces a principal with an empty role, and an empty role is not "dm",
// so the whole campaign was readable by an unauthenticated caller.
//
// The specification writes the same clause, so this is a correction to the
// specification as well as to the code, and it is worth a test that says so.
func TestAnUnidentifiedRequestReadsNothing(t *testing.T) {
	t.Parallel()

	s, campaign := audienceFixture(t)

	nobody := domain.Principal{}

	// Nothing, through the read scope.
	if paths := scopePaths(t, s, readable(campaign.ID, nobody)); len(paths) != 0 {
		t.Errorf("a request that identified nobody read %v", paths)
	}

	// Nothing, through the secret scope.
	if paths := scopePaths(t, s, readableWithSecrets(campaign.ID, nobody)); len(paths) != 0 {
		t.Errorf("a request that identified nobody read secret text from %v", paths)
	}

	// And the clause itself names the role, so that a future simplification of
	// the audience test to a bare visibility check is a failure here.
	if !strings.Contains(aclAudience, "? = 'player' AND p.visibility = 'players'") {
		t.Errorf("the audience test does not require a role before admitting a players page:\\n%s", aclAudience)
	}

	// A player still reads it, which is the other half: the fix is a conjunct
	// and not a wall.
	player := domain.Principal{ID: "p-1", CampaignID: campaign.ID, Role: domain.RolePlayer}
	paths := scopePaths(t, s, readable(campaign.ID, player))
	if !slices.Contains(paths, "locations/rivergate") {
		t.Errorf("a player read %v, want the players' page", paths)
	}
}

// TestAPrincipalOfAnotherCampaignReadsNothing is the tenancy half of the read
// predicate, and it is the half that was missing until M8 put a real principal in
// front of it.
//
// A principal belongs to one campaign — the column is NOT NULL, and a share link
// is scoped to one campaign (ADR 0003) — so asking for a page in campaign B while
// holding a session for campaign A is not a question about rights. It is a
// question with two answers, and the predicate was answering "whatever the role
// clause says" for both of them. A player of the Blackwater could have read
// every `players` page in Thornford, and a DM of the Blackwater every page in it,
// by naming a campaign they had no link to.
//
// Every caller up to here had been the sync engine, which passes
// `AsDM(campaignID)` and is therefore always of the campaign it is reading, so
// the hole was only reachable by a caller with a real principal — which is the
// HTTP layer, and only from M8. A predicate that is correct for callers who pass
// the right pair of arguments is a predicate one handler away from a disclosure.
func TestAPrincipalOfAnotherCampaignReadsNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newTestStore(t)

	first, err := s.CreateCampaign(ctx, domain.Campaign{Slug: "blackwater", Name: "The Blackwater", VaultDir: "vault/blackwater"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	second, err := s.CreateCampaign(ctx, domain.Campaign{Slug: "thornford", Name: "Thornford", VaultDir: "vault/thornford"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	// One page in the second campaign, readable by every player of it.
	thornfordPage := domain.Page{
		CampaignID:  second.ID,
		Path:        "locations/thornford",
		Title:       "Thornford",
		Type:        domain.PageTypeLocation,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: Thornford\n",
		Body:        "A town in a campaign nobody here has a link to.\n",
		ContentHash: "hash-of-thornford",
	}
	if _, upsertErr := s.UpsertPage(ctx, thornfordPage, AsDM(second.ID)); upsertErr != nil {
		t.Fatalf("UpsertPage: %v", upsertErr)
	}

	tests := map[string]struct {
		principal domain.Principal
		want      []string
	}{
		"a player of that campaign reads its players' pages": {
			principal: domain.Principal{ID: "p-thornford", CampaignID: second.ID, Role: domain.RolePlayer},
			want:      []string{"locations/thornford"},
		},
		"a player of another campaign reads none of them": {
			principal: domain.Principal{ID: "p-blackwater", CampaignID: first.ID, Role: domain.RolePlayer},
			want:      nil,
		},
		// The DM row is the one that looks like it could not fail: `? = 'dm'`
		// admits every page of the campaign, and a DM of one campaign reading
		// another is exactly the shape a second DM would have if principals were
		// not per-campaign. They are.
		"a DM of that campaign reads its pages": {
			principal: domain.Principal{ID: "dm-thornford", CampaignID: second.ID, Role: domain.RoleDM},
			want:      []string{"locations/thornford"},
		},
		"a DM of another campaign reads none of them": {
			principal: domain.Principal{ID: "dm-blackwater", CampaignID: first.ID, Role: domain.RoleDM},
			want:      nil,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := scopePaths(t, s, readable(second.ID, tt.principal)); !equalStrings(got, tt.want) {
				t.Errorf("the read scope admitted %v, want %v", got, tt.want)
			}
		})
	}

	// And through a method, because a scope that is right and a query that does
	// not use it are two different facts. `GetPage` takes the campaign as an
	// argument and the principal as its own, which is precisely the shape that
	// let the two disagree.
	stranger := domain.Principal{ID: "p-blackwater", CampaignID: first.ID, Role: domain.RolePlayer}
	if _, getErr := s.GetPage(ctx, second.ID, thornfordPage.Path, stranger); getErr == nil {
		t.Error("GetPage returned a page in a campaign the principal is not of")
	}

	pages, err := s.ListPages(ctx, second.ID, stranger)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("ListPages returned %d pages of another campaign", len(pages))
	}

	// The search is the other read path through the same scope, and it is the one
	// that would be a disclosure with a title in it: a hit carries the page's
	// title, so a player of one campaign seeing another campaign's titles has
	// learned that those pages exist.
	sc := readable(second.ID, stranger)
	if paths := scopePaths(t, s, sc); len(paths) != 0 {
		t.Errorf("the read scope for a stranger admitted %v", paths)
	}
	if got := strings.Count(sc.where, "p.campaign_id = ?"); got != 2 {
		t.Errorf("the scope asks whose campaign %d times, want 2:\n%s", got, sc.where)
	}
}
