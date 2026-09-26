// Package store is the projection of the vault into SQLite.
//
// The direction of travel is one way, and every decision here follows from it:
// the markdown files are the source of truth and this database is a cache of
// them that can be thrown away and rebuilt by `wiki reindex --full` (ADR
// 0001). A page row is therefore never allowed to be the only copy of
// anything, and the store never invents content — it persists what it is
// handed.
//
// The store is the only package that knows SQLite exists. Everything above it
// takes values from internal/domain and a context, which is what makes the
// contract suite in testsuite/ runnable against any implementation.
//
// # Two pools
//
// Reads and writes go to different *sql.DB handles over the same file, with
// the connection settings ADR 0004 fixes: WAL so a reader does not block the
// writer, foreign keys on because SQLite has them off by default, a five second
// busy timeout so a contended write waits rather than fails, and a single
// write connection so two writers cannot collide at all.
//
// # A value a caller leaves zero
//
// An empty ID, CreatedAt or UpdatedAt means "fill it in": the store mints the
// ID from its generator and stamps the timestamps from its clock, then returns
// the row as stored. That convention is why a test can assert on every id and
// every timestamp in a result, and it is the reason this package takes a clock
// and an ID generator rather than reaching for time.Now and a global counter.
//
// # Access control is not here yet
//
// Every method below is campaign-scoped and returns everything in the campaign.
// That is correct for a database that has no principals and no visibility
// column, and it stops being correct the moment 0002_access.sql lands: in M7
// each of these methods gains a principal and filters through
// page_acl_read, as invariant 3 in AGENTS.md requires of every method that can
// return a page. Until then the store is reachable only from tests and from
// code that already knows the answer.
package store
