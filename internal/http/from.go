package http

import (
	"context"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// # What a plugin's handler is given
//
// Three functions rather than an exported request struct, and the reason is that a
// plugin needs two facts and there is no third one it should be able to reach.
//
// A plugin's route hangs inside `/c/{slug}`, so by the time its handler runs the
// session, campaign and redemption middlewares have all answered: there is a
// principal, a campaign and a slug, and they are the *same* ones a page handler gets
// out of the same context value. That is the whole of the guarantee the mounting
// point offers — a plugin that asks who is asking asks the same question through the
// same code and cannot get a different answer by being a plugin.
//
// The zero values are a real case rather than an error: a request that identified
// nobody has a zero principal and the read predicate answers it, and a campaign that
// could not be resolved is a request that has already failed further along. A caller
// that cannot use the zero value checks `ID == ""`, which is one comparison and
// saves a function that returns an error for a situation the middleware has already
// handled.

// Principal is who a request is for, from the request's context.
//
// The zero value is a request that identified nobody, which is the ordinary case for
// a campaign root opened without a session and the one the read predicate is built
// for.
func Principal(ctx context.Context) domain.Principal {
	return requestFrom(ctx).Principal
}

// Campaign is which campaign a request is in, from the request's context.
//
// The zero value's `Slug` is empty, and a campaign root's URL is what says it — a
// caller that cannot use the zero value checks `ID == ""` and answers "no such
// campaign" the way the core does.
func Campaign(ctx context.Context) domain.Campaign {
	return requestFrom(ctx).Campaign
}
