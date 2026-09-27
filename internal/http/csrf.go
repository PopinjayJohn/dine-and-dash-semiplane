package http

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
)

// # CSRF
//
// A double-submit token, tied to the principal rather than to the request.
//
// The token is an HMAC of the principal's id under a secret this process chose,
// so:
//
//   - a player cannot compute one, because they do not have the secret;
//   - a token from one principal does not work for another, so a form on a page
//     one player can read cannot be posted by a different player -- and in
//     particular a token minted for campaign A is useless in campaign B, which
//     is the tenancy check happening in a form field;
//   - it survives a session rotation, so a page a player already has open still
//     works after their role changed, which is the thing that makes them reload
//     at the worst moment otherwise.
//
// It is not tied to the *session* id, which is the more obvious choice and the
// wrong one: a session rotation on a role change is a security event, and a
// token that dies with the session would need a form the player cannot re-render.
//
// The secret is generated at startup when configuration does not supply one. A
// restart therefore invalidates forms already on players' screens: they get a
// 403 and a reload, which is a small price for not having to persist a secret
// somewhere a DM can lose it. Configuration supplies it when there is more than
// one process, and `wiki serve` does not, so this is the single-process case and
// the simplest correct thing.
//
// # The `Origin` check is on top, not instead
//
// `SameSite=Lax` already blocks the cross-site POST this protects against, and
// `Origin` is checked as well because a browser that does not send `Origin` for a
// form post -- which is most of them, for same-origin posts -- leaves nothing to
// compare, and the token is what is left. A forbidden origin is refused even
// before the token is looked at, because a request from another site is
// unambiguous and there is no reason to look further.

// csrfField is the form field and the header a token is carried in. The field is
// what a form posts; the header is what a fetch would send, and Datastar sends
// the header, so both are here and both are checked.
const (
	csrfField  = "csrf"
	csrfHeader = "X-CSRF-Token"
)

// secretSize is the length of the process's CSRF secret. 32 bytes is the size
// `crypto/rand` is usually documented to produce and the size of every other
// credential in this project.
const secretSize = 32

// initSecret gives the process its CSRF secret, when configuration did not.
//
// It is a method rather than a package variable so that the secret belongs to an
// application and two applications in one test binary do not share one, which
// would let a test's token pass another's check.
func (a *app) initSecret() {
	if len(a.cfg.Secret) >= secretSize {
		return
	}
	secret := make([]byte, secretSize)
	if _, err := rand.Read(secret); err != nil {
		// `crypto/rand` failing means this process cannot generate a credential,
		// and a CSRF secret it cannot generate is not one it should invent. The
		// token function treats a short secret as "no tokens", so every mutation
		// is refused and the wiki is read-only -- the fail-closed direction, and
		// the reason New does not return an error for it.
		a.log.LogAttrs(context.TODO(), slog.LevelError, "generating a CSRF secret",
			slog.String("error", err.Error()))
		return
	}
	a.cfg.Secret = secret
}

// csrfToken is the token for a principal, or the empty string for a request that
// identified nobody.
//
// There is no token for an unidentified request, which is what makes a
// mutation from a page nobody is logged in on impossible rather than merely
// unlikely: there is nothing for the form to have carried.
func (a *app) csrfToken(principalID string) string {
	if principalID == "" || len(a.cfg.Secret) < secretSize {
		return ""
	}

	mac := hmac.New(sha256.New, a.cfg.Secret)
	mac.Write([]byte(csrfField))
	mac.Write([]byte{0})
	mac.Write([]byte(principalID))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// errCSRF is what a mutation without a valid token returns. It is a named error
// because the handler turns it into a 403 with a message and the log line says
// why, and a bare boolean would lose that.
var errCSRF = errors.New("http: the request did not carry a valid CSRF token")

// checkCSRF is the gate on a mutation.
//
// Three things, in this order, and the order is the cheap one first:
//
//  1. the `Origin`, when there is one, has to be one this server would have
//     written the form into -- which is the ambiguous-hostname case, and it is
//     why the check compares against a configured origin and never against the
//     `Host` header a caller sent;
//  2. the token has to be present, in the form field or the header;
//  3. and it has to be this principal's.
//
// `hmac.Equal` rather than `==` because the comparison is over a value a caller
// chose, and a byte-by-byte compare that returns early is a timing oracle on a
// value that would be useful to learn. It is a token and not a password, so the
// timing would have to be measured over a network to be worth anything -- and the
// constant-time compare costs nothing.
func (a *app) checkCSRF(r *http.Request) error {
	req := requestFrom(r.Context())

	if a.cfg.AllowedOrigin != "" {
		if origin := r.Header.Get("Origin"); origin != "" && origin != a.cfg.AllowedOrigin {
			return errCSRF
		}
	}

	presented := r.Header.Get(csrfHeader)
	if presented == "" {
		// A form post puts it in the body. `ParseForm` is capped by Go at 10 MB
		// and a logout form is 40 bytes, so there is no reason to read the body
		// more carefully than that.
		if err := r.ParseForm(); err != nil {
			return errCSRF
		}
		presented = r.PostForm.Get(csrfField)
	}
	if presented == "" {
		return errCSRF
	}

	want := a.csrfToken(req.Principal.ID)
	if want == "" {
		// Nobody is logged in, or there is no secret. Both mean there is no
		// token that could be right.
		return errCSRF
	}

	if !hmac.Equal([]byte(presented), []byte(want)) {
		return errCSRF
	}
	return nil
}
