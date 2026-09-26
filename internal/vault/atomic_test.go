package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This file is inside the package because the crash seam is an unexported
// field. That is the whole design of the test: a caller cannot ask for a write
// to die, so the only way to watch one die is from here.

func TestWriteIsAtomicUnderASimulatedCrash(t *testing.T) {
	t.Parallel()

	// The files a crash can leave behind, as the application writes them --
	// frontmatter and all, because a half-written page is not a page with less
	// prose in it.
	before := serialise(t, "A fortified town, before the flood.\n")
	after := serialise(t, "A fortified town, half under water.\n")

	tests := map[string]struct {
		crash crashPoint

		// wantContent is what the page's file must contain afterwards. For
		// every crash but the last it is the old content: a reader either sees
		// the whole old page or the whole new one, and never half of either.
		wantContent string

		// wantLeftOver is whether a temporary file is expected to remain. A
		// real death skips the cleanup, so it does.
		wantLeftOver bool
	}{
		"a death before anything is written leaves the old file": {
			crash:       crashBeforeTemp,
			wantContent: before,
		},
		"a death with the temporary file written leaves the old file": {
			crash:        crashAfterTemp,
			wantContent:  before,
			wantLeftOver: true,
		},
		"a death after the temporary file is synced leaves the old file": {
			crash:        crashAfterTempSync,
			wantContent:  before,
			wantLeftOver: true,
		},
		"a death after the rename leaves the new file, whole": {
			crash:       crashAfterRename,
			wantContent: after,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v := newTestVault(t)

			write(t, v, "A fortified town, before the flood.\n")
			original := read(t, v)

			v.crash = tt.crash
			err := v.Write(testPage, document(t, "A fortified town, half under water.\n"))
			if !errors.Is(err, errCrash) {
				t.Fatalf("Write returned %v, want the simulated crash", err)
			}

			// Read goes through the vault, and an unmodified document hands
			// back exactly the file's bytes, so this is still a check on what
			// is on disk rather than on what the parser made of it.
			got := read(t, v)
			if got != tt.wantContent {
				t.Errorf("the file holds %q, want %q", got, tt.wantContent)
			}
			if tt.wantContent == before && got != original {
				t.Error("the file was changed by a crash that should not have changed it")
			}

			// A crashed write leaves a temporary file, and opening the vault
			// again sweeps it. Opening is the only moment that cannot race: a
			// temporary file found then belongs to a process that is no longer
			// running, because one data directory has one server (ADR 0011).
			// Sweeping at the start of every write instead would delete a live
			// write's temporary file out from under it.
			if leftovers := temporaries(t, v.Root()); (len(leftovers) > 0) != tt.wantLeftOver {
				t.Errorf("temporary files left behind: %v, want leftovers = %t", leftovers, tt.wantLeftOver)
			}

			v.crash = ""
			if writeErr := v.Write(testPage, document(t, "A fortified town, half under water.\n")); writeErr != nil {
				t.Fatalf("the write after the crash: %v", writeErr)
			}
			if got := read(t, v); got != after {
				t.Errorf("after a recovery write the file holds %q, want the new page", got)
			}

			// And opening the vault is what clears them, so a vault that is
			// opened after a crash has nothing left over.
			if closeErr := v.Close(); closeErr != nil {
				t.Fatalf("Close: %v", closeErr)
			}
			reopened, reopenErr := Open(v.Root())
			if reopenErr != nil {
				t.Fatalf("reopening the vault: %v", reopenErr)
			}
			defer func() { _ = reopened.Close() }()
			if leftovers := temporaries(t, v.Root()); len(leftovers) != 0 {
				t.Errorf("temporary files after reopening: %v", leftovers)
			}
		})
	}
}

// TestAFailedWriteLeavesNoTemporaryFile is the other half: an ordinary failure
// is not a crash, and it tidies up after itself.
func TestAFailedWriteLeavesNoTemporaryFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// A file where a directory has to be, so the write cannot proceed.
	if err := os.WriteFile(filepath.Join(root, "locations"), []byte("in the way\n"), 0o600); err != nil {
		t.Fatalf("writing the file in the way: %v", err)
	}

	v := newTestVaultIn(t, root)

	err := v.Write("locations/rivergate", document(t, "A fortified town.\n"))
	if err == nil {
		t.Fatal("Write succeeded where a directory should have been")
	}

	// The message is not asserted on, and that is the fix for a failure this
	// test had on Windows. Which syscall notices first is the operating
	// system's business: Linux reports the file as not a directory from
	// EvalSymlinks, Windows reports it as a file that already exists from
	// MkdirAll, and both refusals are correct. What the test is about is that
	// the write failed and that failing left nothing behind, and the second
	// half is the part a reader cannot verify by reading the first.
	if leftovers := temporaries(t, root); len(leftovers) != 0 {
		t.Errorf("a failed write left temporary files: %v", leftovers)
	}
}

// TestWriteIsAtomicForAReader: the property a reader depends on, checked by
// reading while writes are happening. Every read has to be one of the two
// complete versions of the page, and no write may report a failure.
func TestWriteIsAtomicForAReader(t *testing.T) {
	t.Parallel()

	before := serialise(t, "A fortified town, before the flood.\n")
	after := serialise(t, "A fortified town, half under water.\n")

	v := newTestVault(t)
	write(t, v, "A fortified town, before the flood.\n")

	const (
		writers         = 4
		writesPerWriter = 10
		reads           = 60
	)

	var wg sync.WaitGroup
	failures := make(chan string, writers)

	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range writesPerWriter {
				for _, body := range []string{
					"A fortified town, half under water.\n",
					"A fortified town, before the flood.\n",
				} {
					if err := v.Write(testPage, document(t, body)); err != nil {
						failures <- "writing while a reader reads: " + err.Error()
						return
					}
				}
			}
		}()
	}

	for range reads {
		switch got := read(t, v); got {
		case before, after:
		default:
			t.Fatalf("a reader saw %q, which is neither version of the page", got)
		}
	}

	wg.Wait()
	close(failures)
	for failure := range failures {
		t.Error(failure)
	}
}

func TestSyncDirIsNotAnErrorOnThisPlatform(t *testing.T) {
	t.Parallel()

	// The write path depends on this not failing, and on Windows it is a
	// no-op rather than an error, so the assertion is that a plain write works
	// at all. A test that asserted the syscall succeeded would fail on a
	// platform where it cannot succeed.
	v := newTestVault(t)
	write(t, v, "A fortified town.\n")

	if err := v.syncDir("."); err != nil {
		t.Errorf("syncDir: %v", err)
	}
	if err := v.syncDir("locations"); err != nil {
		t.Errorf("syncDir on a subdirectory: %v", err)
	}
}

func TestWriteSetsTheMode(t *testing.T) {
	t.Parallel()

	if os.PathSeparator == '\\' {
		t.Skip("Windows has no POSIX mode to check")
	}

	v := newTestVault(t)
	write(t, v, "A fortified town.\n")

	info, err := v.root.Stat(testPage + ".md")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("the page is mode %o, want 600: a campaign is not a world-readable document", mode)
	}
}

// helpers

func newTestVault(t *testing.T) *Vault {
	t.Helper()

	return newTestVaultIn(t, t.TempDir())
}

func newTestVaultIn(t *testing.T, root string) *Vault {
	t.Helper()

	v, err := Open(root)
	if err != nil {
		t.Fatalf("Open(%q): %v", root, err)
	}
	return v
}

// testPage is the one page these tests write, so the helpers below do not take
// a path they would only ever be given this one.
const testPage = "locations/rivergate"

// read returns a page's bytes as they are on disk. An unmodified document hands
// back exactly what was read, so this is a byte-level check on the file.
func read(t *testing.T, v *Vault) string {
	t.Helper()

	doc, err := v.Read(testPage)
	if err != nil {
		t.Fatalf("Read(%q): %v", testPage, err)
	}
	data, err := doc.Bytes()
	if err != nil {
		t.Fatalf("Bytes(%q): %v", testPage, err)
	}
	return string(data)
}

func write(t *testing.T, v *Vault, body string) {
	t.Helper()

	if err := v.Write(testPage, document(t, body)); err != nil {
		t.Fatalf("Write(%q): %v", testPage, err)
	}
}

// document builds a page with a title, so a write has frontmatter to serialise
// rather than a file that is one long line.
// serialise is what the application writes for a body, so a test can compare
// a file against a version of the page rather than against a string it wrote by
// hand.
func serialise(t *testing.T, body string) string {
	t.Helper()

	data, err := document(t, body).Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return string(data)
}

func document(t *testing.T, body string) *Document {
	t.Helper()

	doc, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse(%q): %v", body, err)
	}
	if err := doc.Set(KeyTitle, "Rivergate"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	return doc
}

func temporaries(t *testing.T, root string) []string {
	t.Helper()

	var found []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), tempPrefix) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("looking for temporary files: %v", err)
	}
	return found
}
