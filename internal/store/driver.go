package store

import (
	"database/sql"
	"fmt"

	// The driver is imported here and nowhere else, which is ADR 0004's
	// requirement: swapping modernc.org/sqlite for the cgo driver, or for
	// something else entirely, is a change to this file plus go.mod.
	_ "modernc.org/sqlite"
)

// driverName is the database/sql driver this store speaks. Everything else in
// the package works through *sql.DB and does not care which one it is.
const driverName = "sqlite"

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
