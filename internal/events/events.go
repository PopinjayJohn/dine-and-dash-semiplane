// Package events is the bus a plugin subscribes to, and the two pages of the
// application that publish on it.
//
// # Why a package of its own
//
// There are two publishers and any number of subscribers, so the type has to live
// somewhere both can reach without either importing the other. `internal/http`
// publishes `PageViewed` and `ShareLinkUsed` because a request is the only place
// either happens; `internal/edit` publishes `PageSaved` because a save is the editor's
// work; `internal/plugin` composes the bus and hands it to both. A bus declared in
// `internal/plugin` would make the HTTP layer import the plugin registry to publish a
// page view, which is the dependency the whole milestone is arranged to avoid.
//
// # The three events, and what they are not
//
// An event is a **notice that something happened**, and it carries the facts a
// subscriber needs to decide whether it cares. It is not a hook — nothing is
// rendered, sanitised or decided in a subscriber, because a subscriber is not in
// the render path and must not be able to make a page's bytes depend on whether a
// plugin was loaded. That is the difference from `render.RenderHook` and the reason
// there are two things rather than one.
//
// It carries no timestamp. The project has a hard rule about time — nothing reads a
// clock it was not given — and a bus with a clock in it is a bus with an ambient
// dependency. A subscriber that wants the time has the request's context and its own
// `clock.Clock`, and an event that stamped itself would be a second answer to "what
// time is it" for something that does not need one.
//
// A subscriber is isolated exactly as a hook is: a panicking one is recovered,
// logged and skipped, and whatever published still happened. The difference is the
// *direction* of the failure, and it is the same asymmetry `internal/access` argues:
// a hook that crashes costs a missing contribution and a subscriber that crashes
// costs nothing, because nothing was waiting for it.
package events

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/safe"
)

// Event is one occurrence.
//
// The fields are the ones a subscriber needs to decide whether it cares and, if it
// does, to identify the thing it is about. There is deliberately no payload beyond
// them: a bus that carries a page's body, or a principal's token, is a bus whose
// contents depend on who is subscribed, and a subscriber is a plugin.
type Event struct {
	// Name is which of the three it is.
	Name Name

	// Campaign is the campaign it happened in, when it happened in one. The zero
	// value is a real case — a share link redeemed into a campaign the session
	// could not resolve — and a subscriber that needs a campaign is one that should
	// skip that case rather than be handed a placeholder.
	Campaign domain.Campaign

	// Page is the page it happened to, when there was one. Its zero value is an
	// event about a page that does not exist, which is the normal state of
	// `PageSaved` for a newly created page.
	Page domain.Page

	// Principal is who it happened to, when there was somebody. The zero value is
	// the request that identified nobody, and it is a real case rather than a
	// failure: the read predicate answers it and so must a subscriber.
	Principal domain.Principal
}

// Bus is the set of subscribers, grouped by event.
//
// A nil *Bus is a bus with no subscribers in it, and [Bus.Publish] on one is a
// no-op, so every publisher can call it unconditionally. That matters more here
// than the other nil-receiver habits in this project: a publisher is a page view or
// a save, and neither of those is a place to test a configuration field.
type Bus struct {
	mu   sync.RWMutex
	log  *slog.Logger
	subs map[Name][]subscription
}

// subscription is a subscriber with the name of the plugin that registered it, for
// the recovery and the log line.
type subscription struct {
	plugin string
	names  []Name
	fn     Subscriber
}

// New returns a bus that reports a failing subscriber through log. A nil logger
// means [slog.Default], as everywhere else in this project.
func New(log *slog.Logger) *Bus {
	if log == nil {
		log = slog.Default()
	}
	return &Bus{log: log, subs: map[Name][]subscription{}}
}

// Subscribe registers a subscriber for one or more events.
//
// Naming several events is a convenience and not a feature: a subscriber is one
// value and one log line, and a plugin that wants three events is one plugin that
// wants three events. A subscriber for no event is refused rather than registered
// and never called — a subscriber nobody will ever call is a thing that looks like
// it works.
func (b *Bus) Subscribe(plugin string, names []Name, fn Subscriber) error {
	if b == nil {
		return fmt.Errorf("events: %q subscribed to a nil bus", plugin)
	}
	if plugin == "" {
		return fmt.Errorf("events: a subscriber needs the name of the plugin that registered it")
	}
	if fn == nil {
		return fmt.Errorf("events: subscriber from %q is nil", plugin)
	}
	if len(names) == 0 {
		return fmt.Errorf("events: subscriber from %q named no events", plugin)
	}
	for _, name := range names {
		if !isKnown(name) {
			return fmt.Errorf("events: %q is not one of this bus's events", name)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	for _, name := range names {
		b.subs[name] = append(b.subs[name], subscription{plugin: plugin, names: names, fn: fn})
	}
	return nil
}

// Publish hands an event to every subscriber of it, in registration order.
//
// The order is `(Priority, Name)` because the registry sorted the plugins before it
// built the bus and called Subscribe in that order; the bus does not re-sort, because
// a subscriber list is a handful of entries and a second sort is a second answer to
// an order the registry already decided.
//
// A subscriber that panics is recovered and the rest still run. Nothing was waiting
// for any of them — the publish is already on its way back out to a request or a
// save — so a broken subscriber costs a log line and the one after it still runs.
func (b *Bus) Publish(ctx context.Context, event Event) {
	if b == nil {
		return
	}

	b.mu.RLock()
	subscribers := slices.Clone(b.subs[event.Name])
	b.mu.RUnlock()

	for _, sub := range subscribers {
		b.call(ctx, sub, event)
	}
}

// call runs one subscriber and recovers its panic.
func (b *Bus) call(ctx context.Context, sub subscription, event Event) {
	defer safe.Guard(b.log, sub.plugin, "Events."+string(event.Name))

	sub.fn(ctx, event)
}

// Subscribers is how many subscribers an event has, for a test and for a
// `wiki help plugins` that wants to say what a plugin is listening for.
func (b *Bus) Subscribers(name Name) int {
	if b == nil {
		return 0
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	return len(b.subs[name])
}

// isKnown reports whether a name is one of the three.
//
// It is a switch rather than a slice so that adding a fourth event without adding
// it here is a compile error at the constant and not a runtime refusal from a
// function nobody remembers exists.
func isKnown(name Name) bool {
	switch name {
	case PageSaved, PageViewed, ShareLinkUsed:
		return true
	default:
		return false
	}
}
