package http

import (
	"context"
	"net/http"
	"strings"
)

// securityHeaders sets what every response carries, before any handler runs.
//
// A handler that has written a byte has written the status line, and a header set
// afterwards is a header the client never sees. So this is middleware, and it is
// outside the handlers for the same reason `recovery` is.
//
// # The policy
//
// `default-src 'none'` and then exactly the three things a page needs: its own
// script with a per-response nonce, its own stylesheet, and connections to
// itself. Everything else is refused, including images from elsewhere, which
// means a DM's `![](https://example.com/map.png)` renders as a broken image --
// the honest outcome, and the reason `_attachments/` exists.
//
// `style-src 'self'` and not a nonce: a nonce authorises an inline `<style>` or a
// `style=` attribute, and an external stylesheet is not either of those. It is
// linked from the same origin, so `'self'` covers it and nothing needs one.
//
// The nonce is what makes `script-src` work at all. Without `unsafe-inline` --
// which is never acceptable in an application whose pages contain markdown a
// player wrote -- a `<script>` is only allowed if it carries the response's
// nonce, and a nonce that appears in the response is worthless as an XSS vector
// because the attacker cannot read the response they are injecting into.
//
// `frame-ancestors 'none'` and `base-uri 'none'` are both about a campaign page
// being used as a lever against something else: a wiki a player can write content
// in must not be a frame somebody else's page can point at, and must not be a
// document whose `<base>` somebody else's page chooses.
func (a *app) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := requestFrom(r.Context())

		headers := w.Header()
		// A campaign is not a place to be indexed, and its URLs carry a slug a
		// DM chose, so neither a search engine nor a shared cache is welcome.
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("X-Robots-Tag", "noindex, nofollow")
		// A `.js` served as text/plain is not run, and that is the point: the
		// asset handler already sets the right type, and this makes a mistake
		// there a broken feature rather than a hole.
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")

		// **The policy is on every response, whatever the nonce turned out to be.**
		//
		// The first version set the header only when a nonce had been generated, so
		// a `crypto/rand` failure produced a response with *no* Content-Security-Policy
		// at all — which is the opposite of the argument this file makes about a
		// constant with a placeholder in it. A response without a policy is not a
		// page that does not work; it is a page that is open.
		//
		// An empty nonce therefore yields `script-src 'none'`: the header is present,
		// `default-src 'none'` still applies, and the page's own script does not run.
		// The DM loses live updates and search-as-you-type for the duration and sees
		// a log line saying why. Nothing is served that the DM did not write, and
		// nothing an attacker injected runs.
		nonce := req.Nonce
		headers.Set("Content-Security-Policy", contentSecurityPolicy(nonce))
		// The context carries it as well as the header, because anything that
		// injects a script after the headers are written -- an SSE stream -- has to
		// stamp the same nonce, and reaching into the header map to find one that may
		// or may not be there is a worse way to get it. `sse.WithNonce` is where a
		// stream picks it up.
		*r = *r.WithContext(withNonce(r.Context(), nonce))

		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy is the policy for one response, given its nonce.
//
// It is a function and not a constant because the nonce changes per response,
// and a constant with a placeholder in it is a string that can be served without
// one -- which is a page with a policy that does not authorise anything, i.e. a
// page that does not work, rather than a page that is open.
//
// **An empty nonce is the one case where `script-src` is not `'nonce-'`.** A
// `nonce-` with nothing after it is a policy whose script source matches nothing, but
// it is *nearly* nothing, and the difference between "authorises no script" and
// "authorises the empty nonce" is the kind of difference a future edit to this string
// could undo by accident. `script-src 'none'` says what it means.
func contentSecurityPolicy(nonce string) string {
	if nonce == "" {
		return closedContentSecurityPolicy
	}

	var policy strings.Builder
	policy.WriteString("default-src 'none'")
	policy.WriteString("; script-src 'nonce-" + nonce + "'")
	policy.WriteString("; style-src 'self'")
	policy.WriteString("; img-src 'self' data:")
	policy.WriteString("; font-src 'self'")
	policy.WriteString("; connect-src 'self'")
	policy.WriteString("; form-action 'self'")
	policy.WriteString("; base-uri 'none'")
	policy.WriteString("; frame-ancestors 'none'")
	return policy.String()
}

// closedContentSecurityPolicy is the policy for a response whose nonce could not be
// produced.
//
// It is the same policy with the script source replaced, and it is a **constant**
// where the other is a function, because there is nothing per-response about it: two
// responses that both failed to get a nonce have the same policy, and a nonce that
// was not generated cannot be stamped into a header afterwards.
//
// Every other directive is identical to [contentSecurityPolicy]'s, deliberately. The
// point is not to tighten the policy on this path — it is to have a policy at all, and
// a shorter one would be a second thing to keep in step with the first.
const closedContentSecurityPolicy = "default-src 'none'" +
	"; script-src 'none'" +
	"; style-src 'self'" +
	"; img-src 'self' data:" +
	"; font-src 'self'" +
	"; connect-src 'self'" +
	"; form-action 'self'" +
	"; base-uri 'none'" +
	"; frame-ancestors 'none'"

// nonceKey is the context key for the response nonce, and it is this package's
// so that `sse` is handed it by a function rather than reaching for it.
type nonceKey struct{}

func withNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, nonceKey{}, nonce)
}
