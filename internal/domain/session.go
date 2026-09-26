package domain

import "time"

// Session is an exchange of a share link for a cookie: the row that makes
// revocation immediate, because revoking a principal ends every session it
// created on the next request rather than whenever a cookie happens to expire.
type Session struct {
	ID          string
	PrincipalID string
	CreatedAt   time.Time
	ExpiresAt   time.Time

	// UserAgent is recorded for the DM's "active now" view. It is the one
	// piece of anything a visitor sends us that is kept, and it is kept
	// because a DM who suspects a leaked link needs to know how many browsers
	// are holding one.
	UserAgent string
}

// Validate reports whether s is a session that may be persisted.
//
// An expiry in the past is allowed: a session that has already lapsed is
// still a row worth having, and whether it still authenticates is a question
// for Expired, which is asked with a clock rather than guessed at here.
func (s Session) Validate() error {
	switch {
	case s.ID == "":
		return required("session ID")
	case s.PrincipalID == "":
		return required("session principal ID")
	}

	return nil
}

// Expired reports whether the session no longer authenticates as of now. A
// zero expiry means it has none, and is revoked by revoking the principal
// rather than by the clock.
func (s Session) Expired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && !s.ExpiresAt.After(now)
}
