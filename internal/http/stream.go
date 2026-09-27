package http

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
)

// # The live page
//
// A page being read updates itself. That is the whole of the feature, and the
// interesting part is what it does *not* do: the page is not re-rendered by the
// watcher, and no decision ever crosses the hub.
//
// # Why the subscriber renders
//
// The obvious design is the one that breaks: the watcher renders the changed page
// once and hands the same component to everyone watching it. A DM and a player are
// watching the same page, the watcher can only pick one decision, and so either
// the DM's page is full of secrets in a player's stream or the player's is missing
// them. It is a one-line design and it has no correct version.
//
// So the hub carries a *notice* and nothing else, and each subscriber re-reads and
// re-renders under its own principal and its own decision, through
// `renderPage` -- the same function the page route uses. One rendering path, one
// decision per reader, and the bytes on a player's stream are produced by code
// holding that player's decision.
//
// The cost is one store read and one render per reader per change, and the render
// is cached by content hash and decision, so a campaign with four players watching
// one page is one render of the DM's view and one of a player's, not four of
// either.

// articleID is the element the stream patches, and it is the same id the page's
// own template puts on the article.
//
// Three places name it -- the template, the stylesheet and the patch selector --
// and `TestTheStreamPatchesTheArticle` is what holds them together. A patch
// targeting an id the page does not have is a page that silently stops updating,
// which is the failure a reader cannot report because they do not know it should
// have.
const articleID = "page"

// streamKeepAlive is how often a stream that has said nothing says something.
//
// A stream that is quiet for a long time is a stream a proxy has decided to close
// and a browser has decided to stop reconnecting, and the reader's page then looks
// live while it is not. A comment frame is the cheapest thing that keeps a
// connection honest: it carries no data and the client ignores it.
const streamKeepAlive = 25 * time.Second

// streamTopic is the hub's name for one page.
//
// The campaign is the page *id* and not the slug because a topic is an identity
// and ids are identities; the slug can be renamed and the id cannot. It is joined
// with a separator that cannot appear in a UUID or a vault path component, so
// `campaignID + path` and `campaignID + "/x"` cannot collide.
func streamTopic(campaignID, path string) string {
	return campaignID + "\x00" + path
}

// pageStream is a page, as a stream of its own changes.
//
// The order of the four steps is the design:
//
//  1. **The page has to be readable now.** A stream for a page its reader may not
//     see is a stream of nothing, and the answer is a 404 rather than an empty
//     stream, because a stream that never sends anything is a connection a client
//     reconnects to for ever.
//  2. **Subscribe before the first render.** A change that lands between the read
//     and the subscribe would be lost, and the page would sit there looking live
//     while being stale. The first patch is the page as it is now, so a client that
//     connects late is in sync without waiting for the next save.
//  3. **Send the current page once.** Not as an optimisation: a client that
//     connects after a save would otherwise show the old page until the next one.
//  4. **Then loop**, applying whatever the hub hands over until the connection or
//     the server goes away.
func (a *app) pageStream(w http.ResponseWriter, r *http.Request, path string) {
	req := requestFrom(r.Context())

	stored, err := a.cfg.Store.GetPage(r.Context(), req.Campaign.ID, path, req.Principal)
	if err != nil {
		if isNotFound(err) {
			a.notFound(w, r)
			return
		}
		a.fail(w, r, "reading "+path+" for a stream", err)
		return
	}

	// The builder is called on this handler's goroutine, once per change, and it
	// re-reads through the same store call the route above made with the same
	// principal. If the page becomes unreadable -- the DM archived it, or revoked
	// the reader -- the read returns not-found and the builder returns an error,
	// which ends the stream. The client's last frame is then the last state that
	// was allowed, which is the only thing it can be: the page is not going to
	// become readable again by waiting.
	topic := streamTopic(req.Campaign.ID, path)
	patches, cancel, err := a.cfg.Hub.Subscribe(topic, func() (sse.Patch, error) {
		return a.pagePatch(r, path, stored.Title)
	})
	if err != nil {
		// A full hub is a `503` with a `Retry-After`, and a closed one is a plain
		// `503`: they are different things to tell a client and a reader who is
		// told "come back later" is a reader whose page updates in a minute.
		if errors.Is(err, sse.ErrHubFull) {
			w.Header().Set("Retry-After", "30")
			a.fail(w, r, "the stream hub is full", err)
			return
		}
		a.fail(w, r, "the stream hub is closed", err)
		return
	}
	defer cancel()

	// The first frame, before the stream is upgraded, so a client that gets it
	// has a page and a client that fails to get it gets a clean error rather than
	// a half-open stream.
	first, err := a.pagePatch(r, path, stored.Title)
	if err != nil {
		if isNotFound(err) {
			a.notFound(w, r)
			return
		}
		a.fail(w, r, "rendering "+path+" for a stream", err)
		return
	}

	stream := sse.Stream(w, r)
	if err := first(stream); err != nil {
		a.log.LogAttrs(r.Context(), slog.LevelDebug, "a stream's first patch failed",
			slog.String("error", err.Error()),
			slog.String("path", path),
		)
		return
	}

	keepAlive := time.NewTicker(streamKeepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			// The reader closed the tab. There is no error to report and no
			// response left to send, so this is a return.
			return

		case <-keepAlive.C:
			// A comment frame, which the client ignores and the intermediaries
			// do not. `sse` has no method for one -- a Patch is an element swap
			// and inventing a fifth function is a change to ADR 0006 -- so it is
			// written here, which is the one place in the application that knows
			// the framing's spelling. The spike recorded it, and this is the
			// sentence that says why the duplication is deliberate.
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}

		case patch, open := <-patches:
			if !open {
				// The hub is closing: the server is going down.
				return
			}
			if err := patch(stream); err != nil {
				// The client hung up, or the write failed. Either way there is
				// nothing left to write to, and the reader has whatever the last
				// successful frame was.
				return
			}
		}
	}
}

// pagePatch is the function that turns "this page changed" into one frame for
// *this* reader.
//
// It re-reads, re-renders under this request's decision, and swaps the article
// element and nothing else -- so a reader who is halfway down the page keeps their
// scroll position and their place in the sidebar, which is the difference between a
// live page and a page that reloads itself.
func (a *app) pagePatch(r *http.Request, path, title string) (sse.Patch, error) {
	req := requestFrom(r.Context())

	latest, err := a.cfg.Store.GetPage(r.Context(), req.Campaign.ID, path, req.Principal)
	if err != nil {
		return nil, err
	}

	result, err := a.renderPage(r.Context(), req.Campaign, latest, req.Principal)
	if err != nil {
		return nil, err
	}

	view := renderedPage{
		Path:      latest.Path,
		Title:     latest.Title,
		Kind:      latest.Type.String(),
		Updated:   latest.UpdatedAt.UTC().Format(time.RFC3339),
		Audience:  latest.Visibility.String(),
		HTML:      pageFragment(result.HTML),
		TOC:       result.TOC,
		RawURL:    rawURL(req.Campaign, latest.Path),
		StreamURL: streamURL(req.Campaign, latest.Path),
	}
	if view.Title == "" {
		// A page's title is derived from its file when the frontmatter has none,
		// and a frame with an empty `<h1>` is a frame that looks broken.
		view.Title = title
	}

	return func(stream *sse.Sender) error {
		return stream.Swap(articleID, article(view))
	}, nil
}
