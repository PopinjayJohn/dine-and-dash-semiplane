package http_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// # The routes
//
// Every route, what it answers, and what it is allowed to say. The list is
// written out rather than generated, because a route table generated from the
// router is a restatement of the router and proves nothing about the routes.

// TestTheRoutes is the table. Each case is a URL somebody could type, and each
// assertion is something a DM or a player would notice being wrong.
func TestTheRoutes(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	slug := f.campaign.Slug.String()

	tests := map[string]struct {
		target     string
		cookie     *http.Cookie
		wantStatus int
		wantBody   []string
	}{
		"the health line needs no session": {
			target: "/_/healthz", wantStatus: http.StatusOK,
			wantBody: []string{`"status": "ok"`, `"version"`},
		},
		"the campaign root lists what a player may read": {
			target: "/c/" + slug + "/", cookie: player, wantStatus: http.StatusOK,
			wantBody: []string{"rivergate", "dragon-heist", "aria"},
		},
		"a page is a page": {
			target: f.pageURL("locations/rivergate"), cookie: player, wantStatus: http.StatusOK,
			wantBody: []string{`<h1 class="page-title">Rivergate</h1>`, "A fortified town"},
		},
		"a page's links carry the campaign": {
			target: f.pageURL("locations/rivergate"), cookie: player, wantStatus: http.StatusOK,
			// The link is to a page that does not exist yet, so it is
			// unresolved -- but the *campaign* is in the resolver, so the tree
			// and every resolved link agree on the prefix.
			wantBody: []string{`href="/c/blackwater/characters/aria"`},
		},
		"a page the player may not read is a 404": {
			target: f.pageURL("npcs/vel"), cookie: player, wantStatus: http.StatusNotFound,
		},
		"a page that does not exist is a 404": {
			target: f.pageURL("locations/nowhere"), cookie: player, wantStatus: http.StatusNotFound,
		},
		"a campaign that does not exist is a 404": {
			target: "/c/nowhere/", cookie: player, wantStatus: http.StatusNotFound,
		},
		"the stylesheet is served": {
			target: "/static/wiki.css", wantStatus: http.StatusOK,
		},
		"an asset that is not there is a 404": {
			target: "/static/nothing.css", wantStatus: http.StatusNotFound,
		},
		"a path the vault would refuse is a 404": {
			// `_history` is a reserved first segment, so there is no page there
			// and a URL naming one cannot be answered with a page.
			target: f.pageURL("_history/campaign/1-one.md"), cookie: player, wantStatus: http.StatusNotFound,
		},
		"a traversal in the path is a 404": {
			target: "/c/" + slug + "/../../etc/passwd", cookie: player, wantStatus: http.StatusNotFound,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := f.get(tt.target, tt.cookie)
			if got.status != tt.wantStatus {
				t.Errorf("GET %s is %d, want %d\nbody: %s", tt.target, got.status, tt.wantStatus, got.body)
			}
			for _, want := range tt.wantBody {
				if !strings.Contains(got.body, want) {
					t.Errorf("GET %s does not contain %q\nbody: %s", tt.target, want, got.body)
				}
			}
		})
	}
}

// TestTheMethodNotAllowedCarriesAllow: a 405 without `Allow` makes the client
// guess, and a client that guesses well enough to retry has just found a way
// around whatever the 405 was for.
func TestTheMethodNotAllowedCarriesAllow(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// A DELETE to the campaign root: there is no such route and no such method.
	got := f.request(http.MethodDelete, "/c/"+f.campaign.Slug.String()+"/", nil)
	if got.status != http.StatusMethodNotAllowed {
		t.Errorf("a DELETE to the campaign root is %d, want 405", got.status)
	}
	if allow := got.header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("the 405 does not say what is allowed: %q", allow)
	}
}

// TestTheHealthLineSaysWhatTheBinaryIs: a DM reporting a bug says which build, and
// the version is the one fact about a binary that identifies a change.
func TestTheHealthLineSaysWhatTheBinaryIs(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get("/_/healthz")
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200", got.status)
	}
	if contentType := got.header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Errorf("Content-Type is %q, want JSON", contentType)
	}
	for _, want := range []string{`"status": "ok"`, `"version": "dev"`, `"uptime"`} {
		if !strings.Contains(got.body, want) {
			t.Errorf("the health line does not contain %q:\n%s", want, got.body)
		}
	}
	// And it is not cached: a monitoring script that gets a stale answer is a
	// monitoring script that says a dead wiki is alive.
	if got.header.Get("Cache-Control") != "no-store" {
		t.Errorf("the health line is cached: %q", got.header.Get("Cache-Control"))
	}
}

// TestTheSecurityHeaders are on every response, and the CSP is the one that
// matters: a campaign page contains markdown a player wrote, and the policy is
// what stands between that and a script somebody else supplied.
func TestTheSecurityHeaders(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	targets := []string{
		"/_/healthz",
		"/c/" + f.campaign.Slug.String() + "/",
		f.pageURL("locations/rivergate"),
		"/nowhere",
		"/static/wiki.css",
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			got := f.get(target, player)

			if want := "nosniff"; got.header.Get("X-Content-Type-Options") != want {
				t.Errorf("X-Content-Type-Options is %q, want %q", got.header.Get("X-Content-Type-Options"), want)
			}
			// ADR 0003: a player's page must not leak its URL to whatever the
			// browser fetches next.
			if want := "no-referrer"; got.header.Get("Referrer-Policy") != want {
				t.Errorf("Referrer-Policy is %q, want %q", got.header.Get("Referrer-Policy"), want)
			}
			if got.header.Get("X-Robots-Tag") == "" {
				t.Error("there is no X-Robots-Tag, so a search engine may index a campaign")
			}
			if got.header.Get("X-Frame-Options") != "DENY" {
				t.Errorf("X-Frame-Options is %q, want DENY", got.header.Get("X-Frame-Options"))
			}
		})
	}
}

// TestTheContentSecurityPolicyIsPerResponseAndStrict is the CSP test.
//
// Two properties, and the second is the one that is easy to get wrong: the policy
// must authorise *this* response's script and no other. A static policy is a
// policy whose nonce every attacker knows, because it is in every page.
func TestTheContentSecurityPolicyIsPerResponseAndStrict(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first := f.get(f.pageURL("locations/rivergate"))
	second := f.get(f.pageURL("campaign"))

	policy := first.header.Get("Content-Security-Policy")
	if policy == "" {
		t.Fatal("there is no Content-Security-Policy at all")
	}

	for _, want := range []string{
		"default-src 'none'",
		"script-src 'nonce-",
		"style-src 'self'",
		"connect-src 'self'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the policy does not contain %q:\n%s", want, policy)
		}
	}
	for _, forbidden := range []string{"'unsafe-inline'", "'unsafe-eval'", "*"} {
		if strings.Contains(policy, forbidden) {
			t.Errorf("the policy contains %q, which is the thing it is supposed to prevent:\n%s", forbidden, policy)
		}
	}

	// The nonce in the policy is the nonce on the script tag, and it is a new one
	// every response.
	nonceOf := func(r response) string {
		_, after, found := strings.Cut(r.header.Get("Content-Security-Policy"), "'nonce-")
		if !found {
			return ""
		}
		return strings.SplitN(after, "'", 2)[0]
	}

	pageNonce, otherNonce := nonceOf(first), nonceOf(second)
	if pageNonce == "" || otherNonce == "" {
		t.Fatalf("a response has no nonce in its policy: %q and %q", pageNonce, otherNonce)
	}
	if pageNonce == otherNonce {
		t.Error("two responses share a nonce, so a nonce from one authorises a script in the other")
	}
	if !strings.Contains(first.body, `nonce="`+pageNonce+`"`) {
		t.Errorf("the script tag does not carry the response's own nonce\npolicy: %s\nbody: %s", policy, first.body)
	}
}

// TestNoInlineScript: the policy has no `unsafe-inline`, so an inline script is
// dead, and a template that needed one would have a page that silently does
// nothing. The one inline script in a response should be the vendored client, and
// it should be in a tag with the nonce.
func TestNoInlineScript(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	for _, target := range []string{
		"/c/" + f.campaign.Slug.String() + "/",
		f.pageURL("locations/rivergate"),
	} {
		got := f.get(target, player)

		for _, script := range scriptTags(got.body) {
			if !strings.Contains(script, "src=") {
				t.Errorf("GET %s has an inline script, which the policy will refuse:\n%s", target, script)
			}
			if !strings.Contains(script, "nonce=") {
				t.Errorf("GET %s has a script with no nonce:\n%s", target, script)
			}
		}
	}
}

// scriptTags is every `<script ...>` in a body, opening tags only. It is a
// three-line scanner rather than a parser because the assertion is about the
// markup this package wrote, and a parser would be a dependency in a test whose
// job is to notice if a template wrote something unexpected.
func scriptTags(body string) []string {
	var tags []string
	rest := body
	for {
		start := strings.Index(rest, "<script")
		if start < 0 {
			return tags
		}
		end := strings.Index(rest[start:], ">")
		if end < 0 {
			return tags
		}
		tags = append(tags, rest[start:start+end+1])
		rest = rest[start+end:]
	}
}

// TestNoSecretIsInTheCSPOrAHeader is the paranoid half of the header test: a
// canary that ends up in a response *header* is a canary in every log line of
// every proxy between here and the reader.
func TestNoSecretIsInTheCSPOrAHeader(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	// The DM's page has the canary in it, so the DM's response is the one with
	// the most to leak.
	got := f.get(f.pageURL("npcs/vel"), dm)
	if !strings.Contains(got.body, canary) {
		t.Fatalf("the DM was not served their own page:\n%s", got.body)
	}

	for name, values := range got.header {
		for _, value := range values {
			if strings.Contains(value, canary) {
				t.Errorf("the %s header carries the canary: %q", name, value)
			}
		}
	}
}

// TestTheCSPNonceIsNotTheSameOnEveryRequest is the second half of the per-response
// property, kept apart from the policy test so that a failure says which of the
// two broke.
func TestTheCSPNonceIsNotTheSameOnEveryRequest(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	seen := map[string]bool{}
	for i := range 8 {
		got := f.get("/c/" + f.campaign.Slug.String() + "/")
		_, after, found := strings.Cut(got.header.Get("Content-Security-Policy"), "'nonce-")
		if !found {
			t.Fatalf("request %d had no nonce", i)
		}
		nonce := strings.SplitN(after, "'", 2)[0]
		if seen[nonce] {
			t.Fatalf("request %d reused a nonce", i)
		}
		seen[nonce] = true

		if len(nonce) < 16 {
			t.Errorf("the nonce is %d characters, which is not enough for a 128-bit value", len(nonce))
		}
	}
}

// TestTheRequestIdIsOnTheResponseAndIsShort: it is the bridge between a DM saying
// "it broke" and a line in the log, and it has to be a header the client can read.
func TestTheRequestIdIsOnTheResponseAndIsShort(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get("/_/healthz")
	id := got.header.Get("X-Request-Id")
	if id == "" {
		t.Fatal("there is no X-Request-Id header")
	}
	if len(id) < 16 {
		t.Errorf("the request id is %q, which is too short to be unique in a log", id)
	}

	// And two requests do not share one.
	other := f.get("/_/healthz")
	if other.header.Get("X-Request-Id") == id {
		t.Error("two requests share a request id")
	}
}

// TestAnInboundRequestIdIsKeptAndSanitised: a proxy in front of this server may
// already have one and replacing it would break the correlation it is trying to
// make. A caller-supplied one is used when it is short and printable, and thrown
// away when it is neither.
func TestAnInboundRequestIdIsKeptAndSanitised(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inbound   string
		wantKept  bool
		wantValue string
	}{
		"a short printable id is kept":     {inbound: "abc123", wantKept: true, wantValue: "abc123"},
		"an id with a newline is refused":  {inbound: "abc\ndef", wantKept: false},
		"an id with a quote is refused":    {inbound: `abc"def`, wantKept: false},
		"an over-long id is refused":       {inbound: strings.Repeat("a", 200), wantKept: false},
		"an empty id is not an id at all":  {inbound: "", wantKept: false},
		"a carriage return is refused":     {inbound: "abc\rdef", wantKept: false},
		"an id of exactly the limit is ok": {inbound: strings.Repeat("a", 64), wantKept: true, wantValue: strings.Repeat("a", 64)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/_/healthz", nil)
			if err != nil {
				t.Fatalf("building the request: %v", err)
			}
			if tt.inbound != "" {
				req.Header.Set("X-Request-Id", tt.inbound)
			}

			rec := newRecorder(f)
			f.handler.ServeHTTP(rec, req)

			got := rec.Header().Get("X-Request-Id")
			if tt.wantKept && got != tt.wantValue {
				t.Errorf("the request id is %q, want the inbound %q", got, tt.wantValue)
			}
			if !tt.wantKept && got == tt.inbound && tt.inbound != "" {
				t.Errorf("the inbound id %q was used", tt.inbound)
			}
			if !tt.wantKept && got == "" {
				t.Error("no request id at all, so there is nothing to correlate a log line with")
			}
		})
	}
}

// TestTheStatusRecorderSeesTheStatus is the logging middleware's own test, and it
// is here rather than in a logging test because a 404 logged as a 200 makes every
// other log line in this file untrustworthy.
func TestTheStatusRecorderSeesTheStatus(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	f.get("/nowhere")
	if !strings.Contains(f.logs.String(), `"status":404`) {
		t.Errorf("a 404 was not logged as a 404:\n%s", f.logs.String())
	}
	if !strings.Contains(f.logs.String(), `"method":"GET"`) {
		t.Errorf("the method is not in the log line:\n%s", f.logs.String())
	}
	if !strings.Contains(f.logs.String(), `"path":"/nowhere"`) {
		t.Errorf("the path is not in the log line:\n%s", f.logs.String())
	}
}

// TestTheHealthLineUptimeIsAClockConsistentDuration is here because the uptime
// used to be a package variable reading `time.Now()` while everything else used
// the injected clock, and a test with a fixed clock got an uptime of minus five
// thousand hours. It passed as a duration string, which is what makes it the kind
// of wrong that survives review: it is a duration, it is a string, and it is
// nonsense.
func TestTheHealthLineUptimeIsAClockConsistentDuration(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// A minute after the application was built, so the uptime is a known value
	// rather than a shape.
	f.hands.advance(time.Minute)

	got := f.get("/_/healthz")

	_, after, found := strings.Cut(got.body, `"uptime": "`)
	if !found {
		t.Fatalf("the health line has no uptime:\n%s", got.body)
	}
	uptime := strings.SplitN(after, `"`, 2)[0]
	if uptime != "1m0s" {
		t.Errorf("the uptime is %q, want the minute between the two clock readings", uptime)
	}
}
