package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/events"
)

// # Redemption
//
// ADR 0003's shape: a share link is a URL with a token in it, the token is
// exchanged for a cookie once, and the browser is redirected to a URL that does
// not contain it. After that the token is in a cookie and nowhere else.
//
// It is middleware rather than a route of its own because the token may arrive on
// *any* URL in a campaign -- a DM pastes the link into a chat, a player bookmarks
// a deep page and then re-pastes it -- and a route would have to be mounted for
// every page. So the campaign routes redeem, and the handlers below never see a
// `?k=`.
//
// It runs after the campaign middleware and after the session middleware, which
// is the order the two answers depend on:
//
//   - **after campaign**, because the token is scoped to one campaign and
//     redemption has to be able to say "that link belongs to a different
//     campaign" -- which needs the slug resolved, and a request for a campaign
//     that does not exist is a 404 before anything is looked up by hash.
//   - **after session**, so that a browser which already has a session and then
//     arrives with a *new* link is a *new* identity rather than the old one with a
//     new cookie. A player redeeming a second character page's link becomes that
//     character; the session they had is superseded, and the old one is ended
//     rather than left live.
//
// # The token is not echoed
//
// Not in the redirect, not in the log line, not in the error page, and not in the
// `Referer` a subsequent request could carry -- which is why the redirect is to a
// path built here and why `Referrer-Policy: no-referrer` is on every response.
// `TestTheTokenNeverLeavesTheServer` checks all four at once, because the failure
// mode is a token that survives in one of four places.

// retryAfterSeconds is a `Retry-After` header's value: whole seconds, at least one.
//
// A zero or negative window means `auth`'s default, because `auth.NewLimiter`
// already decided that and a `Retry-After: 0` is a client that retries immediately,
// which is a limit that does not limit.
func retryAfterSeconds(window time.Duration) string {
	if window <= 0 {
		window = auth.DefaultRedemptionWindow
	}

	seconds := int(window.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.Itoa(seconds)
}

// trustsProxy is whether a request arrived through a proxy this deployment believes.
//
// **With nothing configured it is false for every request**, which is the safe
// direction: a rate limit applied per proxy is unfair to nobody in a campaign of five,
// and a rate limit a stranger can remove by sending a header is not a rate limit.
// See [auth.ClientIP], whose third argument is exactly this question.
func (a *app) trustsProxy(r *http.Request) bool {
	if len(a.cfg.TrustedProxies) == 0 {
		return false
	}
	return a.proxies.Contains(r.RemoteAddr)
}

// redeem exchanges a token for a cookie and redirects.
//
// A request with no token is not a redemption and goes through untouched: a
// campaign root with no `?k=` is a reader opening the wiki, and it is the common
// case by a long way.
func (a *app) redeem(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := requestFrom(r.Context())

		if !r.URL.Query().Has("k") {
			next.ServeHTTP(w, r)
			return
		}

		// The rate limit, before anything is attempted with the token.
		//
		// **`auth.Limiter` was built and tested in M6 and never called**, and
		// `docs/security.md` has listed `TestRateLimitedRedemption` as the control
		// against "the network, guessing share links" ever since. A test on a
		// component nothing calls is a test of that component, not a control on this
		// route, and the difference is the whole of the gap.
		//
		// It is asked before the token is even read, so a script that is guessing
		// pays the limit whether or not its guesses are well-formed. `trustedProxy` is
		// **false** here and that is the safe direction: [auth.ClientIP]'s third
		// argument asks whether this request arrived through a proxy the deployment
		// trusts, and until `config.yaml`'s `trusted_proxies` is read, nothing does.
		if err := a.limiter.Allow(auth.ClientIPFromRequest(r, a.trustsProxy(r))); err != nil {
			a.redeemFailed(w, r, err)
			return
		}

		token := r.URL.Query().Get("k")
		if token == "" {
			// `?k=` with nothing after it is a link that got truncated, which
			// happens when a DM pastes into a chat client that eats part of a URL.
			// It gets the same answer as a wrong token, because from here it is
			// one.
			a.redeemFailed(w, r, auth.ErrBadToken)
			return
		}

		redeemed, err := a.cfg.Redeemer.Redeem(r.Context(), req.Campaign.Slug, token)
		if err != nil {
			a.redeemFailed(w, r, err)
			return
		}

		// The old session is ended before the new cookie is written, so a browser
		// that redeems a second link does not keep the first one live. A failure
		// here is logged and not fatal: the new session is established, and a
		// stale session is a much smaller problem than a player who cannot log in
		// at all.
		if previous, hadPrevious := a.sessionCookieValue(r); hadPrevious && previous != redeemed.SessionID {
			if _, authErr := a.cfg.Redeemer.Authenticate(r.Context(), previous); authErr == nil {
				if endErr := a.cfg.Redeemer.EndSession(r.Context(), previous); endErr != nil {
					a.log.LogAttrs(r.Context(), slog.LevelWarn, "superseding a session",
						slog.String("error", endErr.Error()),
						slog.String("campaign", req.Campaign.Slug.String()),
					)
				}
			}
		}

		if err := a.setSessionCookie(w, redeemed.SessionID, redeemed.ExpiresAt); err != nil {
			// A redemption that cannot store its credential has not happened. The
			// token is still in the address bar because nothing has redirected, so
			// the player can try again -- which is the whole reason this is an
			// error and not a log line.
			a.fail(w, r, "storing the session cookie", err)
			return
		}

		// A share link was used, and the principal it produced exists. Published
		// after the cookie is stored, because a redemption that could not store its
		// credential has not happened -- a subscriber told about this one would be
		// told about a session that does not work.
		//
		// It carries the principal and the campaign and no token, and a subscriber
		// that wanted the token could not have it. See internal/events.
		a.cfg.Events.Publish(r.Context(), events.Redeemed(
			requestFrom(r.Context()).Campaign, redeemed.Principal))

		a.redirect(w, a.afterRedeem(r, redeemed.RedirectTo))
	})
}

// afterRedeem is where the browser goes, and it is the token-free URL.
//
// `Redeemed.RedirectTo` is auth's answer and is a campaign root, but the reader
// may have arrived on a deep page and a 303 to the root loses where they were
// trying to go. So the path and the query are kept and only the token is removed:
// the same page, without the credential in the address bar, in the history, and
// in anything that reads it later.
//
// The token parameter is dropped rather than blanked. `?k=` in a URL is itself a
// signal -- it says this browser has just redeemed something -- and a page that
// reads its own query would be a page reading a credential's fingerprint.
func (a *app) afterRedeem(r *http.Request, fallback string) string {
	target := *r.URL
	query := target.Query()
	query.Del("k")
	target.RawQuery = query.Encode()
	target.Fragment = ""

	if target.Path == "" {
		return fallback
	}
	return target.String()
}

// redeemFailed answers a redemption that did not happen.
//
// The four answers are the four things a *player* can be told, and they are
// chosen so that none of them is a question about a token's shape:
//
//   - **a wrong or unknown token is a 404.** "Not valid" and "already used" are
//     the same page with the same words, because a caller who could tell them
//     apart could count a DM's links.
//   - **a revoked link is a 403 that says so.** A player whose DM pulled their
//     access needs to know that, because the alternative is a wiki that has
//     mysteriously stopped working.
//   - **an expired link is a 403 that says so**, for the same reason: the fix is
//     a new link, and "not valid" sends them to the DM to ask for one they did
//     not know they needed.
//   - **the wrong campaign is a 403 that says so**, because it is the DM's paste
//     that is wrong rather than the player's link, and saying "this belongs to
//     another campaign" is what lets them fix it.
//
// The log line says which of the four it was, with the path and the request id
// and **not** the token: a DM who revokes a link and then cannot find out whether
// the player used it needs something, and what they need is a count, not the
// credential.
func (a *app) redeemFailed(w http.ResponseWriter, r *http.Request, cause error) {
	req := requestFrom(r.Context())

	status, body := redemptionMessage(cause)
	if status == http.StatusTooManyRequests {
		// A 429 without a `Retry-After` is a 429 a client has to guess at, and the
		// window is a property of the limiter rather than of this handler, so it is
		// read back from the limiter rather than restated here.
		w.Header().Set("Retry-After", retryAfterSeconds(a.cfg.RedemptionWindow))
	}

	a.log.LogAttrs(r.Context(), slog.LevelWarn, "redemption refused",
		slog.String("reason", cause.Error()),
		slog.String("campaign", req.Campaign.Slug.String()),
		slog.String("path", r.URL.Path),
		slog.String("request_id", orDash(req.ID)),
	)

	// The tree is drawn: a player whose link did not work is still standing in a
	// campaign, and a page they can read is the most useful thing the page can say.
	a.render(w, r, status, notice(noticeData{
		shell:   a.noticeShell(req, a.pagesIn(r), "That link did not work"),
		Request: viewRequestFor(req),
		Title:   "That link did not work",
		Body:    body,
		Status:  status,
	}))
}

// redemptionMessage maps a redemption failure to a status and something a player
// can act on.
func redemptionMessage(cause error) (int, string) {
	switch {
	case errors.Is(cause, auth.ErrRateLimited):
		// A 429 rather than the default 404, and that is the one place a rate limit
		// is *told* rather than disguised. A script cannot tell the difference between
		// a limit and a wrong token, which is the point; a person can, and a player
		// who has typed their own link five times deserves to be told to wait rather
		// than told their link is broken. The `Retry-After` header is set beside it.
		return http.StatusTooManyRequests,
			"Too many attempts from this address. Wait a minute and try again."
	case errors.Is(cause, auth.ErrRevoked):
		return http.StatusForbidden, "This link has been revoked, which means the DM took it back. Ask them for a new one."
	case errors.Is(cause, auth.ErrLinkExpired):
		return http.StatusGone, "This link has expired. Ask the DM for a new one."
	case errors.Is(cause, auth.ErrWrongCampaign):
		return http.StatusForbidden, "This link belongs to a different campaign. The address bar and the link do not match."
	case errors.Is(cause, auth.ErrNoSession), errors.Is(cause, auth.ErrNoToken):
		return http.StatusNotFound, "There is no link here. Open the link the DM sent you."
	default:
		// ErrBadToken and anything else: one answer, no detail. A token that is
		// not valid and a token that has already been used are the same page, and
		// neither of them says which.
		return http.StatusNotFound, "This link is not valid, or it has already been used. Ask the DM for a new one."
	}
}
