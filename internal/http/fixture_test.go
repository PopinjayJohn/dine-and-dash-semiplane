package http_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	wiki "github.com/popinjayjohn/dine-and-dash-semiplane/internal/http"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/version"
)

// # The fixture
//
// A real data directory with a real store in it, and a real campaign with pages
// of every audience, because the properties this package is tested for are all
// about what a *real* principal can read and a hand-built fake store would agree
// with whatever the test happened to implement.
//
// The canary is a `[!SECRET]` in a page a player may read, which is the hardest
// case there is: the page comes back, and the secret inside it must not.

const (
	canary     = "ULTRAMARINE-FOXTRAP-7742"
	playerName = "aria"
)

// fixedNow is the clock every test in this file starts at, so that a cookie's
// `Max-Age` and a session's expiry are the same numbers every run.
var fixedNow = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

// fakeClock is a clock a test can move.
//
// It is not `time.Now()` and it is not a `clock.Fixed`, because a duration has
// two ends: the moment the application started and the moment the answer was
// asked for. Reading both from one movable value is the only way to assert "the
// uptime is a minute" rather than "the uptime is a string with a letter in it".
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) advance(by time.Duration) { c.at = c.at.Add(by) }

type fixture struct {
	t *testing.T

	// hands is the clock the application and auth both read, and the only one a
	// test can move. A duration can only be asserted when both of its ends are
	// readable, and that needs a clock rather than a constant.
	hands *fakeClock

	handler http.Handler
	store   *store.Store
	logs    *strings.Builder

	campaign  domain.Campaign
	otherName domain.Campaign

	// ariaID is the character page the player is bound to, which the ownership
	// tests need as an owner column value.
	ariaID string

	// hub is the fixture's, because the hub is the caller's -- the same rule that
	// says the caller closes the store it opened. A test that wants to publish a
	// change publishes it on this one.
	hub *sse.Hub

	dmLink     auth.Issued
	playerLink auth.Issued
	otherLink  auth.Issued
	revoked    auth.Token

	// cfg is the configuration the handler was built from, kept so a test can
	// change one field and rebuild. It is a copy of what `New` was given, because
	// the handler holds its own.
	cfg wiki.Config
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	return newFixtureIn(t, false)
}

// newFixtureIn is the fixture with the deployment shape chosen, because the cookie
// attributes are the one thing about a session that differ between a laptop and a
// server, and a test that only ever saw one of them would be a test of one of
// them.
func newFixtureIn(t *testing.T, production bool) *fixture {
	t.Helper()

	return newFixtureAt(t, fixedNow, production)
}

// newFixtureAt is the fixture with the clock moved, which is how the uptime test
// gets a known value out of a duration rather than a shape.
func newFixtureAt(t *testing.T, now time.Time, production bool) *fixture {
	t.Helper()

	root := t.TempDir()
	ctx := context.Background()
	s, err := store.Open(ctx, datadir.DatabaseFile(root), store.Options{
		Clock: clock.NewFixed(fixedNow, time.Minute),
		IDGen: idgen.NewSequence("id"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	campaign := mustCampaign(t, s, "blackwater", "The Blackwater")
	other := mustCampaign(t, s, "thornford", "Thornford")

	f := &fixture{t: t, store: s, logs: &strings.Builder{}, campaign: campaign, otherName: other}
	f.hands = &fakeClock{at: now}

	f.writePages()

	// Thirty days is the spec's outer bound, and the link and the session share
	// it -- one policy in one place, which is what `auth.Config`'s own comment is
	// about. A player who comes to the table every week should not be logged out
	// mid-session, and a share link should not be a permanent key.
	authCfg := auth.Config{
		BaseURL:         "https://wiki.example",
		Now:             f.hands.now,
		LinkExpiry:      30 * 24 * time.Hour,
		SessionLifetime: 30 * 24 * time.Hour,
	}

	f.dmLink = mustIssue(t, s, authCfg, campaign, domain.RoleDM, "the DM")
	f.playerLink = mustIssue(t, s, authCfg, campaign, domain.RolePlayer, playerName)

	// A revoked link, for the page that says a link has been revoked rather than
	// "not valid".
	revoked := mustIssue(t, s, authCfg, campaign, domain.RolePlayer, "the one who left")
	if err := s.RevokePrincipal(ctx, revoked.Principal.ID); err != nil {
		t.Fatalf("RevokePrincipal: %v", err)
	}
	f.revoked = revoked.Token

	// The binding comes after the link, because a binding names a principal and
	// the principal only exists once the link has been issued.
	f.bindPlayerToAria()

	// A second campaign with a link of its own, for the tests that need a
	// principal who is of somewhere else.
	f.otherLink = mustIssue(t, s, authCfg, other, domain.RolePlayer, "a player of Thornford")

	f.hub = sse.NewHub(wiki.DefaultStreams)
	f.cfg = wiki.Config{
		Store:      s,
		Redeemer:   auth.Redeemer{Backend: s, Config: authCfg},
		Hub:        f.hub,
		Version:    version.Get(),
		Now:        f.hands.now,
		Logger:     slog.New(slog.NewJSONHandler(f.logs, nil)),
		Production: production,
		Secret:     bytes.Repeat([]byte("k"), 32),
	}
	f.handler = mustHandler(t, f.cfg)

	return f
}

// rebuild re-assembles the handler after a test has changed the configuration, so
// that a test can vary one field without rebuilding the whole fixture.
func (f *fixture) rebuild() { f.handler = mustHandler(f.t, f.cfg) }

// newRequest builds a request with a form body, for the tests that need to set a
// header of their own.
func (f *fixture) newRequest(method, target, form string, cookies ...*http.Cookie) *http.Request {
	f.t.Helper()

	req := httptest.NewRequestWithContext(f.t.Context(), method, target, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
	return req
}

// serve runs a request through the handler and reads the response back.
func (f *fixture) serve(req *http.Request) response {
	f.t.Helper()

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return response{
		status:  rec.Code,
		body:    rec.Body.String(),
		header:  rec.Result().Header,
		cookies: rec.Result().Cookies(),
	}
}

// redeemIn exchanges a link for a session in a named campaign, which is how a test
// gets a principal who is of somewhere else.
func (f *fixture) redeemIn(t *testing.T, campaign domain.Campaign, link auth.Issued) *http.Cookie {
	t.Helper()

	got := f.get("/c/" + campaign.Slug.String() + "/?k=" + link.Token.Hex())
	if got.status != http.StatusSeeOther {
		t.Fatalf("redeeming in %s: status %d, want 303\nbody: %s", campaign.Slug, got.status, got.body)
	}
	for _, cookie := range got.cookies {
		if strings.Contains(cookie.Name, "wiki_session") {
			return cookie
		}
	}
	t.Fatalf("redeeming in %s set no session cookie, only %v", campaign.Slug, got.cookies)
	return nil
}

// storeAsDM is the DM principal a test needs in order to write a page, which is
// the only way to put one in the store.
func storeAsDM(campaignID string) domain.Principal { return store.AsDM(campaignID) }

func (f *fixture) writePages() {
	t := f.t
	ctx := context.Background()

	pages := []domain.Page{
		{
			Path:        "locations/rivergate",
			Title:       "Rivergate",
			Type:        domain.PageTypeLocation,
			Visibility:  domain.VisibilityPlayers,
			Frontmatter: "title: Rivergate\nvisibility: players\n",
			Body: "# Rivergate\n\nA fortified town on the confluence.\n\n" +
				"See [[locations/thornford]] across the water.\n",
			ContentHash: "hash-rivergate",
		},
		{
			// The hard case: readable by a player, with a secret inside it.
			Path:        "sessions/09-the-dragon-heist",
			Title:       "Session 9: The Dragon Heist",
			Type:        domain.PageTypeSessionLog,
			Visibility:  domain.VisibilityPlayers,
			Frontmatter: "title: Session 9: The Dragon Heist\nvisibility: players\n",
			Body: "# Session 9: The Dragon Heist\n\nThey tried the postern gate.\n\n" +
				"> [!SECRET] The vault\n> The key is " + canary + ".\n\n" +
				"> [!SECRET]- Shown later\n> The wyrm answers to the canary too.\n",
			ContentHash: "hash-session-nine",
		},
		{
			Path:        "npcs/vel",
			Title:       "Captain Vell",
			Type:        domain.PageTypeNPC,
			Visibility:  domain.VisibilityDMOnly,
			Frontmatter: "title: Captain Vell\nvisibility: dm-only\n",
			Body:        "# Captain Vell\n\n" + canary + " is the canary, and this page is DM's only.\n",
			ContentHash: "hash-vel",
		},
		{
			// A character page, which is its own owner -- the second write below
			// says so, because the owner column holds a page id and a page is not
			// one until it has been stored.
			Path:        "characters/aria",
			Title:       "Aria",
			Type:        domain.PageTypeCharacter,
			Visibility:  domain.VisibilityDMAndOwner,
			Frontmatter: "title: Aria\ncharacter: aria\nvisibility: dm-and-owner\n",
			Body:        "# Aria\n\nAria's own notes, which are hers.\n",
			ContentHash: "hash-aria",
		},
		{
			Path:        "campaign",
			Title:       "The Blackwater",
			Type:        domain.PageTypeNote,
			Visibility:  domain.VisibilityPlayers,
			Frontmatter: "title: The Blackwater\nvisibility: players\n",
			Body:        "# The Blackwater\n\nWelcome. The party is Aria and the collector.\n",
			ContentHash: "hash-campaign",
		},
	}

	for _, page := range pages {
		page.CampaignID = f.campaign.ID
		stored, err := f.store.UpsertPage(ctx, page, store.AsDM(f.campaign.ID))
		if err != nil {
			t.Fatalf("UpsertPage(%q): %v", page.Path, err)
		}
		if page.Path == "characters/aria" {
			// The owner column is a page id, and a page is its own owner, so
			// this is the second write that says so.
			if _, err := f.store.UpsertPage(ctx, domain.Page{
				CampaignID:           f.campaign.ID,
				Path:                 stored.Path,
				Title:                stored.Title,
				Type:                 stored.Type,
				Visibility:           stored.Visibility,
				OwnerCharacterPageID: stored.ID,
				Frontmatter:          stored.Frontmatter,
				Body:                 stored.Body,
				ContentHash:          stored.ContentHash,
			}, store.AsDM(f.campaign.ID)); err != nil {
				t.Fatalf("UpsertPage for the character owner: %v", err)
			}
		}
	}
}

// bindPlayerToAria binds the player to Aria's page, which is what makes the
// `dm-and-owner` audience mean something for her: a page a player owns is hers to
// read and its secrets hers to see.
func (f *fixture) bindPlayerToAria() {
	f.t.Helper()

	aria, err := f.store.GetPage(f.t.Context(), f.campaign.ID, "characters/aria", store.AsDM(f.campaign.ID))
	if err != nil {
		f.t.Fatalf("GetPage for Aria: %v", err)
	}
	if err := f.store.ReplacePrincipalCharacters(f.t.Context(), f.playerLink.Principal.ID, []string{aria.ID}); err != nil {
		f.t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}
	f.ariaID = aria.ID
}

func mustCampaign(t *testing.T, s *store.Store, slug, name string) domain.Campaign {
	t.Helper()

	campaign, err := s.CreateCampaign(context.Background(), domain.Campaign{
		Slug:     domain.Slug(slug),
		Name:     name,
		VaultDir: "vault/" + slug,
	})
	if err != nil {
		t.Fatalf("CreateCampaign(%q): %v", slug, err)
	}
	return campaign
}

func mustIssue(t *testing.T, backend auth.Backend, cfg auth.Config, campaign domain.Campaign, role domain.Role, label string) auth.Issued {
	t.Helper()

	issued, err := auth.Minter{Backend: backend, Config: cfg}.Issue(context.Background(), campaign, role, label)
	if err != nil {
		t.Fatalf("Issue(%s): %v", role, err)
	}
	return issued
}

func mustHandler(t *testing.T, cfg wiki.Config) http.Handler {
	t.Helper()

	handler, err := wiki.New(cfg)
	if err != nil {
		t.Fatalf("wiki.New: %v", err)
	}
	return handler
}

// # Requests

// response is what a test reads back. Three things a handler test always wants
// together, and a struct rather than three return values because every one of
// these tests wants all three and none of them reads them in isolation.
type response struct {
	status  int
	body    string
	header  http.Header
	cookies []*http.Cookie
}

// stable is the body with the two per-response values removed: the CSP nonce and
// the request id.
//
// Two responses to the same request must be the same bytes, and they are not,
// because a nonce that repeats is a nonce that authorises a script an attacker
// injected into an earlier response. So a test comparing two bodies has to take
// those two out by hand, and this is where the hand is.
func (r response) stable() string {
	body := r.body
	for _, value := range nonceValues(r.body) {
		body = strings.ReplaceAll(body, "nonce=\""+value+"\"", `nonce=""`)
	}
	return requestIDPattern.ReplaceAllString(body, `id=""`)
}

var (
	noncePattern     = regexp.MustCompile(`nonce="[^"]*"`)
	requestIDPattern = regexp.MustCompile(`Request [A-Za-z0-9_-]{8,}`)
)

func nonceValues(body string) []string {
	var found []string
	for _, match := range noncePattern.FindAllString(body, -1) {
		found = append(found, strings.TrimSuffix(strings.TrimPrefix(match, `nonce="`), `"`))
	}
	return found
}

// newRecorder is the recorder `request` uses, exposed for the one test that needs
// to set a header of its own before making the request.
func newRecorder(_ *fixture) *httptest.ResponseRecorder {
	return httptest.NewRecorder()
}

// logsContains is whether the request log holds a string, and it is a function
// rather than a field read because two of the tests that use it are the ones
// asserting the log *does not* have something, and they should read the same way
// as the ones asserting it does.
func (f *fixture) logsContains(needle string) bool { return strings.Contains(f.logs.String(), needle) }

func (f *fixture) get(target string, cookies ...*http.Cookie) response {
	f.t.Helper()
	return f.request(http.MethodGet, target, nil, cookies...)
}

// postForm posts a form the way a browser does, which means the content type is
// part of the request: `r.ParseForm` only looks at a body that says it is a form,
// so a POST without the header is a POST with an empty `PostForm` whatever is in
// it. A test that omitted it would find every token refused and conclude the CSRF
// check works.
func (f *fixture) postForm(target string, form map[string]string, cookies ...*http.Cookie) response {
	f.t.Helper()

	values := make([]string, 0, len(form))
	for name, value := range form {
		values = append(values, name+"="+url.QueryEscape(value))
	}

	req := f.newRequest(http.MethodPost, target, strings.Join(values, "&"), cookies...)
	return f.serve(req)
}

// request takes an io.Reader rather than a *strings.Reader so that a GET can
// pass nil, which is what a GET has.
func (f *fixture) request(method, target string, body io.Reader, cookies ...*http.Cookie) response {
	f.t.Helper()

	req := httptest.NewRequestWithContext(f.t.Context(), method, target, body)
	for _, cookie := range cookies {
		// A nil cookie is how the table above says "this surface is reached with
		// no session", and adding it would be a panic rather than a request.
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}

	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)

	result := rec.Result()
	return response{
		status:  result.StatusCode,
		body:    rec.Body.String(),
		header:  result.Header,
		cookies: result.Cookies(),
	}
}

// redeem performs the redemption and returns the cookie, which is how every test
// after the cookie's own gets a session.
//
// It goes through the real route rather than setting the cookie by hand, so a
// test that depends on a session depends on the route that creates one. The
// 303's cookie and nothing else is used: a test that wanted to bypass redemption
// would be testing a session the application cannot produce.
func (f *fixture) redeem(token auth.Token) *http.Cookie {
	f.t.Helper()

	got := f.get("/c/" + f.campaign.Slug.String() + "/?k=" + token.Hex())
	if got.status != http.StatusSeeOther {
		f.t.Fatalf("redeeming: status %d, want 303\nbody: %s", got.status, got.body)
	}
	for _, cookie := range got.cookies {
		if cookie.Name != "" && strings.Contains(cookie.Name, "wiki_session") {
			return cookie
		}
	}
	f.t.Fatalf("redeeming set no session cookie, only %v", got.cookies)
	return nil
}

func (f *fixture) dmSession() *http.Cookie {
	f.t.Helper()
	return f.redeem(f.dmLink.Token)
}

func (f *fixture) playerSession() *http.Cookie {
	f.t.Helper()
	return f.redeem(f.playerLink.Token)
}

// pageURL is a page's address, and it goes through the same rule the renderer
// uses so that a test and the application cannot disagree about where a page is.
func (f *fixture) pageURL(path string) string {
	return "/c/" + f.campaign.Slug.String() + "/" + path
}
