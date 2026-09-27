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

		if nonce := req.Nonce; nonce != "" {
			headers.Set("Content-Security-Policy", contentSecurityPolicy(nonce))
			// The context carries it as well as the header, because anything that
			// injects a script after the headers are written -- an SSE stream --
			// has to stamp the same nonce, and reaching into the header map to
			// find one that may or may not be there is a worse way to get it.
			// `sse.WithNonce` is where a stream picks it up.
			*r = *r.WithContext(withNonce(r.Context(), nonce))
		}

		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy is the policy for one response, given its nonce.
//
// It is a function and not a constant because the nonce changes per response,
// and a constant with a placeholder in it is a string that can be served without
// one -- which is a page with a policy that does not authorise anything, i.e. a
// page that does not work, rather than a page that is open.
func contentSecurityPolicy(nonce string) string {
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

// nonceKey is the context key for the response nonce, and it is this package's
// so that `sse` is handed it by a function rather than reaching for it.
type nonceKey struct{}

func withNonce(ctx context.Context, nonce string) context.Context {
	return context.WithValue(ctx, nonceKey{}, nonce)
}
