package index_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # Link resolution is for the reader
//
// ADR 0020 recorded the finding and M9 recorded the decision not to fix it, and
// this is the fix: the resolver reads the principal out of the context instead of
// using the DM. Everything below is about the shape of the answer, because the
// answer is a page and a page is a disclosure.

// linkFixture is a campaign with one public page and one DM's page, a DM, and a
// player. The two are what a reader can and cannot see, so they are the two cases
// every test here is about.
type linkFixture struct {
	t      *testing.T
	store  *store.Store
	render *render.Renderer
	camp   domain.Campaign

	dm     domain.Principal
	player domain.Principal
}

func newLinkFixture(t *testing.T) *linkFixture {
	t.Helper()

	ctx, s, campaign := resolverFixture(t)

	// A page everybody in the campaign may read, and a page only the DM may.
	for _, page := range []domain.Page{
		{
			CampaignID:  campaign.ID,
			Path:        "locations/rivergate",
			Title:       "Rivergate",
			Type:        domain.PageTypeLocation,
			Visibility:  domain.VisibilityPlayers,
			Frontmatter: "title: Rivergate\nvisibility: players\n",
			Body:        "A fortified town.\n",
			ContentHash: "hash-rivergate",
		},
		{
			CampaignID:  campaign.ID,
			Path:        "npcs/vel",
			Title:       "Captain Vell",
			Type:        domain.PageTypeNPC,
			Visibility:  domain.VisibilityDMOnly,
			Frontmatter: "title: Captain Vell\nvisibility: dm-only\n",
			Body:        "Collects the toll.\n",
			ContentHash: "hash-vel",
		},
	} {
		if _, err := s.UpsertPage(ctx, page, store.AsDM(campaign.ID)); err != nil {
			t.Fatalf("UpsertPage(%s): %v", page.Path, err)
		}
	}

	dm, err := s.CreatePrincipal(ctx, domain.Principal{
		CampaignID: campaign.ID, Label: "the DM", Role: domain.RoleDM,
		TokenHash: "hash-dm", TokenHint: "test", CreatedAt: fixedNow,
	})
	if err != nil {
		t.Fatalf("CreatePrincipal(dm): %v", err)
	}
	player, err := s.CreatePrincipal(ctx, domain.Principal{
		CampaignID: campaign.ID, Label: "Aria", Role: domain.RolePlayer,
		TokenHash: "hash-aria", TokenHint: "test", CreatedAt: fixedNow,
	})
	if err != nil {
		t.Fatalf("CreatePrincipal(player): %v", err)
	}

	return &linkFixture{
		t:      t,
		store:  s,
		camp:   campaign,
		render: render.NewWithLinks(index.NewResolver(s, campaign.ID)),
		dm:     dm,
		player: player,
	}
}

// renderAs renders a body as one of the two principals, with the decision derived
// the way the HTTP layer derives it.
//
// **The decision is not a literal**, and that is the whole reason the first
// version of this test failed in a way that looked like the fix not working. A
// `render.Decision{}` for the DM and the same for the player is a decision that
// contradicts both principals — the DM's real decision has `ReadsAll` set and the
// player's has `CanSeeSecrets` false — and the render cache is right to serve one
// entry to both, because the key says they are the same reader class.
//
// So the test asks the same question the handler asks: `access.For(principal,
// page)` and render with the result. Anything less and the test is asserting that
// a hand-built decision is honoured, which says nothing about the system.
func (f *linkFixture) renderAs(body string, as domain.Principal) string {
	f.t.Helper()

	// A `players` page, which is the one both principals may read, and which is
	// where the two decisions actually differ.
	decision := access.For(access.PrincipalOf(as), access.PageMeta{Visibility: domain.VisibilityPlayers})

	result, err := f.render.Render(
		domain.WithPrincipal(context.Background(), as),
		render.Page{Campaign: f.camp.Slug.String(), Path: "sessions/nine", Body: body, ContentHash: "hash-" + as.ID},
		decision,
	)
	if err != nil {
		f.t.Fatalf("Render: %v", err)
	}
	return result.HTML
}

// TestALinkResolvesForTheReaderAndNotForSomebodyElse is ADR 0020's fix, asserted
// end to end: the same body, the same campaign, two readers, and the link to the
// DM's page resolves for one of them and not the other.
//
// The player's link comes back *unresolved* rather than absent, which is the
// distinction §9 draws: a DM writes `[[the toll collector]]` to a page they intend
// to write, and a link that vanished would be worse than one that says where it
// was going.
func TestALinkResolvesForTheReaderAndNotForSomebodyElse(t *testing.T) {
	t.Parallel()

	f := newLinkFixture(t)
	const body = "Through [[locations/rivergate]] to meet [[npcs/vel|the toll collector]].\n"

	asDM := f.renderAs(body, f.dm)
	asPlayer := f.renderAs(body, f.player)

	// Both links resolve for the DM.
	for _, target := range []string{"locations/rivergate", "npcs/vel"} {
		if want := `href="/c/blackwater/` + target + `"`; !strings.Contains(asDM, want) {
			t.Errorf("the DM's link to %s did not resolve:\n%s", target, asDM)
		}
	}

	// And for a player the two targets part company, which is the whole of it:
	// the public one is a link a player follows, and the DM's is not. A fix that
	// made *every* link unresolved for a player would pass a lazier version of
	// this test and break the wiki, so the first case is asserted as well as the
	// second.
	if want := `href="/c/blackwater/locations/rivergate"`; !strings.Contains(asPlayer, want) {
		t.Errorf("a player cannot follow a link to a public page:\n%s", asPlayer)
	}
	if forbidden := `href="/c/blackwater/npcs/vel"`; strings.Contains(asPlayer, forbidden) {
		t.Errorf("a player was given a resolved link to a DM's page:\n%s", asPlayer)
	}

	// And the player's link is still a link a reader can see and understand.
	if !strings.Contains(asPlayer, "the toll collector") {
		t.Errorf("the player's link lost its text:\n%s", asPlayer)
	}
	if !strings.Contains(asPlayer, "unresolved") {
		t.Errorf("the player's link is neither resolved nor marked unresolved:\n%s", asPlayer)
	}
}

// TestTwoPlayersSeeTheSameBytes is the other half, and it is what says the fix
// costs nothing.
//
// Every page has two classes of reader — those who may see its secrets and those
// who may not — and they resolve the same links, so two players' renders of one
// page are identical. That is why the cache key needs no principal in it
// (`TestTheCacheKeyNamesNoPrincipal` in `internal/render` guards the key's shape),
// and a fix for a disclosure that multiplied the cache by the size of the party
// would be a fix nobody could afford.
func TestTwoPlayersSeeTheSameBytes(t *testing.T) {
	t.Parallel()

	f := newLinkFixture(t)
	const body = "Through [[locations/rivergate]] to meet [[npcs/vel]].\n"

	other, err := f.store.CreatePrincipal(context.Background(), domain.Principal{
		CampaignID: f.camp.ID, Label: "Brian", Role: domain.RolePlayer,
		TokenHash: "hash-brian", TokenHint: "test", CreatedAt: fixedNow,
	})
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	first := f.renderAs(body, f.player)
	second := f.renderAs(body, other)

	if first != second {
		t.Errorf("two players' renders of one page differ, so the cache needs a principal in its key:\n%s\n\n%s", first, second)
	}
}

// TestAMissingPrincipalResolvesNothing is the fail-closed direction, and it is the
// one that covers everything the context does not reach.
//
// A render with no principal in its context is every render that is not a request:
// the golden tests in `internal/render`, a plugin's render hook, a command that
// renders a page to a terminal. Those get unresolved links for everything, which
// is not a second behaviour to reason about — it is the same one, for a reader
// nobody has identified.
func TestAMissingPrincipalResolvesNothing(t *testing.T) {
	t.Parallel()

	f := newLinkFixture(t)

	result, err := f.render.Render(context.Background(), render.Page{
		Campaign: f.camp.Slug.String(), Path: "sessions/nine",
		Body:        "Through [[locations/rivergate]].\n",
		ContentHash: "hash",
	}, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(result.HTML, `href="/c/blackwater/`) {
		t.Errorf("a render with no principal resolved a link:\n%s", result.HTML)
	}
}

// TestAPrincipalOfAnotherCampaignResolvesNothing is the tenancy half, and it is
// the store's conjunct doing what M8 gave it.
//
// A resolver is for one campaign and a principal is of one campaign, so a context
// carrying a principal from elsewhere must resolve nothing rather than that
// campaign's public pages. The resolver passes its own campaign to every lookup,
// so the predicate's second conjunct — `p.campaign_id = ?` with the *principal's*
// campaign — is what refuses it, and this is the test that says the resolver's
// hard-coded campaign is not a way around it.
func TestAPrincipalOfAnotherCampaignResolvesNothing(t *testing.T) {
	t.Parallel()

	f := newLinkFixture(t)

	elsewhere := domain.Principal{ID: "p-elsewhere", CampaignID: "some-other-campaign", Role: domain.RolePlayer}

	result, err := f.render.Render(
		domain.WithPrincipal(context.Background(), elsewhere),
		render.Page{
			Campaign: f.camp.Slug.String(), Path: "sessions/nine",
			Body: "Through [[locations/rivergate]].\n", ContentHash: "hash",
		},
		render.Decision{},
	)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(result.HTML, `href="/c/blackwater/`) {
		t.Errorf("a principal of another campaign resolved a link in this one:\n%s", result.HTML)
	}
}
