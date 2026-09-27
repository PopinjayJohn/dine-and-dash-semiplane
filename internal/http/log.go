package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
)

// # The session log
//
// A list of what changed in this campaign, most recent first, that updates itself
// while a reader is looking at it.
//
// It is the same hub, the same notice and the same "the subscriber renders it under
// its own decision" rule as a live page, over a *campaign-wide* topic rather than a
// page's. That is why it is nearly free: one more subscriber on a topic the
// publisher was already publishing to, and a store query the page view did not
// need.
//
// # Why it is a list of pages and not a list of edits
//
// A page's row carries `updated_at` and that is what this orders by. There is no
// record of *who* changed it or *what they typed* outside the revisions, and a log
// that claimed either would be a log that is wrong on the first edit anybody makes
// through a file rather than the editor. So a line is "this page changed at this
// time", the page's title is resolved *now* rather than remembered, and a reader
// who wants the prose asks the page's history — which is the editor's panel, and it
// keeps the same numbers.
//
// # A reader sees their own campaign
//
// The listing is the read predicate, so a player watching the log sees the public
// pages and never the DM's changes to the private ones — including the fact that a
// private page changed, because a row that does not come back is a row that is not
// in the list. A log that announced hidden edits would undo ADR 0020's fix through
// a different route.

// logEntryLimit is how many lines the log holds.
//
// Twelve, because the log is for "what did I miss in the last half hour" and a
// hundred lines is a page of reading rather than a glance. It is also the depth
// each notice re-reads, so a big number here is a big number of queries per
// keystroke in somebody else's editor.
const logEntryLimit = 12

// logKeepAlive is how often the log says something when nothing has changed.
const logKeepAlive = 60 * time.Second

// logResults is the log's contents as a fragment, for a request that wants the
// list once rather than a stream.
func (a *app) logResults(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	if req.Campaign.ID == "" {
		a.notFound(w, r)
		return
	}

	a.renderFragment(w, r, log(logEntriesFor(a, r.Context(), req)))
}

// logEntriesFor is the campaign's recently changed pages, as the log draws them.
func logEntriesFor(a *app, ctx context.Context, req *request) logView {
	pages, err := a.cfg.Store.ListRecentlyChanged(ctx, req.Campaign.ID, req.Principal, logEntryLimit)
	if err != nil {
		// An empty log and a broken one look the same to a reader, and the log is
		// the one surface here where a failure is not worth interrupting anybody
		// over. The reason is in the log.
		a.log.LogAttrs(ctx, slog.LevelDebug, "listing the session log",
			slog.String("error", err.Error()),
			slog.String("campaign", req.Campaign.Slug.String()))
		return logView{}
	}

	view := logView{Entries: make([]logEntry, 0, len(pages))}
	for _, page := range pages {
		view.Entries = append(view.Entries, logEntry{
			Title:   page.Title,
			Path:    page.Path,
			URL:     pageURL(req.Campaign, page.Path),
			Kind:    page.Type.String(),
			Changed: page.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return view
}

// liveLog is the log with a stream attached, and it is what a reader who has
// navigated to it gets.
//
// The stream is the same hub and the same subscriber discipline as a live page: a
// notice on the campaign topic, and the subscriber re-reads the list under its own
// decision before anything is sent. **No change's content crosses the hub** — only
// the fact that something changed — so a reader who is watching a campaign they
// are only a player in cannot learn anything from the traffic that a reader who is
// the DM could not also learn.
func (a *app) liveLog(w http.ResponseWriter, r *http.Request) {
	req := requestFrom(r.Context())
	topic := streamTopic(req.Campaign.ID, "")

	patches, cancel, err := a.cfg.Hub.Subscribe(topic, func() (sse.Patch, error) {
		return func(stream *sse.Sender) error {
			return stream.Swap(logID, log(logEntriesFor(a, r.Context(), req)))
		}, nil
	})
	if err != nil {
		a.fail(w, r, "subscribing to the session log", err)
		return
	}
	defer cancel()

	stream := sse.Stream(w, r)
	if err := stream.Swap(logID, log(logEntriesFor(a, r.Context(), req))); err != nil {
		return
	}

	keepAlive := time.NewTicker(logKeepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := w.Write([]byte(": keep-alive\n\n")); err != nil {
				return
			}
			if err := http.NewResponseController(w).Flush(); err != nil {
				return
			}
		case patch, open := <-patches:
			if !open {
				return
			}
			if err := patch(stream); err != nil {
				return
			}
		}
	}
}

// logID is the element the stream patches, and it is a constant rather than a
// name built from the campaign: a stream that could patch any element in a page
// is a stream that would be asking the browser to believe more than it should.
const logID = "session-log"
