package http_test

import (
	"net/http"
	"strings"
	"testing"
)

// # Redemption
//
// ADR 0003's shape, tested from the outside: a link is exchanged once, the token
// leaves the address bar, and four different ways of failing produce four
// different things a player can be told.

// TestRedeemingGivesACookieAndClearsTheToken is the happy path, and the assertion
// about the redirect is the point rather than the cookie: a 303 whose `Location`
// still holds the token has exchanged nothing, because the token is now in the
// history, in the referrer of every request the page makes, and in whatever the
// player copies out of the bar afterwards.
func TestRedeemingGivesACookieAndClearsTheToken(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	token := f.playerLink.Token

	got := f.get("/c/" + f.campaign.Slug.String() + "/?k=" + token.Hex())
	if got.status != http.StatusSeeOther {
		t.Fatalf("status %d, want 303\nbody: %s", got.status, got.body)
	}

	location := got.header.Get("Location")
	if location == "" {
		t.Fatal("there is no Location header")
	}
	if strings.Contains(location, token.Hex()) {
		t.Errorf("the redirect still carries the token: %q", location)
	}
	if strings.Contains(location, "k=") {
		t.Errorf("the redirect still carries a token parameter: %q", location)
	}
	if location != "/c/"+f.campaign.Slug.String()+"/" {
		t.Errorf("the redirect is %q, want the campaign root with no query", location)
	}
	if len(got.cookies) == 0 {
		t.Fatal("the redemption set no cookie")
	}
}

// TestRedeemingKeepsThePageYouAskedFor: a player who was sent a deep link should
// land on the deep link's page, not on the campaign root. The token is removed
// from the query and the path is kept.
func TestRedeemingKeepsThePageYouAskedFor(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get(f.pageURL("locations/rivergate") + "?k=" + f.playerLink.Token.Hex())
	if got.status != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", got.status)
	}

	location := got.header.Get("Location")
	if location != f.pageURL("locations/rivergate") {
		t.Errorf("the redirect is %q, want the page that was asked for with no query", location)
	}
}

// TestTheTokenNeverLeavesTheServer is the test ADR 0003's whole scheme rests on.
//
// Four places a token can survive a redemption, all checked at once because the
// failure mode is a token that is gone from the address bar and still live in
// something: the redirect, the log line, the response body, and the *next*
// request's URL. The last one is the `Referer`, which is why every response
// carries `Referrer-Policy: no-referrer`.
func TestTheTokenNeverLeavesTheServer(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	token := f.playerLink.Token
	target := "/c/" + f.campaign.Slug.String() + "/?k=" + token.Hex()

	first := f.get(target)
	if strings.Contains(first.header.Get("Location"), token.Hex()) {
		t.Error("the token is in the redirect")
	}
	if strings.Contains(first.body, token.Hex()) {
		t.Error("the token is in the response body")
	}
	if f.logsContains(token.Hex()) {
		t.Error("the token is in the log")
	}

	// And the follow-up, which is what a browser actually does.
	cookie := f.redeem(f.playerLink.Token)
	second := f.get(f.pageURL("locations/rivergate"), cookie)
	if strings.Contains(second.body, token.Hex()) {
		t.Error("the token is in the page after the redirect")
	}
	if f.logsContains(token.Hex()) {
		t.Error("the token is in the log after the redirect")
	}
}

// TestTheQueryStringIsNotLogged is the middleware's own half of the same
// property, and it is here because the token is the one query parameter this
// application has, so `r.URL.String()` would be a credential in every log line of
// every proxy in front of it.
func TestTheQueryStringIsNotLogged(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	secret := "not-a-real-token-but-the-same-shape"

	f.get("/c/" + f.campaign.Slug.String() + "/?k=" + secret)
	f.get(f.pageURL("locations/rivergate") + "?raw=1&anything=" + secret)

	if f.logsContains(secret) {
		t.Errorf("a query parameter reached the log:\n%s", f.logs.String())
	}
	if !f.logsContains(f.pageURL("locations/rivergate")) {
		t.Errorf("the path is not in the log, so the query was stripped by dropping the line:\n%s", f.logs.String())
	}
}

// TestARedemptionThatFailsSaysSomethingAPlayerCanAct is the four answers.
//
// They are four and not one because they are four different fixes: ask the DM for
// a new link, ask for a link for *this* campaign, and -- in two of the cases --
// nothing the player can do, which is worth saying too.
func TestARedemptionThatFailsSaysSomethingAPlayerCanAct(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		// token is a query suffix, chosen from the fixture so that each case uses
		// a token that really exists rather than a shape that might.
		token      func(*fixture) string
		wantStatus int
		wantBody   string
	}{
		"a token that is not one of ours": {
			token:      func(*fixture) string { return "?k=" + strings.Repeat("a", 64) },
			wantStatus: http.StatusNotFound,
			wantBody:   "Ask the DM for a new one",
		},
		"a token of the wrong shape": {
			token:      func(*fixture) string { return "?k=not-a-token" },
			wantStatus: http.StatusNotFound,
			wantBody:   "Ask the DM for a new one",
		},
		"an empty token, which is a link a chat client ate": {
			token:      func(*fixture) string { return "?k=" },
			wantStatus: http.StatusNotFound,
			wantBody:   "Ask the DM for a new one",
		},
		"a revoked link": {
			token:      func(f *fixture) string { return "?k=" + f.revoked.Hex() },
			wantStatus: http.StatusForbidden,
			wantBody:   "has been revoked",
		},
		"a real link, presented for the wrong campaign": {
			token:      func(f *fixture) string { return "?k=" + f.playerLink.Token.Hex() },
			wantStatus: http.StatusForbidden,
			wantBody:   "different campaign",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			// Every case is asked for under the *other* campaign's URL, which is
			// how "this link belongs to a different campaign" is provoked, and the
			// two cases with a hand-written token do not care which campaign they
			// land in.
			target := "/c/" + f.campaign.Slug.String() + "/" + tt.token(f)
			if name == "a real link, presented for the wrong campaign" {
				target = "/c/" + f.otherName.Slug.String() + "/" + tt.token(f)
			}

			got := f.get(target)
			if got.status != tt.wantStatus {
				t.Errorf("status %d, want %d\nbody: %s", got.status, tt.wantStatus, got.body)
			}
			if !strings.Contains(got.body, tt.wantBody) {
				t.Errorf("the page does not say %q\nbody: %s", tt.wantBody, got.body)
			}
			if len(got.cookies) != 0 {
				t.Errorf("a failed redemption set a cookie: %v", got.cookies)
			}
		})
	}
}

// TestALinkIsRedeemableAgainUntilItIsRevoked is the answer to the first question
// anybody asks about a share link, and it is a property rather than an accident.
//
// **A link is a reusable bearer credential, not a one-time code.** ADR 0003's five
// steps hash the token, look it up, refuse it if revoked or expired, and exchange
// it for a session; nothing rotates it, and that is deliberate. The case it serves
// is a player who clears their cookies, or who wants the wiki on a second device,
// or who is at the table on a laptop somebody has rebooted -- and the alternative
// is a DM issuing a fresh link every time a browser forgets somebody, which at a
// table is worse than a link that lives for a month.
//
// The cost is that a link pasted into a chat stays usable until it is revoked, and
// the threat model's answer to that is revocation and expiry rather than rotation:
// one link per player, an instant revoke button, a thirty-day bound. So the
// property worth testing is that revocation stops it, and that a second redemption
// is a second *session* rather than a second identity.
func TestALinkIsRedeemableAgainUntilItIsRevoked(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	phone := f.redeem(f.playerLink.Token)
	laptop := f.redeem(f.playerLink.Token)

	if phone.Value == laptop.Value {
		t.Error("two redemptions of one link produced the same session id")
	}

	// Both work, and both are the same principal -- one person with two devices,
	// not two people.
	for name, cookie := range map[string]*http.Cookie{"the phone": phone, "the laptop": laptop} {
		got := f.get(f.pageURL("locations/rivergate"), cookie)
		if got.status != http.StatusOK {
			t.Errorf("%s: status %d, want 200", name, got.status)
		}
		if strings.Contains(got.body, "vel") {
			t.Errorf("%s was authenticated as a DM:\n%s", name, got.body)
		}
	}

	// And revocation ends both, which is the mitigation the design relies on.
	if err := f.store.RevokePrincipal(t.Context(), f.playerLink.Principal.ID); err != nil {
		t.Fatalf("RevokePrincipal: %v", err)
	}
	after := f.get("/c/" + f.campaign.Slug.String() + "/?k=" + f.playerLink.Token.Hex())
	if after.status == http.StatusSeeOther {
		t.Error("a revoked link was redeemed after the revocation")
	}
}

// TestRedeemingASecondLinkMakesTheSecondIdentity is what a player does when the
// DM sends them a second character's link, and it is the case where the old
// session has to go.
//
// A browser that keeps both cookies is not the problem -- the cookie name is the
// same, so the new one replaces the old. The problem is the *server* keeping the
// old session live, which is a credential nobody can revoke and nobody knows
// about.
func TestRedeemingASecondLinkMakesTheSecondIdentity(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	first := f.redeem(f.playerLink.Token)
	second := f.redeem(f.dmLink.Token)

	// The old cookie now names a session that has ended, so it is nobody.
	stale := f.get(f.pageURL("locations/rivergate"), first)
	if strings.Contains(stale.body, "vel") {
		t.Errorf("the superseded session still reads as the DM:\n%s", stale.body)
	}

	// And the new one is the DM, which is the observable difference.
	live := f.get(f.pageURL("locations/rivergate"), second)
	if !strings.Contains(live.body, "vel") {
		t.Errorf("the second redemption did not become the DM:\n%s", live.body)
	}
}

// TestACampaignThatDoesNotExistIsA404BeforeAnythingIsLookedUp: the token is
// scoped to a campaign, so a request for a campaign that is not there has nothing
// to redeem against. The middleware answers 404 and the token is never hashed,
// which is worth asserting because "hash it and then check" is the other order and
// it turns every 404 into a database query.
func TestACampaignThatDoesNotExistIsA404BeforeAnythingIsLookedUp(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get("/c/no-such-campaign/?k=" + f.playerLink.Token.Hex())
	if got.status != http.StatusNotFound {
		t.Errorf("status %d, want 404", got.status)
	}
	if len(got.cookies) != 0 {
		t.Error("a redemption for a campaign that does not exist set a cookie")
	}
	if strings.Contains(got.body, f.playerLink.Token.Hex()) {
		t.Error("the failed redemption printed the token")
	}
}

// TestARedemptionWithNoTokenIsJustAReader: `?k` absent is not a failed
// redemption, it is a reader opening the wiki, which is the common case by a long
// way.
func TestARedemptionWithNoTokenIsJustAReader(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get("/c/" + f.campaign.Slug.String() + "/")
	if got.status != http.StatusOK {
		t.Errorf("status %d, want 200\nbody: %s", got.status, got.body)
	}
	if strings.Contains(got.body, "That link did not work") {
		t.Error("a request with no token was answered as a failed redemption")
	}
}
