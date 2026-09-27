package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// A Watcher keeps the index in step with a vault that is being edited elsewhere.
//
// Which is the normal case: a DM edits their notes in Obsidian with the wiki
// open in a browser, and the page they are looking at is the one they just
// saved. A sync that only ran at boot would be a wiki that is right until the
// next session.
//
// # Debouncing
//
// A save in Obsidian is not one filesystem event. It is a write, a rename, a
// rename back and a modify, and "save all" is a burst of them across twenty
// files. Syncing on each would do the same work five times and race the editor's
// own writes, so events are collected and synced once the dust settles. The wait
// is longer than an editor's atomic-save sequence -- a rename over a fraction of
// a millisecond -- and short enough that a DM who presses reload a moment later
// sees their page.

// DefaultDebounce is how long the watcher waits for a vault to stop changing
// before it syncs.
//
// It is a default rather than a constant because a deployment on a network
// filesystem may need longer, and a caller that knows that should be able to say
// so.
const DefaultDebounce = 150 * time.Millisecond

// Watcher syncs a vault as it changes.
type Watcher struct {
	watcher  *fsnotify.Watcher
	syncer   *Syncer
	debounce time.Duration
	events   atomic.Int64
}

// Watch starts a watcher on a syncer's vault.
//
// The caller closes it. A watcher holds a file-descriptor handle and a
// goroutine, so releasing it belongs to the caller's shutdown path rather than
// to a finaliser nobody can reason about.
func Watch(syncer *Syncer, debounce time.Duration) (*Watcher, error) {
	if syncer == nil || syncer.Vault() == nil {
		return nil, fmt.Errorf("watching: there is no vault to watch")
	}
	if debounce <= 0 {
		debounce = DefaultDebounce
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watching %s: %w", syncer.Campaign().Slug, err)
	}

	w := &Watcher{watcher: watcher, syncer: syncer, debounce: debounce}

	// Every directory, and the reserved ones too. A page can be written into a
	// folder that did not exist a moment ago, so a watcher that only knew the
	// directories present when it started would miss the first page of a new
	// section -- and a directory renamed *into* the vault, which is how a DM
	// reorganises, arrives as one event that a filter would throw away.
	if err := w.watchTree(syncer.Vault().Root()); err != nil {
		return nil, fmt.Errorf("%w (the watcher for %s was closed)", err, syncer.Campaign().Slug)
	}

	return w, nil
}

// Run watches until the context is cancelled or the watcher fails.
//
// onSync gets every report and onError every failure, and neither ends the
// watch: a page the DM has not fixed yet is not a reason to stop watching their
// campaign, and a directory somebody deleted in Obsidian comes back with the
// next event. The only things that end a watch are the context being cancelled
// and the watcher breaking, and both are somebody else's decision.
func (w *Watcher) Run(ctx context.Context, onSync func(Report), onError func(error)) error {
	// The channels are taken once, up front, because Close may clear the field
	// underneath: a caller that closes the watcher while Run is selecting on it
	// would otherwise be a nil dereference, and cancelling the context first
	// and then closing is a shape a caller will get right exactly once.
	events := w.watcher.Events
	errors := w.watcher.Errors

	// One timer that is only ever running or stopped, rather than a timer per
	// event: a save burst is one wait, not one per event.
	var (
		timer  *time.Timer
		timerC <-chan time.Time
	)

	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer, timerC = nil, nil
		}
	}
	defer stopTimer()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case event, ok := <-events:
			if !ok {
				return fmt.Errorf("the watcher for %s stopped", w.syncer.Campaign().Slug)
			}
			w.events.Add(1)

			if !w.interesting(event) {
				continue
			}

			// A new directory is not a page, but a page written into it next
			// second is, and a watcher that never learned about the directory
			// would not see that.
			if event.Has(fsnotify.Create) {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if err := w.watchTree(event.Name); err != nil {
						if onError != nil {
							onError(err)
						}
					}
				}
			}

			if timer == nil {
				timer = time.NewTimer(w.debounce)
				timerC = timer.C
			} else {
				// An event during the wait restarts it: the point is to sync
				// after the last change rather than after the first.
				timer.Reset(w.debounce)
			}

		case watchErr, ok := <-errors:
			if !ok {
				return fmt.Errorf("the watcher for %s stopped", w.syncer.Campaign().Slug)
			}
			// A watch error is usually a directory that has gone, and the next
			// event brings it back.
			if onError != nil {
				onError(watchErr)
			}

		case <-timerC:
			stopTimer()

			report, err := w.syncer.Sync(ctx)
			if err != nil {
				if onError != nil {
					onError(fmt.Errorf("syncing %s: %w", w.syncer.Campaign().Slug, err))
				}
				continue
			}
			if onSync != nil {
				onSync(report)
			}
		}
	}
}

// Close stops the watcher and releases its handles. It is safe to call twice.
func (w *Watcher) Close() error {
	if w == nil || w.watcher == nil {
		return nil
	}

	watcher := w.watcher
	w.watcher = nil

	if err := watcher.Close(); err != nil {
		return fmt.Errorf("closing the watcher: %w", err)
	}
	return nil
}

// Events is how many filesystem events the watcher has seen.
//
// It exists because "the wiki is in step with the vault" is otherwise a claim
// nobody can check: a watcher whose loop has wedged looks exactly like one where
// nothing has changed. A /healthz line that reports this is the difference
// between those two.
func (w *Watcher) Events() int {
	if w == nil {
		return 0
	}
	return int(w.events.Load())
}

// Debounce is the wait this watcher is using, for a caller that wants to print
// it next to the event count.
func (w *Watcher) Debounce() time.Duration {
	if w == nil {
		return 0
	}
	return w.debounce
}

// interesting reports whether an event is one the index cares about, and the
// answer is "is it a page path" -- the same rule the sync applies, asked of the
// same function.
//
// That is deliberate rather than a shortcut. A second rule for "is this
// interesting" would be a second list of reserved names, and the two would drift
// in exactly the place where a mistake leaks: a temporary file, a revision, or
// an attachment that somebody's sync then treats as a page.
//
// The name is made relative to the vault first, because that is the form the
// rule is about. An fsnotify event carries the path the watch was registered
// with -- an absolute one -- and an absolute path is not a page path by any
// account, so asking the question without that step says "no" to every event and
// a watcher that never syncs anything looks exactly like a quiet vault.
func (w *Watcher) interesting(event fsnotify.Event) bool {
	name := event.Name
	if root := w.syncer.Vault().Root(); strings.HasPrefix(name, root) {
		name = strings.TrimPrefix(name, root)
	}

	name = strings.TrimPrefix(filepath.ToSlash(name), "/")
	name = strings.TrimSuffix(name, pageExtension)

	_, err := vault.CheckPagePath(name)
	return err == nil
}

// pageExtension is what a page's file is called, and the watcher needs it to ask
// the question about a page rather than about a file name.
const pageExtension = ".md"

// watchTree adds a directory and everything under it to the watcher.
func (w *Watcher) watchTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("watching %s: %w", path, err)
		}
		if !entry.IsDir() {
			return nil
		}
		if err := w.watcher.Add(path); err != nil {
			return fmt.Errorf("watching %s: %w", path, err)
		}
		return nil
	})
}
