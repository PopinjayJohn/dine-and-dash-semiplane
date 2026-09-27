package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/version"
)

// healthz is the liveness line, and it is JSON because the thing reading it is a
// monitoring script and not a person.
//
// It is under `/_/` because every campaign's URLs are under `/c/`, and a health
// line at the root would be a page a player's browser could ask for. It never
// requires a session, because a health check that needed one would report a wiki
// as down every time a player's link was revoked.
func (a *app) healthz(w http.ResponseWriter, r *http.Request) {
	uptime := a.cfg.Now().Sub(a.startedAt).Round(time.Second)

	body, err := json.MarshalIndent(health{
		Status:  "ok",
		Version: a.cfg.Version,
		Uptime:  uptime.String(),
	}, "", "  ")
	if err != nil {
		// Marshalling a struct of strings cannot fail, and a health check that
		// returned 500 when it could not render its own answer would at least have
		// told the operator something.
		a.fail(w, r, "marshalling the health line", err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusOK)
	// The status is sent, so a write error is the client's connection rather than
	// this program's, and the only thing left to do is stop.
	_, _ = w.Write(append(body, '\n'))
}

// health is what the liveness line says. The field names are the ones an operator
// reading `curl /_/healthz` would guess at.
type health struct {
	// Status is always "ok" when the response is 200. It is a field rather than
	// nothing so that a future dependency check has somewhere to put a value that
	// is not "ok" without changing the shape of the answer.
	Status string `json:"status"`

	Version version.Info `json:"version"`

	// Uptime is for a human, and it is a duration string because a bare number of
	// seconds is one everybody has to convert in their heads.
	Uptime string `json:"uptime"`
}

// browse is the campaign root: the campaign's own page if it has one, and the
// tree of everything the reader may see.
//
// It is a page rather than a redirect to some front page because "the front page"
// is a concept a vault of folders does not have, and because the one page a
// campaign might want to be its own -- `campaign.md`, which is in the spec's own
// layout -- is rendered here under the same decision as every other page.
func (a *app) browse(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if req.Campaign.ID == "" {
		a.fail(w, r, "browsing a campaign that is not there", errNoCampaign)
		return
	}

	// `?new=1` is the editor for a page that does not exist yet, and `?users=1` is
	// the campaign's principals. Both hang off the root because a create has no
	// path to hang off and a list of people is not a page.
	switch {
	case r.URL.Query().Has("new"):
		a.newPage(w, r)
		return
	case r.URL.Query().Has(usersQuery):
		a.usersPage(w, r)
		return
	}

	pages, err := a.cfg.Store.ListPages(r.Context(), req.Campaign.ID, req.Principal)
	if err != nil {
		a.fail(w, r, "listing the pages of "+req.Campaign.Slug.String(), err)
		return
	}

	view := browseData{
		shell: a.shellFor(req, pages, req.Campaign.Name),
		Count: len(pages),
	}

	// The landing page is optional, and "not there" is its ordinary state -- most
	// campaigns will not have one -- so a not-found is not a failure and anything
	// else is.
	if landing, err := a.cfg.Store.GetPage(r.Context(), req.Campaign.ID, landingPath, req.Principal); err == nil {
		result, renderErr := a.renderPage(r.Context(), req.Campaign, landing, req.Principal)
		if renderErr != nil {
			a.fail(w, r, "rendering "+landingPath, renderErr)
			return
		}
		view.Intro = pageFragment(result.HTML)
		view.HasIntro = true
	} else if !isNotFound(err) {
		a.fail(w, r, "reading "+landingPath, err)
		return
	}

	a.render(w, r, statusOK, browse(view))
}

// landingPath is the page a campaign's root shows if it has one. It is the one
// file name the spec's own vault layout guarantees, which is why it is a constant
// rather than a frontmatter flag.
const landingPath = "campaign"

// page is a page, and the other two things a page is available as.
//
// The three are one handler rather than three routes because they are one
// resource: a page as HTML, a page as the markdown it is, and a page as a stream
// of changes. The markdown is a `?raw=1` query parameter and the stream is
// `?stream=1` rather than two path segments, because a path segment would be a
// first-segment name the vault could not also use -- and the vault is the source
// of truth (ADR 0001), so it wins any argument about what a URL may look like.
func (a *app) page(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if req.Campaign.ID == "" {
		a.fail(w, r, "reading a page of no campaign", errNoCampaign)
		return
	}

	// `?edit=1` is the editor, and it is reached before the path is resolved: the
	// editor is about a page, so it needs the same 404 for a page that is not there
	// as the page itself does, and resolving the path twice is two chances to
	// resolve it differently.
	if r.URL.Query().Has("edit") {
		path, ok := pagePathFromURL(chi.URLParam(r, "*"))
		if !ok {
			a.notFound(w, r)
			return
		}
		a.editPage(w, r, path)
		return
	}

	path, ok := pagePathFromURL(chi.URLParam(r, "*"))
	if !ok {
		// Not a 400. A URL that cannot name a page is a URL that names nothing,
		// and "your path was malformed" versus "there is no such page" is not a
		// distinction a player-reachable route should offer.
		a.notFound(w, r)
		return
	}

	// The stream is a different response to the same resource, and it reads the
	// page itself: the 404 below has to be the same 404 for both, or a reader who
	// asked for a live copy of a page they may not see gets one thing and a reader
	// who asked for the page gets another.
	streaming := r.URL.Query().Has("stream")

	stored, err := a.cfg.Store.GetPage(r.Context(), req.Campaign.ID, path, req.Principal)
	if err != nil {
		if isNotFound(err) {
			// One answer for "no such page" and "not for you", and no log line: a
			// caller who can tell those two apart has an oracle for finding out
			// which pages exist in a campaign they may not read.
			a.notFound(w, r)
			return
		}
		a.fail(w, r, "reading "+path, err)
		return
	}

	if streaming {
		a.pageStream(w, r, path)
		return
	}

	if r.URL.Query().Get("raw") == "1" {
		a.pageSource(w, r, stored)
		return
	}

	// The decision is computed once and used twice: once to render, and once to
	// decide whether the page offers an Edit link. It is the *same* decision, so
	// the link cannot be offered to somebody the page was rendered against as
	// somebody who may not write it — and the write gate is asked again when the
	// editor is opened, because an affordance is a hint and a gate is a rule.
	decision := a.decisionFor(r.Context(), stored, req.Principal)
	result, err := a.rendererFor(req.Campaign.Slug).Render(r.Context(), render.Page{
		Campaign:    req.Campaign.Slug.String(),
		Path:        stored.Path,
		Body:        stored.Body,
		ContentHash: stored.ContentHash,
	}, decision)
	if err != nil {
		a.fail(w, r, "rendering "+path, err)
		return
	}

	view := pageData{
		shell: a.shellFor(req, a.pagesIn(r), stored.Title),
		Page: renderedPage{
			Path:      stored.Path,
			Title:     stored.Title,
			Kind:      stored.Type.String(),
			Updated:   stored.UpdatedAt.UTC().Format(time.RFC3339),
			Audience:  stored.Visibility.String(),
			HTML:      pageFragment(result.HTML),
			TOC:       result.TOC,
			RawURL:    rawURL(req.Campaign, stored.Path),
			StreamURL: streamURL(req.Campaign, stored.Path),
			EditURL:   editURL(req.Campaign, stored.Path),
			CanEdit:   decision.CanEdit,
		},
	}
	view.Current = stored.Path

	a.render(w, r, statusOK, page(view))
}

// pageSource is `?raw=1`: a page's markdown, under the same decision the HTML
// would be made under.
//
// **This is where a secret leaks if somebody writes `w.Write([]byte(page.Body))`.**
// `page.Body` is the file as the DM wrote it, `[!SECRET]` blocks and all, and a raw
// endpoint that writes it has published every secret in a campaign to every
// player with a link. So the body is not written. What goes out is the body with
// the unrevealed secrets replaced by the renderer's own marker, which is
// `render.PublicText` -- the same function the public search index is built from,
// so a page found by a search and a page fetched raw contain the same characters
// by construction rather than by agreement.
//
// A DM sees the file as they wrote it, because their decision has
// `CanSeeSecrets` set. The frontmatter is kept in both cases: it is the DM's own
// metadata and `visibility:` in it is what a reader needs to understand why a
// block is missing.
func (a *app) pageSource(w http.ResponseWriter, r *http.Request, page domain.Page) {
	req := requestFrom(r.Context())

	decision := a.decisionFor(r.Context(), page, req.Principal)
	source := page.Frontmatter
	if decision.CanSeeSecrets {
		source += page.Body
	} else {
		// The second value is how many secrets were removed, which is the number
		// the log line below wants: a player fetching raw for a page whose
		// secrets were stripped is ordinary, and the count is how a DM confirms
		// the stripping happened rather than trusting that it did.
		public, stripped := render.PublicText(page.Body)
		source += public
		if stripped > 0 {
			a.log.LogAttrs(r.Context(), slog.LevelDebug, "stripped secrets from a raw page",
				slog.String("page", page.Path),
				slog.Int("secrets", stripped),
			)
		}
	}

	w.Header().Set("Content-Type", contentTypeText)
	w.WriteHeader(statusOK)
	// The status is sent. A write error is the client's connection closing, and
	// the only thing to do about it is stop.
	_, _ = w.Write([]byte(source))
}

// pagesIn lists the pages a request's principal may read, for the tree in the
// sidebar.
//
// A failure here is not a failure of the request. The page is readable and the
// navigation is decoration, so an empty tree is better than a 500 for a page that
// exists. The reason is logged, because "the tree is empty" on its own is the
// least diagnosable thing in this application.
func (a *app) pagesIn(r *http.Request) []domain.Page {
	req := requestFrom(r.Context())

	pages, err := a.cfg.Store.ListPages(r.Context(), req.Campaign.ID, req.Principal)
	if err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelWarn, "listing the page tree",
			slog.String("error", err.Error()),
			slog.String("campaign", req.Campaign.Slug.String()),
		)
		return nil
	}
	return pages
}

// logout ends a session.
//
// It is a POST because a GET that ends a session is a session anyone can end: a
// page on another site putting an image tag on `/c/blackwater/` would log a
// player out, which is a nuisance rather than a compromise. The CSRF token is
// checked first, so a forged POST is refused, and `TestAMutationWithoutTheTokenIsRefused`
// is the named test.
//
// The session is ended in the store as well as in the browser. Clearing the
// cookie alone leaves a live session in the database that a copy of its id could
// still use, and the audit row is the record that it happened.
func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if req.Campaign.ID == "" {
		a.fail(w, r, "logging out of no campaign", errNoCampaign)
		return
	}

	if err := a.checkCSRF(r); err != nil {
		// 403 and not 404: the campaign exists, the request is well formed, and
		// the only thing wrong with it is who sent it. Answering 404 would hide
		// the check working.
		a.forbidden(w, r, "logout without a valid CSRF token")
		return
	}

	if sessionID, hasCookie := a.sessionCookieValue(r); hasCookie {
		// Only a session that exists is worth ending. An expired one has already
		// stopped working, and a logout that 500s because there was nothing to
		// log out of is a logout that fails when the user most wants it to work.
		if _, err := a.cfg.Redeemer.Authenticate(r.Context(), sessionID); err == nil {
			if err := a.cfg.Redeemer.EndSession(r.Context(), sessionID); err != nil {
				a.fail(w, r, "ending a session", err)
				return
			}
		}
	}

	a.clearSessionCookie(w)
	a.redirect(w, campaignURL(req.Campaign.Slug))
}

// noticeShell is the shell a notice page is drawn in.
//
// It is a function because there are four notices and the first three of them
// were each building the shell by hand -- which is how the 404 ended up with a
// logout form whose CSRF token was the empty string, a form that could never be
// submitted. One function is one way to be right, and a notice cannot forget a
// field it was never given.
//
// The tree comes from the caller because the 404 and the 500 want one and the 405
// cannot have one: a 405 comes from a URL outside `/c/`, and a campaign that is not
// there has no pages to list.
func (a *app) noticeShell(req *request, pages []domain.Page, title string) shell {
	return shell{
		Title:      title,
		Nonce:      req.Nonce,
		Campaign:   campaignNavFor(req.Campaign),
		Tree:       treeFor(req.Campaign.Slug, pages),
		Identified: req.identified(),
		IsDM:       req.isDM(),
		CSRF:       a.csrfToken(req.Principal.ID),
		Footer:     footerView{Version: a.cfg.Version},
	}
}

// notFound is the 404, and it is a page rather than `http.NotFound` so that a
// campaign's 404 is a campaign page -- with its navigation, so a player who
// followed a link to a page that has not been written yet can see what else there
// is.
//
// It says nothing about *why*. A page that exists but is not for you and a page
// that does not exist produce the same bytes.
func (a *app) notFound(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())

	// A 404 on a URL with a campaign in it still gets a tree: the reader is
	// somewhere real and has a sidebar. A 404 on a URL without one does not, and
	// `campaignRootOf` returns "" for that so the template draws a page with no
	// navigation rather than a link to `/c/`.
	var pages []domain.Page
	if req.Campaign.ID != "" {
		pages = a.pagesIn(r)
	}

	a.render(w, r, http.StatusNotFound, notice(noticeData{
		shell:   a.noticeShell(req, pages, "Not found"),
		Request: viewRequestFor(req),
		Title:   "Not found",
		Body:    "There is no page at this address, or it is not yours to read. Those are the same answer on purpose.",
		Status:  http.StatusNotFound,
	}))
}

// methodNotAllowed is the 405, and it carries `Allow` because a 405 without it
// makes the client guess.
func (a *app) methodNotAllowed(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())

	w.Header().Set("Allow", "GET, HEAD, POST")
	a.render(w, r, http.StatusMethodNotAllowed, notice(noticeData{
		// No pages: a 405 comes from a URL outside the campaign routes, and the
		// campaign is not there to have any.
		shell:   a.noticeShell(req, nil, "Not a thing you can do here"),
		Request: viewRequestFor(req),
		Title:   "Not a thing you can do here",
		Body:    "This address does not answer to that kind of request.",
		Status:  http.StatusMethodNotAllowed,
	}))
}

// serverError is the 500.
//
// It is a page like any other, because a DM reading a stack trace in a browser
// learns nothing useful and a stack trace in a browser is a disclosure. The
// request id is in the body and in the log, and that is the whole of the bridge
// between the message on the screen and the reason in the file.
// It asks the store for **nothing**. The store is the thing that has just failed,
// and a 500 page that lists a page tree is a 500 page that queries the database
// again -- which is a second failure, and a panic inside the recovery handler is a
// panic that takes the process down with every other player's session on it. That
// was found by a test that made the store panic rather than fail.
func (a *app) serverError(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())

	a.render(w, r, http.StatusInternalServerError, notice(noticeData{
		shell:   a.noticeShell(req, nil, "Something went wrong"),
		Request: viewRequestFor(req),
		Title:   "Something went wrong",
		Body:    "The wiki could not answer that. The request id below is in the server's log with the reason.",
		Status:  http.StatusInternalServerError,
	}))
}

// forbidden is the 403, and it is used for exactly one thing: a mutation that
// arrived without a token proving it was not forged.
func (a *app) forbidden(w http.ResponseWriter, r *http.Request, why string) {
	req := requestFrom(r.Context())

	a.log.LogAttrs(r.Context(), slog.LevelWarn, "forbidden",
		slog.String("why", why),
		slog.String("path", r.URL.Path),
		slog.String("request_id", orDash(req.ID)),
	)
	// No tree, for the same reason the 500 has none: this is an error path, and an
	// error path that queries the index is an error path with a second way to fail.
	a.render(w, r, http.StatusForbidden, notice(noticeData{
		shell:   a.noticeShell(req, nil, "Refused"),
		Request: viewRequestFor(req),
		Title:   "Refused",
		Body:    "That request did not come from a page of this wiki, so it was not carried out.",
		Status:  http.StatusForbidden,
	}))
}

// failWith is a 500 that carries the reason in the body, and it is for exactly one
// case: **the DM typed something that is not a page.**
//
// The usual rule is that an error string must not reach a response — an error from
// SQLite carries a table name and a value, and a player is the one reading it. This
// is the other case, and the difference is who typed it: the text in the body is the
// editor's own frontmatter, quoted back with the reason the vault or the index gave
// for it, and the person reading it is the person who can fix it. A DM whose
// `visibility: secret` is a typo and gets "500 Internal Server Error" learns
// nothing and files a bug; a DM who gets "visibility must be one of players,
// dm-only, dm-and-owner" fixes it.
//
// Nothing about the *server* is in the body, and the log line below still has the
// full error for whoever is debugging.
func (a *app) failWith(w http.ResponseWriter, r *http.Request, operation string, cause error) {
	req := requestFrom(r.Context())
	a.log.LogAttrs(r.Context(), slog.LevelError, "a request failed",
		slog.String("operation", operation),
		slog.String("error", cause.Error()),
		slog.String("path", r.URL.Path),
		slog.String("request_id", orDash(req.ID)),
		slog.String("principal", orDash(req.Principal.ID)),
	)

	a.render(w, r, http.StatusInternalServerError, notice(noticeData{
		shell:   a.noticeShell(req, nil, "That did not work"),
		Request: viewRequestFor(req),
		Title:   "That did not work",
		Body:    "Nothing was saved. The wiki said: " + cause.Error(),
		Status:  http.StatusInternalServerError,
	}))
}

// render writes a page, and the buffer is in renderInto: nothing reaches the
// response until the whole document exists.
func (a *app) render(w http.ResponseWriter, r *http.Request, status int, view templ.Component) {
	req := requestFrom(r.Context())

	body, err := renderInto(r.Context(), view)
	if err != nil {
		// A template that cannot render is a bug, and it is the one failure in the
		// request path that is not the caller's fault. It cannot be `fail`,
		// because `fail` renders a page.
		a.log.LogAttrs(r.Context(), slog.LevelError, "rendering a page",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path),
			slog.String("request_id", orDash(req.ID)),
		)
		w.Header().Set("Content-Type", contentTypeText)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("The wiki could not render this page.\n"))
		return
	}

	w.Header().Set("Content-Type", contentTypeHTML)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// redirect is a redirect, and it is always a 303.
//
// 303 rather than 302 so that a browser follows it with a GET whatever method
// arrived: a POST answered with 302 is re-sent as a POST by some clients, and a
// logout that is re-sent is a logout that runs twice.
//
// The status is not a parameter. Every redirect in this package is a 303, and a
// second argument is a 302 somebody eventually passes -- which is the kind of small
// convenience that produces a double submission on a logout.
func (a *app) redirect(w http.ResponseWriter, to string) {
	w.Header().Set("Location", to)
	w.WriteHeader(http.StatusSeeOther)
}

// rawURL is where a page's markdown is, and streamURL where its stream is. Both are
// here rather than in the template because a template that builds a URL is a
// template that can build the wrong one, and these two have to agree with the
// handler about the parameter's names -- `pageSource` and `pageStream` are what
// read them back.
func rawURL(campaign domain.Campaign, path string) string {
	return render.PageURL(campaign.Slug.String(), path) + "?raw=1"
}

func streamURL(campaign domain.Campaign, path string) string {
	return render.PageURL(campaign.Slug.String(), path) + "?stream=1"
}

// editURL is where a page's editor is, and it is a *query* for the reason `?raw=1`
// and `?stream=1` are: a path segment would be a first-segment name the vault could
// not also use, and a DM with a page called `edit` would find that their campaign
// root's editor edits it instead.
func editURL(campaign domain.Campaign, path string) string {
	return render.PageURL(campaign.Slug.String(), path) + "?edit=1"
}

// campaignNavFor is the three template-visible facts about a campaign, and it is a
// function because the zero campaign has to come out as the zero nav -- a 404 on
// a URL outside `/c/` has no campaign, and a template that drew a link to `/c/`
// for it would be sending a reader to a route that 404s.
func campaignNavFor(campaign domain.Campaign) campaignNav {
	return campaignNav{
		Name: campaign.Name,
		Slug: campaign.Slug,
		Root: campaignRootOf(campaign),
	}
}

// errNoCampaign is what a campaign handler sees when the middleware did not
// resolve a campaign, which is a programming error rather than a bad request. It
// is a plain sentinel because there is nothing to recover from and nothing to
// tell the caller.
var errNoCampaign = errors.New("http: the request has no campaign")
