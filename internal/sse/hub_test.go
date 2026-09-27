package sse_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/sse"
)

func TestTheHubFansOneUpdateOutToEveryStreamOnTheTopic(t *testing.T) {
	t.Parallel()

	hub := sse.NewHub(8)
	defer hub.Close()

	watching, _ := subscribe(t, hub, "blackwater/locations/rivergate")
	elsewhere, _ := subscribe(t, hub, "blackwater/sessions/14")

	// Two subscribers on the same topic both take it, which is the case a
	// single global channel would have got wrong in the other direction.
	alsoWatching, _ := subscribe(t, hub, "blackwater/locations/rivergate")

	if got := hub.Publish("blackwater/locations/rivergate", "page", static("changed")); got != 2 {
		t.Fatalf("Publish reached %d streams, want 2", got)
	}

	assertPatch(t, watching, "page", "changed")
	assertPatch(t, alsoWatching, "page", "changed")
	select {
	case patch := <-elsewhere:
		t.Errorf("a stream on another topic received a patch: %v", patch)
	default:
	}
}

// TestAHubWithNoSubscribersPublishesToNobody is not a test of an edge case, it
// is the property that lets the index watcher publish without first checking
// whether anybody is watching: the common case on a DM's machine is no browser
// open at all.
func TestAHubWithNoSubscribersPublishesToNobody(t *testing.T) {
	t.Parallel()

	hub := sse.NewHub(8)
	defer hub.Close()

	if got := hub.Publish("nothing/watching", "page", static("x")); got != 0 {
		t.Errorf("Publish reported %d deliveries to an empty hub", got)
	}
	if hub.Subscribers() != 0 {
		t.Errorf("the hub is holding %d subscriptions after publishing", hub.Subscribers())
	}
}

// TestTheHubIsBounded: a stream is a goroutine and a socket, and a share link
// pasted somewhere reachable is a stream nobody is counting. The bound refuses
// rather than evicts, because an eviction silently closes somebody's stream and
// a refusal produces an error the handler can turn into a status code.
func TestTheHubIsBounded(t *testing.T) {
	t.Parallel()

	hub := sse.NewHub(2)
	defer hub.Close()

	_, firstCancel := subscribe(t, hub, "a")
	_, _ = subscribe(t, hub, "b")

	if _, _, err := hub.Subscribe("c"); !errors.Is(err, sse.ErrHubFull) {
		t.Fatalf("a third subscription on a hub of two returned %v, want ErrHubFull", err)
	}

	// The bound is across topics and not per topic: a hub counting per topic
	// could be filled by one topic, and its real limit would be its number of
	// topics times the number it was configured with.
	if hub.Subscribers() != 2 {
		t.Errorf("the hub reports %d subscriptions, want 2", hub.Subscribers())
	}

	// Ending one makes room, which is what makes a refused stream recoverable
	// rather than permanent.
	firstCancel()
	if _, _, err := hub.Subscribe("c"); err != nil {
		t.Errorf("subscribing after a cancel returned %v", err)
	}
}

// TestASlowSubscriberLosesAFrameRatherThanThePublisher: Publish is called from
// somebody else's request -- the index watcher, a save -- and it may not block.
// Dropping is only sound because an update is a whole element, so the next one
// carries the page as it is then; the count is reported so a test and a curious
// DM can see it happening.
func TestASlowSubscriberLosesAFrameRatherThanThePublisher(t *testing.T) {
	t.Parallel()

	hub := sse.NewHub(8)
	defer hub.Close()

	// A subscriber that is never read from: the reader is the point of the test.
	subscribe(t, hub, "a")

	// Three publishes into a buffer of one. The first is taken and the other two
	// are dropped rather than queued, and none of them blocks.
	done := make(chan int, 1)
	go func() {
		total := 0
		for range 3 {
			total += hub.Publish("a", "page", static("x"))
		}
		done <- total
	}()

	select {
	case delivered := <-done:
		if delivered != 1 {
			t.Errorf("three publishes into a buffer of one delivered %d, want 1", delivered)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a subscriber that was not reading")
	}

	if hub.Dropped() != 2 {
		t.Errorf("the hub dropped %d frames, want 2", hub.Dropped())
	}
}

// TestCancellingStopsTheUpdates: a handler that has returned must not leave a
// subscriber in the hub's map for ever, and must not leave a goroutine parked on
// a channel nothing will ever write to.
func TestCancellingStopsTheUpdates(t *testing.T) {
	t.Parallel()

	hub := sse.NewHub(8)
	defer hub.Close()

	patches, cancel := subscribe(t, hub, "a")
	cancel()

	if hub.Subscribers() != 0 {
		t.Errorf("a cancelled subscription is still counted: %d", hub.Subscribers())
	}
	if got := hub.Publish("a", "page", static("x")); got != 0 {
		t.Errorf("a cancelled subscription took %d updates", got)
	}

	// Cancelling twice is ordinary -- a deferred call and an explicit one both
	// happen -- and must not close a channel twice.
	cancel()

	if _, open := <-patches; open {
		t.Error("the channel of a cancelled subscription is still open")
	}
}

// TestClosingTheHubDrainsEveryStream is the shutdown property ADR 0006 asks for.
// Closing the channels rather than setting a flag is what makes it work: a
// handler ranging over its channel ends, rather than needing a second thing to
// check.
func TestClosingTheHubDrainsEveryStream(t *testing.T) {
	t.Parallel()

	hub := sse.NewHub(8)
	first, _ := subscribe(t, hub, "a")
	second, _ := subscribe(t, hub, "b")

	// Both handlers finish on their own, from a goroutine each, which is the
	// shape a real handler has.
	var wg sync.WaitGroup
	for _, patches := range []<-chan sse.Patch{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range patches { //nolint:revive // draining on purpose
			}
		}()
	}

	hub.Close()
	hub.Close() // a second close is what a deferred call and an explicit one do

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the hub did not end the streams reading from it")
	}

	if _, _, err := hub.Subscribe("a"); !errors.Is(err, sse.ErrHubClosed) {
		t.Errorf("subscribing to a closed hub returned %v, want ErrHubClosed", err)
	}
}

// TestConcurrentSubscribeAndPublishIsRaceFree is a -race test rather than a
// correctness test: a hub is written to by every player opening a page and read
// by the watcher on every save, and the only assertion is that the two do not
// tear.
func TestConcurrentSubscribeAndPublishIsRaceFree(t *testing.T) {
	t.Parallel()

	hub := sse.NewHub(64)
	defer hub.Close()

	var wg sync.WaitGroup
	for i := range 16 {
		topic := "topic/" + string(rune('a'+i%4))
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, cancel, err := hub.Subscribe(topic); err == nil {
				cancel()
			}
		}()
		go func() {
			defer wg.Done()
			for range 16 {
				hub.Publish(topic, "page", static("x"))
			}
		}()
	}
	wg.Wait()
}

func subscribe(t *testing.T, hub *sse.Hub, topic string) (<-chan sse.Patch, func()) {
	t.Helper()

	patches, cancel, err := hub.Subscribe(topic)
	if err != nil {
		t.Fatalf("Subscribe(%q): %v", topic, err)
	}
	t.Cleanup(cancel)
	return patches, cancel
}

// assertPatch takes one update off a stream and says what it patched. It goes
// through a real stream, because a patch that does not apply to one is a patch
// that does not work.
func assertPatch(t *testing.T, patches <-chan sse.Patch, wantID, wantHTML string) {
	t.Helper()

	select {
	case patch := <-patches:
		rec := httptest.NewRecorder()
		stream := sse.Stream(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/c/x/", nil))
		if err := patch(stream); err != nil {
			t.Fatalf("applying the patch: %v", err)
		}

		body := rec.Body.String()
		if want := "selector #" + wantID; !strings.Contains(body, want) {
			t.Errorf("the patch targeted %q, want %q\n%s", body, want, body)
		}
		if want := "elements " + wantHTML; !strings.Contains(body, want) {
			t.Errorf("the patch did not carry %q\n%s", want, body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no update arrived on a subscribed stream")
	}
}

var _ templ.Component = templ.Raw("")
