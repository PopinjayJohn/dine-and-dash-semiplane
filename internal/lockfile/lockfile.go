// Package lockfile is one file on disk that says "this process is working on
// this data directory".
//
// It is the smallest thing that stops the two ways a campaign's index gets
// written by two things at once, and both of those are real rather than
// theoretical:
//
//   - a `wiki sync` in a terminal while the server's watcher is running;
//   - two `wiki sync` commands, because a DM who presses the up-arrow and hits
//     enter twice has done it.
//
// One data directory has one server (ADR 0011), and a lock file is how that is
// enforced rather than hoped for. The lock is per campaign rather than per data
// directory, because two campaigns in one data directory do not touch the same
// rows and a DM with two campaigns should not be told they may only sync one.
package lockfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrHeld says the lock is held by another process. It is a named error so a
// caller can tell "somebody else has this campaign" from "the lock file is
// unreadable", which are different problems with different fixes.
var ErrHeld = errors.New("lockfile: held by another process")

// Lock is a held lock. The zero value is not usable; build one with Acquire.
//
// Holding a lock is holding a file handle. Nothing has to remember to release
// it on the way out, and a process that dies releases it, which is the whole
// reason this is a file and not a row in the database.
type Lock struct {
	path string
	file *os.File
}

// Acquire takes the lock for a campaign, or returns ErrHeld.
//
// `stale` is how old a lock file may be before it is treated as the remains of
// a process that died. A live holder refreshes the file's modification time
// while it works, so a lock that has not been touched in that long belongs to
// something that is no longer running. The window has to be longer than the
// longest gap a live holder can have between refreshes, which is why the holder
// refreshes on a timer rather than after each unit of work: a full reindex of a
// large campaign is a second or two, and the window is minutes.
func Acquire(locksDir string, campaign string, now time.Time, stale time.Duration) (*Lock, error) {
	if err := os.MkdirAll(locksDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", locksDir, err)
	}

	path := filepath.Join(locksDir, sanitize(campaign)+".lock")

	// O_EXCL is the whole mechanism: the create either happens or it does not,
	// and there is no window in which two processes both believe they won.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("taking the lock at %s: %w", path, err)
		}

		heldBy, refreshErr := readHolder(path)
		if refreshErr != nil {
			return nil, refreshErr
		}
		if age := now.Sub(heldBy.at); age < stale {
			return nil, fmt.Errorf("%w: %s, held by process %d since %s (%s ago); if that process is gone, delete %s",
				ErrHeld, campaign, heldBy.pid, heldBy.at.Format(time.RFC3339), age.Round(time.Second).String(), path)
		}

		// Stale: the holder is gone, or its clock is. Take it over, and say so in
		// the file the next reader will find.
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("clearing the stale lock at %s: %w", path, err)
		}
		reopened, reopenErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if reopenErr != nil {
			return nil, fmt.Errorf("taking over the stale lock at %s: %w", path, reopenErr)
		}
		file = reopened
	}

	lock := &Lock{path: path, file: file}
	if err := lock.record(now, ""); err != nil {
		_ = lock.Release()
		return nil, err
	}

	return lock, nil
}

// Path is the lock file's name, for a message that tells a DM which file to
// delete.
func (l *Lock) Path() string {
	return l.path
}

// Refresh tells the next reader that this process is still working.
//
// The holder calls it on a timer. Without it a long reindex would look stale to
// a second `wiki sync` after the window, and the second sync would take the lock
// out from under the first.
func (l *Lock) Refresh(now time.Time) error {
	return l.record(now, "")
}

// record writes the holder into the lock file.
//
// The contents are for a human who finds the file and wonders, and the
// modification time is what the stale check reads: the two are written together
// because writing the file is what updates the time.
func (l *Lock) record(now time.Time, _ string) error {
	if _, err := l.file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewriting %s: %w", l.path, err)
	}
	if err := l.file.Truncate(0); err != nil {
		return fmt.Errorf("rewriting %s: %w", l.path, err)
	}

	_, err := fmt.Fprintf(l.file, "%d\n%s\n%s\n",
		os.Getpid(), now.UTC().Format(time.RFC3339), sanitize("held by pid above"))
	if err != nil {
		return fmt.Errorf("writing %s: %w", l.path, err)
	}

	return nil
}

// Release gives the lock up. It is safe to call more than once.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}

	// The file goes before the handle: a lock file that outlives its handle is a
	// lock nobody can take for the whole stale window.
	//
	// The two calls are written in one expression because the *order* is the
	// point and `errors.Join` evaluates its arguments left to right, so the
	// handle is closed before the name is unlinked. On Windows that is not
	// tidiness — a file with an open handle cannot be deleted at all, so closing
	// second would make Release fail on the one platform where a stale lock
	// cannot be taken over behind a dead process's back.
	path := l.path
	file := l.file
	l.file = nil

	if err := errors.Join(file.Close(), os.Remove(path)); err != nil {
		return fmt.Errorf("releasing the lock at %s: %w", path, err)
	}
	return nil
}

// holder is who a lock file says has it.
type holder struct {
	pid int
	at  time.Time
}

// readHolder reads a lock file's contents.
//
// An unreadable or shapeless lock file is reported as held, and with the age of
// its modification time, because a file we cannot parse is a file we cannot
// reason about and the safe answer is to leave it alone until the window passes.
// The holder's own timestamp is read rather than the file's modification time,
// for two reasons: it is what the file *says*, and a lock file that has been
// copied onto another machine -- which is what a DM does when they back up a
// data directory -- arrives with a modification time that is a lie.
func readHolder(path string) (holder, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return holder{}, fmt.Errorf("reading the lock at %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return holder{}, unreadable(path, data)
	}

	var pid int
	if _, scanErr := fmt.Sscanf(lines[0], "%d", &pid); scanErr != nil {
		return holder{}, unreadable(path, data)
	}

	at, err := time.Parse(time.RFC3339, strings.TrimSpace(lines[1]))
	if err != nil {
		return holder{}, unreadable(path, data)
	}

	return holder{pid: pid, at: at}, nil
}

// unreadable is what a lock file we cannot parse gets. One message for all three
// ways of failing to read it, because from here they are the same situation: a
// file on disk that is not a lock this package wrote, which is left alone until
// the window has passed.
func unreadable(path string, data []byte) error {
	return fmt.Errorf("the lock at %s is not one this application wrote (%q); it is being left alone until it is older than the stale window", path, data)
}

// sanitize turns a campaign slug into a file name.
//
// The slug is already restricted to a safe alphabet, so this is a belt-and-braces
// step: a campaign whose name reaches here from a database row that a person
// typed should not be able to write outside the locks directory.
func sanitize(campaign string) string {
	var b strings.Builder
	for _, r := range campaign {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		default:
			b.WriteRune('-')
		}
	}

	name := b.String()
	if name == "" {
		return "campaign"
	}
	return name
}
