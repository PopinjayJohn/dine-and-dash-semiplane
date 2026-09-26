package migrations_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestDB opens an empty SQLite database in a temporary directory and closes
// it when the test ends.
//
// A file rather than :memory: on purpose. A shared in-memory database lives in
// one connection, so a second connection in the pool — which is what the store
// does for reads — would find an empty database. That mistake is worth
// designing out of every test rather than rediscovering it.
func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "campaigns.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("opening a test database: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("closing the test database: %v", err)
		}
	})

	return db
}

// tableNames returns the tables and indexes in a database, so a test can assert
// on the schema without hard-coding a query per object.
func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(),
		`SELECT name FROM sqlite_master WHERE type IN ('table', 'index') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("reading sqlite_master: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning a schema object name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating schema objects: %v", err)
	}

	return names
}
