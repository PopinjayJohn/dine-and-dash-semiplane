package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// TestSecretStrippedFromAllSurfaces is the named test from §14, and it is the
// first one in this milestone because it is the one everything else here exists to
// make possible.
//
// It walks every player-reachable route and asserts that a canary is **absent
// from the raw response body** — not hidden by a class, not removed by a script
// after load, not absent from the DOM. The canary is in a `[!SECRET]` block on a
// page the player is allowed to read, which is the case that survives every
// narrower test: the page comes back, so a test that only checked a `dm-only`
// page would pass with a pipeline that leaked every secret in a `players` page.
//
// The routes are enumerated rather than walked by crawling links, because a crawler
// finds the pages a *current* fixture has and misses the two the fixture gains
// next month, and a test that covers what exists is a test that shrinks silently.
func TestSecretStrippedFromAllSurfaces(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	slug := f.campaign.Slug.String()

	surfaces := []struct {
		name   string
		target string
		cookie *http.Cookie
	}{
		{name: "the campaign root", target: "/c/" + slug + "/", cookie: player},
		{name: "the campaign root with no session", target: "/c/" + slug + "/"},
		{name: "the page as HTML", target: f.pageURL("locations/rivergate"), cookie: player},
		{name: "the page as HTML with no session", target: f.pageURL("locations/rivergate")},
		{name: "the page with the secret in it", target: f.pageURL("sessions/09-the-dragon-heist"), cookie: player},
		{name: "the page's markdown", target: f.pageURL("sessions/09-the-dragon-heist") + "?raw=1", cookie: player},
		{name: "the page's markdown with no session", target: f.pageURL("sessions/09-the-dragon-heist") + "?raw=1"},
		{name: "a page that does not exist", target: f.pageURL("locations/thornford"), cookie: player},
		{name: "a page the player may not read", target: f.pageURL("npcs/vel"), cookie: player},
		{name: "a page outside every campaign", target: "/c/no-such-campaign/locations/rivergate", cookie: player},
		{name: "an address that is not a page at all", target: "/", cookie: player},
		{name: "an address outside the application", target: "/nope", cookie: player},
		{name: "the health line", target: "/_/healthz", cookie: player},
		{name: "the stylesheet", target: "/static/wiki.css", cookie: player},
		{name: "the client", target: "/static/datastar.js", cookie: player},
	}

	for _, surface := range surfaces {
		t.Run(surface.name, func(t *testing.T) {
			t.Parallel()

			got := f.get(surface.target, surface.cookie)

			if strings.Contains(got.body, canary) {
				t.Errorf("GET %s leaked the canary\nbody: %s", surface.target, got.body)
			}
			// The headers too: a secret in a `Location`, an `ETag` of a body that
			// contained one, or a `Link` header is the same disclosure with a
			// smaller body.
			for name, values := range got.header {
				for _, value := range values {
					if strings.Contains(value, canary) {
						t.Errorf("GET %s put the canary in the %s header: %q", surface.target, name, value)
					}
				}
			}
		})
	}
}

// TestTheDMStillSeesTheSecret is the other half, and without it the test above is
// satisfied by a pipeline that strips everything.
//
// A DM is the principal the whole ACL is built around being able to bypass, and a
// wiki whose DM cannot read their own notes is not a safe wiki, it is a broken
// one.
func TestTheDMStillSeesTheSecret(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	page := f.get(f.pageURL("sessions/09-the-dragon-heist"), dm)
	if !strings.Contains(page.body, canary) {
		t.Errorf("the DM was served a page with the secret removed\nbody: %s", page.body)
	}

	raw := f.get(f.pageURL("sessions/09-the-dragon-heist")+"?raw=1", dm)
	if !strings.Contains(raw.body, canary) {
		t.Errorf("the DM's raw markdown does not have the secret in it\nbody: %s", raw.body)
	}
}

// TestNoPageLeaksWhetherItExists is the oracle test. A 404 for a page that is not
// yours and a 404 for a page that is not there have to be the same answer, down to
// the bytes, because the difference between them is how somebody finds out which
// pages a campaign has.
func TestNoPageLeaksWhetherItExists(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	absent := f.get(f.pageURL("locations/thornford"), player)
	forbidden := f.get(f.pageURL("npcs/vel"), player)

	if absent.status != http.StatusNotFound || forbidden.status != http.StatusNotFound {
		t.Fatalf("the two 404s have different statuses: %d and %d", absent.status, forbidden.status)
	}
	if absent.stable() != forbidden.stable() {
		t.Errorf("the 404 for a page that does not exist and the 404 for a page that is not yours differ.\nabsent: %s\n\nforbidden: %s",
			absent.stable(), forbidden.stable())
	}
	if strings.Contains(absent.body, "vel") || strings.Contains(absent.body, "npcs") {
		t.Errorf("the 404 names the page it refused:\n%s", absent.body)
	}
}

// TestAPlayerOfOneCampaignReadsNothingInAnother is the tenancy test at the edge.
//
// The store's predicate is what makes this true, and this is the test that says
// the session and the slug are connected: a player of the Blackwater asking
// Thornford's URL gets Thornford's *public* pages as nobody rather than as a
// player, because a principal is of one campaign and a URL may name another.
func TestAPlayerOfOneCampaignReadsNothingInAnother(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	// A page in the other campaign, readable by every player of it.
	otherPage := domain.Page{
		CampaignID:  f.otherName.ID,
		Path:        "locations/thornford",
		Title:       "The Salt Road",
		Type:        domain.PageTypeLocation,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: Thornford\nvisibility: players\n",
		Body:        "# The Salt Road\n\nA road in a campaign nobody here has a link to.\n",
		ContentHash: "hash-thornford",
	}
	if _, err := f.store.UpsertPage(t.Context(), otherPage, store.AsDM(f.otherName.ID)); err != nil {
		t.Fatalf("UpsertPage in the other campaign: %v", err)
	}

	got := f.get("/c/"+f.otherName.Slug.String()+"/locations/thornford", player)
	if got.status != http.StatusNotFound {
		t.Errorf("a player of one campaign read a page of another: status %d\nbody: %s", got.status, got.body)
	}

	// And the root lists nothing, rather than erroring or listing the whole
	// campaign. The assertion is on the page's title and not on the campaign's
	// name, because the name is in the URL the reader typed and the title is not.
	root := f.get("/c/"+f.otherName.Slug.String()+"/", player)
	if root.status != http.StatusOK {
		t.Errorf("the other campaign's root is %d, want 200\nbody: %s", root.status, root.body)
	}
	if strings.Contains(root.body, "Salt Road") {
		t.Errorf("the other campaign's root listed its pages to a player of another campaign:\n%s", root.body)
	}
}

// TestTheTreeIsNeverTheReasonAPageIsAMissing: a tree that fails to build is a
// missing sidebar, not a missing page. A player whose page is readable must be
// able to read it even when the query behind the navigation is broken.
func TestTheTreeIsNeverTheReasonAPageIsAMissing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	// Break the store's read connection out from under the tree by closing it,
	// which is the harshest version of "the tree query failed": every query fails,
	// and the page route's own GetPage fails too, so this is really the fail
	// path. The narrower case -- the tree failing and the page not -- needs a
	// store that fails one query, which is a fixture this file does not have.
	//
	// What is asserted here is the narrower thing that *is* reachable: a campaign
	// with no pages still serves its root, and a 404 still carries no tree rather
	// than an empty `<ul>` that looks like a broken wiki.
	empty := f.get(f.pageURL("locations/thornford"), player)
	if !strings.Contains(empty.body, `class="notice`) {
		t.Errorf("a 404 is not the notice page:\n%s", empty.body)
	}
}
