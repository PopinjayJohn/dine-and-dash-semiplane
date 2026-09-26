# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

<!--
House rules:

  - `## [Unreleased]` is always the first section. Never retro-edit a released
    section; add a new version header instead.
  - Every commit that touches internal/, cmd/, plugins/, migrations/ or web/
    must touch this file in the same commit. scripts/check-changelog.sh fails
    the build otherwise.
  - Write entries for humans reading release notes, not for a git log. "Fixed
    the search" is not an entry; "search now ranks exact title matches first"
    is.
  - Each milestone from docs/spec.md lands its own section here, in the same
    commit as the code.
-->

## [Unreleased]

### Added

- `internal/vault`, which reads and writes the markdown files a DM keeps in
  Obsidian. **A file the application did not change comes back out byte for
  byte.** Not semantically equal — byte for byte. A document keeps the bytes it
  was parsed from and hands them back untouched until something actually
  changes, so a reindex, a render and a save of an untouched page all produce a
  zero-byte diff, and a serialiser that tidied somebody's YAML would never put
  a diff in their git history.
- Frontmatter is a YAML parse tree, not a map. Order, comments, quote style and
  every key the application does not understand survive an edit to a key it does
  own; a `map[string]any` round trip would drop the comment beside a key and
  reorder the block on every save. Unknown keys are preserved verbatim, and the
  application refuses to write a key it does not own, because it is a guest in
  the DM's files.
- Reading is forgiving and writing is conventional, which is the only way the
  zero-byte-diff promise holds for everybody. A file with CRLF endings, a
  byte-order mark or no trailing newline is read as it is and written back as
  it is; a file the application wrote has no BOM, its own line ending and
  exactly one trailing newline. A DM on Windows is not a broken DM.
- A file whose frontmatter is not valid YAML, or is not a set of keys, or whose
  `visibility` is not a level the application knows, is **refused rather than
  interpreted** — including a typo like `plyers`. The permissive reading of a
  visibility key is how a `[!SECRET]` block reaches a player. A file with no
  frontmatter at all is not an error: most pages in a new vault have none.
- Two fuzz targets over the parser: one for "parse anything without panicking,
  and return what came in", and one for "a change to a key survives a
  re-parse, keeps the unknown keys and leaves the body alone".
- Page paths and attachment names are checked, and a checked path is not a
  string test. `vault.Open` resolves the campaign directory and every path is
  resolved against it and required to still be inside, so a symlink planted in a
  vault — a directory component *or* the file at the end of it — is a refusal
  rather than a read. A path may not contain `..`, `.`, an empty segment, a
  backslash, a volume name, a NUL, a control character, a leading dot or a
  reserved directory, and may not be a Windows device name or end in a dot or a
  space. A path that would only be valid after cleaning is refused, because
  `a/./b` and `a/b` naming two pages is a duplicate identity.
- Two more fuzz targets, one per sanitiser, whose invariant is one sentence: a
  path this package accepts names a file inside the vault, and a path it refuses
  never becomes one. After the check, the accepted path is joined to a real
  vault and required to be inside it.
- A page is written the way ADR 0001 fixes it: a temporary file in the same
  directory, `fsync`, rename, `fsync` the directory. A reader sees the old page
  or the new one, never a mixture — checked by reading while four writers write,
  and by failing a write at each of its four steps in turn. Every crash leaves
  either the old file or the new one, whole.
- Every file operation goes through an `os.Root` — a directory handle, not a
  name — so the operating system itself refuses anything that would leave the
  vault, following a symlink included. That is stronger than checking a path and
  then opening it, because there is no window between the two for a symlink to
  appear in, and it is why this package has no method that hands back an
  absolute path to open with `os.ReadFile`: a caller holding a path has left the
  guarantee behind.
- A crash leaves a temporary file behind, and opening the vault sweeps it.
  Opening is the only moment that cannot race: one data directory has one server,
  so a temporary file found then belongs to a process that is no longer running.
  Sweeping at the start of every write instead would delete a live write's
  temporary file out from under it, which is an error the caller did nothing to
  deserve.

- `wiki migrate`, for the two questions a person has about a database: what
  schema is it at, and bring it to the one this build knows about. It prints
  the path it touched every time, because a migration command that only says
  "migrated" gets run twice in the wrong directory, and `-status` answers
  without writing anything — including without creating an empty database where
  there was none, and saying so loudly if the path is not one you meant.
- `internal/datadir`, so every command that touches a database agrees on where
  the data directory is: `-data-dir` beats `DDSP_DATA_DIR`, which beats the
  platform default. That is ADR 0011's precedence in three rules, and
  `config.yaml` is not read yet — a half-implemented config loader that
  silently ignored a config file a DM had written would be worse than one that
  does not exist.

- `internal/clock` and `internal/idgen`, the only two packages allowed to know
  what time it is and what a new identifier looks like. Anything that stamps a
  row or mints a key takes a `clock.Clock` or an `idgen.IDGen` instead, so a
  test can hand it a sequence it can predict and assert on every id in a
  golden file. Production gets the system clock and random UUIDs;
  `clock.Fixed` steps forward by a fixed interval on every call, so
  consecutive writes get distinct and ordered timestamps rather than one
  repeated value.
- `internal/domain`, the values the application is about: campaigns, pages,
  revisions, links, principals, sessions and audit entries, with the three
  `Slug`, `Role`, `Visibility` and `LinkKind` vocabularies they use. Every
  value can check itself, so a bad row is refused at the boundary with an
  error naming the field instead of surfacing as `UNIQUE constraint failed`.
  Two consequences worth knowing: a page's path is its identity, so retitling
  never moves a file; and a page is required to carry a content hash, because
  a row that cannot be compared to a file is reindexed on every run. Campaign
  slugs are normalised rather than rejected, so `The Blackwater` becomes
  `the-blackwater`, and a slug can never come out as `..`, absolute or
  containing a path separator — pinned by a property test and a fuzz target.
- `migrations/0001_init`, the base schema: campaigns, pages, revisions, the
  link graph, principals, sessions and the audit log, with the indexes that
  make backlinks and "active now" cheap. The SQL is embedded in the binary
  rather than shipped beside it, which is what "one binary and a data
  directory" means in practice.
- A migration runner that takes a database to a known version or says why it
  cannot. It records one row per applied version, refuses a migration set with
  a hole in it or a version with no way back, stops against a database written
  by a newer build, and marks a version dirty *before* running its SQL — so a
  migration that fails half way stops the next run instead of being retried on
  top of a schema nobody described. `Force` is the documented way out of that
  state, and it is deliberately blunt.
- Timestamps are written as RFC 3339 with a fixed nine-digit fraction, because
  a timestamp column is TEXT: with a variable-width fraction, `…T19:03:00.4Z`
  sorts after `…T19:03:00.45Z` and every `WHERE created_at < ?` in the
  codebase would be subtly wrong. There is a test that asserts the two orders
  agree.
- `store.Open`, which opens a campaign database the way ADR 0004 describes and
  then **verifies** it: if WAL, foreign keys, the busy timeout or the
  synchronous setting did not take, the store refuses to open rather than
  running with a referential-integrity guarantee it does not have. A test asks
  for more connections than queries, on purpose, because a pragma set once is
  a pragma set on one connection.
- Reads and writes go to separate pools over the same file, with a single write
  connection. Two writers cannot collide, so `SQLITE_BUSY` between them cannot
  happen at all, and a reader never queues behind a write.
- A value a caller leaves zero is filled in: the store mints the id and stamps
  the timestamps from the injected clock, then returns the row as stored. A
  database path containing `?` or `#` is refused, because the connection string
  would be truncated at that character and the database would be created
  somewhere else.
- Store methods for campaigns, pages, revisions and the link graph. Every one
  of them fills in what the caller left blank — an id from the generator, a
  timestamp from the clock — and returns the row **as stored** rather than the
  value it was handed, so a test can assert on every field of a result.
  Archiving a page keeps the row: revisions and inbound links point at it, and
  an archive is recoverable. Listing is ordered by slug or by path, because a
  list whose order varies between runs cannot be diffed.
- Storing a page twice at the same path updates the row and keeps its id and
  creation time, because a page's identity is its path and its id is what every
  revision and every inbound link points at. Renaming a page is a different
  operation that rewrites those links, and it is not this method.
- `ReplaceLinks` makes a page's outgoing links be *exactly* the links given, in
  one transaction, including the empty case. Appending would leave links to
  pages the DM deleted from the text, and a link graph that remembers removed
  links is a graph nobody can trust.
- Unique and primary key violations come back as `store.ErrConflict`, a missing
  row as `store.ErrNotFound`, and a driver result code is read through a small
  interface rather than by matching on message text.
- A store contract suite in `internal/store/testsuite`, run against the SQLite
  store as `testsuite.Store(t, factory)`. It is where the properties a second
  implementation would have to match live: a value written comes back unchanged,
  blank ids and timestamps are filled in, writing the same value twice changes
  nothing, lists are ordered and complete, an archived row is invisible but
  still there, a path in two campaigns is two pages, a taken slug is a
  conflict, a missing row is not found, a row that references nothing is
  refused, revision numbers start at one per page, replacing links is all or
  nothing, and the graph holds links to pages that do not exist yet. The suite
  declares the interface it needs itself, so a second implementation is pointed
  at it rather than trusted.
- `wiki` command with `version` and `help` subcommands. The usage text is
  generated from the subcommand table, so a subcommand cannot exist without
  appearing in `wiki help`.
- `internal/version`, which reports the build version, commit, build date and
  Go toolchain. Every field is set with `-ldflags` at link time and defaults to
  `dev` and `unknown` so an unlinked build never claims to be a release.
  `wiki version` prints it; `/healthz` will report the same struct in M8.
- `.golangci.yml`, pinning the linter set: `errcheck`, `gosec` and
  `errorlint` for the access-control and error-handling invariants, plus
  formatting checks on `gofmt` and `goimports`.
- `wiki` returns an exit code of 0 on success and 1 on failure, decided by
  `exitCode` rather than by `os.Exit`, so the mapping is table-tested.

### Fixed

- A write to a closed stdout or stderr is now reported to the caller instead
  of being discarded. Previously `wiki help` could print a truncated page and
  exit 0.
- Pressing Ctrl-C no longer risks leaving the signal handler installed:
  `os.Exit` skipped the deferred cleanup, so `stop` is now called explicitly.

### Development

- `modernc.org/sqlite` moved to v1.59.0 (SQLite 3.53.4) and the module's Go
  directive to 1.25.0, which is the newest Go that driver family needs and the
  newest Go the driver can be used from. v1.47.0 and later declare `go 1.25.0`,
  so holding the directive at 1.24 meant holding the driver two years back.
  FTS5, `snippet()`, `bm25()`, the DSN pragmas and `CGO_ENABLED=0` are all
  re-verified on v1.59.0, because M5's two-index search is built on them.
- The coverage gate now measures the whole module rather than each package's
  own test binary. A package that only ever runs as part of another package's
  tests — `internal/store/testsuite` is the first, and it is most of the M1
  suite — used to report 0% and fail a gate the code was passing all day.
  Per-package numbers in `make cover` output are now "share of the module
  covered by this package's tests"; the gate and `coverage.out` are
  unaffected.
- `Makefile` with `check`, `test`, `cover`, `fuzz`, `lint`, `vet`, `fmt`,
  `build`, `run`, `reindex`, `spike` and `install-tools`. `make help` lists
  them.
- Pinned tool versions live in the `Makefile` and nowhere else, and CI reads
  them from there with `make print-<tool>-version`, so a version bump is one
  edit rather than three.
- `make cover` fails below the 80% coverage floor. The floor is one
  `COVERAGE_MIN` assignment, not a number typed into a workflow.
- GitHub Actions workflow with six jobs: `lint` (gofmt, vet,
  golangci-lint), `test` (Linux, macOS and Windows, which is what keeps
  "no CGO" true), `coverage` (the 80% floor), `build` (compile, run
  `wiki version`, and check the binary is static), `changelog` and
  `spike`. Every job runs a `make` target, so CI and a local machine
  execute the same command. Actions are pinned to commit SHAs.
- `.gitmessage` commit template, documenting the types, the scopes and
  the changelog rule.
- `scripts/check-changelog.sh`, which fails a push that changed
  `internal/`, `cmd/`, `plugins/`, `migrations/` or `web/` without
  changing `CHANGELOG.md` in the same commit. It reports the offending
  commits by short SHA and subject rather than just saying no.
- A Datastar spike under `spike/`, implementing ADR 0006's four-function
  SSE interface twice — once on `datastar-go`, once with only
  `net/http` — and asserting the two agree on the wire. It is a separate
  Go module, so it can be run and re-run without putting Datastar in
  the application's dependency graph. Findings in ADR 0008.

### Documentation

- ADR 0012 is corrected. It originally claimed golang-migrate had no pure-Go
  SQLite driver and that its only one was a cgo binding, which ruled the
  library out under ADR 0004. It has one: `database/sqlite` imports
  `modernc.org/sqlite` and takes the same `*sql.DB` this store already builds.
  The decision to keep an in-repo runner stands, but on the corrected grounds —
  it is already written and tested, it keeps a row per applied version, and it
  is a close call that should be revisited if the runner has to grow — not on a
  blocked door. `docs/spec.md` §3 and its decision table are corrected with it.
- ADR 0008 records the Datastar pins from that spike: client v1.0.4, server
  `datastar-go` v1.2.2, the four-method mapping onto the SDK, the actual wire
  format, and the two defects the spike turned up. It closes the "known risk"
  in `docs/spec.md` §3.
- ADR 0009 records why search uses two FTS5 indexes merged with Reciprocal
  Rank Fusion. A page row and the text inside it can have different readers, so
  filtering by page and showing a snippet is already a disclosure: the match
  position carries the secret. It also fixes `is:dm-only` as a filter applied
  inside the ACL join rather than a bypass, and extends
  `pages.renderer_version` to mean "the renderer version this index was built
  against".
- ADR 0010 records where the line between core and a plugin falls: core owns
  the eight ruleset-agnostic page types, ownership, `[!SECRET]` and the ACL; a
  page type is data rather than a Go type; and a plugin may not add a
  visibility level, register a second render path or FTS index, or read the
  database unfiltered.
- ADR 0011 records the deployment shape: one static binary, one data
  directory, and "copy the directory" as the whole backup procedure. It fixes
  the two things the spec left undecided — one server per data directory via
  `locks/serve.lock`, and environment-over-file configuration precedence.
- `docs/security.md`, the threat model ADR 0003 has referenced since it was
  written. Assets in priority order, the threat actors, every control mapped
  to the named test that enforces it, and six accepted limitations stated
  rather than discovered.

## [0.1.0] - TBD

_First release. Will cover milestones M0 through M12; see `docs/spec.md`
§ Milestones for the breakdown._

[Unreleased]: https://github.com/popinjayjohn/dine-and-dash-semiplane/compare/v0.1.0...HEAD
