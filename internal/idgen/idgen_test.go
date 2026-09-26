package idgen_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
)

func TestGenerators(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		gen   idgen.IDGen
		calls int
		// distinct is how many different ids the calls may return.
		distinct int
		// check asserts something about the nth id, 1-based.
		check func(t *testing.T, n int, id string)
	}{
		"uuid mints parseable unique values": {
			gen:      idgen.UUID{},
			calls:    4,
			distinct: 4,
			check: func(t *testing.T, _ int, id string) {
				t.Helper()

				parsed, err := uuid.Parse(id)
				if err != nil {
					t.Fatalf("NewID() = %q, which is not a UUID: %v", id, err)
				}
				if parsed.Version() != 4 {
					t.Errorf("uuid version = %d, want 4 (random)", parsed.Version())
				}
			},
		},
		"sequence counts from one": {
			gen:      idgen.NewSequence("page"),
			calls:    3,
			distinct: 3,
			check: func(t *testing.T, n int, id string) {
				t.Helper()

				want := "page-" + string(rune('0'+n))
				if id != want {
					t.Errorf("call %d: NewID() = %q, want %q", n, id, want)
				}
			},
		},
		"constant repeats itself": {
			gen:      idgen.Constant("fixed"),
			calls:    3,
			distinct: 1,
			check: func(t *testing.T, _ int, id string) {
				t.Helper()

				if id != "fixed" {
					t.Errorf("NewID() = %q, want %q", id, "fixed")
				}
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			seen := make(map[string]bool, tc.calls)
			for i := range tc.calls {
				id := tc.gen.NewID()
				tc.check(t, i+1, id)
				seen[id] = true
			}

			if len(seen) != tc.distinct {
				t.Errorf("got %d distinct ids over %d calls, want %d", len(seen), tc.calls, tc.distinct)
			}
		})
	}
}

func TestSequenceStringNamesTheNextID(t *testing.T) {
	t.Parallel()

	gen := idgen.NewSequence("rev")
	gen.NewID()

	if got := gen.String(); !strings.Contains(got, "next rev-2") {
		t.Errorf("String() = %q, want it to name rev-2 as the next id", got)
	}
}

func TestGeneratorsAreSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	tests := map[string]idgen.IDGen{
		"uuid":     idgen.UUID{},
		"sequence": idgen.NewSequence("page"),
	}

	for name, gen := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			const goroutines = 8
			const calls = 64

			var (
				wg   sync.WaitGroup
				mu   sync.Mutex
				seen = make(map[string]bool, goroutines*calls)
			)

			for range goroutines {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range calls {
						id := gen.NewID()
						mu.Lock()
						seen[id] = true
						mu.Unlock()
					}
				}()
			}
			wg.Wait()

			if len(seen) != goroutines*calls {
				t.Errorf("got %d distinct ids, want %d: the generator handed out a duplicate", len(seen), goroutines*calls)
			}
		})
	}
}
