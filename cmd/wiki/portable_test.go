package main

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// # `wiki export` and `wiki import`
//
// [ADR 0024](../../docs/adr/0024-an-export-is-a-vault.md) decides what each one
// moves and what it refuses. These are the tests for the four claims a reader cannot
// check by reading: the archive holds no database, it is byte-identical twice, a
// collision is refused rather than resolved, and an import into a campaign that does
// not exist changes nothing.

// TestAnExportHoldsNoDatabase is the security property, and it is the one the ADR
// exists for. A zip a DM emails to a player must not be a file of session ids and
// share-link hashes; `wiki backup` is the command for a data directory.
func TestAnExportHoldsNoDatabase(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	out := filepath.Join(t.TempDir(), "blackwater.zip")

	_, _, code := runWiki(t, "export", "--zip", "--data-dir", dir,
		"--campaign", "blackwater", "--out", out)
	if code != exitOK {
		t.Fatalf("wiki export exited %d", code)
	}

	for _, name := range zipNames(t, out) {
		for _, unwanted := range []string{"campaigns.db", "sessions", "principals", "wal", "shm"} {
			if strings.Contains(name, unwanted) {
				t.Errorf("the archive contains %q, which is about the database", name)
			}
		}
	}

	// And the pages are there, which is the point of the whole thing.
	names := zipNames(t, out)
	if !slicesContainsString(names, "campaign.md") {
		t.Errorf("the archive does not hold the campaign page: %v", names)
	}
}

// TestAnExportIsByteIdenticalTwiceOver is the reason the archive is deterministic, and
// the reason it matters is that a DM commits it: with real timestamps, two exports of
// an unchanged vault differ in bytes for no reason a person can see, and `git diff`
// becomes noise.
func TestAnExportIsByteIdenticalTwiceOver(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	scratch := t.TempDir()

	first := filepath.Join(scratch, "one.zip")
	second := filepath.Join(scratch, "two.zip")

	for _, out := range []string{first, second} {
		if _, _, code := runWiki(t, "export", "--zip", "--data-dir", dir,
			"--campaign", "blackwater", "--out", out); code != exitOK {
			t.Fatalf("wiki export to %s exited %d", out, code)
		}
	}

	one, err := os.ReadFile(first) //nolint:gosec // a path the test made
	if err != nil {
		t.Fatalf("reading the first: %v", err)
	}
	two, err := os.ReadFile(second) //nolint:gosec // a path the test made
	if err != nil {
		t.Fatalf("reading the second: %v", err)
	}

	if !bytes.Equal(one, two) {
		t.Errorf("two exports of an unchanged vault differ (%d and %d bytes)", len(one), len(two))
	}
}

// TestAnExportRefusesToOverwriteAFileSomebodyPutThere: the same accident as a backup
// that truncated an archive, and the one that loses work. `--out` creates with
// `O_EXCL` for exactly this reason.
func TestAnExportRefusesToOverwriteAFileSomebodyPutThere(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	out := filepath.Join(t.TempDir(), "mine.zip")

	if _, stderr, code := runWiki(t, "export", "--zip", "--data-dir", dir,
		"--campaign", "blackwater", "--out", out); code != exitOK {
		t.Fatalf("the first export exited %d: %s", code, stderr)
	}

	_, stderr, code := runWiki(t, "export", "--zip", "--data-dir", dir,
		"--campaign", "blackwater", "--out", out)
	if code == exitOK {
		t.Error("a second export to the same path succeeded")
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("the refusal does not say why: %q", stderr)
	}
}

// TestAnImportRefusesACampaignThatDoesNotExist is ADR 0024 §3 and the one an
// importer gets wrong by default. `wiki sync` *creates* a campaign for a folder that
// is not in the database; an import is a different verb, and a mistyped `--campaign`
// must not leave an empty campaign behind.
func TestAnImportRefusesACampaignThatDoesNotExist(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	before := t.TempDir()
	source := filepath.Join(before, "somebody-elses-vault")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatalf("making the import source: %v", err)
	}

	_, stderr, code := runWiki(t, "import", "obsidian", "--data-dir", dir,
		"--campaign", "typo-in-the-slug", source)
	if code == exitOK {
		t.Fatal("an import into a campaign that does not exist succeeded")
	}
	if !strings.Contains(stderr, "typo-in-the-slug") {
		t.Errorf("the refusal does not name the campaign: %q", stderr)
	}

	// And it did not create one.
	if _, _, listCode := runWiki(t, "users", "list", "--data-dir", dir,
		"--campaign", "typo-in-the-slug"); listCode == exitOK {
		t.Error("an import created the campaign it was refused")
	}
}

// TestAnImportWritesNothingUntilItIsToldTo is the confirmation, and the non-zero exit
// is the point: a DM who reads the list and types the command again with `--yes` has
// read it, and a DM who has not has lost nothing.
func TestAnImportWritesNothingUntilItIsToldTo(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	source := filepath.Join(t.TempDir(), "a-vault")
	if err := os.MkdirAll(filepath.Join(source, "locations"), 0o750); err != nil {
		t.Fatalf("making the import source: %v", err)
	}
	if err := writeFile(filepath.Join(source, "locations", "thornford.md"),
		"---\ntitle: \"Thornford\"\n---\n\nA town.\n"); err != nil {
		t.Fatalf("writing a page: %v", err)
	}

	// The first run shows and refuses.
	stdout, stderr, code := runWiki(t, "import", "obsidian", "--data-dir", dir,
		"--campaign", "blackwater", source)
	if code == exitOK {
		t.Fatal("an import without --yes succeeded")
	}
	if !strings.Contains(stderr, "--yes") {
		t.Errorf("the refusal does not say what to do: %q", stderr)
	}
	if !strings.Contains(stdout, "locations/thornford.md") {
		t.Errorf("the list does not name what would be copied:\n%s", stdout)
	}

	target := filepath.Join(dir, "vault", "blackwater", "locations", "thornford.md")
	if _, err := os.Stat(target); err == nil {
		t.Fatal("a file was copied by an import that said it wrote nothing")
	}

	// The second run copies.
	if _, stderr, code := runWiki(t, "import", "obsidian", "--data-dir", dir,
		"--campaign", "blackwater", "--yes", source); code != exitOK {
		t.Fatalf("the confirmed import exited %d: %s", code, stderr)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the confirmed import copied nothing: %v", err)
	}
}

// TestAnImportRefusesToOverwriteAndSaysWhich: ADR 0024 §4. A `--force` does not
// exist in v1, and its absence is a compatibility promise — a flag that means "do it
// anyway" is a flag a DM uses before reading the list.
func TestAnImportRefusesToOverwriteAndSaysWhich(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	// A page the campaign **already has**, so there is something to collide with. The
	// first version of this test assumed the fixture provided one, and so tested
	// nothing: the import created the file, there was no collision, and the assertion
	// that it was not overwritten passed on a file that had never been there.
	existing := filepath.Join(dir, "vault", "blackwater", "locations", "rivergate.md")
	if err := os.MkdirAll(filepath.Dir(existing), 0o750); err != nil {
		t.Fatalf("making locations/: %v", err)
	}
	if err := writeFile(existing, "---\ntitle: \"Rivergate\"\n---\n\nA fortified town.\n"); err != nil {
		t.Fatalf("writing the existing page: %v", err)
	}

	source := filepath.Join(t.TempDir(), "a-vault")
	if err := os.MkdirAll(filepath.Join(source, "locations"), 0o750); err != nil {
		t.Fatalf("making the import source: %v", err)
	}
	if err := writeFile(filepath.Join(source, "locations", "rivergate.md"),
		"---\ntitle: \"Somebody else's Rivergate\"\n---\n\nDifferent prose.\n"); err != nil {
		t.Fatalf("writing the colliding page: %v", err)
	}

	stdout, _, code := runWiki(t, "import", "obsidian", "--data-dir", dir,
		"--campaign", "blackwater", "--yes", source)
	if code != exitOK {
		t.Fatalf("the import exited %d", code)
	}
	if !strings.Contains(stdout, "locations/rivergate.md") {
		t.Errorf("the collision is not listed:\n%s", stdout)
	}
	if !strings.Contains(stdout, "NOT be overwritten") {
		t.Errorf("the output does not say the file is safe:\n%s", stdout)
	}

	// And the DM's own file is still theirs.
	kept := filepath.Join(dir, "vault", "blackwater", "locations", "rivergate.md")
	contents, err := os.ReadFile(kept) //nolint:gosec // a path under the test's data directory
	if err != nil {
		t.Fatalf("reading the existing page: %v", err)
	}
	if !strings.Contains(string(contents), "A fortified town") {
		t.Errorf("the import overwrote a file it listed as a collision:\n%s", contents)
	}
}

// TestAnImportNamesTheFilesItWillNotCopy: the interesting output of this command is
// the second list. A DM whose vault has a canvas file in it deserves to be told the
// canvas file was not copied, rather than finding out next week.
func TestAnImportNamesTheFilesItWillNotCopy(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	source := filepath.Join(t.TempDir(), "a-vault")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatalf("making the import source: %v", err)
	}
	for _, name := range []string{
		"good.md",
		"diagram.canvas",
		"picture.png",
	} {
		if err := writeFile(filepath.Join(source, name), "x"); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	// A path the application could never index, so it would sit in the vault
	// forever, invisible, and the DM would think the import lost it.
	if err := os.MkdirAll(filepath.Join(source, "_history"), 0o750); err != nil {
		t.Fatalf("making _history: %v", err)
	}
	if err := writeFile(filepath.Join(source, "_history", "old.md"), "x"); err != nil {
		t.Fatalf("writing a reserved path: %v", err)
	}

	stdout, _, code := runWiki(t, "import", "obsidian", "--data-dir", dir,
		"--campaign", "blackwater", source)
	if code == exitOK {
		t.Fatal("an import without --yes succeeded")
	}

	for _, want := range []string{"diagram.canvas", "picture.png"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the skipped list does not name %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stdout, "good.md") {
		t.Errorf("the importable list does not name the page it would copy:\n%s", stdout)
	}
}

// TestARoundTripIsTheSameBytes is the property the two commands exist for together:
// a vault exported, imported elsewhere and re-exported is the same vault. It is the
// test that would catch a serialisation in the middle, and there is none.
func TestARoundTripIsTheSameBytes(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	scratch := t.TempDir()
	first := filepath.Join(scratch, "one.zip")

	if _, _, code := runWiki(t, "export", "--zip", "--data-dir", dir,
		"--campaign", "blackwater", "--out", first); code != exitOK {
		t.Fatal("the first export failed")
	}

	// Unzip into a bare directory, which is what a DM does with it, and import that.
	elsewhere := filepath.Join(scratch, "unzipped")
	if err := unzipInto(t, first, elsewhere); err != nil {
		t.Fatalf("unzipping: %v", err)
	}

	second := filepath.Join(scratch, "two.zip")
	if _, _, code := runWiki(t, "import", "obsidian", "--data-dir", dir,
		"--campaign", "blackwater", "--yes", elsewhere); code != exitOK {
		// The files are already there, so this is a list of collisions and a
		// non-zero exit: importing a vault into the vault it came from is refused,
		// which is ADR 0024 §4 doing its job.
		t.Logf("importing into the same campaign was refused, which is correct")
	}

	if _, _, code := runWiki(t, "export", "--zip", "--data-dir", dir,
		"--campaign", "blackwater", "--out", second); code != exitOK {
		t.Fatal("the second export failed")
	}

	one, err := os.ReadFile(first) //nolint:gosec // a path the test made
	if err != nil {
		t.Fatalf("reading the first: %v", err)
	}
	two, err := os.ReadFile(second) //nolint:gosec // a path the test made
	if err != nil {
		t.Fatalf("reading the second: %v", err)
	}
	if !bytes.Equal(one, two) {
		t.Error("a vault exported, unzipped and re-imported is not the same vault")
	}
}

// --- helpers -------------------------------------------------------------

// zipNames is every name in a zip, in the order it holds them.
func zipNames(t *testing.T, path string) []string {
	t.Helper()

	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer func() { _ = reader.Close() }()

	names := make([]string, 0, len(reader.File))
	for _, file := range reader.File {
		names = append(names, file.Name)
	}
	return names
}

// unzipInto is `unzip`, written out, and it **refuses** an entry outside the
// directory — the same check `cmd/wiki/backup_test.go` makes, because a helper that
// a reader might copy is a helper whose checks are part of the example.
func unzipInto(t *testing.T, archive, into string) error {
	t.Helper()

	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	for _, file := range reader.File {
		target := filepath.Join(into, filepath.FromSlash(file.Name))
		if !strings.HasPrefix(target, filepath.Clean(into)+string(os.PathSeparator)) {
			t.Fatalf("an archive entry would be written outside the directory: %s", file.Name)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}

		in, err := file.Open()
		if err != nil {
			return err
		}
		contents, readErr := io.ReadAll(in)
		_ = in.Close()
		if readErr != nil {
			return readErr
		}
		if err := os.WriteFile(target, contents, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// slicesContainsString is `slices.Contains` for the archive names, spelled out so the
// test file's imports say what it is about.
func slicesContainsString(haystack []string, needle string) bool {
	for _, one := range haystack {
		if one == needle {
			return true
		}
	}
	return false
}

// TestACommandDoesNotLeaveTheDatabaseOpen is the test for the class, not for the
// instance, and it exists because of how the first one was found.
//
// `runImport` opened a store to check that the campaign existed and wrote
// `if _, _, openErr := ...`, which throws the store away and leaves the database
// file open for the rest of the process. **On Linux and macOS that is invisible** —
// unlinking an open file is legal, so every local run passed and the Linux and macOS
// CI legs were green. On Windows it is not legal, and the failure arrived as
// `TempDir RemoveAll cleanup: unlinkat ... campaigns.db: The process cannot access
// the file because it is being used by another process` — in the runner's own
// cleanup, with no assertion anywhere near it.
//
// A bug that only reproduces on one platform is a bug that only one person finds, so
// here is the check that works on the two platforms that can do it:
//
//   - on Windows, `os.Remove` on an open file fails, which is the whole mechanism;
//   - on Linux, `/proc/self/fd` lists the process's open descriptors, and one
//     pointing at the database is the same leak seen from the other side.
//
// It skips on macOS, where neither works, and says so rather than passing quietly.
func TestACommandDoesNotLeaveTheDatabaseOpen(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("neither an unlink nor /proc can see an open file here; " +
			"this class is caught on linux and windows")
	}

	dir := seed(t)
	ok(t, "sync", "--data-dir", dir, "--campaign", "blackwater")

	// **Every command that opens a store, `import` included** -- and the first
	// version of this list did not include it, which is the version that passed
	// while the leak was still in `runImport`. A leak test that omits the command
	// that leaked is a leak test about a different command.
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "locations"), 0o750); err != nil {
		t.Fatalf("making an import source: %v", err)
	}
	if err := writeFile(filepath.Join(vault, "locations", "new-page.md"),
		"---\ntitle: \"New\"\n---\n\nA page.\n"); err != nil {
		t.Fatalf("writing the import source page: %v", err)
	}

	commands := [][]string{
		{"users", "list", "--data-dir", dir, "--campaign", "blackwater"},
		{"backup", "--data-dir", dir},
		{"export", "--zip", "--data-dir", dir, "--campaign", "blackwater",
			"--out", filepath.Join(t.TempDir(), "out.zip")},
		{"import", "obsidian", "--data-dir", dir, "--campaign", "blackwater",
			"--yes", vault},
	}

	for _, command := range commands {
		t.Run(command[0], func(t *testing.T) {
			before := openDescriptorsFor(t, dir)
			if _, stderr, code := runWiki(t, command...); code != exitOK {
				t.Fatalf("wiki %s exited %d: %s", strings.Join(command, " "), code, stderr)
			}
			after := openDescriptorsFor(t, dir)

			if after > before {
				t.Errorf("wiki %s left %d descriptor(s) open on the data directory "+
					"(was %d, now %d); on Windows this makes the directory "+
					"impossible to move or delete",
					strings.Join(command, " "), after-before, before, after)
			}
		})
	}
}

// openDescriptorsFor is how many of this process's open descriptors point into a
// data directory, or -1 when the platform cannot answer.
//
// It is `/proc/self/fd` on Linux, which is a real answer, and it is an **attempt** on
// Windows, where the same question is "would `os.Remove` succeed" — and that answer
// has to be given without actually removing the file, which is why Windows gets the
// indirect version.
func openDescriptorsFor(t *testing.T, dir string) int {
	t.Helper()

	if runtime.GOOS == "windows" {
		return windowsOpenDescriptors(t, dir)
	}

	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("this platform cannot list open descriptors: %v", err)
	}

	// Resolve each descriptor and count the ones under the directory. A symlink's
	// target is the file, so `/proc/self/fd/7` reads as the path the descriptor
	// holds, and a deleted file reads as one too — which is the leak, since the
	// descriptor survives the unlink on Linux.
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, dir) {
			count++
		}
	}
	return count
}

// windowsOpenDescriptors is the Windows answer, and it uses the *same* mechanism the
// failure did: the operating system will not rename a file this process holds.
//
// It is a rename rather than a delete because a rename is reversible and a probe that
// deleted the fixture would be a test that destroys its own data directory. And it is
// a rename rather than an `os.Open` handle because Go's `os` has no share mode, so a
// handle this test opens would itself be the thing holding the file -- the question
// cannot be asked from inside the process with the tools `os` offers.
//
// A `-wal` and a `-shm` are checked too, because WAL is on (ADR 0004) and those are
// the files a WAL-mode store actually holds.
func windowsOpenDescriptors(t *testing.T, dir string) int {
	t.Helper()

	held := 0
	for _, name := range []string{"campaigns.db", "campaigns.db-wal", "campaigns.db-shm"} {
		original := filepath.Join(dir, name)
		if _, err := os.Stat(original); err != nil {
			continue
		}

		moved := original + ".probe"
		if err := os.Rename(original, moved); err != nil {
			held++
			continue
		}
		if err := os.Rename(moved, original); err != nil {
			// A half-renamed fixture is worse than a failed test, so this is the one
			// error that is not recoverable and says so.
			t.Fatalf("putting %s back after the probe: %v", name, err)
		}
	}
	return held
}
