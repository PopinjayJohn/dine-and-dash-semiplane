package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// # The cookie, and each of its attributes
//
// ADR 0003 fixed the attributes and M6 had no HTTP layer to set them on. They
// are here now, and each one is a decision rather than a default:
//
//   - **`__Host-` prefix.** The prefix is a promise the browser enforces, and it
//     is worth having on a credential: a cookie with this prefix is rejected
//     unless it is `Secure`, has `Path=/` and carries no `Domain`. That rules out
//     a sibling subdomain rewriting it, which is the attack the prefix exists
//     for. It is one reason this application never sets a `Domain`.
//
// ...and the prefix is also why the *name* depends on the deployment, which is
// the one thing in this file that is not in ADR 0003. A browser refuses a
// `__Host-` cookie outright when `Secure` is absent, so a DM running the wiki on
// their own machine over plain HTTP could not log in at all -- and the failure is
// a cookie that silently never arrives, so it presents as a wiki that has lost
// the link they were just sent. So the local deployment uses the unprefixed name
// and the production one uses the prefixed name, and `TestTheCookieNameFollowsThe
// Deployment` says which is which. The local name gives up the subdomain
// protection, which is a real cost; it is the right trade for a laptop on a
// network the DM owns, and the cost is written down rather than assumed away.
const (
	// productionSessionCookie is the name a production deployment uses, and the
	// prefix is load-bearing: it is what makes the browser reject the cookie
	// unless `Secure`, `Path=/` and no `Domain`.
	productionSessionCookie = "__Host-wiki_session"

	// localSessionCookie is the name a plain-HTTP deployment uses, because the
	// prefixed one is refused without `Secure`.
	localSessionCookie = "wiki_session"
)

// sessionCookieName is the cookie this deployment uses for its session.
func (a *app) sessionCookieName() string {
	if a.cfg.Production {
		return productionSessionCookie
	}
	return localSessionCookie
}

// setSessionCookie writes the session cookie for a redemption.
//
// The error is returned rather than logged and ignored because a redemption that
// cannot store its credential has not happened: the token is in the URL, the URL
// is about to be cleared with a 303, and a player left with a redirect and no
// cookie has lost the link they were sent. So the caller answers 500 and the
// token stays in the address bar where they can try again, rather than showing a
// blank page and losing the credential.
func (a *app) setSessionCookie(w http.ResponseWriter, sessionID string, expires time.Time) error {
	// All three of HttpOnly, SameSite and Secure are set, and `Secure` is a
	// decision rather than a constant: gosec reads a literal `true` as the only
	// safe value, and this program's local deployment genuinely cannot use it,
	// which is the trade the file's own comment above is about.
	cookie := &http.Cookie{ //nolint:gosec // see above: Secure is a deployment decision
		Name:     a.sessionCookieName(),
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cfg.Production,
		Expires:  expires,
		MaxAge:   maxAge(expires, a.cfg.Now()),
	}

	// `__Host-` and `Domain` are exclusive by specification and a browser refuses
	// the cookie rather than ignoring the attribute, which is a failure with no
	// error message anywhere. It is checked here so that a future edit adding a
	// Domain is a failed test rather than a campaign that quietly stopped
	// working.
	if strings.HasPrefix(cookie.Name, "__Host-") && (cookie.Domain != "" || cookie.Path != "/" || !cookie.Secure) {
		return fmt.Errorf("%w: %q needs Secure, Path=/ and no Domain", errCookieAttributes, cookie.Name)
	}

	w.Header().Add("Set-Cookie", cookie.String())
	return nil
}

// clearSessionCookie expires the session cookie, which is the whole of logout as
// far as the browser is concerned.
//
// The same name, path and attributes, because a cookie is only replaced by a
// cookie with the same name and path: a `Set-Cookie` that omits `Path=/` leaves
// the original in place, and a logout that does not log you out is worse than
// one that is missing.
func (a *app) clearSessionCookie(w http.ResponseWriter) {
	w.Header().Add("Set-Cookie", (&http.Cookie{ //nolint:gosec // see setSessionCookie
		Name:     a.sessionCookieName(),
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.cfg.Production,
		Expires:  time.Unix(1, 0).UTC(),
		MaxAge:   -1,
	}).String())
}

// errCookieAttributes is what setSessionCookie returns when the cookie could not
// be built. It is a distinct error because a browser that refuses a cookie says
// nothing at all, so the check has to happen here or nowhere.
var errCookieAttributes = errors.New("http: the session cookie's attributes are not acceptable")

// maxAge is the cookie's `Max-Age` in seconds.
//
// A negative value is `-1`, which is how a cookie is deleted, so an expiry in the
// past is a deletion and not an error. A session that has already expired gets a
// cookie that expires immediately rather than one that would be valid for as long
// as the browser lives, which is what zero would mean.
func maxAge(expires, now time.Time) int {
	seconds := int(expires.Sub(now).Round(time.Second) / time.Second)
	if seconds < -1 {
		return -1
	}
	return seconds
}

// sessionCookieValue reads the session cookie from a request, and reports whether
// there was one.
//
// A malformed cookie jar -- a cookie with a control character in it, or two
// cookies with the same name, which a browser will not send but a hand-written
// client will -- is *no* cookie rather than an error. A request with a broken
// cookie is an unidentified request, and an unidentified request reads nothing.
//
// Two cookies with one name is refused rather than resolved by picking one,
// because picking is how a session fixation gets to choose which session a
// request uses.
func (a *app) sessionCookieValue(r *http.Request) (string, bool) {
	name := a.sessionCookieName()
	found := ""
	for _, cookie := range r.Cookies() {
		if cookie.Name != name || cookie.Value == "" {
			continue
		}
		if found != "" {
			return "", false
		}
		found = cookie.Value
	}
	return found, found != ""
}

// pagePathFromURL turns the tail of a URL into a page identity, and reports
// whether it named one at all.
//
// The URL carries the path escaped -- a page called `locations/half full` is
// `locations/half%20full` in a browser's address bar -- and the store's path is
// the decoded form, so the unescaping is not optional. `chi` hands the wildcard
// over still encoded, which is the one behaviour of it that this depends on.
//
// An empty path is the campaign root and not a page. A path `vault.CheckPagePath`
// refuses is a URL that cannot name a page -- a 404 rather than a store call,
// because a query for a row the vault would not create is a query for nothing.
func pagePathFromURL(raw string) (string, bool) {
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", false
	}
	trimmed := strings.Trim(decoded, "/")
	if trimmed == "" {
		return "", false
	}
	checked, err := vault.CheckPagePath(trimmed)
	if err != nil {
		return "", false
	}
	return checked, true
}
