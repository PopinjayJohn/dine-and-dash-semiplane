package http

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/a-h/templ"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
)

// # Search as you type
//
// One route, one fragment, and three things it has to be careful about:
//
//   - **It is a request per keystroke.** Datastar fires one on every input event,
//     so the handler has to be cheap and it has to be bounded: a DM typing into a
//     search box must not be able to make this server do a full-campaign FTS
//     fusion per character.
//   - **The candidates are the ACL's answer, not a filter applied afterwards.** The
//     query goes through `search.Run`, which goes through the two indexed scopes,
//     so a `dm-only` page is not in the list for a player — including its
//     *title*, which is the disclosure that a `[[link]]` used to be.
//   - **An empty query is not an error and not a search.** It is "no suggestions",
//     and a dropdown that opens on focus must be able to ask without asking
//     anything.
//
// # The limit is not the query language's
//
// `search.MaxLimit` is fifty and a search box wants eight. A dropdown of fifty
// titles is a dropdown nobody reads, and the fusion cost is linear in the depth it
// fetches, so the two numbers being different is the difference between a search
// box and a denial of service. `searchCandidateLimit` is the dropdown's.

const (
	// searchCandidateLimit is how many candidates a dropdown shows.
	searchCandidateLimit = 8

	// searchMinQuery is the shortest query worth searching for.
	//
	// One character is not a search, it is a prefix test over the whole corpus:
	// the cheapest way to make a server do fifty queries a second is to let
	// somebody type. Two characters is enough for FTS5 to do prefix matching
	// meaningfully and few enough to keep the first keystroke out of the
	// database.
	searchMinQuery = 2
)

// searchResults is `GET ?search=1`: the candidate list for what somebody is typing.
//
// It answers with a *fragment*, and that is the design rather than an
// implementation detail: Datastar swaps it into the dropdown, and a fragment means
// there is exactly one place the candidates are drawn and the page's own chrome is
// not re-rendered underneath a reader's cursor.
func (a *app) searchResults(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if req.Campaign.ID == "" {
		a.fail(w, r, "searching in no campaign", errNoCampaign)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))

	// The two early answers, in the order that saves the most work: too short to be
	// worth a query, and then not a query at all.
	//
	// Both are 200 with an empty fragment, and neither is a 400. A search box
	// fires a request per keystroke and a player mashing the spacebar is not a
	// malformed request; answering an error would put a red box in a dropdown for
	// something that is not wrong.
	if len(query) < searchMinQuery {
		a.renderFragment(w, r, searchResultsView(searchFragment{Candidates: nil, Query: query}))
		return
	}

	limit := searchCandidateLimit
	if asked := r.URL.Query().Get("limit"); asked != "" {
		if parsed, err := strconv.Atoi(asked); err == nil && parsed > 0 {
			limit = min(parsed, search.MaxLimit)
		}
	}

	// `WithPrefixLastTerm` is what makes this a box rather than a form: the last
	// term is the one still being typed, and a search that matches whole words only
	// shows a result after the last character of the word that finds it.
	hits, err := search.Run(r.Context(), a.cfg.Store, req.Campaign.ID, req.Principal, query, limit, search.WithPrefixLastTerm())
	if err != nil {
		// **A query that cannot be read is not a 500.** `search.ErrQuery` is the
		// package saying so, and the only thing that produces it is an `is:` with
		// a level that is not one of the three — a DM typing `is:` on the way to
		// `is:dm-only`. The dropdown says nothing, which is what a dropdown with
		// half a filter in it should say, and a 500 for a keystroke is a wiki
		// that appears to break while somebody types.
		if errors.Is(err, search.ErrQuery) {
			a.renderFragment(w, r, searchResultsView(searchFragment{Candidates: nil, Query: query}))
			return
		}
		a.fail(w, r, "searching for "+query, err)
		return
	}

	a.renderFragment(w, r, searchResultsView(searchFragment{
		Candidates: a.candidatesFor(r, hits),
		Query:      query,
	}))
}

// candidatesFor is a hit list as the dropdown wants it: the URL is built here and
// nowhere else, and the *secret* hits are marked so the view can say so.
//
// **A hit's `FromSecrets` is shown to the reader and not filtered out**, because a
// player searching their own character's page and finding it in the private index
// is the point of that index, and a dropdown that silently hid it would be a
// dropdown that lied. The scope already admitted the hit, so nothing is disclosed
// by saying where it came from.
//
// # And the plugins
//
// A hit is narrowed through the same policies as the page tree, and this is the
// listing where skipping it would be worst: the dropdown is a list of page *titles*
// for a reader typing one character at a time, and a title is the disclosure ADR 0020
// spent a milestone removing from a `[[link]]`. A policy that hides a page from this
// player has to hide its title too, or it has not hidden it.
//
// It is a method rather than the free function it was because it now needs the app's
// policy set and its store, and a function that reached for either through a context
// value would be a function that could be called without them.
func (a *app) candidatesFor(r *http.Request, hits []search.Hit) []candidate {
	req := requestFrom(r.Context())
	kept := a.readableHits(r.Context(), req.Principal, hits)

	candidates := make([]candidate, 0, len(kept))
	for _, hit := range kept {
		candidates = append(candidates, candidate{
			Path:    hit.Path,
			Title:   hit.Title,
			Kind:    hit.Type,
			URL:     pageURL(req.Campaign, hit.Path),
			Snippet: hit.Snippet,
			Secret:  hit.FromSecrets,
		})
	}
	return candidates
}

// readableHits narrows a hit list through the plugins' policies.
//
// A hit is not a [domain.Page] and cannot be one — the store read it out of an FTS5
// index, not out of `pages` — so the [access.PageMeta] is built from the two columns
// the hit carries for the purpose ([search.Hit.Visibility] and
// [search.Hit.OwnerCharacterPageID]) plus the two that are the same for every hit
// that reached this far: it is not archived, because the store does not return an
// archived page, and the query's own `is:` clause is a filter rather than a decision.
func (a *app) readableHits(
	ctx context.Context, as domain.Principal, hits []search.Hit,
) []search.Hit {
	if a.cfg.Policies.IsEmpty() {
		return hits
	}

	bound := a.ownersBound(ctx, as, hitOwners(hits))
	kept := make([]search.Hit, 0, len(hits))

	for _, hit := range hits {
		meta := access.PageMeta{
			Type:       domain.PageType(hit.Type),
			Visibility: domain.Visibility(hit.Visibility),
			Path:       hit.Path,
			Owned:      bound[hit.OwnerCharacterPageID],
		}
		decided := access.For(access.PrincipalOf(as), meta)
		if a.cfg.Policies.Apply(ctx, access.PrincipalOf(as), meta, decided).CanRead {
			kept = append(kept, hit)
		}
	}

	return kept
}

// hitOwners is the distinct owner page ids in a hit list, for the memo to be keyed
// on.
func hitOwners(hits []search.Hit) []string {
	seen := make(map[string]bool, len(hits))
	owners := make([]string, 0, len(hits))
	for _, hit := range hits {
		if hit.OwnerCharacterPageID == "" || seen[hit.OwnerCharacterPageID] {
			continue
		}
		seen[hit.OwnerCharacterPageID] = true
		owners = append(owners, hit.OwnerCharacterPageID)
	}
	return owners
}

// renderFragment writes a fragment for the browser to swap in, and it is a
// function because three routes answer with one and each of them has to set the
// content type and the cache policy itself.
//
// `no-store` is not optional here. A dropdown's contents are a function of what
// somebody is typing and of who they are, and a cached one is a list of page
// titles delivered to the next person to look at that dropdown.
func (a *app) renderFragment(w http.ResponseWriter, r *http.Request, view templ.Component) {
	w.Header().Set("Content-Type", contentTypeHTML)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusOK)
	if err := view.Render(r.Context(), w); err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelDebug, "a fragment failed",
			slog.String("error", err.Error()),
			slog.String("path", r.URL.Path))
	}
}
