package http_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	wiki "github.com/popinjayjohn/dine-and-dash-semiplane/internal/http"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # The live page
//
// A stream does not end, and a test that reads one into a buffer is a test of a
// different program from the one that runs in production. So the response body is a
// pipe: the handler runs in a goroutine, writes when it has something, and is
// stopped the way a browser stops a stream -- by cancelling the request's context.
//
// # Why not a real listener
//
// `httptest.NewServer` would be the honest thing, and it is the first version of
// this file. It is slow enough to be unusable here -- a loopback round trip costs
// seconds on a machine with a proxy on every interface -- and nothing it tests is
// the standard library's. The handler is called directly with a real
// `http.Request` and a real `http.ResponseWriter`, which is everything in this
// package that could be wrong.

// pipeWriter is a ResponseWriter whose body is the read end of a pipe.
//
// It has to be a struct rather than a recorder because the point is that a write
// reaches the reader *immediately*: a recorder accumulates, and a stream that
// accumulates never finishes.
type pipeWriter struct {
	header http.Header
	status int

	writer *io.PipeWriter
}

func (p *pipeWriter) Header() http.Header { return p.header }

func (p *pipeWriter) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}

func (p *pipeWriter) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}
	written, err := p.writer.Write(b)
	if err != nil {
		// A reader that has gone away is a reader who closed the tab, and the
		// error it produces is the pipe's `ErrClosedPipe`. Swallowing it keeps
		// the handler's own error handling honest: it will see a real error when
		// there is one.
		return written, err
	}
	return written, nil
}

// Flush is a no-op, and it exists so that `http.NewResponseController` -- which
// `internal/sse` writes through -- finds a flusher. A pipe write is already
// unbuffered, so there is nothing to do.
func (p *pipeWriter) Flush() {}

// stream is one open stream, and the handle that ends it.
type stream struct {
	cancel context.CancelFunc
	lines  *bufio.Scanner
	writer *pipeWriter
	done   chan struct{}
}

// event is one SSE frame, parsed enough to assert on.
type event struct {
	name string
	data []string
}

func (e event) text() string { return strings.Join(e.data, "\n") }

// openStream starts a stream and reads the first frame, which the handler always
// sends: a client that connects after a save has to be in sync without waiting for
// the next one.
func (f *fixture) openStream(t *testing.T, path string, cookie *http.Cookie) (*stream, event) {
	t.Helper()

	return f.openSSE(t, path+"?stream=1", cookie)
}

// openSSE is the general form, for a stream whose URL is not a page's — the session
// log's `?log=live` is the other one. It takes the target verbatim, which is the
// whole difference and which `openStream` hides behind its own `?stream=1`.
func (f *fixture) openSSE(t *testing.T, target string, cookie *http.Cookie) (*stream, event) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}

	reader, writer := io.Pipe()
	recorder := &pipeWriter{header: http.Header{}, writer: writer}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The handler writes into the pipe and only returns when the context is
		// cancelled, so the pipe's read end is closed afterwards rather than
		// before: closing it first would turn the handler's next write into an
		// error it would log as a client that went away, which is not what
		// happened.
		defer writer.Close()
		f.handler.ServeHTTP(recorder, req)
	}()

	s := &stream{cancel: cancel, lines: bufio.NewScanner(reader), writer: recorder, done: done}
	t.Cleanup(func() {
		cancel()
		_ = reader.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the stream handler did not return after its context was cancelled")
		}
	})

	first, err := s.next(5 * time.Second)
	if err != nil {
		t.Fatalf("the first frame of a stream for %s: %v", target, err)
	}
	return s, first
}

// next reads one frame, or fails.
//
// An SSE frame ends at a blank line, and a comment line -- which is what the
// keep-alive is -- carries nothing and is skipped. Both are the wire format the
// spike recorded, and the reader is written out here rather than using a library so
// that a change in the framing shows up as a failed assertion rather than as a
// dependency upgrade.
func (s *stream) next(within time.Duration) (event, error) {
	type result struct {
		event event
		err   error
	}
	done := make(chan result, 1)

	go func() {
		var cur event
		for s.lines.Scan() {
			line := s.lines.Text()
			switch {
			case strings.HasPrefix(line, ":"):
				continue
			case strings.HasPrefix(line, "event: "):
				cur.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				cur.data = append(cur.data, strings.TrimPrefix(line, "data: "))
			case line == "":
				if cur.name != "" {
					done <- result{event: cur}
					return
				}
			default:
				done <- result{err: errUnexpectedLine(line)}
				return
			}
		}
		if err := s.lines.Err(); err != nil {
			done <- result{err: err}
			return
		}
		done <- result{err: errStreamClosed}
	}()

	select {
	case got := <-done:
		return got.event, got.err
	case <-time.After(within):
		return event{}, errNoFrame
	}
}

// expectNoFrame asserts that nothing arrives within a window, which is how "this
// change did not touch this page" is tested without a sleep long enough to be
// useless to anyone.
func (s *stream) expectNoFrame(t *testing.T, within time.Duration) {
	t.Helper()

	if _, err := s.next(within); err == nil {
		t.Error("a frame arrived that should not have")
	}
}

var (
	errNoFrame        = errors.New("no frame arrived")
	errStreamClosed   = errors.New("the stream closed")
	errUnexpectedLine = func(line string) error {
		return errors.New("a line that is neither a comment, an event nor a dataline: " + line)
	}
)

// TestTheStreamSendsThePageItIsAStreamFor is the happy path: the first frame is the
// page, and it is the whole article rather than the whole document.
//
// The whole document would be a worse answer for a reason that is not obvious: it
// would replace the reader's scroll position and their place in the sidebar every
// time the page changed, which is the difference between a live page and a page
// that reloads itself under you.
func TestTheStreamSendsThePageItIsAStreamFor(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	s, first := f.openStream(t, f.pageURL("locations/rivergate"), player)
	defer s.cancel()

	if first.name != "datastar-patch-elements" {
		t.Errorf("the first frame is %q, want a patch", first.name)
	}
	if want := "selector #page"; !strings.Contains(first.text(), want) {
		t.Errorf("the first frame does not target #page:\n%s", first.text())
	}
	if !strings.Contains(first.text(), "A fortified town") {
		t.Errorf("the first frame is not the page:\n%s", first.text())
	}
	// The article has a table of contents, so `<nav` is in it; what it must not
	// have is the document around it.
	for _, notTheDocument := range []string{"<!DOCTYPE", "datastar.js", `class="sidebar"`, "Log out"} {
		if strings.Contains(first.text(), notTheDocument) {
			t.Errorf("the frame contains %q, so it is the whole document rather than the article", notTheDocument)
		}
	}
}

// TestTheStreamPatchesTheArticle is the contract between three files: the
// template's element id, the stylesheet's selector, and the patch's selector.
//
// A patch targeting an id the page does not have is a page that silently stops
// updating, which is the failure a reader cannot report because they do not know it
// should have been updating.
func TestTheStreamPatchesTheArticle(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	page := f.get(f.pageURL("locations/rivergate"), player)
	if !strings.Contains(page.body, `<article class="page" id="page">`) {
		t.Errorf("the page does not carry the element id the stream patches:\n%s", page.body)
	}

	s, first := f.openStream(t, f.pageURL("locations/rivergate"), player)
	defer s.cancel()
	if !strings.Contains(first.text(), "selector #page") {
		t.Errorf("the stream does not target the id the page has:\n%s", first.text())
	}
}

// TestAChangeIsPushedToAnOpenStream is the feature, and it is tested by doing what
// the index watcher does: rewriting the page's row, then telling the server.
func TestAChangeIsPushedToAnOpenStream(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	s, _ := f.openStream(t, f.pageURL("locations/rivergate"), player)
	defer s.cancel()

	rewriteRivergate(t, f, "The gate is barred and the tide is out.")
	wiki.PageChanged(f.hub, f.campaign.ID, "locations/rivergate")

	frame, err := s.next(5 * time.Second)
	if err != nil {
		t.Fatalf("the change did not arrive: %v", err)
	}
	if !strings.Contains(frame.text(), "The gate is barred") {
		t.Errorf("the pushed frame is not the new page:\n%s", frame.text())
	}
}

// TestAChangeToAnotherPageDoesNotArrive is the other half: the topic is a page, and a
// sync that wrote three pages sends this reader one of them.
func TestAChangeToAnotherPageDoesNotArrive(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	s, _ := f.openStream(t, f.pageURL("locations/rivergate"), player)
	defer s.cancel()

	rewriteRivergate(t, f, "A change to a different page.\n")
	wiki.PageChanged(f.hub, f.campaign.ID, "campaign")

	s.expectNoFrame(t, 300*time.Millisecond)
}

// TestTheStreamRendersUnderTheReadersOwnDecision is the security property, and it
// is the reason the hub carries a notice rather than a page.
//
// A DM and a player watching the same page must get different bytes, and the only
// way to be sure of that is for each of them to render it themselves: if the
// publisher rendered it once, one of the two would be reading the other's page.
func TestTheStreamRendersUnderTheReadersOwnDecision(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	dm := f.dmSession()

	playerStream, _ := f.openStream(t, f.pageURL("locations/rivergate"), player)
	defer playerStream.cancel()
	dmStream, _ := f.openStream(t, f.pageURL("locations/rivergate"), dm)
	defer dmStream.cancel()

	// The DM edits the page and the edit includes a new secret.
	const secret = "CANARY-IN-A-PUSHED-FRAME-9931"
	rewriteRivergate(t, f, "A fortified town.\n\n> [!SECRET] The gate\n> "+secret+"\n")
	wiki.PageChanged(f.hub, f.campaign.ID, "locations/rivergate")

	dmFrame, err := dmStream.next(5 * time.Second)
	if err != nil {
		t.Fatalf("the DM's stream: %v", err)
	}
	if !strings.Contains(dmFrame.text(), secret) {
		t.Errorf("the DM was not sent the secret they wrote:\n%s", dmFrame.text())
	}

	playerFrame, err := playerStream.next(5 * time.Second)
	if err != nil {
		t.Fatalf("the player's stream: %v", err)
	}
	if strings.Contains(playerFrame.text(), secret) {
		t.Errorf("the player was sent the DM's secret in a pushed frame:\n%s", playerFrame.text())
	}
	if !strings.Contains(playerFrame.text(), "A fortified town") {
		t.Errorf("the player's frame is not the page at all:\n%s", playerFrame.text())
	}
}

// TestAStreamForAPageNobodyMayReadIsA404: a stream that never sends anything is a
// connection a client reconnects to for ever, and a stream for a page its reader may
// not see is a stream of nothing. So the 404 comes first, before the upgrade.
func TestAStreamForAPageNobodyMayReadIsA404(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	for _, path := range []string{"npcs/vel", "locations/nowhere"} {
		got := f.get(f.pageURL(path)+"?stream=1", player)

		if got.status != http.StatusNotFound {
			t.Errorf("a stream for %s is %d, want 404", path, got.status)
		}
		// And it was not upgraded, which is the half that matters: a 404 with
		// `text/event-stream` is a client that reads a body that never comes.
		if contentType := got.header.Get("Content-Type"); strings.HasPrefix(contentType, "text/event-stream") {
			t.Errorf("a refused stream was upgraded anyway: %q", contentType)
		}
		if strings.Contains(got.body, "selector #page") {
			t.Errorf("a refused stream sent a patch anyway:\n%s", got.body)
		}
	}
}

// TestAStreamEndsWhenTheReaderGoesAway: a subscriber that does not cancel is a
// goroutine parked on a channel and a socket a browser holds open, and the hub's
// bound is only a bound if the count means something.
func TestAStreamEndsWhenTheReaderGoesAway(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	s, _ := f.openStream(t, f.pageURL("locations/rivergate"), player)
	if f.hub.Subscribers() != 1 {
		t.Fatalf("the hub is holding %d subscriptions, want 1", f.hub.Subscribers())
	}

	s.cancel()

	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream handler did not return after its context was cancelled")
	}

	if got := f.hub.Subscribers(); got != 0 {
		t.Errorf("the hub is still holding %d subscriptions after the reader went away", got)
	}
}

// TestTheStreamIsBounded: a stream is a goroutine and a socket, and a share link
// pasted somewhere reachable is a stream nobody is counting.
func TestTheStreamIsBounded(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	tiny := sse.NewHub(1)
	_, cancel, err := tiny.Subscribe("occupied", noOpPatch)
	if err != nil {
		t.Fatalf("subscribing to the one-slot hub: %v", err)
	}
	t.Cleanup(cancel)
	f.cfg.Hub = tiny
	f.rebuild()

	player := f.playerSession()
	got := f.get(f.pageURL("campaign")+"?stream=1", player)

	if got.status != http.StatusInternalServerError {
		t.Errorf("a stream on a full hub is %d, want 500", got.status)
	}
	if got.header.Get("Retry-After") == "" {
		t.Error("a full hub does not say when to try again")
	}
	// And the page itself still works, because the tree is decoration: a reader
	// whose stream was refused still gets their page.
	if page := f.get(f.pageURL("locations/rivergate"), player); page.status != http.StatusOK {
		t.Errorf("a refused stream took the page with it: %d", page.status)
	}
}

// TestAStreamEndsWhenTheHubCloses is the shutdown property ADR 0006 asks for: a
// handler ranging over its channel ends without checking anything.
func TestAStreamEndsWhenTheHubCloses(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	s, _ := f.openStream(t, f.pageURL("locations/rivergate"), player)
	defer s.cancel()

	f.hub.Close()

	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the hub did not end an open stream")
	}
}

// TestAPageLinksToItsOwnStream is the discoverability half: a stream nobody can find
// is an endpoint that exists and is never opened.
func TestAPageLinksToItsOwnStream(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	got := f.get(f.pageURL("locations/rivergate"), player)
	if want := f.pageURL("locations/rivergate") + "?stream=1"; !strings.Contains(got.body, want) {
		t.Errorf("the page does not link to its own stream (%s):\n%s", want, got.body)
	}
}

// TestTheStreamIsOnlyAWholeReplacement is the property that makes dropping a frame
// sound, and it is worth stating as a test because it is what a future change to
// "send only the diff" would break: a dropped frame is a frame the next one
// replaces, and only because the next one is the whole element.
func TestTheStreamIsOnlyAWholeReplacement(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	s, first := f.openStream(t, f.pageURL("locations/rivergate"), player)
	defer s.cancel()

	// Every frame carries an `elements` dataline, which is the whole new inner
	// HTML. A delta would be distinguishable here and nowhere else.
	if !strings.Contains(first.text(), "elements ") {
		t.Errorf("the frame is not a whole replacement:\n%s", first.text())
	}
	rewriteRivergate(t, f, "Something else entirely.")
	wiki.PageChanged(f.hub, f.campaign.ID, "locations/rivergate")

	frame, err := s.next(5 * time.Second)
	if err != nil {
		t.Fatalf("the second frame: %v", err)
	}
	if !strings.Contains(frame.text(), "Something else entirely.") {
		t.Errorf("the second frame is not the whole page:\n%s", frame.text())
	}
	// The old text is gone, which is what "replacement" means and what a patch
	// that appended would get wrong.
	if strings.Contains(frame.text(), "A fortified town") {
		t.Errorf("the second frame still carries the old page:\n%s", frame.text())
	}
}

// noOpPatch is a subscriber that never changes anything, for a hub that only needs
// to be full.
func noOpPatch() (sse.Patch, error) {
	return func(*sse.Sender) error { return nil }, nil
}

// rewriteRivergate is the DM editing a page, which in this application means
// rewriting its row: the vault is the source of truth and a file write goes through
// a sync, and a test that wrote a file and waited for a watcher would be testing
// the watcher's debounce rather than the stream.
func rewriteRivergate(t *testing.T, f *fixture, prose string) {
	t.Helper()

	if _, err := f.store.UpsertPage(t.Context(), domain.Page{
		CampaignID:  f.campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: Rivergate\nvisibility: players\n",
		Body:        "# Rivergate\n\n" + prose,
		ContentHash: "hash-rivergate-" + prose,
	}, store.AsDM(f.campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
}
