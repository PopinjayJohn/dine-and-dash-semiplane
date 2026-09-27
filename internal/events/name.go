package events

import "context"

// The event names and the subscriber type, in their own file because a type's file
// is where a reader looks for "what are the options" and this file is the answer to
// that and to nothing else.

// The three events, as a closed set.
//
// A closed set is a decision. A plugin cannot add an event, because an event is a
// contract between a publisher and a subscriber and a plugin that could add one
// would be able to publish for a thing that has no publisher — and the first
// subscriber to arrive would be a plugin listening for a silence.
type Name string

const (
	// PageSaved is published after a page's file has been written and its row
	// re-derived. A subscriber that caches something derived from a page's content
	// wants this one; a subscriber that renders a page does not, because the page
	// route is the only thing that renders a page.
	PageSaved Name = "page.saved"

	// PageViewed is published after a page has been served. It is the event a
	// "recently changed" or "most read" feature would want, and it is published
	// per request rather than per unique reader.
	PageViewed Name = "page.viewed"

	// ShareLinkUsed is published when a share link is redeemed, which is the moment
	// a principal exists. A subscriber that wants to know somebody joined — for a
	// lobby screen, for a session log, for a plugin that keeps per-principal state —
	// wants this one and not PageViewed.
	ShareLinkUsed Name = "share-link.used"
)

// Subscriber is what a plugin registers.
//
// It is a function type rather than an interface with a method because a subscriber
// has no state of its own worth naming — the plugin that has state holds it in the
// closure — and because a func type in a map is a value the registry can copy
// without knowing anything about what is behind it.
type Subscriber func(ctx context.Context, event Event)
