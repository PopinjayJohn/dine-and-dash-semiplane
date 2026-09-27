package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// # `wiki backup`, and the test three documents name
//
// `TestBackupsRestoreIdenticalIndex` is required by `docs/spec.md` §14, by
// `docs/security.md` ("A vault backup plus `reindex --full` rebuilds a byte-identical
// index") and by [ADR 0012] ("`wiki reindex --full` and
// `TestBackupsRestoreIdenticalIndex` are this project's tools for that"). It has not
// existed, because there was no backup to take.
//
// [ADR 0012]: ../../docs/adr/0012-migration-runner-in-repo.md
//
// The property it names is worth being precise about, because the obvious version of
// it is not true and a test for the obvious version would be a test for a fiction:
//
//   - **The index is not identical, and must not be.** It is a projection and its
//     rows carry `updated_at` and a `content_hash` that depend on when the sync ran.
//     A test asserting the two databases match byte for byte would fail for a
//     correct backup and pass for one that copied nothing.
//   - **The *files* are identical**, because they are the source of truth
//     (ADR 0001), and that is the property worth asserting: a backup of a data
//     directory restores a campaign, and everything the index says can be rebuilt
//     from the vault with `wiki reindex --full`.
//   - **And the rebuild from the restored vault reaches the same pages**, which is
//     the part that would actually catch a backup that dropped a file.

// TestBackupsRestoreIdenticalIndex is the named test, and the assertion is the one
// that is true: a backup, restored into a fresh data directory, has **the same
// files**, and `wiki reindex --full` over those files reaches the same set of page
// paths and the same content hashes.
func TestBackupsRestoreIdenticalIndex(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	// A page with content, so the rebuild has something to hash.
	if err := writeFile(filepath.Join(dir, "vault", "blackwater", "rivergate.md"),
		"---\ntitle: \"Rivergate\"\ntype: location\nvisibility: players\n---\n\nA fortified town.\n"); err != nil {
		t.Fatalf("writing a page: %v", err)
	}
	ok(t, "sync", "--data-dir", dir, "--campaign", "blackwater")

	before := pagesIn(t, dir, "blackwater")

	// The backup.
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := writeBackup(t.Context(), dir, archive); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}

	// The restore: `tar -xzf` into an empty directory, which is the whole of
	// ADR 0011's "a restore is untarring it somewhere else".
	restored := t.TempDir()
	if err := extractArchive(t, archive, restored); err != nil {
		t.Fatalf("extracting %s: %v", archive, err)
	}

	// **The files**, which is the property that is true and the one that matters.
	if got, want := vaultFiles(t, restored), vaultFiles(t, dir); !equal(got, want) {
		t.Fatalf("the restored vault has %v, want %v", got, want)
	}

	// **The index rebuilt from the restored files.** A backup whose database was
	// unusable would still pass the file comparison, so this is the half that says
	// the *whole* directory was worth archiving.
	ok(t, "reindex", "--data-dir", restored, "--full", "--campaign", "blackwater")
	after := pagesIn(t, restored, "blackwater")

	if len(after) != len(before) {
		t.Fatalf("the restored index has %d pages, want %d: %v", len(after), len(before), after)
	}
	for path, hash := range before {
		if after[path] != hash {
			t.Errorf("%s has content hash %q after a restore, want %q",
				path, after[path], hash)
		}
	}
}

// TestTheArchiveIsAPlainTarball is ADR 0011's sentence about a restore not
// depending on this binary, and it is a test because the alternative — a format
// only this program can read — is a format a DM needs *this program working* to
// recover their campaign from.
func TestTheArchiveIsAPlainTarball(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := writeBackup(t.Context(), dir, archive); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}

	names := archiveNames(t, archive)

	for _, want := range []string{"campaigns.db", "vault/blackwater/campaign.md"} {
		if !slicesContains(names, want) {
			t.Errorf("the archive does not contain %q: %v", want, names)
		}
	}

	// The two directories that are about *this process* rather than the campaign, and
	// that restoring them would actively harm: a lock file is a two-minute stale wait
	// and the archives are older copies of the same thing growing without bound.
	for _, unwanted := range []string{"backups/", "locks/"} {
		for _, name := range names {
			if strings.HasPrefix(name, unwanted) {
				t.Errorf("the archive contains %q, which is about the process and not the campaign", name)
			}
		}
	}
}

// TestTheDatabaseInsideTheArchiveIsAWholeDatabase is the half that makes the
// database half worth having: a `cp` of a WAL database gives you a file whose
// siblings were not copied, and the archive has to contain a database that opens on
// its own.
func TestTheDatabaseInsideTheArchiveIsAWholeDatabase(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if _, err := writeBackup(t.Context(), dir, archive); err != nil {
		t.Fatalf("writeBackup: %v", err)
	}

	restored := t.TempDir()
	if err := extractArchive(t, archive, restored); err != nil {
		t.Fatalf("extracting: %v", err)
	}

	// Opened with nothing else: no migrations, no store, no campaign. If the archive
	// holds half a database this is where it shows.
	db, err := sql.Open("sqlite", filepath.Join(restored, "campaigns.db"))
	if err != nil {
		t.Fatalf("opening the restored database: %v", err)
	}
	defer func() { _ = db.Close() }()

	var version int
	if err := db.QueryRowContext(t.Context(),
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatalf("the restored database has no schema: %v", err)
	}
	if version == 0 {
		t.Error("the restored database is at version 0, so it was copied before it was migrated")
	}

	// And the vault tables are there, which is what "a backup of a campaign" means.
	var pages int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pages").Scan(&pages); err != nil {
		t.Fatalf("the restored database has no pages table: %v", err)
	}
	if pages == 0 {
		t.Error("the restored database has no pages, so the campaign was not in it")
	}
}

// TestPruneKeepsTheMostRecentAndNothingElse: a retention policy that keeps the
// wrong end is worse than none, because a DM who runs it believes the disk is under
// control.
func TestPruneKeepsTheMostRecentAndNothingElse(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatalf("making backups/: %v", err)
	}

	// Ten archives with timestamps a minute apart, oldest first.
	for n := range 10 {
		name := fmt.Sprintf("%s2006010%d-0000-00-00%s", archivePrefix, n, archiveSuffix)
		if err := writeFile(filepath.Join(backups, name), "x"); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	// And something that is not an archive, which pruning must not touch: a DM's own
	// notes beside the archives are not the backup tool's business.
	if err := writeFile(filepath.Join(backups, "notes.txt"), "keep me"); err != nil {
		t.Fatalf("writing notes.txt: %v", err)
	}

	removed, err := pruneBackups(dir, 7)
	if err != nil {
		t.Fatalf("pruneBackups: %v", err)
	}
	if removed != 3 {
		t.Errorf("pruneBackups removed %d, want 3 (ten archives, keeping seven)", removed)
	}

	// The listing is of everything in the directory, and `notes.txt` is in it, so the
	// assertion filters — and a test that asserted `len(remaining) == 7` would have
	// been a test about a file the backup tool correctly ignores.
	var remaining []string
	for _, name := range archiveNamesIn(t, backups) {
		if strings.HasPrefix(name, archivePrefix) {
			remaining = append(remaining, name)
		}
	}
	if len(remaining) != 7 {
		t.Errorf("%d archives remain, want 7: %v", len(remaining), remaining)
	}
	// The three *oldest* are the ones that go, which is the direction that matters:
	// a campaign from last month is worth less than one from yesterday.
	if strings.Contains(strings.Join(remaining, " "), "20060100-0000-00-00") {
		t.Errorf("pruning kept the oldest archive: %v", remaining)
	}
	if _, err := os.Stat(filepath.Join(backups, "notes.txt")); err != nil {
		t.Errorf("pruning removed a file that is not an archive: %v", err)
	}
}

// TestPruningNothingIsNotAnError: a data directory with no backups/ is a data
// directory nobody has backed up, and `--prune` on one is a no-op rather than a
// failure.
func TestPruningNothingIsNotAnError(t *testing.T) {
	t.Parallel()

	removed, err := pruneBackups(t.TempDir(), 7)
	if err != nil {
		t.Fatalf("pruneBackups on a directory with no backups: %v", err)
	}
	if removed != 0 {
		t.Errorf("pruneBackups removed %d, want 0", removed)
	}
}

// TestTwoBackupsInOneSecondDoNotOverwriteEachOther is the collision case, and a
// collision is a lost campaign: `wiki backup` twice inside one second is a DM in a
// script, and the second archive silently replacing the first is a thing they only
// find out when they need it.
func TestTwoBackupsInOneSecondDoNotOverwriteEachOther(t *testing.T) {
	t.Parallel()

	dir := seed(t)
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatalf("making backups/: %v", err)
	}

	first := filepath.Join(backups, "one.tar.gz")
	second := filepath.Join(backups, "two.tar.gz")

	if _, err := writeBackup(t.Context(), dir, first); err != nil {
		t.Fatalf("the first backup: %v", err)
	}

	// **The same name again is refused rather than truncated**, which is the whole
	// of the property: `O_EXCL` makes the reservation atomic, so a caller that lost
	// the race knows it lost instead of having quietly replaced a campaign.
	_, err := writeBackup(t.Context(), dir, first)
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("a second backup to the same name = %v, want os.ErrExist", err)
	}

	// And the first is still whole, which is the half that matters.
	if _, err := os.Stat(first); err != nil {
		t.Errorf("the first archive is gone: %v", err)
	}

	// A different name is fine, which is what the caller's retry loop relies on.
	if _, err := writeBackup(t.Context(), dir, second); err != nil {
		t.Errorf("a second backup to a new name: %v", err)
	}
}

// --- helpers -------------------------------------------------------------

// extractArchive is `tar -xzf`, written out.
//
// It is a helper rather than a shell command because a test that shells out is a test
// that does not run on Windows, and CI's matrix is three operating systems for
// exactly that reason.
func extractArchive(t *testing.T, archive, into string) error {
	t.Helper()

	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	gzipped, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gzipped.Close() }()

	reader := tar.NewReader(gzipped)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		// **Every path is checked.** An archive that could write outside the
		// directory it is extracted into is a zip-slip, and the only safe thing is
		// for the extractor to refuse; this test is not the place a bad one would be
		// found, but it is the place a *missing* check would be noticed, because the
		// helper is the code a reader would copy.
		target := filepath.Join(into, filepath.FromSlash(header.Name))
		if !strings.HasPrefix(target, filepath.Clean(into)+string(os.PathSeparator)) {
			return errEscapes
		}

		if header.Typeflag != tar.TypeReg {
			continue
		}

		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o700); mkdirErr != nil {
			return mkdirErr
		}
		contents, readErr := io.ReadAll(reader)
		if readErr != nil {
			return readErr
		}
		if writeErr := os.WriteFile(target, contents, 0o600); writeErr != nil {
			return writeErr
		}
	}
}

// errEscapes is this test's own refusal, for an archive entry outside the directory.
var errEscapes = &escapeError{}

type escapeError struct{}

func (*escapeError) Error() string { return "an archive entry would be written outside the directory" }

// archiveNames is every regular file in an archive, in the order tar holds them.
func archiveNames(t *testing.T, archive string) []string {
	t.Helper()

	contents, err := os.ReadFile(archive) //nolint:gosec // a path the test made
	if err != nil {
		t.Fatalf("reading %s: %v", archive, err)
	}

	gzipped, err := gzip.NewReader(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("%s is not a gzip stream: %v", archive, err)
	}
	defer func() { _ = gzipped.Close() }()

	var names []string
	reader := tar.NewReader(gzipped)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			t.Fatalf("reading %s: %v", archive, err)
		}
		if header.Typeflag == tar.TypeReg {
			names = append(names, header.Name)
		}
	}
}

// archiveNamesIn is the same, for a directory rather than an archive.
func archiveNamesIn(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

// vaultFiles is every file under a data directory's `vault/`, as sorted
// campaign-relative paths.
func vaultFiles(t *testing.T, dir string) []string {
	t.Helper()

	root := filepath.Join(dir, "vault")
	var names []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	sort.Strings(names)
	return names
}

// pagesIn is the paths and content hashes a data directory's index holds, which is
// what "the rebuild reached the same campaign" means.
func pagesIn(t *testing.T, dir, slug string) map[string]string {
	t.Helper()

	ok(t, "sync", "--data-dir", dir, "--campaign", slug)

	db, err := sql.Open("sqlite", filepath.Join(dir, "campaigns.db"))
	if err != nil {
		t.Fatalf("opening %s: %v", dir, err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.QueryContext(t.Context(), "SELECT path, content_hash FROM pages")
	if err != nil {
		t.Fatalf("reading pages: %v", err)
	}
	defer func() { _ = rows.Close() }()

	pages := make(map[string]string)
	for rows.Next() {
		var path, hash string
		if err := rows.Scan(&path, &hash); err != nil {
			t.Fatalf("scanning a page: %v", err)
		}
		pages[path] = hash
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading pages: %v", err)
	}
	return pages
}

// equal is string-slice equality, and it is a function so that the message says
// which side differs.
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// slicesContains is `slices.Contains`, spelled out because the test file is about
// the archive's shape and an import of `slices` for one call is noise.
func slicesContains(haystack []string, needle string) bool {
	for _, one := range haystack {
		if one == needle {
			return true
		}
	}
	return false
}
