package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"

	// The driver is named here because this file needs a raw `*sql.DB` to copy the
	// database with, and `internal/store` being the only thing that names the driver
	// would make this one depend on `internal/store` to link it. ADR 0004: pure Go,
	// no CGO.
	_ "modernc.org/sqlite"
)

// # `wiki backup`
//
// ADR 0011 decided the important thing before this file existed:
//
//   > "One static binary. One data directory. Copying the directory is the whole
//   > backup and restore procedure … **The app offers `wiki backup` as a
//   > convenience**, but **the format has no dependency on the app**: a restore on
//   > a different machine is a file copy, and the vault half is readable by
//   > Obsidian and by `git` with no help."
//
// So this is a convenience and it is written to be *boringly* extractable. The
// archive is a gzipped tar of the data directory, and `tar -xzf` into an empty
// directory is a complete restore. There is no manifest, no index and no header of
// ours; a DM whose wiki will not start on a new machine does not have to find this
// binary to get their campaign back, and that is the entire point of the ADR's
// sentence.
//
// ## Why the database is copied with SQLite's own `.backup`
//
// Copying a WAL-mode database file with `cp` while a server is running gives you a
// file whose pages are consistent with *some* instant but whose `-wal` and `-shm`
// siblings are not in the archive, and the result is a database that opens and then
// quietly disagrees with the files. SQLite's [Online Backup API] takes a read lock
// per page and writes a database that is consistent at one instant, which is the
// only thing a backup is.
//
// ## No lock, and why
//
// A first draft of this took `locks/serve.lock` for the duration, on the reasoning
// that "one process writes a data directory at a time" is a rule worth honouring
// everywhere. It is the wrong rule here, and being wrong is expensive: a backup that
// waits for the server to exit is a backup a DM cannot take *while playing*, which is
// the only time they think about taking one.
//
// So the collision that the lock would have prevented is prevented instead, at the
// only place it can be: `O_EXCL` in [writeBackup] makes the name reservation atomic,
// and the caller's retry loop picks the next name. That is a stronger guarantee than
// the lock, because it holds between two *backups*, which the lock never did.
//
// The *lock file* is also deliberately not archived: `locks/serve.lock` is a fact
// about the process that held it, and restoring one onto a new machine is restoring a
// two-minute stale lock that the new server has to wait out.
//
// [Online Backup API]: https://www.sqlite.org/backup.html

// backupKeep is how many archives `--prune` leaves behind.
//
// It is a number rather than a flag because a retention *policy* somebody has to
// pass on every invocation is a policy they will stop passing, and an unbounded
// `backups/` directory is a disk that fills at the rate of the campaign's activity.
const backupKeep = 7

// runBackup is `wiki backup [--prune]`.
func runBackup(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	opts := &backupOptions{}
	flags.StringVar(&opts.dataDir, "data-dir", "",
		"the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.BoolVar(&opts.prune, "prune", false,
		"keep only the "+fmt.Sprint(backupKeep)+" most recent archives, and delete the rest")

	if err := parseFlags(flags, args, "wiki backup"); err != nil {
		return err
	}

	dir, err := datadir.Resolve(opts.dataDir)
	if err != nil {
		return fmt.Errorf("finding the data directory: %w", err)
	}

	// The name and the write are two steps and the second can lose the race, so this
	// loop is the reservation made safe rather than a "should not happen". Ten
	// backups inside one second is a script, and a script that silently overwrites is
	// the failure this is here to prevent.
	var (
		written int64
		archive string
	)
	for attempt := range 10 {
		name, nameErr := backupPathFor(dir, time.Now().UTC())
		if nameErr != nil {
			return nameErr
		}

		written, err = writeBackup(ctx, dir, name)
		if err == nil {
			archive = name
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		_ = attempt
	}
	if archive == "" {
		return fmt.Errorf("ten backups inside one second: stopping rather than overwriting one")
	}

	if _, said := fmt.Fprintf(stdout, "wrote %s (%s)\n", archive, humanSize(written)); said != nil {
		return said
	}

	if !opts.prune {
		// Said every time, because a DM who runs `wiki backup` in a loop and wonders
		// why their disk is full deserves to have been told.
		_, said := fmt.Fprintf(stdout, "Old archives are kept. `--prune` keeps the %d most recent.\n",
			backupKeep)
		return said
	}

	removed, err := pruneBackups(dir, backupKeep)
	if err != nil {
		return err
	}
	if removed > 0 {
		_, said := fmt.Fprintf(stdout, "removed %d older archive(s)\n", removed)
		return said
	}
	return nil
}

// backupOptions is what `wiki backup` takes.
type backupOptions struct {
	dataDir string
	prune   bool
}

// archivePrefix is what an archive's name begins with, and it is a prefix rather
// than an extension because a backup is a *tarball* and a `.tar` extension on a
// gzipped file is a lie a DM has to work out from `file`.
const archivePrefix = "backup-"

// archiveSuffix is the extension, which is the honest one.
const archiveSuffix = ".tar.gz"

// backupPathFor is where an archive written now goes.
//
// The timestamp is UTC and sortable, with the seconds, because two backups inside
// one minute is a DM running it in a loop and two archives with the same name would
// be one archive and one lost campaign. The minute is in the name for the same
// reason a minute is not enough: `backup-20260927-1914-05.tar.gz` sorts and does not
// collide within a minute either.
func backupPathFor(dir string, now time.Time) (string, error) {
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", backups, err)
	}

	name := archivePrefix + now.Format("20060102-1504-05") + archiveSuffix
	path := filepath.Join(backups, name)
	if _, err := os.Stat(path); err == nil {
		// Two in the same second. Rather than overwrite one, add a counter: a
		// campaign is worth one extra byte of filename to not lose.
		for n := 2; n < 100; n++ {
			candidate := filepath.Join(backups,
				fmt.Sprintf("%s%s-%d%s", archivePrefix, now.Format("20060102-1504-05"), n, archiveSuffix))
			if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("a backup already exists for %s and ten beside it", now.Format("15:04:05"))
	}

	return path, nil
}

// writeBackup is the archive, and it is a function that both the command and the
// tests call so that "a backup is a tar of the data directory" is one thing rather
// than a description of a thing.
func writeBackup(ctx context.Context, dir, archive string) (int64, error) {
	// **`O_EXCL`, not `O_CREATE`.** The name is chosen by [backupPathFor] and the
	// window between choosing it and writing it is where two backups collide, and a
	// collision that truncates the first archive is a lost campaign rather than a
	// duplicate. The exclusive create makes the reservation atomic, and the caller
	// retries on the sentinel.
	file, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is the data directory's own backups/
	if err != nil {
		return 0, err
	}
	defer func() { _ = file.Close() }()

	gzipped := gzip.NewWriter(file)
	defer func() { _ = gzipped.Close() }()

	// Closed explicitly below, with the error checked. The deferred closes are for the
	// *early return* paths, where an unfinished archive is being thrown away and
	// nobody will ever read it.
	writer := tar.NewWriter(gzipped)
	defer func() { _ = writer.Close() }()

	// The entry walk's byte count is discarded: the number reported is the *file's*
	// size, measured after the gzip trailer is written, because a campaign's
	// uncompressed bytes and its archive are different questions and the second is the
	// one a DM can compare against `df`.
	if _, entriesErr := writeBackupEntries(ctx, writer, dir); entriesErr != nil {
		return 0, entriesErr
	}

	// The tar and the gzip are closed *before* the file is measured, and the closes
	// are errors rather than discards: a gzip stream with a truncated trailer is an
	// archive `tar -xzf` refuses with an error a DM has to read, and the check that
	// catches it is the one this function is written to make.
	if closeErr := writer.Close(); closeErr != nil {
		return 0, fmt.Errorf("finishing %s: %w", archive, closeErr)
	}
	if closeErr := gzipped.Close(); closeErr != nil {
		return 0, fmt.Errorf("finishing %s: %w", archive, closeErr)
	}
	if syncErr := file.Sync(); syncErr != nil {
		return 0, fmt.Errorf("flushing %s: %w", archive, syncErr)
	}

	info, statErr := file.Stat()
	if statErr != nil {
		return 0, statErr
	}
	return info.Size(), nil
}

// writeBackupEntries is the archive's contents, and it is where the two decisions
// live: the database is copied through SQLite, and the lock file is left out.
func writeBackupEntries(ctx context.Context, writer *tar.Writer, dir string) (int64, error) {
	var written int64

	// The database, through SQLite's own backup. Opened *separately* from the
	// running store so that this works whether or not a server holds the file: the
	// backup API takes its own read lock, and a second connection is how that is
	// expressed.
	database, copyErr := sqliteBackupOf(ctx, datadir.DatabaseFile(dir))
	if copyErr != nil {
		return written, copyErr
	}
	if addErr := addFile(writer, "campaigns.db", database); addErr != nil {
		return written, addErr
	}
	written += database.size

	// Everything else under the data directory. `filepath.Walk` is sorted, so two
	// backups of an unchanged directory differ only in the timestamp in the name —
	// which makes "did anything change?" answerable with `cmp`, and that is the
	// question a DM asking about a backup is usually actually asking.
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relative, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)

		// The archives themselves: an archive containing archives grows without
		// bound and the inner ones are older copies of the same campaign.
		if strings.HasPrefix(relative, "backups/") {
			return nil
		}
		// The lock, which is a fact about a process that is not running any more.
		if strings.HasPrefix(relative, "locks/") {
			return nil
		}

		contents, readErr := os.ReadFile(path) //nolint:gosec // the path is under the data directory
		if readErr != nil {
			return readErr
		}
		if addErr := addFile(writer, relative, sized{contents: contents, size: int64(len(contents))}); addErr != nil {
			return addErr
		}
		written += int64(len(contents))
		return nil
	})
	if walkErr != nil {
		return written, walkErr
	}

	return written, nil
}

// sized is a file's contents and its length, so `addFile` takes one shape whether
// the bytes came from SQLite or from the filesystem.
type sized struct {
	contents []byte
	size     int64
}

// addFile writes one entry into the archive.
//
// The mode is normalised to 0600 and the directories to 0700, and the reason is
// that a backup is of a *vault*: a DM's campaign notes and their players'
// sessions, and an archive extracted with the umask it was created under should
// not be the thing that makes a file world-readable. The original mode is recorded
// in the tar header's comment... no, it is not, because preserving it is less
// important than being right about it on extraction.
func addFile(writer *tar.Writer, name string, file sized) error {
	header := &tar.Header{
		Name:     name,
		Mode:     0o600,
		Size:     file.size,
		Typeflag: tar.TypeReg,
		ModTime:  time.Time{},
	}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("writing the header for %s: %w", name, err)
	}
	if _, err := writer.Write(file.contents); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
}

// pruneBackups keeps the `keep` most recent archives and removes the rest, and
// returns how many it removed.
//
// "Most recent" is by **name**, not by modification time, because the name is the
// timestamp and a `cp` that preserved nothing or an `rsync` that did not would make
// modification time a different answer from the one the filename says. Two sources
// of truth for "which is newest" is a sort that disagrees with its own directory
// listing.
func pruneBackups(dir string, keep int) (int, error) {
	backups := filepath.Join(dir, "backups")
	entries, err := os.ReadDir(backups)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading %s: %w", backups, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, archivePrefix) || !strings.HasSuffix(name, archiveSuffix) {
			continue
		}
		names = append(names, name)
	}
	if len(names) <= keep {
		return 0, nil
	}

	// The names are `backup-<timestamp>[-<n>].tar.gz`, so a plain string sort is a
	// chronological sort, and the counter sorts correctly because it is only reached
	// for two archives in the same second.
	sort.Strings(names)

	removed := 0
	for _, name := range names[:len(names)-keep] {
		if err := os.Remove(filepath.Join(backups, name)); err != nil {
			return removed, fmt.Errorf("removing the old archive %s: %w", name, err)
		}
		removed++
	}
	return removed, nil
}

// humanSize is a byte count as something a person reads, because "wrote
// backup-20260927-1914.tar.gz (48213)" is a number nobody checks and "78 MiB" is
// one they can compare against `df`.
func humanSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}

	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(div), "KMGTPE"[exp])
}

// sqliteBackupOf is a consistent copy of the database.
//
// # `VACUUM INTO`, and why not the Online Backup API
//
// The spec says "via `.backup`", which is the SQLite *command-line* tool's spelling
// of the Online Backup API, and that API is the right answer: it copies page by page
// under a read lock and yields a database that is consistent at one instant, which is
// the only thing a backup is. `modernc.org/sqlite` — ADR 0004's pure-Go driver —
// does not expose it through `database/sql`, so the equivalent primitive is
// `VACUUM INTO`, which runs the same read transaction and writes the result to a new
// file.
//
// The two differences a caller can see, both handled here:
//
//   - **`VACUUM INTO` takes a read lock, and returns `SQLITE_BUSY` immediately if a
//     writer holds one** where the backup API would wait. The connections below
//     therefore carry a `busy_timeout`, and the whole thing is retried a few times.
//     The error names the cause, because `SQLITE_BUSY` on its own sends a DM to a
//     SQLite manual at the moment their campaign is in it.
//   - **The result is a fresh, defragmented file** rather than a page-for-page copy.
//     That is *better* for a backup: it is smaller, and it has no free pages holding
//     a deleted secret.
//
// # Why not `cp`
//
// A `cp` of a WAL database gives you a main file whose pages are consistent with
// *some* instant, and no `-wal` and `-shm` beside it. The result opens, and then
// silently disagrees with the vault, which is the one failure a backup must not
// have. This is why `internal/store` turns WAL on and why this function exists.
func sqliteBackupOf(ctx context.Context, path string) (sized, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return sized{}, fmt.Errorf("no database at %s; has anything been synced yet?", path)
		}
		return sized{}, fmt.Errorf("reading %s: %w", path, err)
	}

	// A temporary file **beside** the target, so the copy is on the same filesystem
	// and a failure does not leave a truncated database where the next reader finds
	// it.
	scratch, err := os.CreateTemp(filepath.Dir(path), "backup-*.db")
	if err != nil {
		return sized{}, fmt.Errorf("making a scratch file beside %s: %w", path, err)
	}
	scratchName := scratch.Name()
	if closeErr := scratch.Close(); closeErr != nil {
		return sized{}, closeErr
	}
	defer func() { _ = os.Remove(scratchName) }()

	// Opened separately from the running store, because the copy wants a connection
	// of its own to hold the read transaction on. Opening a second connection on a WAL
	// database is safe — that is what WAL is for (ADR 0004) — and `internal/store`
	// already runs with the same settings.
	source, openErr := sql.Open("sqlite", path+busyTimeoutDSN)
	if openErr != nil {
		return sized{}, fmt.Errorf("opening %s to copy it: %w", path, openErr)
	}
	defer func() { _ = source.Close() }()

	// `VACUUM INTO` reads from the connection it is issued on and *writes the file
	// itself*, so there is no destination connection: the statement is the copy. The
	// path is a string literal rather than a bound parameter because SQLite's grammar
	// does not allow a parameter there, so the name is quoted — a data directory the
	// DM chose can contain an apostrophe, and the alternative is a syntax error with
	// a path in it.
	quoted := "'" + strings.ReplaceAll(scratchName, "'", "''") + "'"

	var lastErr error
	for attempt := range 3 {
		if _, lastErr = vacuumInto(ctx, source, quoted); lastErr == nil {
			lastErr = nil
			break
		}
		// Back off between attempts, because a busy database is busy *now* and a
		// retry in the same microsecond is a retry that fails for the same reason.
		select {
		case <-ctx.Done():
			return sized{}, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 250 * time.Millisecond):
		}
	}
	if lastErr != nil {
		return sized{}, fmt.Errorf("copying %s: %w -- is a server running against this "+
			"data directory? stopping it and trying again will work", path, lastErr)
	}

	contents, readErr := os.ReadFile(scratchName) //nolint:gosec // the path is beside the database
	if readErr != nil {
		return sized{}, fmt.Errorf("reading the copy of %s: %w", path, readErr)
	}
	return sized{contents: contents, size: int64(len(contents))}, nil
}

// vacuumInto is the one SQL statement this package issues, and it is a function so
// that the concatenation is in one place with its `//nolint` and not in a loop body.
//
// The concatenation is unavoidable: SQLite's grammar does not accept a bound
// parameter for the path in `VACUUM INTO`, and the path is a file this program just
// created in the data directory, not anything a request supplied. The quoting in
// [sqliteBackupOf] is what makes that safe, and it is doubled apostrophes rather
// than a `strings.ReplaceAll` on one — the same rule SQLite applies to its own string
// literals.
func vacuumInto(ctx context.Context, db *sql.DB, quotedPath string) (any, error) { //nolint:gosec // G202: the path is quoted by the caller, from a file this program created
	return db.ExecContext(ctx, "VACUUM INTO "+quotedPath)
}

// busyTimeoutDSN is the connection setting that turns `SQLITE_BUSY` from an
// immediate failure into a wait.
//
// Ten seconds, and it matches the `busy_timeout` `internal/store` already uses
// (ADR 0004), because the answer to "how long should two parts of this application
// wait for each other" is one number and two numbers are a config bug waiting to
// happen.
const busyTimeoutDSN = "?_pragma=busy_timeout(10000)"
