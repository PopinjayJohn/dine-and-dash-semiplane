package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/edit"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # The two pages a DM needs that are not pages
//
// **The users page**, which is the missing half of handing somebody a link: §10
// says the DM clicks "new player link" and the plaintext is shown *once*, and
// nothing in M0 through M8 had a button to click. And **the page tools**, which
// are on the editor page because that is where a DM is when they want them.
//
// Both are DM-only, and both check the role rather than a decision: a principal's
// label and a share link are not a page's audience, and pretending otherwise
// would put a fifth decision field in the resolver for a question that has two
// answers.

// usersURL is where the users page is, and it is a query on the campaign root
// because `users` is exactly the kind of page a DM writes: a path segment here
// would be a name the vault could not also use.
func usersURL(campaign campaignNav) string {
	return campaign.Root + "?" + usersQuery + "=1"
}

// The actions on the users page, in a query parameter rather than a path.
//
// `?users=1&action=new` and not `/c/<slug>/users/new`, because a path segment is a
// first-segment name the vault could not also use — and this time the name in
// question is `users`, which is exactly the kind of page a DM writes.
const (
	usersQuery = "users"
)

// usersPage is `GET ?users=1`: the campaign's principals, for a DM.
//
// A player gets a 403 rather than a 404, and the difference matters: the URL is
// one a DM hands out, and a player who has found it knows the campaign has
// players. That is not a secret worth a not-found, and a 403 says "that is not
// yours" in a way a reader can act on.
func (a *app) usersPage(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if !req.isDM() {
		a.forbidden(w, r, "only a DM can see who has access to this campaign")
		return
	}

	principals, err := a.cfg.Store.ListPrincipals(r.Context(), req.Campaign.ID)
	if err != nil {
		a.fail(w, r, "listing who has access", err)
		return
	}

	a.render(w, r, statusOK, users(usersView{
		shell: a.noticeShell(req, nil, "Who has access"),
		// A freshly minted link, and it is the only time it will ever be here.
		Fresh: r.URL.Query().Get("issued"),
		List:  principalsFor(principals),
		Form: usersForm{
			Action: "/c/" + req.Campaign.Slug.String() + "/?" + usersQuery + "=1",
			CSRF:   a.csrfToken(req.Principal.ID),
		},
	}))
}

// usersPost mints or revokes, and it is one handler because both are one
// operation on a principal with the difference being which row changes.
func (a *app) usersPost(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if !req.isDM() {
		a.forbidden(w, r, "only a DM can hand out or take back a link")
		return
	}
	if err := a.checkCSRF(r); err != nil {
		a.forbidden(w, r, "a change to the player list without a valid CSRF token")
		return
	}

	switch r.FormValue("action") {
	case "new":
		a.issueLink(w, r)
		return
	case "revoke":
		a.revokeLink(w, r)
		return
	default:
		a.badRequest(w, r, "That is not something this page can do")
		return
	}
}

// issueLink mints a share link and shows it once.
//
// **The response is the page, not a redirect, and that is the whole of "shown
// once".** The token is stored only as a hash, so there is no second chance: a
// 303 would put the link in a URL that a history entry, a `Referer` and a
// screenshot all remember, and the token would be readable by anybody who ever
// saw the address bar. The page is `no-store` (ADR 0003), it is not in any log, and
// this is the only time the plaintext exists outside the DM's clipboard.
//
// The URL is built from `auth.Config.BaseURL` rather than from the request's
// `Host`, because the whole question is "the host I would have written a link
// into" and a `Host` the caller chose answers nothing.
func (a *app) issueLink(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())

	label := strings.TrimSpace(r.FormValue("label"))
	if label == "" {
		a.badRequest(w, r, "A link needs a name for the person it is for")
		return
	}

	role := domain.Role(strings.TrimSpace(r.FormValue("role")))
	if !role.Valid() {
		role = domain.RolePlayer
	}

	issued, err := a.cfg.Minter.Issue(r.Context(), req.Campaign, role, label)
	if err != nil {
		a.fail(w, r, "minting a link for "+label, err)
		return
	}

	// The audit row the redemption will also write, written here as well: the
	// issuing of a credential is the fact a DM needs when a player says "my link
	// stopped working", and the redemption's row is three minutes and a paste
	// later.
	if _, err := a.cfg.Store.AppendAudit(r.Context(), domain.AuditEntry{
		CampaignID:  req.Campaign.ID,
		PrincipalID: issued.Principal.ID,
		Action:      auditIssued,
		At:          a.cfg.Now().UTC(),
		Detail:      label,
	}); err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelError, "recording a link that was issued",
			slog.String("error", err.Error()),
			slog.String("label", label))
	}

	a.log.LogAttrs(r.Context(), slog.LevelInfo, "a link was issued",
		slog.String("principal", issued.Principal.ID),
		slog.String("role", role.String()),
		slog.String("label", label),
		slog.String("request_id", orDash(req.ID)),
	)

	// The link, in the page, once. The query parameter carries it so that a
	// refresh does not re-issue and a paste of the *page* is not the link.
	a.redirect(w, "/c/"+req.Campaign.Slug.String()+"/?"+usersQuery+"=1&issued="+a.cfg.BaseURL+"/c/"+req.Campaign.Slug.String()+"/?k="+issued.Token.Hex())
}

// auditIssued is the audit action for a link being minted. It is a constant here
// rather than in `internal/auth` because the auth package decides *authentication*
// events and the issue is a DM's action in a campaign — and a test for it is a
// test of the users page, not of the exchange.
const auditIssued = "share_link_issued"

// revokeLink ends a principal's access, which is instant and total.
//
// §10: "sessions are rows, so deleting the principal ends every existing
// browser's access on the next request." `RevokePrincipal` does exactly that, and
// the page says so rather than implying the player has been logged out — because
// they have not been *logged out*, they have been *cut off*, and a player who
// refreshes and finds their session still there would conclude the button did
// nothing.
func (a *app) revokeLink(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())

	id := strings.TrimSpace(r.FormValue("id"))
	if id == "" {
		a.badRequest(w, r, "There is no link to take back")
		return
	}

	// The principal has to be in *this* campaign. A revocation is a write on a
	// row, and a row in another campaign is not this DM's to end.
	target, found, err := a.cfg.Store.PrincipalByID(r.Context(), id)
	if err != nil {
		a.fail(w, r, "looking up the link to revoke", err)
		return
	}
	if !found || target.CampaignID != req.Campaign.ID {
		a.notFound(w, r)
		return
	}
	if target.ID == req.Principal.ID {
		// The DM is a principal too, and a DM who revokes their own link locks
		// every player out of the campaign with no way back in except the data
		// directory. It is one click and it is not undoable.
		a.badRequest(w, r, "That is your own link. Revoking it would end your own access to this campaign")
		return
	}

	if err := a.cfg.Store.RevokePrincipal(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.notFound(w, r)
			return
		}
		a.fail(w, r, "revoking "+target.Label, err)
		return
	}

	if _, err := a.cfg.Store.AppendAudit(r.Context(), domain.AuditEntry{
		CampaignID:  req.Campaign.ID,
		PrincipalID: target.ID,
		Action:      auditRevoked,
		At:          a.cfg.Now().UTC(),
		Detail:      target.Label,
	}); err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelError, "recording a revoked link",
			slog.String("error", err.Error()),
			slog.String("label", target.Label))
	}

	a.log.LogAttrs(r.Context(), slog.LevelInfo, "a link was revoked",
		slog.String("principal", id),
		slog.String("label", target.Label),
		slog.String("request_id", orDash(req.ID)),
	)

	a.redirect(w, "/c/"+req.Campaign.Slug.String()+"/?"+usersQuery+"=1")
}

// auditRevoked is the audit action for a link being taken back.
const auditRevoked = "share_link_revoked"

// The freshly minted link is carried in a query parameter rather than only in the
// body, and that is a security decision worth stating: a fragment is not sent to
// the server, so a link in a fragment would not survive a refresh, and a query
// parameter does. The cost is that the URL is in the address bar and in the
// history, which is why the response is `no-store`, why the logging middleware does
// not log the query string, and why this is a single-use display rather than
// something a DM keeps.
//
// One `?issued=` value and not a list, because a second token in the same response
// is a second token in a URL.

// principalsFor is the users page's view of a principal list: what a DM needs to
// decide and nothing else.
//
// **No token, no hash, no hint** — the hint is four characters of a token the store
// does keep, and four characters of a 32-byte credential is a fingerprint worth
// having in a list of six people at a table. What a DM needs to identify a row is
// the label they typed.
func principalsFor(principals []domain.Principal) []principalRow {
	rows := make([]principalRow, 0, len(principals))
	for _, principal := range principals {
		rows = append(rows, principalRow{
			ID:       principal.ID,
			Label:    principal.Label,
			Role:     principal.Role.String(),
			Revoked:  !principal.RevokedAt.IsZero(),
			LastUsed: lastUsed(principal),
		})
	}
	return rows
}

func lastUsed(principal domain.Principal) string {
	if principal.LastUsedAt.IsZero() {
		return "never"
	}
	return principal.LastUsedAt.UTC().Format("2006-01-02")
}

// # The page tools
//
// Four operations on a page, all of them a POST, all of them DM-only except the
// archive of a page the caller owns. They hang off the editor's URL with an
// `op`, for the reason the users page's actions do: a path segment is a name the
// vault could also use.

// pageTool is the editor's operation dispatcher.
func (a *app) pageTool(w http.ResponseWriter, r *http.Request, path string) {
	req := requestFrom(r.Context())
	writer := a.editorFor(req.Campaign)
	if writer == nil {
		a.forbidden(w, r, "this server has no editor configured")
		return
	}

	if err := a.checkCSRF(r); err != nil {
		a.forbidden(w, r, "a change to this page without a valid CSRF token")
		return
	}

	switch r.FormValue("op") {
	case "rename":
		a.renamePage(w, r, writer, path)
	case "archive":
		a.archivePage(w, r, writer, path)
	case "purge":
		a.purgePage(w, r, writer, path)
	case "restore":
		a.restorePage(w, r, writer, path)
	default:
		a.badRequest(w, r, "That is not something an editor can do")
	}
}

// renamePage moves a page and follows its links.
//
// The new path is a field, and a blank one is refused rather than read as "the
// old one" — a rename with no destination is a save, and a save goes to `?edit=1`
// without an `op`.
func (a *app) renamePage(w http.ResponseWriter, r *http.Request, writer *edit.Editor, path string) {
	req := requestFrom(r.Context())

	to := strings.TrimSpace(r.FormValue("path"))
	if to == "" {
		a.badRequest(w, r, "A rename needs somewhere to rename it to")
		return
	}

	moved, followed, err := writer.Rename(r.Context(), path, to, req.Principal)
	if err != nil {
		a.pageToolFailed(w, r, "renaming "+path+" to "+to, err)
		return
	}

	a.log.LogAttrs(r.Context(), slog.LevelInfo, "a page was renamed",
		slog.String("from", path),
		slog.String("to", moved.Path),
		slog.Int("links_followed", len(followed)),
		slog.String("request_id", orDash(req.ID)),
	)

	a.redirect(w, pageURL(req.Campaign, moved.Path))
}

// archivePage removes the file and keeps the row, which is recoverable.
func (a *app) archivePage(w http.ResponseWriter, r *http.Request, writer *edit.Editor, path string) {
	req := requestFrom(r.Context())

	if err := writer.Archive(r.Context(), path, req.Principal); err != nil {
		a.pageToolFailed(w, r, "archiving "+path, err)
		return
	}

	a.log.LogAttrs(r.Context(), slog.LevelInfo, "a page was archived",
		slog.String("page", path),
		slog.String("request_id", orDash(req.ID)),
	)

	a.redirect(w, campaignURL(req.Campaign.Slug))
}

// purgePage forgets a page for good, and the editor says so twice before it does.
//
// The editor is where a DM is, and a DM who clicks "purge" has usually meant
// "archive" — the two differ by whether it can be undone and one of them cannot be
// undone at all. The form is a submit with a page that says what goes, and
// `TestAPurgeIsNotOfferedWithoutSayingWhatItLoses` is the test.
func (a *app) purgePage(w http.ResponseWriter, r *http.Request, writer *edit.Editor, path string) {
	req := requestFrom(r.Context())

	// A typed confirmation, because the browser's `confirm()` dialog is not one:
	// it is suppressed by a prefetch, it is not announced by a screen reader, and
	// a DM who has pressed Enter twice has a page they cannot get back.
	if strings.TrimSpace(r.FormValue("confirm")) != path {
		a.badRequest(w, r, "Purging "+path+" would throw its history away for good. Type its path to confirm")
		return
	}

	if err := writer.Purge(r.Context(), path, req.Principal); err != nil {
		a.pageToolFailed(w, r, "purging "+path, err)
		return
	}

	a.log.LogAttrs(r.Context(), slog.LevelInfo, "a page was purged",
		slog.String("page", path),
		slog.String("request_id", orDash(req.ID)),
	)

	a.redirect(w, campaignURL(req.Campaign.Slug))
}

// restorePage puts a revision back, and it is a save, so it cannot overwrite a
// page that has changed since the history panel was drawn.
func (a *app) restorePage(w http.ResponseWriter, r *http.Request, writer *edit.Editor, path string) {
	req := requestFrom(r.Context())

	rev := 0
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(r.FormValue("rev")), "%d", &rev); scanErr != nil || rev < 1 {
		a.badRequest(w, r, "Which revision? It is a number")
		return
	}

	page, err := writer.Restore(r.Context(), path, rev, etagFrom(r), req.Principal)
	if err != nil {
		a.pageToolFailed(w, r, "restoring "+path+" to revision "+r.FormValue("rev"), err)
		return
	}

	a.redirect(w, pageURL(req.Campaign, page.Path))
}

// pageToolFailed is the one place the four tools turn an error into a response, so
// that the four answers — a refusal, a conflict, a bad request and a genuine
// failure — are chosen in one place rather than four times.
func (a *app) pageToolFailed(w http.ResponseWriter, r *http.Request, operation string, err error) {
	switch {
	case errors.Is(err, store.ErrNotAllowed):
		a.forbidden(w, r, "you may not do that to this page: "+err.Error())
	case errors.Is(err, edit.ErrConflict):
		// The editor is the right place to see a conflict, and the three-way diff
		// is already written. This is the "somebody saved it while you were on the
		// history panel" case, and the form is re-rendered with the diff.
		a.conflict(w, r, edit.Save{Path: strings.TrimPrefix(r.FormValue("path"), "/"), Markdown: editorMarkup(r), Expect: etagFrom(r), As: requestFrom(r.Context()).Principal}, err)
	case errors.Is(err, edit.ErrNoSuchPage):
		a.notFound(w, r)
	case errors.Is(err, edit.ErrNoSuchRevision):
		a.notFound(w, r)
	default:
		// A typed confirmation that did not match, a path the vault refuses, a
		// rename to a page that is already there. All of them are the DM's input
		// and all of them deserve the reason.
		a.failWith(w, r, operation, err)
	}
}
