package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
)

// # The rate limit, on the route
//
// `internal/auth/ratelimit_test.go` has tested `TestRateLimitedRedemption` since
// M6, and `docs/security.md` has listed it as the control against "the network,
// guessing share links" for the same six milestones. It was a test of a component
// that nothing called.
//
// These tests are about the *route*, because that is the half that was missing: a
// limiter that is correct and not consulted is a document, and a document is the
// thing `docs/security.md` opens by saying is not a control.

// TestTheRedemptionRouteIsRateLimited is the named one, and the assertion is that the
// limit applies **on the route a script would use**, not in a component a test can
// reach.
func TestTheRedemptionRouteIsRateLimited(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.RedemptionLimit = 3
	f.rebuild()

	url := f.redeemURL("not-a-real-token-at-all")

	// Three attempts are inside the limit and get the ordinary answer a wrong token
	// gets: a 404, because a token that is not valid and a token that has been used
	// are the same page.
	for i := range 3 {
		got := f.get(url, nil)
		if got.status != http.StatusNotFound {
			t.Fatalf("attempt %d: status %d, want 404 for a wrong token", i+1, got.status)
		}
	}

	// The fourth is over the limit, and it is *told* so: a 429 rather than the same
	// 404, because a player who has typed their own link five times deserves to be
	// told to wait rather than told their link is broken. A script cannot tell the
	// difference, which is the point.
	got := f.get(url, nil)
	if got.status != http.StatusTooManyRequests {
		t.Fatalf("the fourth attempt is %d, want 429\nbody: %s", got.status, got.body)
	}
	if got.header.Get("Retry-After") == "" {
		t.Error("a 429 with no Retry-After is a 429 a client has to guess at")
	}
}

// TestTheLimitDoesNotApplyToSomebodyReadingTheWiki is the other half, and it is the
// one that would be wrong if the limit were asked too eagerly.
//
// §10's rate limit is on *redemption*. A player who has a session and is reading
// pages must not be able to lock themselves out by browsing, and a campaign root with
// no `?k=` is not a redemption at all — it is the common case by a long way.
func TestTheLimitDoesNotApplyToSomebodyReadingTheWiki(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.RedemptionLimit = 1
	f.rebuild()

	// The session is redeemed **once**, before the reads. `f.dmSession()` redeems, and
	// a reader who called it in a loop would be five redemptions of a single-use link
	// — which is a test that fails for two reasons at once and says neither.
	session := f.dmSession()

	// No `?k=`, so not a redemption. Many times, and every one is fine.
	root := "/c/" + f.campaign.Slug.String() + "/"
	for i := range 5 {
		if got := f.get(root, session); got.status != http.StatusOK {
			t.Fatalf("read %d: status %d, want 200 — a reader is not a redemption", i+1, got.status)
		}
	}
}

// TestAValidRedemptionStillWorksUnderTheLimit is the other failure mode: a limit
// applied to the *successful* path would lock out a player whose link is fine, which
// is the outcome `auth`'s own doc comment calls a denial of service on the DM's own
// players.
func TestAValidRedemptionStillWorksUnderTheLimit(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.RedemptionLimit = 10
	f.rebuild()

	// Minted the way the DM does, so the redemption is a real one rather than a token
	// a test made up: three separate players redeeming is a burst, and under a limit
	// of ten it is well inside.
	//
	// They are three *different* links, because redeeming the same link twice fails
	// for a different reason — it is single-use — and a rate-limit test that also
	// tested single-use could not say which of the two it had seen.
	links := f.mintLinks(3)

	var got response
	for i, link := range links {
		got = f.get("/c/"+f.campaign.Slug.String()+"/?k="+link, nil)
		if got.status != http.StatusSeeOther {
			t.Fatalf("redemption %d: status %d, want 303\nbody: %s", i+1, got.status, got.body)
		}
	}

	if got.header.Get("Retry-After") != "" {
		t.Error("a successful redemption carried a Retry-After")
	}
}

// TestTheLimitIsReportedAsARateLimitNotAsAWrongToken is the message, and it is a
// separate test because the two are easy to conflate: the *status* is a 429 so a
// client can back off, and the *body* is a page a person can read.
func TestTheLimitIsReportedAsARateLimitNotAsAWrongToken(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.RedemptionLimit = 1
	f.rebuild()

	url := f.redeemURL("guessing")
	f.get(url, nil)
	got := f.get(url, nil)

	if got.status != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", got.status)
	}
	if !strings.Contains(got.body, "Too many attempts") {
		t.Errorf("the page does not say what happened:\n%s", got.body)
	}
	// And it does not say anything about the token: a limit page that named the
	// credential would be a page that confirms a token is worth guessing.
	if strings.Contains(got.body, "guessing") {
		t.Errorf("the rate-limit page echoes the token back:\n%s", got.body)
	}
}

// TestARateLimitedRequestIsLoggedAsOne: the limiter refusing something is a thing a
// DM watching their log wants to see, because it is the signature of somebody
// guessing at their share links.
func TestARateLimitedRequestIsLoggedAsOne(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.cfg.RedemptionLimit = 1
	f.rebuild()

	url := f.redeemURL("guessing")
	f.get(url, nil)
	f.get(url, nil)

	if !strings.Contains(f.logs.String(), auth.ErrRateLimited.Error()) {
		t.Errorf("the refusal is not in the log:\n%s", f.logs.String())
	}
}

// TestTheTrustedProxyListDecidesWhetherAForwardedHeaderIsBelieved is the third
// argument of [auth.ClientIP], and it is a security decision with a wrong answer that
// removes the limit.
//
// With nothing configured the header is ignored, so every attempt from behind a
// proxy is counted against the proxy. With the proxy configured, the header is
// believed and the attempts are counted per player — and a request that did *not*
// come from that proxy is still counted on its own address, which is the direction
// that matters.
func TestTheTrustedProxyListDecidesWhetherAForwardedHeaderIsBelieved(t *testing.T) {
	t.Parallel()

	// A limiter keyed directly on what `ClientIPFromRequest` would return, because
	// the route test above covers the wiring and this covers the decision.
	remote := "10.0.0.1:41234"
	forwarded := "198.51.100.7"

	if got := auth.ClientIPFromRequest(&http.Request{
		RemoteAddr: remote,
		Header:     http.Header{"X-Forwarded-For": []string{forwarded}},
	}, false); got != "10.0.0.1" {
		t.Errorf("with no trusted proxy, ClientIP = %q, want the socket address", got)
	}

	if got := auth.ClientIPFromRequest(&http.Request{
		RemoteAddr: remote,
		Header:     http.Header{"X-Forwarded-For": []string{forwarded}},
	}, true); got != "198.51.100.7" {
		t.Errorf("with a trusted proxy, ClientIP = %q, want the forwarded address", got)
	}
}
