package sse

import (
	"errors"
	"sync"

	"github.com/a-h/templ"
)

// Hub is the fan-out behind a live page: one thing changed, and every browser
// watching it is told.
//
// # Why a hub and not a broadcaster channel
//
// Because a wiki's streams are *per topic* -- the page being read, the session
// log being followed -- and a single global channel would mean the handler for
// one page has to filter out every other page's updates before deciding whether
// one of them was its own. That is a filter in the hot path that is really a
// correctness question ("did I drop the one that was mine?") wearing a
// performance question's clothes.
//
// # Why dropping an update is safe here and would not be elsewhere
//
// A subscriber's channel holds one update and a publish never blocks on it, so a
// slow reader loses frames. That is only sound because an update is a *whole
// element*: the next one carries the page as it is then, so a skipped frame is a
// frame the reader would have replaced anyway. A hub carrying deltas, or ordered
// events such as a chat log, cannot drop, and a caller who needs that has to say
// so rather than inherit this guarantee by accident. The dropped count is
// reported rather than swallowed, so a test -- and a curious DM reading the
// server's log -- can see it happening.
//
// # The bound
//
// A stream is a goroutine parked in a `select` and a socket a DM's browser holds
// open, and a share link pasted somewhere reachable is a stream nobody is
// counting. The cap is on the number of streams a hub hands out and it refuses
// rather than evicts: eviction would silently close a reader's stream, and a
// refused one gets an honest error the handler can turn into a `503`.
type Hub struct {
	mu    sync.Mutex
	limit int

	subs   map[*subscriber]struct{}
	topics map[string]map[*subscriber]struct{}

	dropped int
	closed  bool
}

// ErrHubFull says the hub is already at its bound and the caller should not open
// a stream.
//
// It is a distinct error rather than a nil channel because the two answers a
// handler has are different: one is "here is your stream" and the other is "this
// server is full, come back later", and a handler that cannot tell them apart
// writes a stream it did not mean to write.
var ErrHubFull = errors.New("sse: the hub is at its stream limit")

// ErrHubClosed says the hub is shutting down. It is separate from ErrHubFull so
// that a handler can answer a shutdown with a `503` and a full hub with a
// `Retry-After`, which are different things to tell a client.
var ErrHubClosed = errors.New("sse: the hub is closed")

// patchBuffer is how many updates a subscriber may fall behind by.
//
// One is enough: the second would be a frame the first replaced. Two is not
// obviously more useful and doubles what a slow reader costs, so the number is
// small on purpose and the drop is counted.
const patchBuffer = 1

// NewHub returns a hub holding at most limit streams. A limit of zero or less
// means the hub accepts nothing, which is a thing a test may want and a
// production build must not have.
func NewHub(limit int) *Hub {
	if limit < 0 {
		limit = 0
	}
	return &Hub{
		limit:  limit,
		subs:   map[*subscriber]struct{}{},
		topics: map[string]map[*subscriber]struct{}{},
	}
}

// Patch is one update, as something to apply to a stream rather than as data to
// read: give it the stream the handler is holding and it swaps the element in.
//
// It is a function rather than a struct with an id and a component because those
// two things are only meaningful together -- an id with no component is a
// deletion and a component with no id is a patch of nothing -- and a struct
// invites a handler to read one without the other. The hub is also the only
// thing that knows both, because it is what received the page.
type Patch func(*Sender) error

// Subscribe starts watching a topic.
//
// It returns the channel updates arrive on and the function that ends the
// subscription. Both halves are needed: the handler ranges over the channel
// until it closes, and it has to close it, or it leaves a goroutine parked on a
// channel nothing will ever write to and a subscriber in the hub's map for ever.
//
// The channel is closed by the cancel function and by `Hub.Close`, so a
// `for range` in a handler ends in both cases without the handler checking
// anything.
func (h *Hub) Subscribe(topic string) (<-chan Patch, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, nil, ErrHubClosed
	}
	if h.limit > 0 && len(h.subs) >= h.limit {
		return nil, nil, ErrHubFull
	}

	sub := &subscriber{
		hub:   h,
		topic: topic,
		patch: make(chan Patch, patchBuffer),
	}

	h.subs[sub] = struct{}{}
	if h.topics[topic] == nil {
		h.topics[topic] = map[*subscriber]struct{}{}
	}
	h.topics[topic][sub] = struct{}{}

	return sub.patch, sub.cancel, nil
}

// Publish sends an update to every subscriber on a topic and returns how many
// took it. It never blocks, and it never queues: a subscriber that is behind
// loses this update and is counted in Dropped.
//
// The component is handed over, not rendered here. Rendering under the hub's lock
// would serialise every stream in the server behind the slowest render, and the
// handler that subscribed is the one that knows the access decision the render
// has to be made under -- which is the field the render cache is keyed on, and
// therefore a field a hub-level render would have to invent.
func (h *Hub) Publish(topic, id string, c templ.Component) int {
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.topics[topic]))
	for sub := range h.topics[topic] {
		subs = append(subs, sub)
	}
	h.mu.Unlock()

	patch := Patch(func(s *Sender) error { return s.Swap(id, c) })

	delivered := 0
	for _, sub := range subs {
		select {
		case sub.patch <- patch:
			delivered++
		default:
			h.mu.Lock()
			h.dropped++
			h.mu.Unlock()
		}
	}

	return delivered
}

// Subscribers is how many streams are open across every topic. It takes the lock,
// so it is for a test and for the health line rather than for a hot path.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.subs)
}

// Dropped is how many updates were discarded because a subscriber was behind. It
// is never reset, and it is the number to read when somebody asks why a live
// page did not update.
func (h *Hub) Dropped() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.dropped
}

// Close ends every subscription and refuses any more.
//
// It is the drain ADR 0006 asks for on shutdown, and closing the channels rather
// than setting a flag is what makes it work: a handler ranging over its channel
// ends, and a handler blocked writing to a dead client finds out from the write.
//
// Closing twice is safe, because a shutdown path and a deferred Close are both
// ordinary and one of them will run first.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true

	subs := make([]*subscriber, 0, len(h.subs))
	for sub := range h.subs {
		subs = append(subs, sub)
	}
	h.subs = map[*subscriber]struct{}{}
	h.topics = map[string]map[*subscriber]struct{}{}
	h.mu.Unlock()

	for _, sub := range subs {
		sub.close()
	}
}

// subscriber is one stream, from the hub's side. It is unexported because
// nothing outside this package has an opinion about it: a caller gets a channel
// and a cancel function, which is everything a handler can use.
type subscriber struct {
	hub   *Hub
	topic string

	patch chan Patch

	mu     sync.Mutex
	closed bool
}

// cancel is the function Subscribe hands back. It is idempotent, because a
// handler closing on the way out and a test closing at the end of a case are both
// ordinary and only one of them runs first.
func (s *subscriber) cancel() {
	s.hub.mu.Lock()
	delete(s.hub.subs, s)
	if peers, live := s.hub.topics[s.topic]; live {
		delete(peers, s)
		if len(peers) == 0 {
			delete(s.hub.topics, s.topic)
		}
	}
	s.hub.mu.Unlock()

	s.close()
}

// close shuts the channel. The subscriber's own flag is what makes a double
// close -- from the cancel function and from Hub.Close, in either order -- a
// no-op rather than a panic in somebody else's shutdown path.
func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return
	}
	s.closed = true
	close(s.patch)
}
