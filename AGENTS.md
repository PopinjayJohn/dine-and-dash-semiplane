# AGENTS.md

Guidance for AI coding agents — and humans — working in this repository. Tool
agnostic on purpose: nothing here depends on which assistant you are using.

## What this project is

A TTRPG wiki for DMs and their players. Markdown files on disk are the source
of truth and are Obsidian-compatible; SQLite is a rebuildable index that powers
search and access control. A DM hosts it on their own machine and shares one
link per player.

The technical stack is Go, chi, Datastar, templ, goldmark and SQLite.

**Read `docs/spec.md` before writing any code.** It is the authoritative
specification. The ADRs in `docs/adr/` record decisions already made; do not
re-litigate one without a new ADR that supersedes it.

## Current state

- `README.md` and `LICENSE` (MIT, © 2026 Johan Englund) only.
- **No `go.mod`, no `cmd/`, no `migrations/`, no production code, no tests.**
- The full plan lives in `docs/spec.md`. The milestone list is the last section
  of that file.

Next milestone: **M0 — Foundation**. Its commit sequence is in
`docs/spec.md` § Milestones.

## Non-negotiable invariants

These are security and correctness properties. Each has a named test. If a
change appears to require breaking one, the change is wrong, or the invariant
needs an explicit decision recorded as a new ADR.

1. **Files are the source of truth.** Page content lives in
   `vault/<campaign>/**.md`. SQLite is a projection and may be dropped and
   rebuilt at any time with `wiki reindex --full`. Never treat the database as
   the only copy of anything. → [ADR 0001](docs/adr/0001-files-as-source-of-truth.md)

2. **Secrets are stripped server-side at render time.** `[!SECRET]` block
   content must not exist in any byte of a response a principal may not read.
   There is no CSS hiding, ever — not for secrets, not for drafts, not for
   anything. → [ADR 0007](docs/adr/0007-access-control-model.md)

3. **No `Store` method may return a page without the access predicate.** Search,
   listings, the page tree, backlinks and tag clouds all go through
   `page_acl_read` or a byte-identical `WHERE` clause. If you add a query over
   `pages`, you add the predicate, and you add a test.

4. **Rendered markdown is sanitised for every author, DM included.** Now that
   players author pages, the DM is the highest-value target. The sanitiser is
   not "on for untrusted authors only".

5. **There is exactly one render path.** It takes the full body plus an
   `access.Decision` and strips what must be stripped. A second "public render"
   path would mean a second set of golden tests and a second chance to leak.

6. **Fail closed.** The default secret policy permits nobody to see secrets, so
   an unfinished access-control feature leaks nothing.

## Conventions

- Module path: `github.com/popinjayjohn/dine-and-dash-semiplane`.
- Conventional Commits, with scopes: `vault`, `index`, `search`, `access`,
  `auth`, `render`, `plugin`, `http`, `sse`, `cli`, `dnd5e`.
- Every commit touching `internal/`, `cmd/`, `plugins/`, `migrations/` or
  `web/` must touch `CHANGELOG.md` in the same commit. CI enforces it.
- One milestone is one branch, one PR, one changelog section.
- Commits are small and independently green. A commit that breaks the build
  does not get pushed, not even "just to push it and fix it next".
- One concern per package, under `internal/`, except `cmd/wiki` and `plugins/`.
- Interfaces are declared by the consumer, never by the provider.
- Deterministic output. No `time.Now()` or `rand` outside an injected `Clock`
  or `IDGen` — this is what makes golden files work.

## Commands

Not available until M0 lands. The intended `Makefile` targets:

```
make check    # fmt, vet, lint, go test -race -shuffle=on ./...
make test     # tests only
make lint     # golangci-lint run
make cover    # coverage report and gate
make fuzz     # short fuzz runs
make run      # build and serve
make reindex  # wiki reindex --full against the local data dir
```

## Testing expectations

Every change ships with tests. Before proposing a milestone as done:

- `go test -race -shuffle=on ./...` is green.
- `go vet ./...` and `golangci-lint run` are clean.
- Coverage has not dropped below the gate.
- Golden files were regenerated deliberately and the diff was reviewed — a
  surprise golden diff is a bug until proven otherwise.
- New logic has a table test; anything parsing untrusted input has a fuzz
  target.
- Any new access-control path has a test that asserts a secret canary is
  **absent** from the raw response body, not merely hidden in the DOM.

## Ways to get this wrong

Concrete mistakes this project has already designed against:

- Adding a `SELECT ... FROM pages` without the ACL predicate, "just for the
  page tree".
- Filtering secrets in Go *after* building a search excerpt, so the snippet
  already contains the text.
- Hiding a draft or a secret with a CSS class, because it was quicker than
  changing the query.
- Sanitising only player-authored markdown, leaving the DM's path unguarded.
- Renaming a page file when its title changes, breaking every inbound
  `[[wiki link]]`.
- Serialising frontmatter and dropping keys the app does not understand, quietly
  deleting the DM's notes from their own vault.
- Accepting a raw `sqlite` `UPDATE` from a handler instead of going through
  `Store`.
