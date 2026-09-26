package store

import (
	"database/sql"
	"errors"
	"fmt"

	// The driver is imported here and nowhere else, which is ADR 0004's
	// requirement: swapping modernc.org/sqlite for the cgo driver, or for
	// something else entirely, is a change to this file plus go.mod.
	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver this store speaks. Everything else in
// the package works through *sql.DB and does not care which one it is.
const driverName = "sqlite"

// SQLite result codes this package distinguishes.
//
// The driver reports the extended code, which refines the primary one: 1555 is
// "constraint: primary key" and 2067 is "constraint: this value is not
// unique", both of which are 19 shifted up. Masking the low byte is what lets
// one check recognise a foreign key violation and a NOT NULL violation as the
// family they are.
const (
	codeConstraint           = 19
	codeConstraintPrimaryKey = codeConstraint | (6 << 8)
	codeConstraintUnique     = codeConstraint | (8 << 8)
)

// codedError is the part of a driver's error this package needs. Declaring it
// here rather than importing the driver's error type keeps the driver an
// implementation detail of this file: a driver that cannot report a result
// code simply never produces a sentinel, and every error is reported as itself.
type codedError interface {
	Code() int
}

// sqliteCode returns the SQLite result code an error carries, or 0 when it
// carries none.
func sqliteCode(err error) int {
	var coded codedError
	if !errors.As(err, &coded) {
		return 0
	}
	return coded.Code()
}

// isConstraint reports whether err is any kind of constraint violation.
func isConstraint(err error) bool {
	code := sqliteCode(err)
	return code != 0 && code&0xff == codeConstraint
}

// openDB opens one handle on a SQLite file and bounds how many connections it
// may use.
//
// The bounds are half of ADR 0004's connection configuration. The other half
// — the pragmas — lives in the DSN, because database/sql hands out whatever
// connection in the pool a query lands on, and a pragma set by an Exec after
// opening would apply to one connection and not the next.
func openDB(dsn string, maxOpen int) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", driverName, err)
	}

	// One connection for the whole write pool: a campaign wiki has a handful
	// of writers, and a single connection makes SQLITE_BUSY between them
	// impossible rather than unlikely.
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)

	return db, nil
}
