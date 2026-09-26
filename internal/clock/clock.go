package clock

import "time"

// Clock reports the current time. Implementations are safe for concurrent use.
//
// This interface, and the clock implementations beside it, are the reason no
// package in this repository calls time.Now(). A timestamp that cannot be
// reproduced is a timestamp that cannot be asserted on.
type Clock interface {
	// Now returns the current time, always in UTC. Implementations must not
	// return a time with a monotonic reading: a monotonic component cannot be
	// serialised, so two identical writes would compare unequal.
	Now() time.Time
}
