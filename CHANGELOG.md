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
