package sse

import (
	"errors"
	"sync"
)

// Hub is the fan-out behind a live page: one thing changed, and every browser
// watching it is told.
//
// # The hub carries a signal, not content
//
// `Publish` takes a topic and nothing else. What the reader does about it is the
// subscriber's own business, so each subscriber re-reads and re-renders under its
// *own* access decision.
//
// That is not a convenience. The obvious alternative -- the publisher rendering
// the change once and handing the same component to everyone -- is a way for a
// DM's render to reach a player's stream, because the two are watching the same
// page and the publisher can only pick one decision. With a signal, the decision
// never crosses the hub at all: a subscriber's bytes are produced by its own
// code, from its own decision, and the question does not arise. It is also what
// makes the drop property sound, which is the next thing.
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
// # Why dropping a change is safe here and would not be elsewhere
//
// A subscriber's channel holds one change and a publish never blocks on it, so a
// slow reader loses some. That is only sound because a change is not a *delta*:
// the subscriber re-reads the current state, so a skipped notification is one it
// would have replaced with a newer read anyway. A hub carrying deltas, or ordered
// events such as a chat log, cannot drop, and a caller who needs that has to say
// so rather than inherit this guarantee by accident. The dropped count is reported
// rather than swallowed, so a test -- and a curious DM reading the server's log
// -- can see it happening.
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
// two things are only meaningful together -- an id with no component is a deletion
// and a component with no id is a patch of nothing -- and a struct invites a
// handler to read one without the other.
type Patch func(*Sender) error

// builder makes the patch for one change, and it is the subscriber's own code.
//
// An error from it ends the subscriber's stream rather than sending an empty
// patch: the page it was watching has gone, or become something its decision will
// not admit, and there is nothing to keep sending.
type builder func() (Patch, error)

// Subscribe starts watching a topic, with the function that turns one change into
// one patch.
//
// The builder is given at subscription rather than at publish because it belongs
// to the subscriber: it holds the access decision, the campaign and the page, and
// none of those are the hub's to know. That is the whole reason a change carries
// no content -- see the type's own comment.
//
// It returns the channel patches arrive on and the function that ends the
// subscription. Both halves are needed: the handler ranges over the channel until
// it closes, and it has to close it, or it leaves a goroutine parked on a channel
// nothing will ever write to and a subscriber in the hub's map for ever.
//
// The channel is closed by the cancel function and by `Hub.Close`, so a
// `for range` in a handler ends in both cases without the handler checking
// anything.
func (h *Hub) Subscribe(topic string, build builder) (<-chan Patch, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.closed {
		return nil, nil, ErrHubClosed
	}
	if h.limit > 0 && len(h.subs) >= h.limit {
		return nil, nil, ErrHubFull
	}

	sub := &subscriber{
		hub:     h,
		topic:   topic,
		build:   build,
		patches: make(chan Patch, patchBuffer),
	}

	h.subs[sub] = struct{}{}
	if h.topics[topic] == nil {
		h.topics[topic] = map[*subscriber]struct{}{}
	}
	h.topics[topic][sub] = struct{}{}

	return sub.patches, sub.cancel, nil
}

// Publish says that a topic changed, to every subscriber on it, and returns how
// many took the notice. It never blocks, and it never queues: a subscriber that is
// behind loses this one and is counted in Dropped.
//
// Each subscriber's own builder makes the patch, so the work happens on whichever
// goroutine the handler is already running rather than here -- publishing from the
// index watcher must not serialise every stream in the server behind the slowest
// render, and it must not have to know any of them.
func (h *Hub) Publish(topic string) int {
	h.mu.Lock()
	subs := make([]*subscriber, 0, len(h.topics[topic]))
	for sub := range h.topics[topic] {
		subs = append(subs, sub)
	}
	h.mu.Unlock()

	delivered := 0
	for _, sub := range subs {
		// **The build is outside every lock**, and that is deliberate twice over: it
		// is the subscriber's own code holding its own decision, and it is a store
		// read and a render. Serialising the slowest render in the server against
		// every publish and every subscribe is not a trade anybody makes twice.
		patch, err := sub.build()
		if err != nil {
			// The subscriber cannot make sense of the change, so it is finished: its
			// page is gone or its decision no longer admits it. It is not counted as
			// dropped -- nothing was lost that it could have had -- and it is left
			// for its own cancel, because a subscriber that has ended on its own
			// terms is not the publisher's to close.
			continue
		}

		switch sub.send(patch) {
		case sendDelivered:
			delivered++
		case sendDropped:
			// Counted *after* `send` released the subscriber's lock, so the order is
			// subscriber-then-hub and never the other way round. `Close` takes them
			// in the opposite order and releases the hub's lock in between, so there
			// is no cycle to deadlock on.
			h.mu.Lock()
			h.dropped++
			h.mu.Unlock()
		case sendGone:
			// It unsubscribed between the snapshot and the send. That is not a drop:
			// nobody was there to miss anything.
		}
	}

	return delivered
}

// sendResult is what happened to one patch on its way to one subscriber. The three
// cases are three because counting a cancelled subscriber as dropped would put a
// number in the log that a DM reads to work out why a live page did not update.
type sendResult int

const (
	// sendDropped means the subscriber was a whole frame behind and could not take
	// another, which is safe: the next read replaces what this one would have.
	sendDropped sendResult = iota

	// sendDelivered means the patch is in the channel.
	sendDelivered

	// sendGone means the subscriber unsubscribed while the patch was being built.
	sendGone
)

// send is one patch to one subscriber, and it is the whole of the concurrency
// question in this package.
//
// The mutex is **per subscriber** and not the hub's, so two streams never wait on
// each other, and it is **not** held while the patch is built — a build is a store
// read and a render, and the hub's lock is held by every publish and every
// subscribe in the process.
//
// **And the `closed` check is inside the lock, which is the fix for a race this
// package had.** `Publish` takes a snapshot of the subscriber list under the hub's
// lock and then sends *outside* it, so a cancel in between closed the channel
// under a send: `panic: send on closed channel`, not an error, on whichever
// machine lost the race first. A previous version of this comment claimed the
// package did not have that race, which is the kind of sentence that convinces
// the next reader to keep the same shape.
//
// The three cases are the three, because the difference between "this reader was
// too far behind" and "this reader had gone" is the difference between a number
// worth looking at and noise.
func (s *subscriber) send(p Patch) sendResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.closed:
		return sendGone
	default:
		select {
		case s.patches <- p:
			return sendDelivered
		default:
			return sendDropped
		}
	}
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

	build   builder
	patches chan Patch

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
	close(s.patches)
}
