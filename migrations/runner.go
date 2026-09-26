package migrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
)

// TimeLayout is how every timestamp column in the schema is written: RFC 3339,
// UTC, with a fixed nine-digit fraction.
//
// The fixed width is the whole point. With a variable-width fraction,
// "…T19:03:00.4Z" sorts *after* "…T19:03:00.45Z" as text, so a `WHERE
// created_at < ?` in SQL would order two events backwards. Since every
// timestamp this application writes goes through this layout, text order and
// chronological order are the same order.
const TimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// ErrDirty is returned when a previous migration did not finish. The schema is
// then in an unknown state and every subsequent run stops, which is the
// opposite of what a wiki would otherwise do with a half-built database.
var ErrDirty = errors.New("schema is dirty")

// schemaTable is where the runner records what it has applied. One row per
// version, rather than one row overwritten in place, so a database can be
// listed as the sequence of migrations that built it.
const schemaTable = "schema_migrations"

// createSchemaTable is executed before anything else and is safe to re-run.
// It is a CREATE TABLE IF NOT EXISTS rather than a migration because the runner
// has to be able to read the state of a database that predates it.
const createSchemaTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL,
  dirty      INTEGER NOT NULL CHECK (dirty IN (0, 1)),
  applied_at TEXT    NOT NULL
)`

// State is where a database's schema stands.
type State struct {
	// Version is the highest applied migration, or 0 for a database that has
	// never been migrated.
	Version int

	// Name is that migration's name, for a message a human can act on. It is
	// empty at version 0.
	Name string

	// Dirty reports that the last migration to run did not finish. A dirty
	// schema is not used; it is repaired, with Force, by someone who knows
	// what happened.
	Dirty bool
}

// String renders the state for a log line or an error message.
func (s State) String() string {
	if s.Version == 0 {
		return "no migrations applied"
	}

	state := "clean"
	if s.Dirty {
		state = "dirty"
	}
	if s.Name == "" {
		return fmt.Sprintf("version %d, %s", s.Version, state)
	}
	return fmt.Sprintf("version %d (%s), %s", s.Version, s.Name, state)
}

// Migration is one version's worth of schema change.
type Migration struct {
	Version int
	Name    string

	// Up is the SQL that applies the version and Down the SQL that reverses
	// it. Both are required: a migration that cannot be rolled back is a
	// migration nobody has thought about failing.
	Up   string
	Down string
}

// Up brings db to the latest version this binary ships, and reports where it
// ended up. Running it again is a no-op, which is what makes it safe to call
// on every boot.
func Up(ctx context.Context, db *sql.DB, clk clock.Clock) (State, error) {
	return up(ctx, db, clk, sqlFiles)
}

// Down rolls back the given number of versions, oldest first, and reports
// where the database ended up. Rolling back a schema change that touched data
// is how a database is lost, so a test is the right place for this and a
// recovery is not: Force is the tool for that.
func Down(ctx context.Context, db *sql.DB, steps int) (State, error) {
	return down(ctx, db, steps, sqlFiles)
}

// Version reports the schema state of db without changing anything. A database
// that has never been migrated reports version 0 and no error, which is the
// same thing it means when there is no table to look in.
func Version(ctx context.Context, db *sql.DB) (State, error) {
	return version(ctx, db)
}

// Latest reports the highest version this binary ships, so a caller can check
// that a database is not ahead of the code reading it.
func Latest() (int, error) {
	return latest(sqlFiles)
}

func latest(fsys fs.FS) (int, error) {
	loaded, err := Load(fsys)
	if err != nil {
		return 0, err
	}
	if len(loaded) == 0 {
		return 0, errors.New("no migrations are embedded in this binary")
	}
	return loaded[len(loaded)-1].Version, nil
}

// Force records version as applied without running any SQL, and forgets any
// higher version. It is the documented way out of a dirty schema, and it is
// dangerous by construction: it asserts something about the database that
// nothing checked. Only do it knowing what the migration would have done.
func Force(ctx context.Context, db *sql.DB, clk clock.Clock, version int) error {
	if version < 0 {
		return fmt.Errorf("cannot force version %d, which does not exist", version)
	}

	if err := createTable(ctx, db); err != nil {
		return err
	}

	at := clk.Now().UTC().Format(TimeLayout)

	if version == 0 {
		if _, err := db.ExecContext(ctx, `DELETE FROM `+schemaTable); err != nil {
			return fmt.Errorf("forgetting the schema version: %w", err)
		}
		return nil
	}

	if _, err := db.ExecContext(ctx,
		`DELETE FROM `+schemaTable+` WHERE version > ?`, version); err != nil {
		return fmt.Errorf("forgetting migrations after version %d: %w", version, err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO `+schemaTable+` (version, name, dirty, applied_at)
		VALUES (?, ?, 0, ?)
		ON CONFLICT(version) DO UPDATE SET name = excluded.name, dirty = 0, applied_at = excluded.applied_at`,
		version, "forced", at,
	); err != nil {
		return fmt.Errorf("recording version %d as applied: %w", version, err)
	}

	return nil
}

func up(ctx context.Context, db *sql.DB, clk clock.Clock, fsys fs.FS) (State, error) {
	state, err := version(ctx, db)
	if err != nil {
		return State{}, err
	}

	if state.Dirty {
		return State{}, fmt.Errorf("%w: %s did not finish; inspect it, then Force the version it should be at", ErrDirty, state)
	}

	loaded, err := Load(fsys)
	if err != nil {
		return State{}, err
	}
	if len(loaded) == 0 {
		return State{}, errors.New("no migrations are embedded in this binary")
	}

	latest := loaded[len(loaded)-1]
	if state.Version > latest.Version {
		return State{}, fmt.Errorf("database is at %s but this binary only ships version %d (%s): it was written by a newer build",
			state, latest.Version, latest.Name)
	}

	if err := createTable(ctx, db); err != nil {
		return State{}, err
	}

	for _, m := range loaded {
		if m.Version <= state.Version {
			continue
		}
		if err := apply(ctx, db, clk, m); err != nil {
			return State{}, err
		}
		state = State{Version: m.Version, Name: m.Name}
	}

	return state, nil
}

func down(ctx context.Context, db *sql.DB, steps int, fsys fs.FS) (State, error) {
	if steps < 1 {
		return State{}, fmt.Errorf("cannot roll back %d versions, want at least 1", steps)
	}

	state, err := version(ctx, db)
	if err != nil {
		return State{}, err
	}
	if state.Dirty {
		return State{}, fmt.Errorf("%w: %s did not finish; inspect it, then Force the version it should be at", ErrDirty, state)
	}

	loaded, err := Load(fsys)
	if err != nil {
		return State{}, err
	}

	for range steps {
		if state.Version == 0 {
			return state, nil
		}

		m, ok := find(loaded, state.Version)
		if !ok {
			return State{}, fmt.Errorf("cannot roll back %s: this binary does not ship version %d", state, state.Version)
		}

		if err := run(ctx, db, m.Down); err != nil {
			return State{}, fmt.Errorf("rolling back migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM `+schemaTable+` WHERE version = ?`, m.Version); err != nil {
			return State{}, fmt.Errorf("forgetting migration %d: %w", m.Version, err)
		}

		previous, ok := find(loaded, state.Version-1)
		if !ok {
			state = State{}
			break
		}
		state = State{Version: previous.Version, Name: previous.Name}
	}

	return state, nil
}

// apply runs one migration. The marker is committed before the SQL, in its own
// transaction, so that a migration which fails halfway leaves the version
// marked dirty: the next run stops and says so, rather than re-running the SQL
// on top of a schema that is half built and failing in a more confusing way.
func apply(ctx context.Context, db *sql.DB, clk clock.Clock, m Migration) error {
	if _, err := db.ExecContext(ctx, `INSERT INTO `+schemaTable+` (version, name, dirty, applied_at)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(version) DO UPDATE SET name = excluded.name, dirty = 1, applied_at = excluded.applied_at`,
		m.Version, m.Name, clk.Now().UTC().Format(TimeLayout),
	); err != nil {
		return fmt.Errorf("marking migration %d (%s) as started: %w", m.Version, m.Name, err)
	}

	if err := run(ctx, db, m.Up); err != nil {
		return fmt.Errorf("applying migration %d (%s): %w", m.Version, m.Name, err)
	}

	if _, err := db.ExecContext(ctx,
		`UPDATE `+schemaTable+` SET dirty = 0 WHERE version = ?`, m.Version); err != nil {
		return fmt.Errorf("clearing the dirty flag on migration %d (%s): %w", m.Version, m.Name, err)
	}

	return nil
}

// run executes one migration's SQL in a single transaction, so a statement
// that fails part way through leaves nothing behind.
func run(ctx context.Context, db *sql.DB, statement string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return err
	}
	return tx.Commit()
}

func version(ctx context.Context, db *sql.DB) (State, error) {
	var present int
	err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, schemaTable).Scan(&present)
	if err != nil {
		return State{}, fmt.Errorf("looking for the %s table: %w", schemaTable, err)
	}
	if present == 0 {
		return State{}, nil
	}

	var state State
	var dirty int
	err = db.QueryRowContext(ctx,
		`SELECT version, name, dirty FROM `+schemaTable+` ORDER BY version DESC LIMIT 1`).
		Scan(&state.Version, &state.Name, &dirty)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("reading the schema version: %w", err)
	}
	state.Dirty = dirty == 1

	return state, nil
}

func createTable(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, createSchemaTable); err != nil {
		return fmt.Errorf("creating the %s table: %w", schemaTable, err)
	}
	return nil
}

func find(loaded []Migration, version int) (Migration, bool) {
	for _, m := range loaded {
		if m.Version == version {
			return m, true
		}
	}
	return Migration{}, false
}
