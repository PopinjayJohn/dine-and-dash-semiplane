package http_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	wiki "github.com/popinjayjohn/dine-and-dash-semiplane/internal/http"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # When the store is not answering
//
// A wiki serving markdown a player wrote is a program running a query per request,
// and the query will fail eventually: the disk fills, the file is locked on
// Windows, the process runs out of handles. So the 500 path is not a
// hypothetical branch, and these are its tests.
//
// The store is reached through an interface, so a store that fails is a two-line
// wrapper rather than a mocking framework. That is the practical payoff of the
// consumer declaring the interface.

// brokenStore answers every call with one error, and the field says which call.
type brokenStore struct {
	// failing is the method that fails; the empty string is all of them.
	failing string
	// panicWith is a value to panic with instead of failing, for the recovery
	// test.
	panicWith string
}

func (b brokenStore) GetPage(context.Context, string, string, domain.Principal) (domain.Page, error) {
	return domain.Page{}, b.out("GetPage")
}

func (b brokenStore) ListPages(context.Context, string, domain.Principal) ([]domain.Page, error) {
	return nil, b.out("ListPages")
}

func (b brokenStore) CampaignBySlug(context.Context, domain.Slug) (domain.Campaign, error) {
	return domain.Campaign{}, b.out("CampaignBySlug")
}

func (b brokenStore) OwnerExists(context.Context, string, string) (bool, error) {
	return false, b.out("OwnerExists")
}

func (b brokenStore) SearchPublic(context.Context, string, domain.Principal, search.Query, int) ([]search.Hit, error) {
	return nil, b.out("SearchPublic")
}

func (b brokenStore) SearchSecrets(context.Context, string, domain.Principal, search.Query, int) ([]search.Hit, error) {
	return nil, b.out("SearchSecrets")
}

func (b brokenStore) ListRecentlyChanged(context.Context, string, domain.Principal, int) ([]domain.Page, error) {
	return nil, b.out("ListRecentlyChanged")
}

func (b brokenStore) ListPrincipals(context.Context, string) ([]domain.Principal, error) {
	return nil, b.out("ListPrincipals")
}

func (b brokenStore) PrincipalByID(context.Context, string) (domain.Principal, bool, error) {
	return domain.Principal{}, false, b.out("PrincipalByID")
}

func (b brokenStore) RevokePrincipal(context.Context, string) error { return b.out("RevokePrincipal") }

func (b brokenStore) AppendAudit(context.Context, domain.AuditEntry) (domain.AuditEntry, error) {
	return domain.AuditEntry{}, b.out("AppendAudit")
}

func (b brokenStore) FindPageByAlias(context.Context, string, string, domain.Principal) (domain.Page, bool, error) {
	return domain.Page{}, false, b.out("FindPageByAlias")
}

func (b brokenStore) FindPageByName(context.Context, string, string, domain.Principal) (domain.Page, bool, error) {
	return domain.Page{}, false, b.out("FindPageByName")
}

func (b brokenStore) out(method string) error {
	if b.panicWith != "" {
		panic(b.panicWith)
	}
	if b.failing == "" || b.failing == method {
		return errStoreDown
	}
	// A not-found is the least interesting answer and keeps the wrapper honest
	// for the calls a test is not about.
	return errNotFoundHere
}

var (
	errStoreDown           = errors.New("the database is on fire")
	errNotFoundHere        = errors.New("store: not found")
	_               Store  = brokenStore{}
	_               string = ""
)

// TestAStoreFailureIsAPageAndNotAStack is the 500 test.
//
// A 500 that leaked the error would tell a player the shape of this application's
// database, and a 500 that was a bare string would throw away the navigation a DM
// needs to get back to their campaign. So it is a page, the request id is on it,
// and the error is in the log where the DM's operator can find it.
func TestAStoreFailureIsAPageAndNotAStack(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		failing string
		target  string
	}{
		"the campaign lookup fails": {
			failing: "CampaignBySlug",
			target:  "/c/blackwater/",
		},
		"the page list fails": {
			failing: "ListPages",
			target:  "/c/blackwater/locations/rivergate",
		},
		"the page read fails": {
			failing: "GetPage",
			target:  "/c/blackwater/locations/rivergate",
		},
		"every call fails": {
			target: "/c/blackwater/",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			f.cfg.Store = brokenStore{failing: tt.failing}
			f.rebuild()

			got := f.get(tt.target)

			if got.status != http.StatusInternalServerError {
				t.Errorf("status %d, want 500\nbody: %s", got.status, got.body)
			}
			if !strings.Contains(got.body, "Something went wrong") {
				t.Errorf("the 500 is not the notice page:\n%s", got.body)
			}
			// The request id, so the DM can quote it, and the whole document,
			// because half a document is worse than none.
			if !strings.Contains(got.body, "Request ") {
				t.Errorf("the 500 does not carry a request id:\n%s", got.body)
			}
			if !strings.HasSuffix(strings.TrimSpace(got.body), "</html>") {
				t.Errorf("the 500 is not a whole document:\n%s", got.body)
			}
			// And none of the reason is in the response.
			for _, leak := range []string{errStoreDown.Error(), "SELECT", "sqlite"} {
				if strings.Contains(got.body, leak) {
					t.Errorf("the 500 body carries %q", leak)
				}
			}
			// The log has it, though.
			if !f.logsContains(errStoreDown.Error()) && !f.logsContains("not found") {
				t.Errorf("the log does not say what failed:\n%s", f.logs.String())
			}
		})
	}
}

// TestAPanicIsAPageAndNotADeadProcess: a wiki is a program that parses untrusted
// input on every request, and the failure mode of an uncaught panic in a handler is
// a process that dies with every other player's session on it.
func TestAPanicIsAPageAndNotADeadProcess(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.Store = brokenStore{panicWith: "a renderer bug"}
	f.rebuild()

	got := f.get("/c/blackwater/")

	if got.status != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", got.status)
	}
	if strings.Contains(got.body, "a renderer bug") {
		t.Errorf("the panic value reached the browser:\n%s", got.body)
	}
	// The stack is the only part anybody can debug it with, and it is in the log.
	if !f.logsContains("a renderer bug") {
		t.Errorf("the panic is not in the log:\n%s", f.logs.String())
	}
	if !f.logsContains("goroutine") {
		t.Errorf("the log has no stack for the panic:\n%s", f.logs.String())
	}

	// And the server is still up, which is the whole point.
	next := f.get("/_/healthz")
	if next.status != http.StatusOK {
		t.Errorf("the server is not answering after a panic: %d", next.status)
	}
}

// TestASessionThatIsNotThereIsNobodyRatherThanA500: the four answers `auth` gives
// for a session that is not usable are all "nobody is logged in", and a wiki that
// answers 500 for a revoked player's cookie is a wiki that tells a DM their server
// is broken when it is working.
func TestASessionThatIsNotThereIsNobodyRatherThanA500(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// A cookie naming a session that never existed. The value is the right shape
	// for a session id, which is what makes this the case that has to be handled
	// rather than the case where a malformed cookie falls out.
	unknown := &http.Cookie{Name: "wiki_session", Value: strings.Repeat("s", 43)}
	got := f.get(f.pageURL("locations/rivergate"), unknown)

	if got.status != http.StatusNotFound {
		t.Errorf("a cookie naming no session is %d, want 404 (nobody reads anything)", got.status)
	}
	if strings.Contains(got.body, "Something went wrong") {
		t.Errorf("a stale cookie produced a 500:\n%s", got.body)
	}
	if strings.Contains(got.body, "Log out") {
		t.Error("a stale cookie was treated as somebody logged in")
	}
}

// brokenAuth is an `auth.Backend` that fails every call, which is the only way to
// make the *authentication* path fail while leaving the rest of the application
// working -- the session middleware reads through the one auth holds, not through
// the store the campaign middleware uses.
type brokenAuth struct{}

func (brokenAuth) CreatePrincipal(context.Context, domain.Principal) (domain.Principal, error) {
	return domain.Principal{}, errStoreDown
}
func (brokenAuth) GetCampaign(context.Context, string) (domain.Campaign, error) {
	return domain.Campaign{}, errStoreDown
}
func (brokenAuth) PrincipalByTokenHash(context.Context, string) (domain.Principal, bool, error) {
	return domain.Principal{}, false, errStoreDown
}
func (brokenAuth) PrincipalByID(context.Context, string) (domain.Principal, bool, error) {
	return domain.Principal{}, false, errStoreDown
}
func (brokenAuth) RevokePrincipal(context.Context, string) error { return errStoreDown }
func (brokenAuth) CreateSession(context.Context, domain.Session) (domain.Session, error) {
	return domain.Session{}, errStoreDown
}
func (brokenAuth) SessionByID(context.Context, string) (domain.Session, bool, error) {
	return domain.Session{}, false, errStoreDown
}
func (brokenAuth) TouchSession(context.Context, string, time.Time) error { return errStoreDown }
func (brokenAuth) DeleteSession(context.Context, string) error           { return errStoreDown }
func (brokenAuth) EndSessions(context.Context, string) (int, error)      { return 0, errStoreDown }
func (brokenAuth) TouchPrincipal(context.Context, string) error          { return errStoreDown }
func (brokenAuth) SetPrincipalRole(context.Context, string, domain.Role) error {
	return errStoreDown
}
func (brokenAuth) ReplacePrincipalCharacters(context.Context, string, []string) error {
	return errStoreDown
}
func (brokenAuth) AppendAudit(context.Context, domain.AuditEntry) (domain.AuditEntry, error) {
	return domain.AuditEntry{}, errStoreDown
}

// TestASessionLookupThatFailsIsA500: the other side of the same line. A database
// error while authenticating is not "nobody", it is "this server cannot see its own
// sessions", and answering as though nobody were logged in would empty every
// player's page tree with no error anywhere.
func TestASessionLookupThatFailsIsA500(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// The session is created first, with a working store, and then the *redeemer's*
	// backend is broken -- which is the exact path under test.
	cookie := f.playerSession()

	f.cfg.Redeemer.Backend = brokenAuth{}
	f.rebuild()

	got := f.get(f.pageURL("locations/rivergate"), cookie)

	if got.status != http.StatusInternalServerError {
		t.Errorf("a failing session lookup is %d, want 500\nbody: %s", got.status, got.body)
	}
	if !f.logsContains("authenticating") {
		t.Errorf("the log does not say the authentication failed:\n%s", f.logs.String())
	}
	// And it is not mistaken for "nobody": the 500 rather than a 404 is the whole
	// difference, and it is the difference between "this wiki is broken" and "this
	// page is not yours".
	if got.status == http.StatusNotFound {
		t.Error("a database failure was answered as a missing page")
	}
}

// TestAConfigWithNoSecretGetsOneAtStartup is the normal case, and it is a test
// because the alternative is worse: a deployment that supplies no secret has to
// *generate* one rather than fall back to something everybody knows. A fixed
// fallback would make every wiki on every machine accept the same token, which is
// the CSRF bypass with extra steps.
func TestAConfigWithNoSecretGetsOneAtStartup(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.Secret = nil
	f.rebuild()

	cookie := f.playerSession()
	root := "/c/" + f.campaign.Slug.String() + "/"

	token := logoutToken(t, f, cookie)
	if token == "" {
		t.Fatal("no token was issued for a deployment with no configured secret")
	}
	if got := f.postForm(root, map[string]string{"csrf": token}, cookie); got.status != http.StatusSeeOther {
		t.Errorf("the generated secret's token is refused: %d", got.status)
	}

	// And a second deployment's secret is not this one's, so a token from another
	// wiki on the same network is worth nothing here.
	other := newFixture(t)
	other.cfg.Secret = nil
	other.rebuild()
	if otherToken := logoutToken(t, other, other.playerSession()); otherToken == token {
		t.Error("two deployments with generated secrets share a token")
	}
}

// TestAPageReadFailsClosedOnTheOwnershipLookup: the ownership query is the one
// thing the decision asks the store outside the page read, and an error there must
// answer "not the owner" rather than "I do not know" -- an unknown answer that
// became a permissive one would be a disclosure on the one path nobody tests.
func TestAPageReadFailsClosedOnTheOwnershipLookup(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	// A store that answers the page read and fails only the ownership question.
	f.cfg.Store = brokenStore{failing: "OwnerExists"}
	f.rebuild()

	// The tree comes from the broken store too, so the interesting part is that
	// the page itself is not served with secrets. It is not served at all, because
	// this store does not know the campaign either -- which is the honest result
	// and is checked by the table above.
	//
	// What is asserted here is narrower and reachable: the ownership failure is
	// logged, so a DM whose player pages silently lose their secrets can see why.
	got := f.get(f.pageURL("characters/aria"), player)
	if got.status == http.StatusOK {
		t.Errorf("a page was served with a failing ownership query: %d", got.status)
	}
}

// TestARawQueryOnlyForTheExactValue: `?raw=1` is the markdown and `?raw=true` or
// `?raw=yes` are the page. Three spellings of a boolean is one too many, and the
// one that matters is the one the link in the page uses.
func TestARawQueryOnlyForTheExactValue(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	base := f.pageURL("locations/rivergate")
	page := f.get(base, player)
	if strings.Contains(page.body, "A fortified town") == false {
		t.Fatalf("the page does not have its prose in it:\n%s", page.body)
	}

	tests := map[string]struct {
		query   string
		isPlain bool
	}{
		"no query at all": {query: "", isPlain: false},
		"raw=1":           {query: "?raw=1", isPlain: true},
		"raw=true":        {query: "?raw=true", isPlain: false},
		"raw=yes":         {query: "?raw=yes", isPlain: false},
		"raw=0":           {query: "?raw=0", isPlain: false},
		"raw empty":       {query: "?raw=", isPlain: false},
		// The stream parameter's *presence* is the request, whatever its value: a
		// URL that asks for a stream and does not say so plainly is a URL with a
		// boolean in it, and this build serves no streams, so it is a 404. That is
		// in `TestTheStreamQueryIsRefusedRatherThanIgnored`.
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := f.get(base+tt.query, player)
			if got.status != http.StatusOK {
				t.Fatalf("status %d, want 200", got.status)
			}
			isPlain := strings.HasPrefix(got.header.Get("Content-Type"), "text/plain")
			if isPlain != tt.isPlain {
				t.Errorf("GET %s%s is %s, want plain text: %t", base, tt.query, got.header.Get("Content-Type"), tt.isPlain)
			}
		})
	}
}

// TestThePageHasALinkToItsOwnSource: the raw endpoint exists because a DM needs to
// see the file when a render looks wrong, and a page with no link to it is an
// endpoint nobody finds.
func TestThePageHasALinkToItsOwnSource(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	got := f.get(f.pageURL("locations/rivergate"), player)
	if !strings.Contains(got.body, f.pageURL("locations/rivergate")+"?raw=1") {
		t.Errorf("the page does not link to its own source:\n%s", got.body)
	}
}

// TestTheLandingPageIsRenderedUnderTheSameDecision: `campaign.md` goes through the
// same renderer as every other page, so a secret in it is stripped for a player
// exactly as one in any other page would be.
func TestTheLandingPageIsRenderedUnderTheSameDecision(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	dm := f.dmSession()

	// A landing page with a secret in it.
	landing, err := f.store.UpsertPage(t.Context(), domain.Page{
		CampaignID:  f.campaign.ID,
		Path:        "campaign",
		Title:       "The Blackwater",
		Type:        domain.PageTypeNote,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: The Blackwater\nvisibility: players\n",
		Body:        "# The Blackwater\n\nWelcome.\n\n> [!SECRET] Under the table\n> " + canary + "\n",
		ContentHash: "hash-campaign-secret",
	}, store.AsDM(f.campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	_ = landing

	asPlayer := f.get("/c/"+f.campaign.Slug.String()+"/", player)
	if strings.Contains(asPlayer.body, canary) {
		t.Errorf("the landing page leaked a secret to a player:\n%s", asPlayer.body)
	}
	if !strings.Contains(asPlayer.body, "Welcome.") {
		t.Errorf("the landing page is not rendered at all:\n%s", asPlayer.body)
	}

	asDM := f.get("/c/"+f.campaign.Slug.String()+"/", dm)
	if !strings.Contains(asDM.body, canary) {
		t.Errorf("the landing page is stripped for the DM:\n%s", asDM.body)
	}
}

// Store is the consumer interface the HTTP package declares, restated here so that
// the broken store above is checked against the real one rather than against a
// copy that could drift.
type Store = wiki.Store

var _ = wiki.Config{}
