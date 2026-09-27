package auth

import "time"

// Config is how a deployment is set up for authentication, in one place.
//
// It is a struct rather than fields on the Minter and the Redeemer because the
// two windows are *one* decision and not two. A link's expiry and a session's
// lifetime are the same policy — "how long does somebody's access last without
// the DM doing something" — and putting them on two structs means a caller sets
// them in two places and can get them out of step, which is a way to issue a link
// that outlives the session it is exchanged for, or the reverse, without anything
// failing.
//
// Everything here has a default. A config with nothing set is a working
// configuration, because a DM who has never thought about expiry should get links
// that do not expire rather than an error.
type Config struct {
	// BaseURL is the origin share links are built against: scheme and host, and
	// nothing else.
	//
	// It comes from configuration and never from a request header, because a
	// caller that took it from an untrusted `Host` could mint links pointing
	// anywhere, and a DM who followed one would hand their player's token to
	// whoever chose the host. M8 is where this is read.
	BaseURL string

	// Now is the clock. Injected for the same reason as everything else in this
	// project: deterministic tests and a golden `now`.
	//
	// Nil means the system clock, which is the right default for a program
	// running on a DM's own machine. There is deliberately no way to inject a
	// *random* source: a token minted from a seeded generator is a token anybody
	// with the seed can mint.
	Now func() time.Time

	// LinkExpiry is when a newly minted link stops being redeemable. Zero, the
	// default, means never — most DMs would rather revoke by hand than remember
	// to set a date, and a link that quietly expired would arrive as "my player's
	// link stopped working" with no cause.
	LinkExpiry time.Duration

	// SessionLifetime is how long a redeemed link is good for.
	//
	// A fortnight, which is long enough that nobody at a table is logged out
	// mid-session and short enough that a link pasted somewhere public has gone
	// stale on its own even if the DM never gets round to revoking it. A player
	// who has not played in a month clicks their link again and it works, which is
	// the whole point of a capability link rather than a password.
	//
	// Zero means the default, not "never": a session that does not expire is a
	// credential that outlives the reason it was issued.
	SessionLifetime time.Duration
}

// The two windows, resolved. Both are methods rather than constants at the point
// of use so that "unset means default" is decided in one place per window.
const (
	// DefaultLinkExpiry is never, and is a named constant so that a reader
	// looking for the default does not have to infer it from a zero literal.
	DefaultLinkExpiry time.Duration = 0

	// DefaultSessionLifetime is a fortnight; see Config.SessionLifetime.
	DefaultSessionLifetime = 14 * 24 * time.Hour
)

func (c Config) now() time.Time {
	if c.Now == nil {
		return time.Now()
	}
	return c.Now()
}

func (c Config) linkExpiry() time.Duration { return c.LinkExpiry }

func (c Config) sessionLifetime() time.Duration {
	if c.SessionLifetime <= 0 {
		return DefaultSessionLifetime
	}
	return c.SessionLifetime
}
