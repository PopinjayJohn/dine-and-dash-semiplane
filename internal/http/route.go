package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Route is a path a plugin mounts inside the campaign.
//
// # Inside the campaign group, and why that is the only place
//
// A plugin's route hangs off `/c/{slug}`, which means it is behind the same three
// middlewares as a page: the session, the campaign and the redemption. That is the
// point of the shape, and it is why a plugin cannot mount one anywhere else — there
// is nowhere else.
//
// The three give a plugin handler a resolved `*request` in its context, with a
// principal, a campaign and a slug, and they give it the *same* answers a page gets.
// A plugin that asked who is asking and what campaign they are in therefore asks the
// same question through the same code, and cannot get a different answer by being a
// plugin.
//
// # A first segment the vault could not also use
//
// The pattern is mounted at the campaign root, so a plugin's route and a page's path
// share a namespace: `GET /c/blackwater/wordcount` is a plugin's route *or* a page
// at `wordcount`, and the first wins. That is the same tension `?raw=1` and
// `?edit=1` resolved by being query parameters instead (see `app.page`), and it is
// worth being honest that this capability resolved it the other way.
//
// The reason is that a plugin's route is a *view*, and a view is what the query
// parameters on the campaign root are for: `?wordcount=1` would have been the
// consistent answer. What a route buys over a query parameter is that it can be a
// fragment, a redirect, a download, or a sub-path of its own — which is what
// `wordcount` needs and what no query parameter can be. The cost is that a DM with
// a page at that path has a plugin's route instead, and the resolution is the
// router's: the route is mounted before the catch-all `/*`.
//
// A plugin that wants no such claim on the namespace mounts a sub-path of its own —
// `/c/{slug}/wordcount/report` — and leaves the first segment free. `docs/plugins.md`
// says so where a plugin author will read it.
type Route struct {
	// Plugin is the name of the plugin that registered this route, for a log line
	// and for `wiki help plugins`. It is the normalised slug.
	Plugin string

	// Pattern is a chi pattern, relative to the campaign root. It may contain
	// `{slug}`-style parameters of its own and it may end in `/*` for a subtree; the
	// campaign's own `{slug}` is already consumed by the group it is mounted in.
	//
	// A pattern that is not a valid chi pattern is refused by
	// [Registry.AddRoute] before the server starts rather than by a panic in the
	// router at the first request.
	Pattern string

	// Handler answers the request. It gets the campaign's principal, campaign and
	// slug from the context, through the same [request] a page handler uses.
	Handler http.Handler
}

// mountRoutes registers a plugin's routes inside the campaign group.
//
// It is called once, at construction, from the same place the core's own routes are
// registered — and the ordering is the point of the paragraph on [Route]: the plugin
// routes go on the group *before* the catch-all, so they win, and a DM whose page
// shares a first segment with a plugin has the plugin.
//
// There is no dynamic registration. A route is mounted when the router is built,
// which is before the listener is up, and a plugin that wanted to add one later
// would want the router to be mutable, which is a data race wearing a feature.
func (a *app) mountRoutes(c chi.Router) {
	for _, route := range a.cfg.Routes {
		if route.Pattern == "" || route.Handler == nil {
			// Unreachable: `http.New` has already refused both, and a config that
			// somehow got here is a bug in this package rather than in a plugin. The
			// check is cheap and the alternative is a nil dereference in a request
			// goroutine, which is where a nil dereference is least welcome.
			continue
		}
		c.Get(route.Pattern, route.Handler.ServeHTTP)
	}
}
