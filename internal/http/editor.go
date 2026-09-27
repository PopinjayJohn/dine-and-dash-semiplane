package http

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/edit"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # The editor
//
// A textarea, a preview, a save, and the three ways a save can be refused. The
// refusal is the interesting half, and each of the three has its own answer
// because they are three different facts:
//
//   - **not writable** — 403. A player who followed an Edit link somebody sent
//     them, or who has a page open from before they lost the binding, and the
//     gate says no.
//   - **a conflict** — 409 with the three texts. The page moved since they loaded
//     it, and the DM has to decide.
//   - **the page is gone** — 404. Somebody archived it, which is the same answer
//     as not existing and for the same reason.
//
// # `?edit=1`, and why not a path
//
// The editor is a mode of a page rather than a page, and it is a query parameter
// because a path segment would be a first-segment name the vault could not also
// use — and the vault is the source of truth (ADR 0001). A DM with a page at
// `locations/edit` gets it, because nothing is reserved.
//
// A new page is `?new=1` on the campaign root for the same reason: there is no
// path to make special until there is a page.

// rootPost is a POST to the campaign root, and there are two things it can mean.
//
// A `?new=1` is a create, and a bare one is a logout. The query is the only thing
// that tells them apart, which is why the create is a query parameter and not a
// path: `/c/<slug>/new` would be a page path, and a DM with a page called `new`
// would find that their campaign root's create button edits it instead.
func (a *app) rootPost(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Query().Has("new"):
		a.savePage(w, r, "", true)
		return
	case r.URL.Query().Has(usersQuery):
		a.usersPost(w, r)
		return
	case r.URL.Query().Has("preview"):
		a.preview(w, r, "", true)
		return
	}
	a.logout(w, r)
}

// pagePost is a POST to a page URL, and there is one thing it can mean: a save.
//
// A POST to a page with no `?edit=1` and no `?preview=1` is a 405, and that is a
// 405 rather than a redirect to the editor because a form that posts to the wrong
// address has a bug in it and silently working around the bug is how it survives to
// be a security problem later.
func (a *app) pagePost(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if req.Campaign.ID == "" {
		a.fail(w, r, "posting to a page of no campaign", errNoCampaign)
		return
	}

	switch {
	// **The tool is checked before the save**, because a tool is a different verb
	// on the same URL and the more specific one has to win. The other order is a
	// silent one: the tool's form arrives with no `markdown` field and no ETag, so
	// the save reports a conflict with itself, and a DM's "purge" button appears to
	// be a save that cannot be saved. That is what happened, and
	// `TestAPurgeIsNotOfferedWithoutSayingWhatItLoses` is where it was found.
	// The op is a *form field* and not a query parameter, and that is deliberate:
	// a URL a DM bookmarks should keep the same verb, and a field cannot be lost by
	// an edit to the query. The dispatcher therefore reads the body before it looks
	// at anything, and a GET -- which has no body -- never sees one.
	case r.FormValue("op") != "":
		path, ok := pagePathFromURL(chi.URLParam(r, "*"))
		if !ok {
			a.notFound(w, r)
			return
		}
		a.pageTool(w, r, path)
		return

	case r.URL.Query().Has("edit"):
		path, ok := pagePathFromURL(chi.URLParam(r, "*"))
		if !ok {
			a.notFound(w, r)
			return
		}
		a.savePage(w, r, path, false)
		return

	case r.URL.Query().Has("preview"):
		path, _ := pagePathFromURL(chi.URLParam(r, "*"))
		a.preview(w, r, path, false)
		return

	case r.URL.Query().Has("new"):
		// A new page hangs off the campaign root, so a POST that means one arrives
		// there -- and this is the one place a POST to a page URL means it anyway,
		// because a DM who typed a path into a stale tab is not doing anything wrong.
		a.savePage(w, r, "", true)
		return

	default:
		w.Header().Set("Allow", "GET, HEAD")
		a.render(w, r, http.StatusMethodNotAllowed, notice(noticeData{
			shell:   a.noticeShell(req, nil, "Not a thing you can do here"),
			Request: viewRequestFor(req),
			Title:   "Not a thing you can do here",
			Body:    "A page is read with GET. To change one, open its editor and save from there.",
			Status:  http.StatusMethodNotAllowed,
		}))
	}
}

// editorETagField is the hidden form field the ETag travels in, for a save from a
// browser form. `If-Match` is the same value and takes precedence, so a save from
// a script that speaks HTTP properly needs no field at all.
const editorETagField = "etag"

// editorMarkupField is the textarea's name, and the only name a save reads a body
// from. A second name — `body` — would be a second way to be wrong.
const editorMarkupField = "markdown"

// editPage is `GET ?edit=1`: the editor for a page.
//
// It is a separate function from `savePage` rather than a branch in `page`,
// because the two have nothing in common beyond the page they are about: one
// renders a form and one writes a file, and a handler that does both is a handler
// whose two halves are tested by the tests of whichever half somebody writes
// first.
func (a *app) editPage(w http.ResponseWriter, r *http.Request, path string) {
	req := requestFrom(r.Context())
	writer := a.editorFor(req.Campaign)
	if writer == nil {
		a.forbidden(w, r, "this server has no editor configured")
		return
	}

	page, err := a.cfg.Store.GetPage(r.Context(), req.Campaign.ID, path, req.Principal)
	if err != nil {
		if isNotFound(err) {
			a.notFound(w, r)
			return
		}
		a.fail(w, r, "reading "+path+" to edit it", err)
		return
	}

	// Read and write are two questions. A player may read a page they may not
	// write — every player reads every `players` page in the campaign — and the
	// editor is only for the second.
	if writeErr := writer.MayWrite(r.Context(), path, req.Principal); writeErr != nil {
		a.forbidden(w, r, "this page is not yours to edit: "+writeErr.Error())
		return
	}

	text, hash, readErr := writer.Read(r.Context(), path)
	if readErr != nil {
		a.fail(w, r, "reading "+path+" to edit it", readErr)
		return
	}

	// The ETag is the file's content hash, which is the same string the page
	// response carries, so a save from a form and a save from a script compare the
	// same thing.
	w.Header().Set("ETag", etagOf(hash))

	a.renderEditor(w, r, http.StatusOK, editorView{
		shell:   a.editorShell(req, page),
		Page:    renderedPage{Path: page.Path, Title: page.Title, Kind: page.Type.String()},
		History: a.revisionsFor(r, writer, page.Path),
		Form: editorForm{
			Action:   pageURL(req.Campaign, page.Path) + "?edit=1",
			Path:     page.Path,
			Text:     text,
			ETag:     etagOf(hash),
			CSRF:     a.csrfToken(req.Principal.ID),
			Creating: false,
			Preview:  pageURL(req.Campaign, page.Path) + "?preview=1",
		},
	})
}

// newPage is `GET ?new=1` on the campaign root: an empty editor for a page that
// does not exist yet.
//
// The path is a field and not part of the URL, because the path is the thing being
// chosen. It is validated here rather than in the browser, because the browser's
// validation is a convenience and this is the one that is the rule.
func (a *app) newPage(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	writer := a.editorFor(req.Campaign)
	if writer == nil {
		a.forbidden(w, r, "this server has no editor configured")
		return
	}

	// A player may create a page only inside their own character's folder, and the
	// rule that decides that is the store's: a save of a path they may not write is
	// refused at save time, and the check here means the form is not offered for a
	// path that could only ever fail.
	suggested := "characters/" + req.Principal.Label
	if req.isDM() {
		suggested = ""
	}

	a.renderEditor(w, r, http.StatusOK, editorView{
		shell: a.editorShell(req, domain.Page{Title: "A new page"}),
		Page:  renderedPage{Path: "", Title: "A new page", Kind: domain.PageTypeNote.String()},
		Form: editorForm{
			Action:   "/c/" + req.Campaign.Slug.String() + "/?new=1",
			Path:     suggested,
			Text:     "",
			CSRF:     a.csrfToken(req.Principal.ID),
			Creating: true,
			Preview:  "/c/" + req.Campaign.Slug.String() + "/?preview=1",
		},
	})
}

// revisionsFor is a page's history for the editor's list, and an empty list for a
// page that has never been edited here — which is a *miss* rather than a failure.
//
// The history is read as the DM rather than as the caller, because a player
// editing their own page may see the list: their own revisions are their own
// prose, and a page's history is not an audience question the way its secrets
// are. The *restore* is gated; looking is not.
func (a *app) revisionsFor(r *http.Request, writer *edit.Editor, path string) []revisionRow {
	revisions, err := writer.History(r.Context(), path)
	if err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelWarn, "listing a page's history",
			slog.String("error", err.Error()),
			slog.String("path", path))
		return nil
	}

	rows := make([]revisionRow, 0, len(revisions))
	for _, revision := range revisions {
		rows = append(rows, revisionRow{
			Rev:       revision.Rev,
			CreatedAt: revision.CreatedAt.UTC().Format(time.RFC3339),
			Message:   revision.Message,
		})
	}
	return rows
}

// renderEditor is the editor page, and it is one function so that the create form
// and the edit form cannot differ in a field. The difference between them is four
// booleans in the data, which is where a difference belongs.
func (a *app) renderEditor(w http.ResponseWriter, r *http.Request, status int, view editorView) {
	a.render(w, r, status, editorPage(view))
}

// editorShell is the chrome around an editor: the campaign, the tree, and the
// CSRF token.
//
// **The tree is not there**, and that is deliberate. An editor is a full-width
// thing — a textarea and a preview side by side — and a sidebar that pushes both
// into a third of the screen is a sidebar a DM reads in order to ignore. The
// navigation is one link away, in the editor's own header.
func (a *app) editorShell(req *request, page domain.Page) shell {
	return shell{
		Title:      page.Title + " — edit",
		Nonce:      req.Nonce,
		Campaign:   campaignNavFor(req.Campaign),
		Footer:     footerView{Version: a.cfg.Version},
		Identified: req.identified(),
		IsDM:       req.isDM(),
		CSRF:       a.csrfToken(req.Principal.ID),
		// The path being edited, so a template that wants to say "you are editing
		// this" has it, and nothing else about the page: a template given a whole
		// `domain.Page` is a template that can print its visibility.
		Path: page.Path,
	}
}

// savePage is `POST ?edit=1` and `POST ?new=1`: the save.
//
// The order of the three refusals is the order they are checked in, and it is the
// order a DM would want them: CSRF first, because a forged POST is not a
// conversation; then the ETag, because a conflict is the one that needs a human;
// then the save, whose refusals are the gate's.
func (a *app) savePage(w http.ResponseWriter, r *http.Request, path string, creating bool) {
	req := requestFrom(r.Context())

	if err := a.checkCSRF(r); err != nil {
		a.forbidden(w, r, "a save without a valid CSRF token")
		return
	}

	writer := a.editorFor(req.Campaign)
	if writer == nil {
		a.forbidden(w, r, "this server has no editor configured")
		return
	}

	in := edit.Save{
		Path:     path,
		Markdown: editorMarkup(r),
		Expect:   etagFrom(r),
		Creating: creating,
		Message:  r.FormValue("message"),
		As:       req.Principal,
	}
	if creating {
		in.Path = r.FormValue("path")
		if in.Path == "" {
			a.badRequest(w, r, "a new page needs a path")
			return
		}
	}

	page, _, err := writer.Save(r.Context(), in)
	switch {
	case err == nil:
		// The push, and then the redirect. In that order, so a reader who is
		// watching the page sees the edit arrive before the editor navigates away,
		// and so a failed push does not stop a save that has happened.
		a.publishChange(req.Campaign.ID, page.Path)

		// 303 rather than a 200 with a body: the browser follows it with a GET, so
		// a refresh does not re-post the form.
		w.Header().Set("Location", pageURL(req.Campaign, page.Path))
		w.WriteHeader(http.StatusSeeOther)
		return

	case errors.Is(err, edit.ErrConflict):
		a.conflict(w, r, in, err)
		return

	case errors.Is(err, store.ErrNotAllowed):
		// The gate. A player who may not write this page, and the answer says so
		// rather than "not found" -- they were looking at a page, so they know it
		// exists, and a 404 would be a lie about a page they have just read.
		a.forbidden(w, r, "this page is not yours to write: "+err.Error())
		return

	case errors.Is(err, edit.ErrNoSuchPage):
		a.notFound(w, r)
		return

	default:
		// A path the vault refuses, a frontmatter it cannot read: the DM is going
		// to have to look at this, and a 500 says "the wiki broke" when the truth
		// is "what you typed is not a page". The text goes in the body because the
		// person reading it typed it.
		a.failWith(w, r, "saving "+in.Path, err)
		return
	}
}

// conflict is the 409, and it is the one response in this file that carries three
// versions of a page.
//
// The three texts are fetched rather than carried on the error, so a save that is
// not in conflict does not pay for a diff nobody looks at, and they are rendered
// side by side rather than merged. A merge is a decision about somebody's prose
// that no server should make on its own, and §9's rule — a second "public render"
// path would mean a second set of golden tests and a second chance to leak — is the
// same rule: one path, one place where the answer is made.
func (a *app) conflict(w http.ResponseWriter, r *http.Request, in edit.Save, cause error) {
	req := requestFrom(r.Context())
	writer := a.editorFor(req.Campaign)

	base, current, err := writer.Versions(r.Context(), in.Path, in.Expect, in.Markdown)
	if err != nil {
		// The conflict is real and the diff is not available, which is worse than
		// not knowing but much better than losing the edit: the editor's form
		// carries the text, so a refresh puts it back in the textarea.
		a.failWith(w, r, "reading the three versions of "+in.Path, cause)
		return
	}

	a.log.LogAttrs(r.Context(), slog.LevelWarn, "a save conflicted",
		slog.String("page", in.Path),
		slog.String("expected", in.Expect),
		slog.String("reason", cause.Error()),
		slog.String("request_id", orDash(req.ID)),
	)

	a.render(w, r, http.StatusConflict, editorPage(editorView{
		shell: a.editorShell(req, domain.Page{Path: in.Path, Title: in.Path}),
		Page:  renderedPage{Path: in.Path, Title: in.Path, Kind: domain.PageTypeNote.String()},
		Form: editorForm{
			Action:   in.Path,
			Path:     in.Path,
			Text:     in.Markdown,
			ETag:     etagOf(current),
			CSRF:     a.csrfToken(req.Principal.ID),
			Preview:  pageURL(req.Campaign, in.Path) + "?preview=1",
			Conflict: &conflictView{Base: base, Current: current, Incoming: in.Markdown},
		},
	}))
}

// preview is `POST ?preview=1`: the page as it would render, under this request's
// decision, without saving anything.
//
// It is the same `renderPage` the page route uses and the same decision, so a
// preview is the page — including a player's page rendering its own secrets
// stripped, which is the answer a player needs to see.
//
// **It saves nothing and it writes no file.** A preview is a question and a save
// is an act, and a route that answered a question by doing the act would be a
// route a browser's prefetch could write to.
func (a *app) preview(w http.ResponseWriter, r *http.Request, path string, creating bool) {
	req := requestFrom(r.Context())
	if err := a.checkCSRF(r); err != nil {
		a.forbidden(w, r, "a preview without a valid CSRF token")
		return
	}

	// The path a preview is rendered *as* is the path it would be saved to, and
	// for a new page that is the one in the form. The links inside a preview
	// therefore resolve against the path the page will have, which is the whole
	// reason a preview is worth having.
	if creating {
		path = r.FormValue("path")
	}
	if path == "" {
		a.badRequest(w, r, "a preview needs a path")
		return
	}

	// The editor, not this handler: a preview is the same derivation and the same
	// decision as the save, and a preview that built its own would be a second
	// answer to "what does this page look like" — which is §9's rule, applied to a
	// path nobody thought about when it was written.
	writer := a.editorFor(req.Campaign)
	if writer == nil {
		a.forbidden(w, r, "this server has no editor configured")
		return
	}

	result, err := writer.Preview(r.Context(), path, []byte(editorMarkup(r)), req.Principal)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotAllowed):
		// The gate, and it is the *write* gate: there is no editor for a page the
		// reader may not write, so a preview of one is a question nobody was asked.
		a.forbidden(w, r, "this page is not yours to write: "+err.Error())
		return
	default:
		// A preview of something that is not a page is a *preview of that*: the DM
		// typed it, they are still typing, and a 500 while typing is a wiki that
		// appears to be broken. The reason goes in the pane because the person
		// reading it is the person who can fix it.
		a.previewError(w, r, err)
		return
	}

	a.renderPreview(w, r, article(renderedPage{
		Path:  path,
		Title: pageTitle(path),
		HTML:  pageFragment(result.HTML),
		TOC:   result.TOC,
	}))
}

// renderPreview writes a fragment into the preview pane: the article, the problem,
// or nothing. It is one function so that a preview is always one of those three
// and a handler cannot invent a fourth.
func (a *app) renderPreview(w http.ResponseWriter, r *http.Request, view templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(statusOK)
	if err := view.Render(r.Context(), w); err != nil {
		// The status is sent, so this is a truncated frame. The editor's script
		// throws a short read away rather than half-replacing the pane, so the
		// visible effect is a pane that did not update.
		a.log.LogAttrs(r.Context(), slog.LevelDebug, "a preview fragment failed",
			slog.String("error", err.Error()))
	}
}

// editorMarkup is the textarea's contents, and it is read from the form with a
// size bound.
//
// The bound is `http.MaxBytesReader`'s, not a hand-written one: a textarea is
// free text and a DM pastes a whole campaign into one occasionally, and the thing
// that must not happen is a paste big enough to exhaust memory. 2 MiB is far
// above any page and far below anything that hurts.
func editorMarkup(r *http.Request) string {
	if err := r.ParseForm(); err != nil {
		return ""
	}
	return r.PostForm.Get(editorMarkupField)
}

// etagFrom is the content hash the caller was looking at, from `If-Match` or the
// form field.
//
// `If-Match` first, because a client that speaks HTTP properly sends it and a
// form field is the fallback for a browser form. The header may be quoted or not,
// because a hand-written client quotes it and a browser form cannot send it at
// all, and stripping the quotes is the difference between "the tag did not match"
// and "the tag was never read".
func etagFrom(r *http.Request) string {
	if match := strings.TrimSpace(r.Header.Get("If-Match")); match != "" {
		return unquoteETag(match)
	}
	return unquoteETag(strings.TrimSpace(r.FormValue(editorETagField)))
}

// etagOf is a content hash as an ETag.
func etagOf(hash string) string {
	if hash == "" {
		return ""
	}
	return `"` + hash + `"`
}

func unquoteETag(value string) string {
	value = strings.TrimPrefix(value, "W/")
	if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
		return value[1 : len(value)-1]
	}
	return value
}

// pageURL is a page's address, and `pagePathFromURL`'s other half.
func pageURL(campaign domain.Campaign, path string) string {
	return render.PageURL(campaign.Slug.String(), path)
}

// pageTitle is what an editor's heading says for a page with no title in it: the
// last segment of the path, which is what the tree shows too.
func pageTitle(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

// publishChange says a page's rows moved, so every browser reading it is sent a
// new copy.
//
// It is the same call `PageChanged` makes and it goes through the same topic, so
// an edit and a file written in Obsidian are indistinguishable to a reader —
// which is the property that makes the watcher worth having.
func (a *app) publishChange(campaignID, path string) {
	PageChanged(a.cfg.Hub, campaignID, path)
}

// badRequest is the 400, and it is its own handler because there are exactly three
// things in this file that are a bad request and all three are a DM's typo: a new
// page with no path, and a preview with no path.
func (a *app) badRequest(w http.ResponseWriter, r *http.Request, why string) {
	a.log.LogAttrs(r.Context(), slog.LevelWarn, "a malformed request",
		slog.String("why", why),
		slog.String("path", r.URL.Path),
		slog.String("request_id", orDash(requestFrom(r.Context()).ID)),
	)
	a.render(w, r, http.StatusBadRequest, notice(noticeData{
		shell:   a.noticeShell(requestFrom(r.Context()), nil, "That is not a page"),
		Request: viewRequestFor(requestFrom(r.Context())),
		Title:   "That is not a page",
		Body:    why + ". The page you were editing has not been changed.",
		Status:  http.StatusBadRequest,
	}))
}

// previewError is what a preview says when the text is not a page yet, which is
// most of the time a DM is typing one.
func (a *app) previewError(w http.ResponseWriter, r *http.Request, err error) {
	a.renderPreview(w, r, previewProblemView(previewProblem{why: err.Error()}))
}
