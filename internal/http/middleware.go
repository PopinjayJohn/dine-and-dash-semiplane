package http

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// requestIDHeader is the response header carrying the request id, and the header
// a caller may send one in. Reading an inbound one is a convenience for a DM
// correlating a log line with a report; it is not trusted for anything, and the
// value is length-capped so a caller cannot put a megabyte in a log line.
const requestIDHeader = "X-Request-Id"

// maxInboundRequestID is how long an inbound request id may be before it is
// replaced. A caller that sends a long one gets a fresh one rather than an error:
// the request is still a request, and the id is for humans.
const maxInboundRequestID = 64

// requestID gives every request a name, and puts it in the response so that a
// player who reports "it did not work" and a DM with a log line can be put
// together.
//
// The id is 16 bytes of `crypto/rand` in base64url. It is not a secret and does
// not need to be: its only job is to be unique enough that two log lines in a
// campaign's history do not share one, and `crypto/rand` is what is already in
// the binary. The inbound header is honoured because a reverse proxy in front of
// this server may already have one, and replacing it would break the
// correlation the proxy is trying to make; it is truncated and stripped of
// anything that would break a log line.
func (a *app) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := inboundRequestID(r)
		if id == "" {
			id = newRequestID()
		}

		w.Header().Set(requestIDHeader, id)
		ctx := withRequest(r.Context(), &request{ID: id, Nonce: a.nonce()})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func inboundRequestID(r *http.Request) string {
	inbound := strings.TrimSpace(r.Header.Get(requestIDHeader))
	if len(inbound) > maxInboundRequestID {
		return ""
	}
	// Newlines and quotes would let a caller forge a log line, so the value is
	// kept to the characters a log line can hold unescaped.
	if strings.ContainsAny(inbound, "\r\n\"") {
		return ""
	}
	return inbound
}

func newRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// `crypto/rand` failing is not a thing this program can do anything
		// about, and a request with no id is better than a request refused: the
		// id is a convenience, not a security control. An empty one is logged
		// as "-", which reads as "there was none".
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

// logRequests writes one line per request.
//
// **The query string is not in it.** `?k=<token>` is the share-link credential,
// and `r.URL.String()` would put it in this line, in every proxy in front of this
// server, and in whatever the DM pastes into a bug report. The path is the only
// part of the URL that is logged, and the path is the part that says which page
// was asked for.
//
// The logger is the redacting handler from `internal/auth`, which is what makes
// that the *only* thing standing between a credential and a log file -- and it
// is why a test that greps a full auth flow's output for the token is in that
// package and not in this one.
func (a *app) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := a.cfg.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		req := requestFrom(r.Context())
		a.log.LogAttrs(r.Context(), levelFor(recorder.status),
			"request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Int64("bytes", recorder.written),
			slog.Duration("took", a.cfg.Now().Sub(started)),
			slog.String("request_id", orDash(req.ID)),
			slog.String("principal", orDash(req.Principal.ID)),
			slog.String("campaign", orDash(req.Campaign.Slug.String())),
		)
	})
}

// statusRecorder remembers the status and the size, because a logging middleware
// that cannot see either of them logs "200" for a 404.
//
// It is a wrapper rather than a `ResponseWriter` reconstruction on purpose: it
// keeps the optional interfaces (`Flusher`, `Hijacker`, `ReaderFrom`) of whatever
// it wraps, because a stream handler that panics because the logging middleware
// hid its `http.Flusher` would be a logging middleware that broke the thing it
// was measuring.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int64
	wrote   bool
}

func (w *statusRecorder) WriteHeader(status int) {
	if !w.wrote {
		w.status = status
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	w.wrote = true
	written, err := w.ResponseWriter.Write(b)
	w.written += int64(written)
	return written, err
}

// Flush is here because `internal/sse` flushes through a
// `http.ResponseController`, which finds the flusher by unwrapping. Without this
// method the wrapper is not a `http.Flusher` and the controller reports that a
// stream cannot be flushed -- which it can, and which the SSE package has to be
// able to do.
func (w *statusRecorder) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer, and anything
// else that walks the chain.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func levelFor(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// recoverPanics turns a panic into a 500 and a log line, and keeps the process
// up.
//
// A wiki serving markdown a player wrote is a program that parses untrusted
// input on every request, and the failure mode of a panic in a handler is a
// process that dies with every other player's session on it. So it is caught,
// it is logged with the stack -- which is the only part anybody can debug it
// with -- and the request is answered with the error page.
//
// The log line carries the principal and the campaign, so "it crashed" is at
// least attributable to a page and a reader.
func (a *app) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// A panic that is an `http.ErrAbortHandler` is the standard
			// library's own signal that a client hung up mid-response, and
			// re-panicking on it is what the contract asks for: it is not a bug
			// in this program and it must not be logged as one.
			if err, isError := recovered.(error); isError && errors.Is(err, http.ErrAbortHandler) {
				panic(recovered)
			}

			req := requestFrom(r.Context())
			a.log.LogAttrs(r.Context(), slog.LevelError, "panic",
				slog.Any("panic", recovered),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("request_id", orDash(req.ID)),
				slog.String("principal", orDash(req.Principal.ID)),
				slog.String("stack", string(debug.Stack())),
			)

			a.serverError(w, r)
		}()

		next.ServeHTTP(w, r)
	})
}

// session turns a cookie into a principal, and a cookie that does not resolve
// into nobody.
//
// **A failed session is not an error.** A player whose link was revoked, whose
// browser sent a cookie from a previous install, or who has simply never been
// here, is an unidentified request -- and an unidentified request reads nothing
// and is answered, not refused. The one exception worth noticing is a revoked
// principal, which is *also* nobody, because a revocation that resolves to a live
// session resolves against the player being logged in.
//
// The campaign check is here as well as in the store's predicate. The store's
// conjunct is the one that matters -- a handler cannot forget it -- and this one
// is here so that a principal from another campaign is not even *named* in the
// rest of the request: not in a decision, not in a link, not in a log line.
func (a *app) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := requestFrom(r.Context())
		// **Cleared, not defaulted.** A request whose session is refused must not
		// keep whatever was in the context already, and the context is reused down
		// the chain; so the first thing this middleware does is put nobody there
		// and the last thing is to put the answer there.
		req.Principal = domain.Principal{}
		r = r.WithContext(domain.WithPrincipal(r.Context(), domain.Principal{}))

		sessionID, hasCookie := a.sessionCookieValue(r)
		if hasCookie {
			principal, authErr := a.cfg.Redeemer.Authenticate(r.Context(), sessionID)
			switch {
			case authErr != nil && isExpectedAuthFailure(authErr):
				// One of the answers that means "nobody": no such session,
				// revoked, expired, or issued for a campaign this is not. The
				// request goes on unidentified, which reads nothing.
			case authErr != nil:
				// Anything else is the database failing, and a wiki that cannot
				// see its own sessions should say so rather than pretend nobody
				// is logged in -- a 500 that a DM can read beats a page tree
				// that has mysteriously emptied.
				a.log.LogAttrs(r.Context(), slog.LevelError, "authenticating",
					slog.String("error", authErr.Error()),
					slog.String("path", r.URL.Path),
				)
				a.serverError(w, r)
				return
			default:
				req.Principal = principal
			}
		}

		// The principal goes in the context as well as in the request, and the
		// context is the copy that travels: the link resolver reads it from inside
		// the renderer, which is not a handler and never will be (ADR 0020).
		ctx := domain.WithPrincipal(r.Context(), req.Principal)

		next.ServeHTTP(w, r.WithContext(withRequest(ctx, req)))
	})
}

// isExpectedAuthFailure reports whether an authentication error is one of the
// answers `auth` gives for a session that is not there.
// The answers `auth` gives for a session that is not usable, and none of them is
// a failure of this request: they are all "nobody is logged in", which is what an
// ordinary first visit looks like too.
var expectedAuthFailures = []error{
	auth.ErrNoSession,
	auth.ErrRevoked,
	auth.ErrBadToken,
	auth.ErrLinkExpired,
	auth.ErrNoToken,
	auth.ErrWrongCampaign,
}

func isExpectedAuthFailure(err error) bool {
	for _, expected := range expectedAuthFailures {
		if errors.Is(err, expected) {
			return true
		}
	}
	return false
}

// campaign resolves the slug in the URL and refuses a principal who is not of
// that campaign.
//
// A slug that is not a campaign is a 404, not a redirect to a list of campaigns:
// this server serves one link per campaign, and a URL that guesses at one is a
// URL that would have to enumerate them.
func (a *app) campaign(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := requestFrom(r.Context())

		// ADR 0003: a campaign response must not be cached by anything between
		// the browser and here, and this is set before the slug is even resolved
		// so that the 404 for an unknown campaign carries it too.
		w.Header().Set("Cache-Control", "no-store")

		slug, slugErr := domain.NewSlug(chi.URLParam(r, "slug"))
		if slugErr != nil {
			a.notFound(w, r)
			return
		}

		campaign, err := a.cfg.Store.CampaignBySlug(r.Context(), slug)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				a.notFound(w, r)
				return
			}
			a.fail(w, r, "looking up the campaign "+slug.String(), err)
			return
		}
		req.Campaign = campaign

		// The tenancy check, at the edge as well as in SQL. The store's conjunct
		// is the one that cannot be forgotten; this one means a principal of
		// another campaign never becomes a principal *of this request*, so nothing
		// downstream has to remember that the two can differ.
		if req.Principal.ID != "" && req.Principal.CampaignID != campaign.ID {
			a.log.LogAttrs(r.Context(), slog.LevelWarn, "cross-campaign session",
				slog.String("path", r.URL.Path),
				slog.String("principal", req.Principal.ID),
				slog.String("principal_campaign", req.Principal.CampaignID),
				slog.String("campaign", campaign.Slug.String()),
			)
			req.Principal = domain.Principal{}
		}

		next.ServeHTTP(w, r.WithContext(withRequest(r.Context(), req)))
	})
}

// nonce is the per-response Content-Security-Policy nonce, or the empty string when
// one could not be produced.
//
// **The empty string is a policy that authorises nothing, and that is the point.**
// It used to mean *no `Content-Security-Policy` header at all*, which is the exact
// opposite of what the header file argues for: "a constant with a placeholder in it is
// a string that can be served without one — which is a page with a policy that does
// not authorise anything, i.e. a page that does not work, rather than a page that is
// open." A `crypto/rand` failure is rare enough that nobody would ever see it and
// catastrophic enough that a page without a policy is the wrong way to spend the
// occasion. The reader is a DM whose page loses its live updates; nothing is served
// that the DM did not write.
func (a *app) nonce() string {
	produced, err := a.cfg.Nonce()
	if err != nil || produced == "" {
		a.log.LogAttrs(context.TODO(), slog.LevelError, "generating a CSP nonce",
			slog.String("error", errString(err)),
		)
		return ""
	}
	return produced
}

// randomNonce is the default source: 16 bytes from `crypto/rand`, base64rawurl.
//
// Sixteen bytes is 128 bits, which is the size the CSP specification's own examples
// use and which is far more than a nonce needs — the requirement is that it be
// unguessable *and unique per response*, and 128 random bits is unique for as long as
// this server is running.
func randomNonce() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("reading random bytes for a CSP nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

// errString is an error's text, or a placeholder for a nil one, so a log call for a
// failure whose error is nil still says something.
func errString(err error) string {
	if err == nil {
		return "the source returned no nonce and no error"
	}
	return err.Error()
}
