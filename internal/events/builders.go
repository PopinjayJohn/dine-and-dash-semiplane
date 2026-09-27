package events

import "github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"

// Page is the path within a campaign, for the subscribers that only need that.
//
// It is a function rather than a field so that a subscriber cannot be handed a page
// by accident: an event's `Page` is a `domain.Page`, which carries a body, and a
// subscriber is a plugin. The five facts below are the ones an event is *about*.
func (e Event) Path() string { return e.Page.Path }

// Saved builds a [PageSaved] for a page.
func Saved(campaign domain.Campaign, page domain.Page, as domain.Principal) Event {
	return Event{Name: PageSaved, Campaign: campaign, Page: page, Principal: as}
}

// Viewed builds a [PageViewed] for a page.
func Viewed(campaign domain.Campaign, page domain.Page, as domain.Principal) Event {
	return Event{Name: PageViewed, Campaign: campaign, Page: page, Principal: as}
}

// Redeemed builds a [ShareLinkUsed] for the principal the link produced.
func Redeemed(campaign domain.Campaign, as domain.Principal) Event {
	return Event{Name: ShareLinkUsed, Campaign: campaign, Principal: as}
}
