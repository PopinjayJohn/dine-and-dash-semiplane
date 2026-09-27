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
  resolved through an `os.Root`, and every write is atomic. It has **no
  `internal/http` and no plugins**, and it does not talk to the store or the
  renderer yet.
- **M3 — Renderer is on `m3-renderer`.** `internal/render` is the goldmark
  pipeline with the wiki-link and callout extensions, the table of contents, the
  secret stripper, the sanitiser and the render cache. It has **no
  `internal/http` and no plugins**: nothing serves what it renders yet.
- **M4 — Index sync is on `m4-index-sync`.** `internal/index` reads a vault into
  the store, notices drift, rebuilds on request and watches for changes;
  `internal/lockfile` keeps two writers off one campaign; `wiki sync` and
  `wiki reindex --full` are the commands, so the Makefile's `reindex` target does
  something. It has **no `internal/http` and no plugins**: nothing serves the wiki
  yet, and the watcher is what M8's server will run.
- **M5 — Search is on `m5-search`.** `internal/search` is the query language, the
  fusion and `Run`, all pure; `internal/store` owns the FTS5 grammar, the two
  indexes and the read predicate. The two lists are merged with Reciprocal Rank
  Fusion ([ADR 0009](docs/adr/0009-two-index-search-with-rrf.md)), and the ACL is
  in SQL rather than in Go. The query language is documented once, in
  `docs/search.md`. It has **no `internal/http`**: a search runs against a store,
  and nothing serves one yet.
- **A page's audience is recorded; it is not enforced yet.** `pages.visibility`
  exists and the sync writes the value it reads, because M4 was already reading
  and discarding it. **Every other page-returning store method is still
  campaign-scoped and unfiltered** — `GetPage`, `GetPageByID`, `ListPages`,
  `Backlinks`, the target lookups. Only the two search queries filter. That is
  correct only while nothing outside the package can reach them, and M7 gives each
  of them a principal. See [ADR 0015](docs/adr/0015-search-records-the-audience.md).
- **`pages.body_public` is empty and stays empty until M7.** It is what the public
  index is fed from, so a value in it is a value anybody can find. Working out
  which text a *principal* may be shown needs access control; until that exists the
  only safe value is the empty string. So a page is findable by title, aliases,
  tags and type, and not by prose — a missing feature, in the safe direction.
  **The secret index is populated**: which text is secret is a *parsing* question,
  so a DM can find their own secrets by content today.
- **The read predicate is written once, in `internal/store/acl.go`,** and both
  search queries go through it. Its ownership test is `1 = 0` until
  `principal_characters` exists, and a test asserts the branch is still there —
  leaving it out would silently widen every `dm-and-owner` page. Never widen that
  file without reading its header.
- **Nothing a caller typed is concatenated into SQL.** Every query clause is
  quoted with FTS5's own string quoting and the expression is a bound parameter.
  There is a corpus and a fuzzer for it, and a test that strips the literals back
  out and asserts nothing but the builder's own operators are left.
- **The sync engine reads files and writes rows, and never writes a file.**
  That is the property ADR 0001 is about, it is the first thing a change here
  can break, and a test hashes every file's contents *and* modification time
  before and after a sync to keep it that way. The editor (M9) and the importer
  (M12) are the only things that will write markdown.
- **A page is "settled" when re-deriving it from its file would produce the row
  that is already there** — not when its content hash matches, and not only for
  the page row: the aliases, the link graph and both search rows are compared the
  same way. A hash says a file has not changed, which is a different thing, and
  settling on the hash alone meant a row could rot in place unnoticed and a change
  to how fields are derived would leave every row stale with matching hashes. One
  function decides it, so `Sync` and `Check` cannot disagree about what is out of
  step.
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
- `spike/datastar/` is a separate Go module. `go test ./...` at the root does
  not reach it; `make spike` does. It is deleted in M10.
- The full plan lives in `docs/spec.md`. The milestone list is the last section
  of that file, and each shipped milestone's commit sequence is recorded there
  too.

Next milestone: **M6 — Auth and principals**. Token mint and verify, the cookie
exchange, sessions, rate limits, revocation, the audit log, and **character
binding** — which is the `principal_characters` table the ownership test has been
waiting for. Its key tests are every hardening item in §10 as a named test.

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
