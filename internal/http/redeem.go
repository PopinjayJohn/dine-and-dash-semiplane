package http

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
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
