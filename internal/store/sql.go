package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/migrations"
)

// Sentinel errors, so a caller can ask what happened instead of matching on a
// message. Anything else that comes back from a method is a bug or an I/O
// failure and is wrapped with the operation that produced it.
var (
	// ErrNotFound means the row does not exist, or is archived. A deleted
	// page is not found, on purpose: a caller that wants the row behind an
	// archive asks the sync engine, which is the thing that knows the file is
	// still there.
	ErrNotFound = errors.New("store: not found")

	// ErrConflict means the row is already there: a campaign slug that is
	// taken, or a page path that is taken within the campaign. Page identity
	// is its path (ADR 0001), so this is the error a rename runs into.
	ErrConflict = errors.New("store: already exists")
)

// rowScanner is a single row, whether it came from QueryRowContext or from an
// iteration over *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// timeLayout is how every timestamp column is written, named once so the
// reason it is fixed-width is stated once too.
const timeLayout = migrations.TimeLayout

// now returns the store's idea of the current time, in the layout every
// timestamp column uses. It is UTC because a local timestamp sorts wrongly on
// every machine that did not write it.
func (s *Store) now() time.Time {
	return s.clock.Now().UTC()
}

// mint returns a new primary key.
func (s *Store) mint() string {
	return s.ids.NewID()
}

// requiredTime decodes a non-null timestamp column.
func requiredTime(column, value string) (time.Time, error) {
	parsed, err := time.Parse(timeLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("column %s holds %q, which is not a timestamp this store writes: %w",
			column, value, err)
	}
	return parsed.UTC(), nil
}

// notFound builds the error a query that found nothing returns.
func notFound(what string, key any) error {
	return fmt.Errorf("%s %v: %w", what, key, ErrNotFound)
}

// writeError turns a driver's failure into an error a caller can act on. A
// unique or primary key violation is a conflict the caller can resolve — a
// different slug, a different path — so it gets a sentinel. Everything else is
// wrapped as it is: this store validates before it writes, so a constraint it
// did not expect is a bug and is reported like one.
func writeError(operation string, err error) error {
	switch code := sqliteCode(err); {
	case code == codeConstraintPrimaryKey, code == codeConstraintUnique:
		return fmt.Errorf("%s: %w: %w", operation, ErrConflict, err)
	case isConstraint(err):
		return fmt.Errorf("%s: a constraint this store does not check was violated, which is a bug: %w", operation, err)
	default:
		return fmt.Errorf("%s: %w", operation, err)
	}
}

// inTx runs fn inside a transaction, committing when it returns nil and
// rolling back when it does not.
//
// Every write that has to be all-or-nothing goes through here, on the
// single-connection write pool, so two of them cannot interleave.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning a transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing: %w", err)
	}
	return nil
}
