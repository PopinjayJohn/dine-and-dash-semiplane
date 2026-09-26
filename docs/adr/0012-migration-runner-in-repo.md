# 0012. A migration runner in this repository, not golang-migrate

- **Status:** Accepted
- **Date:** 2026-09-26
- **Corrected:** 2026-09-26, same day. The Context section originally claimed
  golang-migrate had no pure-Go SQLite driver. It has one. The claim was wrong
  and is replaced below; the decision stands on the corrected reasoning, and
  it is a close call rather than the clear one it first read as.
- **Relates to:** [0004](0004-pure-go-sqlite.md),
  [0001](0001-files-as-source-of-truth.md),
  [0011](0011-single-binary-data-directory-backup-unit.md)

## Context

`docs/spec.md` §3 names `github.com/golang-migrate/migrate/v4` as the
migration tool, with the rationale "versioned SQL, embedded with `go:embed`".
That is the right shape, and `migrations/` implements it.

The first version of this ADR claimed the library could not be used here at
all, on the grounds that golang-migrate's only SQLite driver is
`database/sqlite3` and that driver imports `github.com/mattn/go-sqlite3`, a cgo
binding, which [ADR 0004](0004-pure-go-sqlite.md) rules out. **That is
false.** golang-migrate v4 ships two SQLite drivers, and one of them is pure
Go:

```
// database/sqlite/sqlite.go in golang-migrate v4.20.1
import (
	_ "modernc.org/sqlite"
)
```

The pure-Go driver is a first-class alternative, registered under the name
`sqlite` as opposed to `sqlite3`, and it takes the same `WithInstance`
database/sql handle this store already builds with ADR 0004's pragmas. So the
cgo objection does not apply, and anyone reading the first version of this
document would have drawn a false conclusion about what was available.

Corrected: the choice is real, and it is a judgement about a small subsystem
rather than a blocked door.

## Decision

Keep the runner. It lives in `migrations/` next to the SQL it applies, and it
does four things:

- **Loads and validates the set before running anything.** Versions start at 1
  with no gaps, and every version has both an up and a down file. A set with a
  hole in it would otherwise be applied as far as it goes, building a schema
  nobody described.
- **Records one row per applied version** in `schema_migrations`, with a name
  and an applied-at timestamp. golang-migrate's table holds exactly one row,
  which answers "what version is this" and not "which migrations built this".
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
`0001_init.up.sql` and `0001_init.down.sql` — so replacing this runner is a
change to `migrations/runner.go` and not to the schema.

## Consequences

**Good**

- No cgo, which ADR 0004 requires — though the library's `sqlite` driver would
  have satisfied that too, so this is not the deciding factor.
- No new dependency in the one place a heavy dependency is least welcome: the
  binary a DM copies to a laptop.
- Every behaviour above is asserted by a test in this repository, including the
  dirty path and the newer-database refusal, which are the two behaviours
  nobody exercises until they matter.
- A row per version, so `wiki migrate -status` can say which migrations built
  a database and not only which one it is at.

**Bad**

- It is code of ours to maintain instead of somebody else's, and the honest
  version of this argument is that a 150-line migration runner is a
  distraction from a wiki for DMs.
- `Force` with a step count, the `error` file and the multistmt driver are not
  implemented. `Down` takes a step count and stops at the bottom.
- It does not lock across processes. Neither does the library in a way that
  helps: golang-migrate's SQLite `Lock` is an in-process `atomic.Bool`, so
  `locks/serve.lock` from ADR 0011 is what prevents two servers on one data
  directory, and that remains the fix.
- Detection of a database whose schema was changed outside the runner is
  absent, exactly as it is in the library. `wiki reindex --full` and
  `TestBackupsRestoreIdenticalIndex` are this project's tools for that.

`docs/spec.md` §3 is corrected by this ADR: the migration tool is the
repository's own runner, not golang-migrate. Everything else in that table
stands.

## Alternatives rejected

**golang-migrate with its pure-Go `sqlite` driver.** The real alternative, and
a reasonable one. It is a mature library with the same dirty-flag semantics
and the same transaction wrapping, and adopting it means deleting a tested
subsystem rather than maintaining one. It was not adopted because the runner
is already written, is covered by tests that pin the failure paths, and its
per-version history is genuinely better for a data directory a DM will copy
around. If it grows — dirty-state tooling, multi-statement drivers, a CLI —
this decision should be revisited rather than defended.
