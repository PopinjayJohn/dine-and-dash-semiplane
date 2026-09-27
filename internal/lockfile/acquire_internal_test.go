package lockfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The branch where a lock looks stale and will not go is reachable on every
// platform and is the *normal* case on Windows, where a file with an open handle
// cannot be deleted at all. It needs the unlink to fail to be reached from a
// test, which is why `acquire` takes one; the parallel tests in the package must
// not be racing a package-level variable, so the test of it is in here rather
// than beside them.
func TestAcquireReportsAStaleLockThatWillNotGo(t *testing.T) {
	t.Parallel()

	var refuse = errors.New("the process cannot access the file because it is being used by another process")

	tests := map[string]struct {
		unlink func(string) error
		want   string
	}{
		// What Windows does to a lock file a live holder still has open.
		"the file is in use": {
			unlink: func(string) error { return refuse },
			want:   "stop that process and try again",
		},
		// Somebody else cleared it between the read and the unlink. The lock is
		// gone, which is what we wanted, and reporting an error here would fail
		// a `wiki sync` over a lock that no longer exists.
		"somebody else cleared it first": {
			unlink: func(path string) error {
				_ = os.Remove(path)
				return &fs.PathError{Op: "remove", Path: path, Err: fs.ErrNotExist}
			},
			want: "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			locks := t.TempDir()
			heldSince := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)
			abandoned := fmt.Sprintf("%d\n%s\nheld by pid above\n", os.Getpid(), heldSince.Format(time.RFC3339))
			if err := os.WriteFile(filepath.Join(locks, "blackwater.lock"), []byte(abandoned), 0o600); err != nil {
				t.Fatalf("writing the abandoned lock: %v", err)
			}

			// A day later, so the lock is well past any window anybody would use.
			now := heldSince.Add(24 * time.Hour)
			lock, err := acquire(locks, "blackwater", now, time.Minute, tt.unlink)

			if tt.want == "" {
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
				if releaseErr := lock.Release(); releaseErr != nil {
					t.Errorf("Release: %v", releaseErr)
				}
				return
			}

			// The error class is the point. A caller that tells "somebody else
			// has this campaign" from "the lock file is unreadable" — which is
			// what ErrHeld exists for — must land in the first bucket, because
			// that is what this is.
			if !errors.Is(err, ErrHeld) {
				t.Fatalf("Acquire = %v, want an error matching ErrHeld", err)
			}
			if lock != nil {
				t.Errorf("Acquire returned a lock and an error: %v", lock)
			}

			// And the operating system's own words survive, because "the file is
			// in use by another process" is the part that says what happened.
			if !errors.Is(err, refuse) {
				t.Errorf("the error does not wrap the reason the unlink failed: %v", err)
			}

			// And the message has to be enough to act on. Deleting the file is
			// exactly what does not work here, so the message must not tell a DM
			// to do it -- it must name the process to stop, and say how old the
			// lock is and what the window was, or nobody can tell a stuck process
			// from a slow one.
			for _, want := range []string{
				"blackwater",
				itoa(os.Getpid()),
				"24h0m0s ago",
				"past the 1m0s window",
				"has it open",
				"stop that process",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the message does not mention %q: %v", want, err)
				}
			}

			if strings.Contains(err.Error(), "delete "+filepath.Join(locks, "blackwater.lock")) {
				t.Errorf("the message tells the reader to delete the file, which is the one thing that will not work: %v", err)
			}
		})
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
