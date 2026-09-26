package migrations

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"

	_ "modernc.org/sqlite"
)

// This file tests the parts of the runner that a real schema cannot reach: a
// migration whose SQL fails half way, a version with no down file to roll back
// to, a set of migrations the runner has never seen. Everything else is
// tested against the migrations this binary actually ships.

func openDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "campaigns.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("opening a test database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func fixedClock() *clock.Fixed {
	return clock.NewFixed(time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC), time.Minute)
}

// twoMigrations is a valid synthetic set: version 1 creates a table, version 2
// adds a column to it.
func twoMigrations() fstest.MapFS {
	return fstest.MapFS{
		"0001_init.up.sql":     &fstest.MapFile{Data: []byte("CREATE TABLE pages (id TEXT PRIMARY KEY);")},
		"0001_init.down.sql":   &fstest.MapFile{Data: []byte("DROP TABLE pages;")},
		"0002_access.up.sql":   &fstest.MapFile{Data: []byte("ALTER TABLE pages ADD COLUMN visibility TEXT NOT NULL DEFAULT 'players';")},
		"0002_access.down.sql": &fstest.MapFile{Data: []byte("ALTER TABLE pages DROP COLUMN visibility;")},
	}
}

func TestUpAppliesEveryMigrationAfterTheCurrentVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openDB(t)
	clk := fixedClock()

	// A database that has only seen version 1 gets only version 2. Force is
	// how a half-migrated database is described to the runner.
	if err := exec(ctx, db, "CREATE TABLE pages (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("preparing a half-migrated database: %v", err)
	}
	if err := Force(ctx, db, clk, 1); err != nil {
		t.Fatalf("recording version 1: %v", err)
	}

	state, err := up(ctx, db, clk, twoMigrations())
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if state.Version != 2 || state.Name != "access" {
		t.Errorf("up left the schema at %v, want version 2 (access)", state)
	}

	var versions []int
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("reading the recorded versions: %v", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatalf("scanning a recorded version: %v", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating the recorded versions: %v", err)
	}

	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Errorf("recorded versions %v, want [1 2]", versions)
	}

	// The second migration ran: the column exists.
	if _, err := db.ExecContext(ctx, `INSERT INTO pages (id) VALUES ('p1')`); err != nil {
		t.Errorf("inserting into the migrated table: %v", err)
	}
}

// TestAFailedMigrationLeavesTheSchemaDirty is the reason the marker is written
// before the SQL rather than after it. A migration that fails half way leaves
// the schema in a state nobody described, and the next run has to say so
// instead of trying again on top of it.
func TestAFailedMigrationLeavesTheSchemaDirty(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openDB(t)
	clk := fixedClock()

	broken := fstest.MapFS{
		"0001_init.up.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE pages (id TEXT PRIMARY KEY); THIS IS NOT SQL;")},
		"0001_init.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE pages;")},
	}

	_, err := up(ctx, db, clk, broken)
	if err == nil {
		t.Fatal("up applied a migration containing invalid SQL")
	}

	state, err := version(ctx, db)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !state.Dirty {
		t.Errorf("after a failed migration the schema reports %v, want it dirty: the next run must not try again", state)
	}

	// The next run refuses, and says why.
	_, err = up(ctx, db, clk, broken)
	if !errors.Is(err, ErrDirty) {
		t.Errorf("up after a failure returned %v, want an error matching ErrDirty", err)
	}

	// So does a rollback, because rolling back a schema that is already
	// unknown makes it more unknown.
	_, rollbackErr := down(ctx, db, 1, broken)
	if !errors.Is(rollbackErr, ErrDirty) {
		t.Errorf("down after a failure returned %v, want an error matching ErrDirty", rollbackErr)
	}

	// Force is the documented way out.
	if forceErr := Force(ctx, db, clk, 0); forceErr != nil {
		t.Fatalf("Force(0): %v", forceErr)
	}
	forced, err := version(ctx, db)
	if err != nil {
		t.Fatalf("version after Force(0): %v", err)
	}
	if forced.Dirty || forced.Version != 0 {
		t.Errorf("after Force(0) the schema reports %v, want version 0 and clean", forced)
	}
}

// TestAFailedMigrationRollsBackItsOwnStatements: the SQL of a migration runs
// in a transaction, so a statement that fails leaves nothing of that
// migration behind.
func TestAFailedMigrationRollsBackItsOwnStatements(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openDB(t)

	broken := fstest.MapFS{
		"0001_init.up.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE pages (id TEXT PRIMARY KEY); INSERT INTO nosuchtable VALUES (1);")},
		"0001_init.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE pages;")},
	}

	if _, err := up(ctx, db, fixedClock(), broken); err == nil {
		t.Fatal("up applied a migration that fails half way")
	}

	var count int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'pages'`).Scan(&count)
	if err != nil {
		t.Fatalf("counting tables: %v", err)
	}
	if count != 0 {
		t.Error("the pages table survived a migration whose SQL failed: the migration did not run in a transaction")
	}
}

func TestDownStopsAtTheBottom(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openDB(t)
	clk := fixedClock()

	if _, err := up(ctx, db, clk, twoMigrations()); err != nil {
		t.Fatalf("up: %v", err)
	}

	// Asking for more rollbacks than there are versions stops at the bottom
	// rather than reporting an error: the caller wanted the schema gone.
	state, err := down(ctx, db, 5, twoMigrations())
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if state.Version != 0 {
		t.Errorf("down left the schema at version %d, want 0", state.Version)
	}

	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'pages'`).Scan(&count); err != nil {
		t.Fatalf("counting tables: %v", err)
	}
	if count != 0 {
		t.Error("the pages table survived a full rollback")
	}
}

// TestDownRefusesAVersionThisBinaryDoesNotShip is the case where a database has
// been moved between builds and the older binary is asked to roll it back.
func TestDownRefusesAVersionThisBinaryDoesNotShip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openDB(t)
	clk := fixedClock()

	if _, err := up(ctx, db, clk, twoMigrations()); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := Force(ctx, db, clk, 7); err != nil {
		t.Fatalf("Force: %v", err)
	}

	_, err := down(ctx, db, 1, twoMigrations())
	assertErrorContains(t, err, "does not ship version 7")
}

func TestUpRefusesAnEmptyMigrationSet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openDB(t)

	_, err := up(ctx, db, fixedClock(), fstest.MapFS{})
	assertErrorContains(t, err, "no migrations are embedded")

	if _, err := latest(fstest.MapFS{}); err == nil {
		t.Error("latest reported a version for an empty migration set")
	}
}

func exec(ctx context.Context, db *sql.DB, statement string) error {
	_, err := db.ExecContext(ctx, statement)
	return err
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	switch {
	case err == nil:
		t.Fatalf("no error, want one containing %q", want)
	case !strings.Contains(err.Error(), want):
		t.Fatalf("error %q, want one containing %q", err, want)
	}
}
