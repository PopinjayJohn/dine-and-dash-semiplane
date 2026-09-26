// Package migrations is the versioned schema, and the code that gets a
// database to it.
//
// The SQL is embedded with go:embed rather than shipped as loose files, for
// the reason ADR 0011 gives for embedding everything else: one static binary
// and a data directory, with nothing to install and nothing to fetch. It is
// also why the file names keep the golang-migrate convention
// (`0001_init.up.sql`): if this runner is ever replaced by that library, the
// migrations themselves are already in the shape it expects.
//
// The rule this package enforces is the boring one: a database at a known
// version, or an error saying why it is not. A half-applied migration leaves a
// marker and stops the next run, because a schema that is quietly missing a
// column is a leak waiting to be found by a DM rather than by us.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var sqlFiles embed.FS

// FS returns the migration files as a filesystem: NNNN_name.up.sql and
// NNNN_name.down.sql, one pair per version.
func FS() fs.FS {
	return sqlFiles
}
