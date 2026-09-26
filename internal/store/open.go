package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/migrations"
)

// DefaultReadPoolSize is how many connections the read pool gets. A campaign
// wiki is a DM and four players; this is generous, and it is small enough that
// SQLite never has to think about write starvation on a machine with one core.
const DefaultReadPoolSize = 4

// Options are the dependencies a store cannot invent for itself. Every field
// has a default, so `Options{}` is a working store — but a test that means to
// assert on ids or timestamps has to set them.
type Options struct {
	// Clock stamps rows. Defaults to the system clock.
	Clock clock.Clock

	// IDGen mints primary keys for rows whose ID the caller left empty.
	// Defaults to random UUIDs.
	IDGen idgen.IDGen

	// ReadPoolSize bounds the read pool. Zero means DefaultReadPoolSize.
	ReadPoolSize int
}

func (o Options) clock() clock.Clock {
	if o.Clock == nil {
		return clock.System{}
	}
	return o.Clock
}

func (o Options) idgen() idgen.IDGen {
	if o.IDGen == nil {
		return idgen.UUID{}
	}
	return o.IDGen
}

func (o Options) readPoolSize() int {
	if o.ReadPoolSize < 1 {
		return DefaultReadPoolSize
	}
	return o.ReadPoolSize
}

// Store is a handle on one campaign database. It is safe for concurrent use:
// writes are serialised by a single connection, reads are pooled.
//
// Every method that returns pages is campaign-scoped and unfiltered by
// principal, which is correct only while the schema has no visibility column.
// See the package comment for what happens in M7.
type Store struct {
	// write is the single-connection pool. Everything that mutates a row,
	// and everything that runs in a transaction, uses it.
	write *sql.DB

	// read is the pooled reader. Reads do not queue behind a write, which is
	// the point of WAL.
	read *sql.DB

	clock clock.Clock
	ids   idgen.IDGen
}

// Open opens (or creates) the database at path and verifies that it is
// configured the way ADR 0004 requires. It does not migrate: applying
// migrations is an explicit step, because a program that quietly rewrites a
// database on startup is a program nobody can reason about at 2am.
//
// The parent directory is created if it is missing, since the data directory
// is assembled by a command that may not have run yet.
func Open(ctx context.Context, path string, opts Options) (*Store, error) {
	if path == "" {
		return nil, errors.New("no database path was given")
	}

	connection, err := dsn(path)
	if err != nil {
		return nil, err
	}

	if dir := filepath.Dir(path); dir != "" {
		if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
			return nil, fmt.Errorf("creating %s: %w", dir, mkdirErr)
		}
	}

	write, err := openDB(connection, 1)
	if err != nil {
		return nil, err
	}

	read, err := openDB(connection, opts.readPoolSize())
	if err != nil {
		if closeErr := write.Close(); closeErr != nil {
			return nil, errors.Join(err, closeErr)
		}
		return nil, err
	}

	s := &Store{
		write: write,
		read:  read,
		clock: opts.clock(),
		ids:   opts.idgen(),
	}

	// Both pools are checked, because a pragma that took on the write
	// connection and not on a reader is exactly the bug this refuses to run
	// with.
	for _, pool := range []struct {
		name string
		db   *sql.DB
	}{
		{name: "write", db: write},
		{name: "read", db: read},
	} {
		if err := verifyPragmas(ctx, pool.db, pool.name); err != nil {
			if closeErr := s.Close(); closeErr != nil {
				return nil, errors.Join(err, closeErr)
			}
			return nil, err
		}
	}

	return s, nil
}

// Close releases both pools. It is safe to call more than once, which matters
// because a test that fails mid-way closes through a deferred call and Open
// closes on its own error path.
func (s *Store) Close() error {
	return errors.Join(s.write.Close(), s.read.Close())
}

// Migrate brings the schema up to the version this binary ships and reports
// where it ended up. It is safe to call on every boot.
func (s *Store) Migrate(ctx context.Context) (migrations.State, error) {
	return migrations.Up(ctx, s.write, s.clock)
}

// SchemaVersion reports the schema state without changing anything.
func (s *Store) SchemaVersion(ctx context.Context) (migrations.State, error) {
	return migrations.Version(ctx, s.write)
}

// dsn builds the connection string for one handle on the database file.
//
// Every pragma is in the DSN, because database/sql decides which pooled
// connection a query runs on and a pragma set with an Exec afterwards applies
// to exactly one of them. foreign_keys in particular is off by default in
// SQLite: a pool where one connection in four enforces the foreign keys this
// schema is built on is worse than a pool that enforces none, because it fails
// at random and nobody can reproduce it.
//
// The four settings are ADR 0004's, and the comment in that ADR is the
// reasoning for each: WAL so a reader does not block the writer, foreign keys
// on, a busy timeout so a contended write waits rather than failing, and
// synchronous NORMAL, which is safe under WAL and much faster than FULL.
func dsn(path string) (string, error) {
	// The DSN is a URL whose query string carries the pragmas, so a path
	// containing a '?' or a '#' would be truncated at that character and the
	// database would quietly be created somewhere else. Refusing is the only
	// honest answer available without escaping support from the driver.
	if strings.ContainsAny(path, "?#") {
		return "", fmt.Errorf("a database path may not contain '?' or '#': %s", path)
	}

	return "file:" + path + "?" + params, nil
}

const params = "" +
	"_pragma=journal_mode(WAL)" +
	"&_pragma=foreign_keys(1)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=synchronous(1)"

// pragma is one connection setting the store refuses to work without. The
// values are what SQLite reports back, not what was asked for: journal_mode
// answers "wal" in lower case whatever case it was set in.
type pragma struct {
	name string
	want string
}

// requiredPragmas is the list a store checks on both of its pools before it
// will hand anything back. An unverified pragma is a setting that will be off
// on the connection that matters, and a foreign key that is quietly not
// enforced is a campaign whose index can disagree with its vault.
var requiredPragmas = []pragma{
	{name: "journal_mode", want: "wal"},
	{name: "foreign_keys", want: "1"},
	{name: "busy_timeout", want: "5000"},
	{name: "synchronous", want: "1"},
}

func verifyPragmas(ctx context.Context, db *sql.DB, pool string) error {
	for _, p := range requiredPragmas {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&got); err != nil {
			return fmt.Errorf("reading %s on the %s pool: %w", p.name, pool, err)
		}
		if !strings.EqualFold(got, p.want) {
			return fmt.Errorf("the %s pool reports %s = %q, want %q: this database is not configured as ADR 0004 requires",
				pool, p.name, got, p.want)
		}
	}

	return nil
}
