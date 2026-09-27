package index_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
)

// The watcher's tests wait for the filesystem rather than for a duration, because
// a test that sleeps for a fixed time is a test that is slow on a good machine
// and flaky on a busy one. The only exception is the negative case, which has
// nothing to wait for and is given a bounded window instead.

// waitFor polls until the condition holds or the deadline passes. The deadline is
// generous: a CI runner doing three things at once is not a failing watcher.
func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatchPicksUpAnEditMadeElsewhere(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	watcher, err := index.Watch(syncer, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	// A DM edits a page in Obsidian with the wiki open. This is the case the
	// watcher exists for -- and it has to happen *after* the watcher is
	// watching, because a filesystem watcher has no history: an edit made before
	// it started is an edit it will never see.
	writeVaultFile(t, vaultDir, "locations/rivergate.md",
		"---\ntitle: Rivergate\n---\n\nA fortified town, half under water since the winter.\n")

	reports := make(chan index.Report, 16)
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()

	done := make(chan error, 1)
	go func() { done <- watcher.Run(watchCtx, func(r index.Report) { reports <- r }, nil) }()

	waitFor(t, "the index to hold the edited page", func() bool {
		page, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "locations/rivergate")
		return err == nil && strings.Contains(page.Body, "half under water")
	})

	if watcher.Events() == 0 {
		t.Error("the watcher saw no events, so the edit was noticed by something else")
	}

	// And it reported a sync rather than doing it silently: a watcher a DM
	// cannot see working is a watcher they cannot trust.
	waitFor(t, "a sync report", func() bool { return len(reports) > 0 })
	if last := <-reports; !slices.Contains(last.Indexed, "locations/rivergate") {
		t.Errorf("the report says %v, want it to include the edited page", last.Indexed)
	}

	stop()
	if err := <-done; err != nil && !isCancelled(err) {
		t.Errorf("Run returned %v, want the context's cancellation", err)
	}
}

func TestWatchPicksUpANewPageAndANewDirectory(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	watcher, err := index.Watch(syncer, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = watcher.Run(watchCtx, nil, nil) }()

	// A page in a directory that did not exist a moment ago: the watcher has to
	// learn about the directory, or the first page of every new section is
	// invisible to it.
	const page = "factions/the-toll-keepers/overview"
	writeVaultFile(t, vaultDir, page+".md", "---\ntitle: The Toll Keepers\n---\n\nA faction.\n")

	waitFor(t, "a page in a new directory to be indexed", func() bool {
		_, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, page)
		return err == nil
	})
}

func TestWatchIgnoresWhatIsNotAPage(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	watcher, err := index.Watch(syncer, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	reports := make(chan index.Report, 16)
	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = watcher.Run(watchCtx, func(r index.Report) { reports <- r }, nil) }()

	// A revision, an attachment, a temporary file and Obsidian's own
	// configuration. None of them is a page, and the watcher has to say so using
	// the same rule the sync uses rather than a list of its own.
	for _, path := range []string{
		"_history/locations/rivergate/1-2026-02-14T19-03-00Z.md",
		"_attachments/map-rivergate.png",
		".obsidian/app.json",
		"locations/.wiki-tmp-1234-1",
		"locations/rivergate.md.wiki-tmp-1234-1",
	} {
		full := filepath.Join(vaultDir, path)
		if dirErr := os.MkdirAll(filepath.Dir(full), 0o700); dirErr != nil {
			t.Fatalf("creating the directory for %s: %v", path, dirErr)
		}
		if writeErr := os.WriteFile(full, []byte("x"), 0o600); writeErr != nil {
			t.Fatalf("writing %s: %v", path, writeErr)
		}
	}

	// A page, so the watcher has something to do and we can tell the two apart.
	writeVaultFile(t, vaultDir, "locations/thornford.md", "---\ntitle: Thornford\n---\n\nThe other town.\n")

	waitFor(t, "the page to be indexed", func() bool {
		_, getErr := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "locations/thornford")
		return getErr == nil
	})

	waitFor(t, "a report about it", func() bool { return len(reports) > 0 })

	for _, report := range drain(reports) {
		for _, indexed := range report.Indexed {
			for _, prefix := range []string{"_history", "_attachments", ".obsidian"} {
				if strings.HasPrefix(indexed, prefix) {
					t.Errorf("the watcher indexed %q, which is inside %s", indexed, prefix)
				}
			}
			if strings.Contains(indexed, "wiki-tmp") {
				t.Errorf("the watcher indexed a temporary file: %q", indexed)
			}
		}
	}

	// And nothing that is not a page ended up in the index at all.
	pages, err := syncer.Store().ListPages(ctx, syncer.Campaign().ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	for _, page := range pages {
		if !strings.HasSuffix(page.Path, ".md") {
			continue
		}
		for _, prefix := range []string{"_history", "_attachments", ".obsidian"} {
			if strings.HasPrefix(page.Path, prefix) {
				t.Errorf("the index holds %q, which is inside %s", page.Path, prefix)
			}
		}
	}
}

func TestWatchStopsWhenTheContextDoes(t *testing.T) {
	t.Parallel()

	_, syncer, _ := newSyncer(t)

	watcher, err := index.Watch(syncer, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	watchCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- watcher.Run(watchCtx, nil, nil) }()

	stop()

	select {
	case runErr := <-done:
		if runErr != nil && !isCancelled(runErr) {
			t.Errorf("Run returned %v, want the context's cancellation", runErr)
		}
	case <-time.After(5 * time.Second):
		t.Error("Run did not return after its context was cancelled")
	}
}

func TestWatchDefaultsTheDebounceAndRefusesNoVault(t *testing.T) {
	t.Parallel()

	_, syncer, _ := newSyncer(t)

	watcher, err := index.Watch(syncer, 0)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	if watcher.Debounce() != index.DefaultDebounce {
		t.Errorf("the debounce is %v, want the default %v", watcher.Debounce(), index.DefaultDebounce)
	}

	if _, err := index.Watch(nil, 0); err == nil {
		t.Error("Watch accepted a nil syncer")
	}
}

// TestWatchDoesNotTouchTheVault is the same property the sync has, in the
// watcher: a debounced sync is still a sync, and a sync never writes a file.
func TestWatchDoesNotTouchTheVault(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	watcher, err := index.Watch(syncer, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = watcher.Run(watchCtx, nil, nil) }()

	before := vaultFingerprint(t, root)

	writeVaultFile(t, vaultDir, "locations/thornford.md", "---\ntitle: Thornford\n---\n\nThe other town.\n")

	waitFor(t, "the index to hold the new page", func() bool {
		_, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "locations/thornford")
		return err == nil
	})

	// Every file is still the file the test or the watcher left it as, except
	// for the one the test just wrote.
	after := vaultFingerprint(t, root)
	for _, entry := range after {
		if strings.Contains(entry, "thornford") {
			continue
		}
		if !slices.Contains(before, entry) {
			t.Errorf("the watcher changed a file: %q", entry)
		}
	}
	if len(after) != len(before)+1 {
		t.Errorf("the vault has %d files, want %d", len(after), len(before)+1)
	}
}

// TestAWatcherAndASyncDoNotRace is the case the lock is for, exercised here:
// two writers, and the index ends up consistent rather than half-written.
func TestAWatcherAndASyncDoNotRace(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	watcher, err := index.Watch(syncer, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = watcher.Close() }()

	watchCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = watcher.Run(watchCtx, nil, nil) }()

	// Edits and syncs, interleaved, which is what a DM with an editor and a
	// terminal open does.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			page := "notes/note-" + string(rune('a'+i))
			writeVaultFile(t, vaultDir, page+".md", "---\ntitle: Note\n---\n\nA note.\n")
			if _, err := syncer.Sync(ctx); err != nil {
				t.Errorf("Sync: %v", err)
			}
		}(i)
	}
	wg.Wait()

	// Whatever the interleaving, the index ends up describing the files: every
	// page is there, and the check says in step.
	waitFor(t, "the index to settle", func() bool {
		report, err := syncer.Check(ctx)
		return err == nil && report.InStep()
	})
}

// helpers

func drain(reports chan index.Report) []index.Report {
	var all []index.Report
	for {
		select {
		case r := <-reports:
			all = append(all, r)
		default:
			return all
		}
	}
}

func isCancelled(err error) bool {
	return errors.Is(err, context.Canceled)
}
