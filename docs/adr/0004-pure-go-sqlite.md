# 0004. SQLite via modernc.org/sqlite (pure Go, no CGO)

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The application is a single static binary that a DM runs on their own machine,
and the test suite is expected to be runnable by anyone who clones the repo
without installing a C toolchain or setting build flags.

That argues against `mattn/go-sqlite3`, the most-used SQLite driver for Go,
which is a cgo binding: it requires a working C compiler, cross-compilation
requires a cross C toolchain, and `CGO_ENABLED=0` builds silently lose the
driver. It is a poor fit for a project whose test suite is a stated priority.

## Decision

Use `modernc.org/sqlite`, a pure-Go translation of SQLite, accessed through
`database/sql`.

The driver is never imported outside one function:

```go
// internal/store/driver.go
package store

// open is the only place that knows which SQLite driver is in use.
// Everything above it takes a *sql.DB.
func open(dsn string) (*sql.DB, error) { ... }
```

All other store code takes a `*sql.DB`, so swapping to `mattn/go-sqlite3` or a
different engine is a change to one function plus `go.mod`.

Connection configuration is set explicitly rather than left to defaults,
because the defaults are wrong for a concurrent web application:

```sql
PRAGMA journal_mode = WAL;     -- readers do not block the writer
PRAGMA foreign_keys = ON;       -- off by default in SQLite
PRAGMA busy_timeout = 5000;    -- wait rather than fail on a locked db
PRAGMA synchronous = NORMAL;   -- safe under WAL, much faster
```

The write pool is a single connection (`SetMaxOpenConns(1)`), which eliminates
`SQLITE_BUSY` between writers outright; readers use a separate pool. A
campaign-scale wiki does not need write concurrency, and correctness is worth
more than throughput here.

FTS5 is available in `modernc.org/sqlite`, so full-text search needs no second
dependency.

## Consequences

**Good**

- `go test ./...` works on any machine with a Go toolchain. No C compiler, no
  `CGO_ENABLED` matrix in CI, no build tags.
- Static cross-compilation for Linux, macOS and Windows from one command.
- FTS5 is available, which is what makes the two-index search design in
  [0009](0009-two-index-search-with-rrf.md) possible.

**Bad**

- `modernc.org/sqlite` is a large package and its translation is slower than
  cgo SQLite. For a campaign vault — thousands of pages, a handful of
  concurrent users — this is irrelevant, but benchmarks in the M5 test suite
  exist to catch a regression.
- It lags new upstream SQLite features. Project SQL should stay on
  well-established features.
- One connection for writes means a slow write blocks other writes. Audit-log
  and revision writes are small and fast, so this is fine.
