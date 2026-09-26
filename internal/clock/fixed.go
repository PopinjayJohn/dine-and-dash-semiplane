package clock

import (
	"sync"
	"time"
)

// Fixed is a clock that advances only when told to. Each call to Now returns
// the current instant and then steps forward, so a sequence of writes gets
// distinct, ordered timestamps instead of one value repeated, which is what a
// revision or an audit trail needs to be testable at all.
type Fixed struct {
	mu   sync.Mutex
	now  time.Time
	step time.Duration
}

// NewFixed returns a clock that starts at start and advances by step on every
// call to Now. A zero step makes it stand still.
func NewFixed(start time.Time, step time.Duration) *Fixed {
	return &Fixed{now: start.UTC(), step: step}
}

// Now returns the current instant and advances the clock by its step.
func (f *Fixed) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now
	f.now = f.now.Add(f.step)
	return now
}

// Advance moves the clock forward by d, for a test that needs a gap between
// two events rather than between two calls.
func (f *Fixed) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
