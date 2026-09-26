package lockfile_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/lockfile"
)

var testNow = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

func TestAcquireAndRelease(t *testing.T) {
	t.Parallel()

	locks := t.TempDir()

	lock, err := lockfile.Acquire(locks, "blackwater", testNow, time.Hour)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// The file exists, and it says who has it, for a DM who finds it.
	path := lock.Path()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the lock file is not there: %v", err)
	}
	if !strings.Contains(string(data), itoa(os.Getpid())) {
		t.Errorf("the lock file does not name this process:\n%s", data)
	}
	if filepath.Base(path) != "blackwater.lock" {
		t.Errorf("the lock file is named %q, want blackwater.lock", filepath.Base(path))
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the lock file is still there after Release: %v", err)
	}

	// Releasing twice is not an error, because a deferred Release and an
	// explicit one on the error path is a shape a caller will write.
	if err := lock.Release(); err != nil {
		t.Errorf("the second Release: %v", err)
	}
}

func TestASecondAcquireIsRefused(t *testing.T) {
	t.Parallel()

	locks := t.TempDir()

	held, err := lockfile.Acquire(locks, "blackwater", testNow, time.Hour)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = held.Release() }()

	_, err = lockfile.Acquire(locks, "blackwater", testNow.Add(time.Minute), time.Hour)
	if !errors.Is(err, lockfile.ErrHeld) {
		t.Fatalf("the second Acquire = %v, want an error matching ErrHeld", err)
	}

	// The message has to be enough to act on: who has it, since when, and what
	// to delete if they are not there.
	for _, want := range []string{"blackwater", "process", "delete"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

func TestTwoCampaignsDoNotBlockEachOther(t *testing.T) {
	t.Parallel()

	locks := t.TempDir()

	first, err := lockfile.Acquire(locks, "blackwater", testNow, time.Hour)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = first.Release() }()

	// A DM with two campaigns should not be told they may only sync one: the two
	// do not touch the same rows.
	second, err := lockfile.Acquire(locks, "rivergate-county", testNow, time.Hour)
	if err != nil {
		t.Fatalf("a second campaign's Acquire: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
}

func TestAStaleLockIsTakenOver(t *testing.T) {
	t.Parallel()

	// Each case says when the holder locked and when the second process looks,
	// because "stale" is a comparison between those two and not a property of
	// the lock on its own.
	tests := map[string]struct {
		heldSince  time.Time
		readAt     time.Time
		stale      time.Duration
		wantRefuse bool
	}{
		"a lock looked at immediately is not stale": {
			heldSince:  testNow,
			readAt:     testNow,
			stale:      time.Hour,
			wantRefuse: true,
		},
		"a lock inside the window is not stale": {
			heldSince:  testNow,
			readAt:     testNow.Add(time.Minute),
			stale:      2 * time.Minute,
			wantRefuse: true,
		},
		"a lock past the window is stale": {
			heldSince:  testNow,
			readAt:     testNow.Add(10 * time.Minute),
			stale:      2 * time.Minute,
			wantRefuse: false,
		},
		"a lock from yesterday is stale": {
			heldSince:  testNow.Add(-24 * time.Hour),
			readAt:     testNow,
			stale:      time.Minute,
			wantRefuse: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			locks := t.TempDir()

			held, err := lockfile.Acquire(locks, "blackwater", tt.heldSince, time.Hour)
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			defer func() { _ = held.Release() }()

			_, err = lockfile.Acquire(locks, "blackwater", tt.readAt, tt.stale)
			if tt.wantRefuse {
				if !errors.Is(err, lockfile.ErrHeld) {
					t.Fatalf("Acquire = %v, want an error matching ErrHeld", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("a stale lock was not taken over: %v", err)
			}
		})
	}
}

func TestARefreshKeepsALockAlive(t *testing.T) {
	t.Parallel()

	locks := t.TempDir()

	// A lock that has been working for a while, with a holder that has been
	// telling the file it is alive.
	held, err := lockfile.Acquire(locks, "blackwater", testNow.Add(-time.Hour), time.Hour)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = held.Release() }()

	if refreshErr := held.Refresh(testNow); refreshErr != nil {
		t.Fatalf("Refresh: %v", refreshErr)
	}

	// With the window at a minute, a lock that has just been refreshed is held
	// and a lock that has not would be stale.
	_, err = lockfile.Acquire(locks, "blackwater", testNow, time.Minute)
	if !errors.Is(err, lockfile.ErrHeld) {
		t.Errorf("a refreshed lock was taken over: %v", err)
	}
}

// TestALockFileWeDidNotWriteIsHeld: a file we cannot parse is a file we cannot
// reason about, and the safe answer is to leave it alone until the window has
// passed rather than to decide it is rubbish and take the lock.
func TestALockFileWeDidNotWriteIsHeld(t *testing.T) {
	t.Parallel()

	locks := t.TempDir()
	if err := os.MkdirAll(locks, 0o700); err != nil {
		t.Fatalf("creating the locks directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locks, "blackwater.lock"), []byte("what is this\n"), 0o600); err != nil {
		t.Fatalf("writing the lock file: %v", err)
	}

	_, err := lockfile.Acquire(locks, "blackwater", testNow, time.Hour)
	if err == nil {
		t.Fatal("Acquire took a lock file it could not read")
	}
	if !strings.Contains(err.Error(), "not one this application wrote") {
		t.Errorf("error %q, want it to say the file is not one of ours", err)
	}
}

func TestACampaignNameCannotEscapeTheLocksDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	locks := filepath.Join(root, "locks")

	// A name with separators and dots in it, which a slug would not have and
	// which must not be able to write outside the directory whatever it is.
	lock, err := lockfile.Acquire(locks, "../../etc/passwd", testNow, time.Hour)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = lock.Release() }()

	if filepath.Dir(lock.Path()) != locks {
		t.Errorf("the lock file is at %q, which is outside %q", lock.Path(), locks)
	}
	if strings.ContainsAny(filepath.Base(lock.Path()), `/\`) {
		t.Errorf("the lock file is named %q, which has a path in it", filepath.Base(lock.Path()))
	}
}

func TestAnUnusableLocksDirectoryIsReported(t *testing.T) {
	t.Parallel()

	// A file where the locks directory has to be.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "locks"), []byte("in the way\n"), 0o600); err != nil {
		t.Fatalf("writing the file in the way: %v", err)
	}

	if _, err := lockfile.Acquire(filepath.Join(root, "locks"), "blackwater", testNow, time.Hour); err == nil {
		t.Error("Acquire succeeded with no locks directory to lock in")
	}
}

func TestANilLockReleasesCleanly(t *testing.T) {
	t.Parallel()

	// The shape a caller writes when a lock was never taken: `defer
	// lock.Release()` where lock is nil.
	var lock *lockfile.Lock
	if err := lock.Release(); err != nil {
		t.Errorf("releasing a lock that was never taken: %v", err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	digits := ""
	for ; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	return digits
}
