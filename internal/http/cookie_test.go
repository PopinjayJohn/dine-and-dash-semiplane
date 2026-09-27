package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// # The cookie
//
// ADR 0003 fixed the attributes and this is the test that says the two
// deployments get the two sets of attributes that actually work. It is early in
// this file because the failure is silent: a browser that refuses a cookie does
// not report it, so a player sees a wiki that has forgotten them.

// TestTheCookieCarriesADR0003sAttributes is the attribute test, and it runs for
// both deployments.
//
// Every attribute is asserted, because the point is not that the cookie is
// refused but that it carries what a credential is supposed to carry, and a cookie
// with four of the five is one nobody notices until a browser decides to be strict.
func TestTheCookieCarriesADR0003sAttributes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		production bool
		wantName   string
		wantSecure bool
	}{
		"a production deployment": {
			production: true,
			wantName:   "__Host-wiki_session",
			wantSecure: true,
		},
		// The name is not prefixed here, and that is the whole point: a browser
		// refuses a `__Host-` cookie without `Secure`, silently, so a local
		// deployment that kept the prefix would be a wiki where every redemption
		// works and no page is ever readable.
		"a plain-HTTP deployment": {
			production: false,
			wantName:   "wiki_session",
			wantSecure: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixtureIn(t, tt.production)
			session := f.redeem(f.playerLink.Token)

			if session.Name != tt.wantName {
				t.Errorf("the cookie is named %q, want %q", session.Name, tt.wantName)
			}
			if !session.HttpOnly {
				t.Error("the cookie is not HttpOnly, so a script on the page can read the credential")
			}
			if session.SameSite != http.SameSiteLaxMode {
				t.Errorf("SameSite is %v, want Lax", session.SameSite)
			}
			if session.Secure != tt.wantSecure {
				t.Errorf("Secure is %t, want %t", session.Secure, tt.wantSecure)
			}
			if session.Path != "/" {
				t.Errorf("Path is %q, want /", session.Path)
			}
			if session.Domain != "" {
				t.Errorf("the cookie has Domain %q, and a __Host- cookie may not have one", session.Domain)
			}
			if strings.HasPrefix(session.Name, "__Host-") && !session.Secure {
				t.Error("a __Host- cookie without Secure is refused by every browser, silently")
			}

			// The expiry is the session's own rather than the browser's, so a player
			// who puts their phone down mid-session is still logged in.
			if session.Expires.IsZero() {
				t.Error("the cookie has no expiry, so it is a browser session cookie")
			}
			if session.MaxAge <= 0 {
				t.Errorf("Max-Age is %d, want the session's own lifetime", session.MaxAge)
			}
		})
	}
}

// TestTheCookieIsSentBackAndWorks: the cookie the redemption sets is the one the
// session middleware reads. A cookie written with one name and read with another is
// a wiki where every redemption works and no page is ever readable, and the only
// symptom is an empty tree.
func TestTheCookieIsSentBackAndWorks(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	cookie := f.playerSession()

	anonymous := f.get(f.pageURL("locations/rivergate"))
	if strings.Contains(anonymous.body, "Log out") {
		t.Error("a request with no cookie has a logout form in it")
	}

	identified := f.get(f.pageURL("locations/rivergate"), cookie)
	if !strings.Contains(identified.body, "Log out") {
		t.Errorf("a request with a cookie has no logout form in it:\n%s", identified.body)
	}
}

// TestTheCookieIsNeverInTheBody: a credential in a page is a credential in the
// player's cache, in a `Ctrl-F`, in a screenshot, and in every proxy that ever
// sees the response.
func TestTheCookieIsNeverInTheBody(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	cookie := f.playerSession()

	for _, target := range []string{
		f.pageURL("locations/rivergate"),
		f.pageURL("locations/rivergate") + "?raw=1",
		"/c/" + f.campaign.Slug.String() + "/",
	} {
		got := f.get(target, cookie)
		if strings.Contains(got.body, cookie.Value) {
			t.Errorf("GET %s printed the session id in the body", target)
		}
	}
}

// TestTheCookieCarriesTheSessionBoundAndNotTheBrowser's: the two windows in §10
// are one policy -- twelve hours idle, thirty days absolute -- and they are
// enforced in different places. The *idle* window is the store's, checked on every
// request; the *absolute* window is the cookie's, because after it the browser
// stops offering the credential and no server-side rule can bring it back.
//
// So `Max-Age` is the session's remaining lifetime and not the idle window. A
// cookie carrying the idle window would log an active player out every twelve
// hours no matter how recently they had used the wiki, which is a bug a DM would
// report as "the wiki forgets me" and which no test of the store would catch.
func TestTheCookieCarriesTheSessionBoundAndNotTheBrowsers(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	session := f.redeem(f.playerLink.Token)

	const lifetime = 30 * 24 * time.Hour
	got := time.Duration(session.MaxAge) * time.Second
	if got <= 0 {
		t.Fatalf("Max-Age is %s", got)
	}
	if got != lifetime {
		t.Errorf("the cookie lives for %s, want the session's %s bound", got, lifetime)
	}
	if session.Expires.IsZero() || !session.Expires.After(fixedNow) {
		t.Errorf("Expires is %s, which is not after the moment the cookie was set", session.Expires)
	}
}

// TestTwoCookiesWithOneNameIsNobody: a hand-written client can send two cookies
// with the same name and a browser will not, and resolving that by picking one is
// how a session fixation gets to choose which session a request uses.
//
// The answer is no session at all, and the assertion is that the request is
// answered *exactly* as one with no cookie is -- same status, same bytes. That is
// stronger than asserting "the DM's page is not there", because it also catches a
// handler that resolves the ambiguity in the other direction.
func TestTwoCookiesWithOneNameIsNobody(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	player := f.playerSession()

	anonymous := f.get(f.pageURL("campaign"))
	ambiguous := f.request(http.MethodGet, f.pageURL("campaign"), nil, dm, player)

	if ambiguous.status != anonymous.status || ambiguous.stable() != anonymous.stable() {
		t.Errorf("a request with two session cookies is not the same answer as one with none\n status: %d vs %d\n body: %s\n vs: %s",
			ambiguous.status, anonymous.status, ambiguous.stable(), anonymous.stable())
	}
}
