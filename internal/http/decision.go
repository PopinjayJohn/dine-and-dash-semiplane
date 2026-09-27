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
// It calls `access.For` rather than reading `page.Visibility` itself, because the
// rights matrix has 36 cells and this handler is not in a position to
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
//
// # And then the plugins
//
// `access.Policies.Apply` narrows whatever the matrix said, and it runs here and
// nowhere else. That is the whole of "composition, not override" at the HTTP layer:
// the matrix's answer is the input, the plugins may only subtract from it, and there
// is no ordering in which a plugin runs first.
//
// The ordering has a consequence worth stating, because it is a property a reader
// would otherwise have to infer: **a policy is only ever asked about a page the store
// returned.** A `dm-only` page is `ErrNotFound` to a player before this function is
// called, so no policy ever sees a player's request for one. That is why a policy can
// safely be given a page's visibility and path and type, and why the alternative --
// asking policies first -- would be a way to hand a plugin the metadata ADR 0020
// spent a milestone removing from a link resolution.
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

	decided := access.For(access.PrincipalOf(as), access.MetaFor(page, owned))

	return a.cfg.Policies.Apply(ctx, access.PrincipalOf(as), access.MetaFor(page, owned), decided)
}

// ownedAmong answers, for a list of pages, which of them this principal owns.
//
// It is a thin wrapper over [app.ownersBound] because a page tree and a search
// dropdown want the same answer in two different shapes, and the memo behind it is
// the part with a cost in it.
func (a *app) ownedAmong(
	ctx context.Context, as domain.Principal, pages []domain.Page,
) []bool {
	owners := make([]string, 0, 8)
	seen := make(map[string]bool, len(pages))
	for _, page := range pages {
		if page.OwnerCharacterPageID == "" || seen[page.OwnerCharacterPageID] {
			continue
		}
		seen[page.OwnerCharacterPageID] = true
		owners = append(owners, page.OwnerCharacterPageID)
	}

	bound := a.ownersBound(ctx, as, owners)

	owned := make([]bool, len(pages))
	for i, page := range pages {
		owned[i] = bound[page.OwnerCharacterPageID]
	}
	return owned
}

// ownersBound answers "is this principal bound to each of these owner page ids", with
// one query per *distinct* owner rather than one per page.
//
// The memo is by owner and not by page, which is the whole of the optimisation: a
// campaign has one owner per character, so a page tree of three hundred pages under
// five characters is five queries rather than three hundred. The alternative is a
// round trip per row, which is the difference between a sidebar that appears and one
// that does not.
//
// A build with no plugins never reaches this — the callers check
// `Policies.IsEmpty()` first — so the cost is only paid by a build that registered a
// policy, which is the right way round for a feature most deployments will not use.
//
// An empty owner is never owned, which is the same answer `decisionFor` gives and for
// the same reason: there is no character to be bound to. An owner that does not
// exist in the table is also not owned, and does not log: a page whose owner
// character was deleted is a page with no owner, and a warning for each of them on
// every page-tree request is a log nobody reads.
func (a *app) ownersBound(
	ctx context.Context, as domain.Principal, owners []string,
) map[string]bool {
	bound := make(map[string]bool, len(owners))
	if as.ID == "" {
		return bound
	}

	for _, owner := range owners {
		if owner == "" {
			continue
		}
		found, err := a.cfg.Store.OwnerExists(ctx, as.ID, owner)
		if err != nil {
			// The same answer `decisionFor` gives on a failed lookup: not the owner.
			// A listing missing a few pages is better than a listing that is a 500,
			// and the direction a leak is not in is the same one here as it is there.
			a.log.LogAttrs(ctx, slog.LevelWarn, "checking ownership for a listing",
				slog.String("error", err.Error()),
				slog.String("principal", as.ID),
			)
		}
		bound[owner] = found
	}

	return bound
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

// errUnreadable is what a live stream's builder returns when a plugin's policy has
// stopped this principal reading the page it is streaming.
//
// It is *not* a second not-found: the store's is the only one, and a stream builder
// has no way to tell the two apart either, because the difference between "the DM
// archived this" and "a policy hid this from you" is one a reader must not be able
// to make. The handler treats both as "the stream is over", which is what the store
// has always done with a not-found here.
var errUnreadable = errors.New("http: this principal may not read the page")
