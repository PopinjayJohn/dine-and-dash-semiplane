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

- **M0 — Foundation is landed on `m0-foundation`.** `go.mod`, `Makefile`,
  `.golangci.yml`, GitHub Actions, `.gitmessage`,
  `scripts/check-changelog.sh` and the Datastar spike all exist and CI is
  wired to run them.
- **M1 — Domain and store is on `m1-domain-store`.** `internal/domain`
  describes the world, `internal/clock` and `internal/idgen` are the injected
  sources of time and identity, `migrations/` holds the base schema and the
  runner that applies it, and `internal/store` projects campaigns, pages,
  revisions and the link graph into SQLite.
- **M2 — Obsidian storage is on `m2-obsidian-storage`.** `internal/vault`
  reads and writes the markdown files: a Document is the bytes it was read as,
  the frontmatter is a parse tree rather than a map, paths are checked and then
  resolved through an `os.Root`, and every write is atomic. There is **no
  `internal/http`, no plugins, no front end, no search and no access control**,
  and the store and the vault do not talk to each other yet.
- **The store has no access control yet, and says so.** There is no
  `visibility` column, no `owner_character_page_id` and no principal, so
  every page-returning method is campaign-scoped and unfiltered. That is
  correct for a schema that has nothing to filter on and it is *not* correct
  for a served application: M7 adds the columns and every one of those methods
  gains a principal and routes through `page_acl_read`, per invariant 3. Until
  then the store is reachable only from tests and from code that already knows
  the answer. A commit that adds a page query to the store must not ship with
  that comment removed and nothing in its place.
- **The vault and the store are separate halves, on purpose.** Nothing in M2
  knows what a `domain.Page` is, and nothing in the store knows what a file
  is. M4 joins them, and it is the first place a change can break the
  zero-byte-diff promise: anything that reads a page and writes it back must
  write back the document it read, not a re-serialisation of the values it
  took out of it.
- `spike/datastar/` is a separate Go module. `go test ./...` at the root does
  not reach it; `make spike` does. It is deleted in M10.
- The full plan lives in `docs/spec.md`. The milestone list is the last section
  of that file, and each shipped milestone's commit sequence is recorded there
  too.

Next milestone: **M3 — Renderer**. The goldmark pipeline, the wiki-link
extension, the callout extension, the `[!SECRET]` parsing and the fail-closed
stripper, the table of contents, the sanitiser and the render cache. Its key
tests are golden files, a no-panic fuzz target, an XSS corpus and cache
invalidation. The stripping is the whole milestone: the policy's default
permits nobody to see a secret, so a half-finished M3 leaks nothing.

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

`make help` lists these. `make check` is the one to run before every push; CI
runs the same targets, so a green local `make check` is a green CI.

```
make check    # fmt-check, vet, lint, go test -race -shuffle=on ./...
make test     # tests only
make lint     # golangci-lint run
make cover    # coverage report, fails below the 80% gate
make fuzz     # short fuzz runs
make spike    # the Datastar spike, a separate module under spike/
make run      # build and serve
make reindex  # wiki reindex --full against the local data dir
make install-tools  # the pinned golangci-lint and templ
```

`run` and `reindex` name subcommands that arrive in later milestones, so they
do nothing yet. The pinned tool versions live in the `Makefile` and nowhere
else; CI reads them from there with `make print-<tool>-version`.

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
