package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
)

func TestSystemIsUTCAndCurrent(t *testing.T) {
	t.Parallel()

	var c clock.Clock = clock.System{}

	before := time.Now().UTC().Add(-time.Minute)
	got := c.Now()
	after := time.Now().UTC().Add(time.Minute)

	if got.Location() != time.UTC {
		t.Errorf("Now() location = %v, want UTC", got.Location())
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want between %v and %v", got, before, after)
	}
	// A monotonic reading would make an identical instant compare unequal to
	// its serialised form, which is the bug this package exists to prevent.
	if got.Round(0) != got {
		t.Errorf("Now() = %v carries a monotonic reading", got)
	}
}

func TestFixedNow(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)
	step := time.Minute

	tests := map[string]struct {
		clock *clock.Fixed
		calls int
		want  []time.Time
	}{
		"stands still with a zero step": {
			clock: clock.NewFixed(start, 0),
			calls: 3,
			want:  []time.Time{start, start, start},
		},
		"advances by its step on every call": {
			clock: clock.NewFixed(start, step),
			calls: 3,
			want: []time.Time{
				start,
				start.Add(step),
				start.Add(2 * step),
			},
		},
		"starts at the given instant whatever the input zone": {
			clock: clock.NewFixed(start.In(time.FixedZone("elsewhere", 3600)), step),
			calls: 1,
			want:  []time.Time{start},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for i, want := range tc.want {
				if got := tc.clock.Now(); !got.Equal(want) {
					t.Errorf("call %d: Now() = %v, want %v", i+1, got, want)
				}
			}
		})
	}
}

func TestFixedAdvance(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)
	c := clock.NewFixed(start, 0)

	c.Advance(90 * time.Minute)

	if got := c.Now(); !got.Equal(start.Add(90 * time.Minute)) {
		t.Errorf("Now() = %v, want %v", got, start.Add(90*time.Minute))
	}
}

func TestFixedIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	c := clock.NewFixed(time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC), time.Second)

	const goroutines = 8
	const calls = 32

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[time.Time]bool, goroutines*calls)
	)

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range calls {
				now := c.Now()
				mu.Lock()
				seen[now] = true
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if len(seen) != goroutines*calls {
		t.Errorf("got %d distinct instants, want %d: the step is not being handed out once per call", len(seen), goroutines*calls)
	}
}
