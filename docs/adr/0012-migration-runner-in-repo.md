# 0012. A migration runner in this repository, not golang-migrate

- **Status:** Accepted
- **Date:** 2026-09-26
- **Relates to:** [0004](0004-pure-go-sqlite.md),
  [0001](0001-files-as-source-of-truth.md),
  [0011](0011-single-binary-data-directory-backup-unit.md)

## Context

`docs/spec.md` §3 names `github.com/golang-migrate/migrate/v4` as the
migration tool, with the rationale "versioned SQL, embedded with `go:embed`".
That is the right shape, and the second half of it is exactly what
`migrations/` does today.

The library is the problem, and only for SQLite. golang-migrate v4 has one
SQLite driver, `database/sqlite3`, and that driver imports
`github.com/mattn/go-sqlite3`, which is a cgo binding:

```go
// database/sqlite3/sqlite3.go in golang-migrate v4.20.1
import (
	_ "github.com/mattn/go-sqlite3"
)
```

[ADR 0004](0004-pure-go-sqlite.md) rules cgo out, and the reason it gives is
not aesthetic: `go test ./...` has to work on a machine with a Go toolchain and
nothing else, and CI runs the test job on Linux, macOS and Windows, which is
what keeps "no CGO" true rather than aspirational. Depending on golang-migrate
would break the project's own stated test requirement, and would do it
silently: `CGO_ENABLED=0` builds do not fail when a cgo driver is missing, they
just fail to register it at run time.

There is no pure-Go driver in the library to reach for instead, and there is no
version of it that is likely to appear: the driver is a thin wrapper over
database/sql, and the only way to write one is to use the one database/sql
already has.

## Decision

Write the runner. It lives in `migrations/` next to the SQL it applies, and it
does four things:

- **Loads and validates the set before running anything.** Versions start at 1
  with no gaps, and every version has both an up and a down file. A set with a
  hole in it would otherwise be applied as far as it goes, building a schema
  nobody described.
- **Records one row per applied version** in `schema_migrations`, with a name
  and an applied-at timestamp, so a database can be listed as the sequence of
  migrations that built it.
- **Marks a version dirty before running its SQL and clears it afterwards.**
  A migration that fails half way leaves the marker, and the next run refuses
  with `migrations.ErrDirty` rather than retrying on top of a half-built
  schema. Each migration's SQL runs in a transaction, so nothing of a failed
  migration survives.
- **Refuses a database that is ahead of the binary.** A rolled-back build
  pointed at a newer schema fails with a message saying so, rather than
  reading a column it believes is not there.

`Force` is the documented way out of a dirty state, and it is deliberately
blunt: it asserts a version without running anything, and nothing checks that
assertion.

The migration files keep golang-migrate's naming convention —
`0001_init.up.sql` and `0001_init.down.sql` — so replacing this runner later is
a change to `migrations/runner.go` and not to the schema.

## Consequences

**Good**

- No CGO, so `go test ./...` runs anywhere a Go toolchain does, and the
  three-OS CI matrix keeps meaning something.
- One fewer dependency, in the one place where a heavy dependency is least
  welcome: the binary a DM copies to a laptop.
- The dirty flag and the "database is newer than this binary" check are the
  two behaviours worth having, and both are about 40 lines each.

**Bad**

- `migrate force`, `migrate down` with a step count, the `error` file, and
  multi-statement drivers are not implemented. `Down` takes a step count and
  stops at the bottom; there is no CLI, because M1 has no command that opens a
  store and the DX milestone owns the CLI.
- The runner does not detect a database whose schema was changed outside it.
  Neither does golang-migrate; `wiki reindex --full` and `TestBackupsRestoreIdenticalIndex`
  are the tools for that in this project.
- The dirty flag is a row an operator can edit by hand, which is also true of
  golang-migrate's.

`docs/spec.md` §3 is corrected by this ADR: the migration tool is the
repository's own runner, not golang-migrate. Everything else in that table
stands.
