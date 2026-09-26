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

- `wiki` command with `version` and `help` subcommands. The usage text is
  generated from the subcommand table, so a subcommand cannot exist without
  appearing in `wiki help`.
- `internal/version`, which reports the build version, commit, build date and
  Go toolchain. Every field is set with `-ldflags` at link time and defaults to
  `dev` and `unknown` so an unlinked build never claims to be a release.
- `wiki version` and `/healthz` report the same `version.Info` struct, so a
  bug report from a DM names the exact build.
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

## [0.1.0] - TBD

_First release. Will cover milestones M0 through M12; see `docs/spec.md`
§ Milestones for the breakdown._

[Unreleased]: https://github.com/popinjayjohn/dine-and-dash-semiplane/compare/v0.1.0...HEAD
