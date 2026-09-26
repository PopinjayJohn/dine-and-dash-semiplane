package clock

import "time"

// System is the real clock, and the only implementation whose output changes
// between runs.
type System struct{}

// Now returns the current time in UTC. UTC everywhere, because a local
// timestamp in the database sorts wrongly on every machine that is not the one
// that wrote it.
func (System) Now() time.Time {
	return time.Now().UTC()
}
