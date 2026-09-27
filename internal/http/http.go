// Package http is the web shell: the router, the middleware, the handlers and
// the templates that make a campaign readable in a browser.
//
// # What is in the request
//
// A handler is handed a `*request`: who is asking, which campaign they are
// asking about, the per-response nonce and the request id. All four arrive from
// middleware, and none of them is a parameter a handler takes, because a handler
// that takes a principal as an argument is a handler whose caller decides who the
// caller is. The whole point of the session middleware is that there is exactly
// one place that turns a cookie into a principal.
//
// # The middleware order is the argument
//
//	request id -> logging -> recovery -> security headers -> session -> campaign
//
// Read from the inside out, it says what each layer is for:
//
//   - **Redeem** exchanges a `?k=` share link for a cookie and redirects. It is
//     inside campaign and session because a token is scoped to one campaign and
//     because a browser that already has a session and then arrives with a new
//     link is becoming a *new* identity rather than the old one with a new
//     cookie.
//   - **Campaign** resolves `/c/<slug>` and refuses a principal who is not of
//     that campaign. It is inside redeem because redemption has to be able to say
//     "that link belongs to a different campaign", and it is outside everything
//     below it because a handler that runs first has no campaign to scope
//     anything to.
//   - **Session** turns a cookie into a principal, and a cookie that does not
//     resolve into *nobody* rather than into an error. An unidentified request
//     is a request; it reads nothing.
//   - **Security headers** are set before a handler runs, because a handler that
//     has written a body has written the headers too and cannot add to them.
//   - **Recovery** catches a panic, so it is outside the headers: it has to be
//     able to produce a response of its own.
//   - **Logging** sees every request, including the ones that panicked.
//   - **Request id** is outermost because everything else's log line names it.
//
// # The query string is not logged
//
// `?k=<token>` is the share-link credential, and a logging middleware that writes
// `r.URL.String()` puts it in every log line, in every proxy in front of this
// server, and in whatever the DM pastes into a bug report. The log line here is
// built from the method and the path and nothing else.
//
// # The cookie attributes are ADR 0003's
//
// `__Host-` prefix, `HttpOnly`, `SameSite=Lax`, `Secure` when the deployment is
// production, `Path=/`, and an expiry that is the session's own rather than the
// browser's. See cookie.go for why each one is there, because each of them is a
// decision and not a default.
package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/edit"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/events"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/version"
	"github.com/popinjayjohn/dine-and-dash-semiplane/web"
)

// Config is everything the application cannot invent for itself.
//
// It is a struct of dependencies with defaults rather than a constructor per
// combination, because the only caller that assembles it is `wiki serve` and the
// other callers are tests. A field that may be nil and has a documented meaning
// is a field a test does not have to set in order to test something else.
type Config struct {
	// Store is the index. It is an interface rather than `*store.Store` because
	// this package is the consumer: it needs to read a page, list the pages a
	// principal may see, resolve a campaign and ask about ownership, and nothing
	// else. A method added to the store does not have to be added here, and one
	// added here has to be one somebody can implement.
	Store Store

	// Redeemer exchanges a share link for a session, and a cookie's value for a
	// principal. It is a value rather than a pointer because it holds a Backend
	// and a Config and has no state of its own.
	Redeemer auth.Redeemer

	// Minter issues a share link, for the users page. It is a separate field from
	// the redeemer because the two answer different questions — "may this be a
	// session" and "may this person be a principal" — and a caller that had to
	// reach through the redeemer to mint one would be reaching past the gate.
	Minter auth.Minter

	// EditorFor returns the writer for a campaign, and it is a function because the
	// writer needs a vault and a vault is a directory -- which `wiki serve` has
	// open per campaign and this package has no business opening.
	//
	// It is the same shape as a `Store` field: the consumer says what it needs and
	// the caller assembles it. The result is cached per campaign, because an editor
	// is an editor and a 500-entry cache; a DM with a dozen campaigns over a year
	// has a dozen of them.
	EditorFor func(campaign domain.Campaign) (*edit.Editor, error)

	// BaseURL is the origin share links are built against, and it is what the
	// users page builds a link from — not a request's `Host`, for the reason
	// `auth.Config.BaseURL` gives.
	BaseURL string

	// Hub is the SSE fan-out, and it is required rather than optional.
	//
	// The hub is the caller's because its lifecycle is the caller's: `wiki serve`
	// closes it on the way out, which is the drain ADR 0006 asks for, and a hub
	// the application built for itself would be one nobody could close. `Streams`
	// is how one is built, and a test that does not care about live pages makes
	// one with a bound of one.
	Hub *sse.Hub

	// Logger is where the request log goes. Nil means the default slog logger,
	// which is what `go run` wants and what a test replaces with one writing to a
	// buffer, because `TestNoTokenInLogs` reads what was written.
	Logger *slog.Logger

	// Version is reported by /_/healthz and in the page footer. It is passed in
	// rather than imported because the footer is telling the reader which binary
	// they are running and internal/version is where that is assembled.
	Version version.Info

	// Nonce is the source of the per-response Content-Security-Policy nonce, and
	// the error is the interesting half: a source that cannot produce one must
	// still produce a response, and that response must carry a policy.
	//
	// It is here because the alternative is a `crypto/rand` call inside a
	// middleware, which is a failure path with no test — and this failure path is
	// the difference between a page that is open and a page whose script does not
	// run. See [app.nonce] and `TestARequestWhoseNonceCannotBeGeneratedStillGetsAPolicy`.
	//
	// Nil means [crypto/rand].
	Nonce func() (string, error)

	// Now is the clock. Nil means the system clock, which is the right default
	// for a program running on a DM's own machine and the wrong one for a test
	// that wants a CSRF token to expire.
	Now func() time.Time

	// Production says whether this is a production deployment, and the only
	// thing it changes is `Secure` on the cookie. A DM running the wiki on their
	// own machine over plain HTTP on a LAN has to be able to use it, and a
	// cookie the browser refuses is a wiki nobody can log in to.
	Production bool

	// AllowedOrigin is the scheme and host a mutation has to come from, checked
	// against `Origin` so that a page on another site cannot post to this one.
	// Empty skips the check, which is the right default on a LAN where a browser
	// sends no `Origin` for a same-origin form post -- and the double-submit
	// token is the check that does not depend on the header being there.
	//
	// It is configuration and never derived from a request's `Host`, because the
	// whole question is "is this the host I would have written a form into" and a
	// `Host` the caller chose answers nothing.
	AllowedOrigin string

	// Secret is the CSRF secret, and a nil or short one makes a fresh random
	// secret at startup. Supplying it is for the deployment with more than one
	// process, where a per-process secret would mean a form written by one is
	// refused by the other.
	Secret []byte

	// Hooks are the plugins' render hooks, and the editor is given the same set
	// from `EditorFor` -- a caller that wires one and not the other has a preview
	// that disagrees with the page it precedes, which is a bug that is reported as
	// "the preview lies" rather than as a missing plugin.
	//
	// The zero value renders exactly what a build without plugins rendered, so
	// every test that does not care about plugins does not have to say so.
	Hooks render.Hooks

	// Routes are the paths the plugins mount inside the campaign group, in the
	// plugins' `(Priority, Name)` order. They are mounted before the catch-all page
	// route, so a plugin's first segment beats a page at the same path — see
	// [Route] for why the answer is a route rather than a query parameter and what a
	// plugin author can do about it.
	//
	// A nil slice is the same application as an empty one, and a build with no
	// plugins never reaches [app.mountRoutes] with anything in it.
	Routes []Route

	// Events is the bus the plugins subscribe to. A nil one is a bus with no
	// subscribers, so every publisher calls it unconditionally rather than testing a
	// configuration field on the path of a page view.
	Events *events.Bus

	// Policies are the plugins' access rules, composed with the rights matrix and
	// never replacing it. A nil one is the same application as an empty one, so
	// the three places that ask -- `decisionFor`, the page tree and the search
	// dropdown -- ask unconditionally.
	//
	// A policy narrows what is *served and listed*, not what the store's read
	// predicate admits. That is a real line and it is the safe side of it: the SQL
	// is the invariant, and a policy is a second, stricter layer on top. See
	// internal/access/policy.go.
	Policies *access.Policies
}

// DefaultStreams is how many live page streams one server holds open. A campaign
// is a DM and four players, each with a tab or two open, so this is generous; it
// is here rather than hard-coded into the hub because the hub is a general
// fan-out and this is one deployment's appetite, and it is a constant so that the
// one test that wants a bound can ask for a different one.
const DefaultStreams = 32

// Store is what the HTTP layer reads through.
//
// The lookups are `index.Lookups` because the link resolver in a rendered page
// needs exactly those and the renderer will not take a second, narrower view of
// the same index: a `[[link]]` and the page tree must agree about what exists,
// and two interfaces that each listed a subset of the store is a way for them to
// stop agreeing.
type Store interface {
	index.Lookups

	// ListPages returns the pages a principal may read, ordered by path, which
	// is the page tree and everything else that is "what is in this campaign".
	ListPages(ctx context.Context, campaignID string, as domain.Principal) ([]domain.Page, error)

	// CampaignBySlug resolves the slug in a URL. A slug that is not a campaign
	// is `store.ErrNotFound` and the route answers 404.
	CampaignBySlug(ctx context.Context, slug domain.Slug) (domain.Campaign, error)

	// ListRecentlyChanged returns the pages a principal may read, most recently
	// changed first, and at most `limit` of them. It is the session log's one
	// query, and it carries the read predicate like every other page-returning
	// method here — a player watching the log must not learn that a private page
	// changed.
	ListRecentlyChanged(ctx context.Context, campaignID string, as domain.Principal, limit int) ([]domain.Page, error)

	// ListPrincipals returns a campaign's principals, for the users page. It is a
	// DM-only caller's use and the page checks the role itself: a principal list is
	// not a page's audience, so it has no decision and pretending otherwise would put
	// a fifth field in the resolver for a question with two answers.
	ListPrincipals(ctx context.Context, campaignID string) ([]domain.Principal, error)

	// PrincipalByID is one of them, and it is how a revocation checks that the row
	// it is about is in *this* campaign. A revocation is a write, and a write on
	// another campaign's row is not this DM's.
	PrincipalByID(ctx context.Context, id string) (domain.Principal, bool, error)

	// RevokePrincipal ends a principal's access, and AppendAudit records that it
	// happened. The audit row is written by the caller because the page is where
	// the reason is known.
	RevokePrincipal(ctx context.Context, id string) error
	AppendAudit(ctx context.Context, e domain.AuditEntry) (domain.AuditEntry, error)

	// The two searches, which are what a search box is. They are a named pair
	// rather than one method with a flag because they read different tables and
	// are filtered by different scopes, and only one of them may be called for a
	// principal who is not entitled to it -- a flag would make the unsafe call a
	// one-character mistake. The comment is `search.Searcher`'s and is the reason
	// they are here rather than reached for separately.
	search.Searcher

	// OwnerExists answers "is this principal bound to this character page", and
	// takes the *character's* page id rather than the page being decided about.
	// The resolver's `Owned` is that answer, and it must not be approximated by
	// asking whether a character owns the page: that is the version which gives
	// a player the whole subtree or none of it.
	OwnerExists(ctx context.Context, principalID, characterPageID string) (bool, error)
}

// request is what a handler is given: everything the middleware decided, and
// nothing a handler has to go and look up.
//
// It is one struct rather than four context values because a handler that reads
// one out of a context and then has to nil-check it has a code path for "the
// middleware did not run", and that path is the one nobody tests. It is
// unexported because the only way in is `requestFrom`, and a type that is in a
// context under an unexported key is not much use to a caller who cannot set it.
type request struct {
	// Principal is who is asking. The zero value is nobody, and nobody reads
	// anything: the store's predicate admits nothing for it, which is a
	// property of the predicate and not of this package.
	Principal domain.Principal

	// Campaign is the campaign in the URL, and the zero value on a route outside
	// `/c/`.
	Campaign domain.Campaign

	// Nonce is the per-response CSP nonce. It is in the request because the
	// templates need it, and because a stream that injects a script needs the
	// same one or the page's own policy refuses it.
	Nonce string

	// ID is the request id, which is in the response headers and in the log line.
	ID string
}

// identified reports whether a principal was found for this request. It is a
// method rather than a test of a field because a handler asking "is anybody
// there" is asking about the session, and the answer is not "is the id non-empty".
func (r *request) identified() bool { return r.Principal.ID != "" }

// isDM reports whether the caller is a DM of this campaign. It reads the role
// rather than a decision because the things it gates -- the logout form, the DM's
// own navigation -- are not per-page rights, and pretending they were would put a
// fourth Decision field in the resolver for nothing.
func (r *request) isDM() bool { return r.Principal.Role == domain.RoleDM }

// app is the application's own state, and it is a separate type from `Server` so
// that the two things a caller can do beyond making a request are visible on the
// type and everything else is not.
type app struct {
	cfg Config
	log *slog.Logger

	// startedAt is when this application was built, on the *injected* clock.
	//
	// It was a package variable reading `time.Now()`, and a test with a fixed
	// clock then reported an uptime of minus five thousand hours -- which is the
	// whole argument for injecting a clock in the first place, and the reason the
	// uptime is computed here rather than from a global.
	startedAt time.Time

	// editors is one writer per campaign, built on first use, for the same reason
	// `renderers` is one renderer per campaign.
	editorsMu sync.Mutex
	editors   map[domain.Slug]*edit.Editor

	// renderers is one renderer per campaign, built on first use.
	//
	// A renderer holds a link resolver and a resolver belongs to a campaign --
	// `index.NewResolver` takes one -- so a single shared renderer could not
	// resolve anything. Each has its own LRU, and sharing one cache across
	// campaigns would have been the cheaper choice except that the campaign is in
	// the cache key precisely because two campaigns can hold byte-identical
	// pages.
	renderersMu sync.Mutex
	renderers   map[domain.Slug]*render.Renderer
}

// The two things New refuses without. They are refusals rather than defaults
// because an application with no store answers every request with a 500, and one
// with a hub nobody can close leaks a goroutine per open tab on every restart.
var (
	errNoStore = errors.New("http: no store was configured")
	errNoHub   = errors.New("http: no stream hub was configured")
)

// PageChanged says that a page's rows have been written, so every browser reading
// it is sent a new copy.
//
// The paths are the ones a sync reported -- `index.Report.Indexed` and
// `index.Report.Archived` -- because those are the pages whose *rows* moved. A file
// that was written and produced no change is not a change, and telling every open
// page about it would be a render per reader per keystroke in an editor.
//
// It is a function taking the hub and not a method on the application because the
// only thing it needs is the hub and the rule for a topic's name, and the rule for
// a topic's name is a rule about how this package spells a page. A watcher that
// had to agree with the router about that would be a second implementation of a
// URL.
//
// It is exported rather than a method because the caller of `New` is the only thing
// that knows when a sync has finished, and the hub is the caller's -- the same
// rule that says the caller closes the store it opened.
func PageChanged(hub *sse.Hub, campaignID string, paths ...string) {
	// The campaign's own topic first, and it is the same notice: the session log
	// wants to know that *something* changed so it can re-read the list, and it
	// re-reads it under its own decision. So there is no second kind of message
	// here -- a change to a page is a change to the campaign, and the log
	// subscribes to both because it wants the campaign-wide one.
	hub.Publish(streamTopic(campaignID, ""))

	for _, path := range paths {
		hub.Publish(streamTopic(campaignID, path))
	}
}

// New returns the application.
func New(cfg Config) (http.Handler, error) {
	if cfg.Store == nil {
		return nil, errNoStore
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Nonce == nil {
		cfg.Nonce = randomNonce
	}

	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Hub == nil {
		return nil, errNoHub
	}

	a := &app{
		cfg:       cfg,
		log:       cfg.Logger,
		renderers: map[domain.Slug]*render.Renderer{},
		editors:   map[domain.Slug]*edit.Editor{},
		startedAt: cfg.Now(),
	}
	a.initSecret()

	router := chi.NewRouter()
	router.NotFound(a.notFound)
	router.MethodNotAllowed(a.methodNotAllowed)

	// The order of the middleware is in the package comment and none of it is
	// arbitrary: each layer is outside the one that needs it.
	router.Use(a.requestID)
	router.Use(a.logRequests)
	router.Use(a.recoverPanics)
	router.Use(a.securityHeaders)

	router.Get("/_/healthz", a.healthz)
	router.Handle("/static/*", web.Handler("/static/"))

	router.Route("/c/{slug}", func(c chi.Router) {
		// The order of these three is the answer to "who is asking, about what,
		// and do they have a link": session first because a cookie is the only
		// thing that carries an identity, campaign second because a token is
		// scoped to one, and redeem last because it needs both.
		c.Use(a.session)
		c.Use(a.campaign)
		c.Use(a.redeem)

		// The campaign root, which is where a redemption lands and where a reader
		// lands with nothing else to read: every page they may see. The editor for a
		// new page hangs off it as a query, because a create has no path yet and a
		// path segment would be a name the vault could not also use.
		//
		// `?users=1` is the same root and not a path: `users` is exactly the kind
		// of page a DM writes, and a path segment here would be a name the vault
		// could not also use.
		c.Get("/", a.browse)
		c.Post("/", a.rootPost)

		// A plugin's routes, before the catch-all below, because the catch-all is
		// `/*` and everything is behind it.
		a.mountRoutes(c)

		// A page. `?raw=1` is the same page as markdown, `?stream=1` is the same
		// page as a stream, and `?edit=1` is the same page being edited. All
		// three are query parameters and not path segments, because a path
		// segment would be a first-segment name that the vault could not also
		// use -- and the vault is the source of truth (ADR 0001), so it wins any
		// argument about what a URL may look like. A DM with a page at
		// `locations/edit` has it.
		c.Get("/*", a.page)
		c.Post("/*", a.pagePost)
	})

	return router, nil
}

// rendererFor returns the renderer for a campaign, building it the first time it
// is asked for.
//
// A campaign can appear while the server is running -- a DM drops a folder into
// `vault/` and the watcher indexes it -- so the map fills lazily rather than at
// startup, and it is never pruned: a renderer is a renderer and a 500-entry
// cache, and a DM with a dozen campaigns over a year has a dozen of them.
// editorFor is the writer for a campaign.
//
// A missing `EditorFor` is a 500 on the editor route and a *missing Edit link*
// everywhere else, rather than a refusal at startup: a server with no editor is a
// read-only wiki, and a read-only wiki is a thing somebody might want (a DM
// syncing on one machine and serving on another), so it is a missing feature in the
// safe direction rather than a misconfiguration.
func (a *app) editorFor(campaign domain.Campaign) *edit.Editor {
	a.editorsMu.Lock()
	defer a.editorsMu.Unlock()

	if existing, found := a.editors[campaign.Slug]; found {
		return existing
	}
	if a.cfg.EditorFor == nil {
		return nil
	}

	built, err := a.cfg.EditorFor(campaign)
	if err != nil {
		// Logged and cached as nil, so a vault that cannot be opened is one log
		// line rather than one per request.
		a.log.LogAttrs(context.TODO(), slog.LevelError, "opening a campaign's writer",
			slog.String("campaign", campaign.Slug.String()),
			slog.String("error", err.Error()))
		a.editors[campaign.Slug] = nil
		return nil
	}

	a.editors[campaign.Slug] = built
	return built
}

func (a *app) rendererFor(slug domain.Slug) *render.Renderer {
	a.renderersMu.Lock()
	defer a.renderersMu.Unlock()

	if existing, found := a.renderers[slug]; found {
		return existing
	}

	built := render.NewWith(render.Options{
		Links: index.NewResolver(a.cfg.Store, slug.String()),
		Hooks: a.cfg.Hooks,
		Log:   a.log,
	})
	a.renderers[slug] = built
	return built
}

// contextKey is the private type behind the request in a context, so that
// nothing outside this package can put one there or read one out by accident.
type contextKey struct{}

// requestFrom is the request a handler is working on. It is never nil: the
// middleware stack puts one in for every request, and a handler that found nil
// would have been mounted outside the router rather than inside it.
func requestFrom(ctx context.Context) *request {
	if req, ok := ctx.Value(contextKey{}).(*request); ok {
		return req
	}
	return &request{}
}

// campaignURL is a campaign's root: where a redemption lands, and what the
// browse route answers.
//
// It is `render.PageURL` and not a second copy of the rule, because the URLs the
// renderer writes into a page and the URLs this handler redirects to are the same
// URLs. Two implementations of "where does a campaign live" is a campaign that
// works until somebody changes one of them.
func campaignURL(slug domain.Slug) string {
	return render.PageURL(slug.String(), "")
}

// withRequest puts a request in a context. It is one function because the
// middleware chain and nothing else builds these contexts, and a context value
// assembled in two places is two places to forget the key.
func withRequest(ctx context.Context, req *request) context.Context {
	return context.WithValue(ctx, contextKey{}, req)
}
