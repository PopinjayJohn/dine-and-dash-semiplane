package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// decisionFor is the access decision for one page and one request, and it is the
// only place in the HTTP layer that asks the question.
//
// It calls `access.For` rather than reading `page.Visibility` itself, because
// the rights matrix has 36 cells and this handler is not in a position to
// reproduce any of them. Two fields of the answer have to come from somewhere the
// resolver cannot get them, and both are named here because both were once
// guesses:
//
//   - **Owned** is whether *this* principal is bound to this page's owner
//     character. It is a question about `principal_characters`, so it is asked of
//     the store with the **owner's** page id. Passing the page's own id instead
//     gives the version M7 shipped, where a player may read the one file they are
//     bound to and not the twenty notes under it -- and a `characters/<slug>/**`
//     subtree is exactly those twenty notes.
//   - **Archived** is `page.IsDeleted`, which the store reads as "is deleted or
//     archived" and the resolver reads as one flag. A page the store would not
//     return is never rendered at all, so this is belt and braces: a page that
//     somehow arrived with the flag set is rendered as unpublished rather than
//     as live.
//
// A decision that cannot be computed is the safe one. `access.For` cannot fail,
// but the ownership query can, and an error there answers "not the owner", which
// is the direction a leak is not in.
func (a *app) decisionFor(ctx context.Context, page domain.Page, as domain.Principal) access.Decision {
	owned := false
	if as.ID != "" && page.OwnerCharacterPageID != "" {
		// The context is the request's, so a cancellation reaches the query and a
		// player who closed the tab does not leave a query running.
		found, err := a.cfg.Store.OwnerExists(ctx, as.ID, page.OwnerCharacterPageID)
		if err != nil {
			// Logged rather than returned, and then treated as "no": the page
			// renders without its secrets instead of not rendering at all, and
			// the DM has a log line saying why.
			a.log.LogAttrs(ctx, slog.LevelError, "checking ownership",
				slog.String("error", err.Error()),
				slog.String("page", page.Path),
				slog.String("principal", as.ID),
			)
		}
		owned = found
	}

	return access.For(access.PrincipalOf(as), access.PageMeta{
		Visibility: page.Visibility,
		Owned:      owned,
		Archived:   page.IsDeleted,
	})
}

// renderPage turns a page into a rendered result under a decision.
//
// The campaign is a parameter rather than something read out of the context
// because the renderer builds every link in the output from it, and a value that
// comes from a context is a value a handler can render with the wrong one. There
// is one campaign per request, so there is one way to be wrong, and this is not
// it.
func (a *app) renderPage(ctx context.Context, campaign domain.Campaign, page domain.Page, as domain.Principal) (render.Result, error) {
	return a.rendererFor(campaign.Slug).Render(ctx, render.Page{
		Campaign:    campaign.Slug.String(),
		Path:        page.Path,
		Body:        page.Body,
		ContentHash: page.ContentHash,
	}, a.decisionFor(ctx, page, as))
}

// fail answers a request whose work could not be completed, and logs what it was.
//
// The response is the plain 500 page and the log line is the operation that
// failed, so a DM reporting "it broke" gives a log line that says which query.
// The error is not in the response: an error string from SQLite can carry a table
// name and a value, and a player is the one reading it.
func (a *app) fail(w http.ResponseWriter, r *http.Request, operation string, err error) {
	req := requestFrom(r.Context())
	a.log.LogAttrs(r.Context(), slog.LevelError, "request failed",
		slog.String("operation", operation),
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path),
		slog.String("request_id", orDash(req.ID)),
		slog.String("principal", orDash(req.Principal.ID)),
	)
	a.serverError(w, r)
}

// isNotFound reports whether an error from the store means "there is no such
// row, or you may not have it".
//
// `store.ErrNotFound` is imported for the sentinel rather than declared here
// because it *is* the store's vocabulary: a consumer interface can say which
// methods it wants, but the way that store says "no" is part of its contract and
// inventing a second sentinel for the same condition is how a caller ends up
// treating a real failure as a 404.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
