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
// # Access control
//
// The read predicate is written once, in acl.go, and the two search indexes are
// read through it — the public one through the audience scope, the private one
// through the stricter scope that also asks whether the principal may see that
// page's secrets. That is invariant 3 satisfied for the queries that exist.
//
// Everything else is still campaign-scoped and unfiltered: GetPage,
// GetPageByID, ListPages, Backlinks and the target lookups return whatever is in
// the campaign. That is correct only while nothing outside this package can reach
// them, and it stops being correct the moment a handler does. The next milestone
// gives each of those a principal and routes it through the same scopes; until
// then the store is reachable only from tests and from code that already knows
// the answer. Do not add a method here that returns a page without a scope.
package store
