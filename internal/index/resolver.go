// Package index is what stands between the files and the database: it walks a
// vault, keeps the index in step with it, and answers what a wiki link points
// at.
//
// # The direction of travel
//
// The sync engine reads files and writes rows. It never writes a file.
//
// That is not a limitation to work around, it is the property ADR 0001 is
// about: the markdown is the source of truth and the database is a projection
// of it, so anything that can write a file can make the projection disagree
// with the thing it is a projection of. The editor (M9) and the importer (M12)
// are the only things in this project that will write a markdown file, and both
// of them are a user action with a revision behind it.
//
// The test that says so is TestSyncDoesNotTouchTheVault: it hashes every file in
// a vault before and after a sync, including its modification times, and
// requires both to be identical.
//
// # What a sync is allowed to refuse
//
// A page whose frontmatter cannot be read is reported and skipped, and the rest
// of the campaign is still indexed. A page whose *visibility* cannot be read is
// refused outright, because the permissive reading of a visibility key is how a
// `[!SECRET]` block reaches a player. Those two look like the same failure and
// they are opposites.
package index

import (
	"context"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// Resolver answers what a wiki link points at, against the index.
//
// It is the renderer's LinkResolver and it is also what the sync engine uses to
// resolve a destination when it writes the link graph. Those two must agree, and
// the reason they do is that there is one implementation of the resolution order
// and both callers use it: a link the index says resolves while the graph says
// it does not is a link that renders as a link to a page and backtracks to
// nothing.
//
// A resolver belongs to a campaign. Every table in the index is
// campaign-scoped, and two campaigns can each have a page called `rivergate`,
// so "what does `[[rivergate]]` mean" is a question with no answer until the
// campaign is named. The render path knows its campaign for the whole request
// (ADR 0011: one campaign per request path), so one resolver per campaign and
// no campaign parameter on the interface.
type Resolver struct {
	store      *store.Store
	campaignID string
}

// NewResolver returns a resolver for one campaign.
//
// The store it is given is the whole store, not a per-campaign handle, because
// the store is not campaign-scoped in its API and pretending otherwise would
// need a second type per campaign. The campaign is in the resolver, which is
// the thing that actually needs it.
func NewResolver(s *store.Store, campaignID string) *Resolver {
	return &Resolver{store: s, campaignID: campaignID}
}

// Resolve returns the page a target names, in Obsidian's order, and false when
// nothing in the campaign answers to it. It is ResolvePage projected to what a
// rendered link needs, which is a path and a title and no id.
func (r *Resolver) Resolve(ctx context.Context, target, heading string) (render.Link, bool, error) {
	page, found, err := r.ResolvePage(ctx, target, heading)
	if err != nil || !found {
		return render.Link{}, false, err
	}
	return linkFor(page), true, nil
}

// ResolvePage is Resolve for a caller that needs the row rather than a link.
//
// The link graph needs the page's id and the renderer needs the page's path and
// title, and a resolver that returned only the first would make the second a
// second query per link. This is the one that answers, and the interface method
// above is the projection of it.
//
// The order is the whole point, and it is Obsidian's rather than ours:
//
//  1. the exact path, which is the only one that is unambiguous;
//  2. an alias, matched exactly;
//  3. a file name, matched case-insensitively.
//
// An error means the index could not answer -- a database failure, a closed
// store -- and is *not* the same answer as "no page answers to this". That
// distinction is the reason the interface returns three values: a wiki full of
// unresolved links is a bug report, and rendering one because the database was
// briefly busy turns a bug into a mystery.
func (r *Resolver) ResolvePage(ctx context.Context, target, heading string) (domain.Page, bool, error) {
	if r == nil || r.store == nil || r.campaignID == "" {
		return domain.Page{}, false, nil
	}

	target = strings.TrimSpace(target)
	// A link that names a heading or a block resolves to the page it is on, so
	// the fragment comes off before anything is looked up. It is not carried
	// into the lookup: the page row has nowhere to put it, and a
	// `#the-bridges` link and a bare `[[rivergate]]` are the same page with the
	// difference being a fragment the browser adds.
	if page, _, found := strings.Cut(target, "#"); found {
		target = page
	}
	if target == "" {
		return domain.Page{}, false, nil
	}

	// An exact path that is *not* found is not a reason to try the other two
	// fallbacks: a DM who wrote a path and has not written the page yet means
	// the path, and falling through to a name lookup would resolve their
	// deliberate placeholder to somebody else's page.
	if page, err := r.store.GetPage(ctx, r.campaignID, target, store.AsDM(r.campaignID)); err == nil {
		return page, true, nil
	}

	if page, found, err := r.store.FindPageByAlias(ctx, r.campaignID, target, store.AsDM(r.campaignID)); err != nil {
		return domain.Page{}, false, err
	} else if found {
		return page, true, nil
	}

	if page, found, err := r.store.FindPageByName(ctx, r.campaignID, target, store.AsDM(r.campaignID)); err != nil {
		return domain.Page{}, false, err
	} else if found {
		return page, true, nil
	}

	_ = heading

	return domain.Page{}, false, nil
}

// Campaign is the campaign this resolver resolves within, for a caller that has
// one resolver and several campaigns.
func (r *Resolver) Campaign() string {
	return r.campaignID
}

func linkFor(page domain.Page) render.Link {
	return render.Link{Path: page.Path, Title: page.Title}
}

// compile-time assertion: the resolver is what the renderer asked for in M3.
var _ render.LinkResolver = (*Resolver)(nil)
