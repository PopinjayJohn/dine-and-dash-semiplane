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
- **M3 — Renderer is on `m3-renderer`.** `internal/render` is the goldmark
  pipeline with the wiki-link and callout extensions, the table of contents, the
  secret stripper, the sanitiser and the render cache. It has **no
  `internal/http` and no plugins**: nothing serves what it renders yet.
- **M4 — Index sync is on `m4-index-sync`.** `internal/index` reads a vault
  into the store, notices drift, rebuilds on request and watches for changes;
  `internal/lockfile` keeps two writers off one campaign; `wiki sync` and
  `wiki reindex --full` are the commands, so the Makefile's `reindex` target does
  something. It has **no `internal/http` and no plugins**: nothing serves the
  wiki yet, and the watcher is what M8's server will run.
- **The sync engine reads files and writes rows, and never writes a file.**
  That is the property ADR 0001 is about, it is the first thing a change here
  can break, and a test hashes every file's contents *and* modification time
  before and after a sync to keep it that way. The editor (M9) and the importer
  (M12) are the only things that will write markdown.
- **A page is "settled" when re-deriving it from its file would produce the row
  that is already there** — not when its content hash matches. A hash says a file
  has not changed, which is a different thing, and settling on the hash alone
  meant a row could rot in place unnoticed and a change to how fields are derived
  would leave every row stale with matching hashes. One function decides it, so
  `Sync` and `Check` cannot disagree about what is out of step.
- **Ownership is resolved but not stored.** A page is character-owned when its
  path begins with `characters/<slug>/` or its frontmatter declares
  `character: <slug>`, and the path wins where they disagree. The column arrives
  with M7; the rule and its validation are here, because the rule decides which
  subtree a player may write in and is easy to get subtly wrong.
- **The renderer's `Decision` is not `access.Decision`, and its zero value
  permits no secrets.** That is the safe direction: a caller that has not
  decided anything gets a page with no secrets in it, which is a missing
  feature rather than a disclosure. M7 replaces the field with the real
  decision, and the render cache is keyed by it, so a render made for a DM can
  never be served to a player.
- **The store has no access control yet, and says so.** There is no
  `visibility` column, no `owner_character_page_id` and no principal, so
  every page-returning method is campaign-scoped and unfiltered. That is
  correct for a schema that has nothing to filter on and it is *not* correct
  for a served application: M7 adds the columns and every one of those methods
  gains a principal and routes through `page_acl_read`, per invariant 3. Until
  then the store is reachable only from tests and from code that already knows
  the answer. A commit that adds a page query to the store must not ship with
  that comment removed and nothing in its place.
- `spike/datastar/` is a separate Go module. `go test ./...` at the root does
  not reach it; `make spike` does. It is deleted in M10.
- The full plan lives in `docs/spec.md`. The milestone list is the last section
  of that file, and each shipped milestone's commit sequence is recorded there
  too.

Next milestone: **M5 — Search**. Two FTS5 indexes — `pages` and
`pages_secrets_fts`, which holds the text a player may see — merged with
Reciprocal Rank Fusion, and **ACL in SQL** rather than in Go. Its key tests are
relevance, an FTS-injection corpus, "the secret never appears in a result", and
a benchmark. The M3 stripper keeps its zero value throughout: the index it
feeds is built from `body_public`, which is empty today and is M7's to fill.

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
