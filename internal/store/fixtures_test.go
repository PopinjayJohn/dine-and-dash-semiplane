package store

import (
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
)

// Fixtures shared by the store's own tests. Every test in this package uses the
// same clock and the same id sequence, so a failure message that shows a row
// shows the same row every time the suite runs.

// testTime is an instant from the spec's example vault, and the step means two
// writes in one test get two different timestamps.
var testTime = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

func fixedClock() *clock.Fixed {
	return clock.NewFixed(testTime, time.Minute)
}

func sequenceIDs() idgen.IDGen {
	return idgen.NewSequence("id")
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	switch {
	case err == nil:
		t.Fatalf("no error, want one containing %q", want)
	case !strings.Contains(err.Error(), want):
		t.Fatalf("error %q, want one containing %q", err, want)
	}
}
