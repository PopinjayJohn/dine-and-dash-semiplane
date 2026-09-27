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
- **M6 — Auth and principals is on `m6-auth-principals`.** `internal/auth` mints a
  share link, redeems it for a session, rate limits redemption, rotates sessions on
  a role or binding change, and logs through a handler that redacts. It has **no
  `internal/http` and no plugins**: the exchange returns values and M8's router
  turns them into a response, because every property in §10 is testable with two
  arguments and a store and is not testable with a server — see
  [ADR 0016](docs/adr/0016-auth-decides-and-returns-values.md). The cookie's
  attributes are ADR 0003's and are M8's.
- **M7 — Access control is on `m7-access-control`.** `internal/access` is the one
  place that answers "what may this principal do with this page", and `For` is
  pure — no store, no clock — because the rights matrix has 36 cells and the only
  way to be sure all 36 behave as documented is to write down all 36 and run them
  in a millisecond. Every page-returning store method takes a principal,
  `UpsertPage` does too, and `render.Decision` *is* `access.Decision`.
- **The read predicate's ownership test is real, and it correlates on the page's
  *owner*** — `pc.character_page_id = p.owner_character_page_id`, the spec's form.
  The version M6 shipped correlated on the page itself, because the owner column
  did not exist yet, and it said a player may read the one file they are bound to
  and not the twenty notes under it.
- **The audience test requires a role.** `? = 'player' AND p.visibility =
  'players'`, not `p.visibility = 'players'` alone. Without the conjunct a request
  that identified nobody read the whole campaign, and
  `TestStoreReadPredicateMatchesResolver` found it on its first run. **The spec
  wrote the same clause**; §8 now says otherwise.
- **The write gate is in the store and checks ownership, not position.** It is in
  the store because a handler can forget a line and a signature cannot. It checks
  ownership because that is a question about two tables; the *position* half of
  §8's rule is `internal/index`'s `OwnerOf` and duplicating it would be a third
  implementation of one rule. The residual is named in `writes.go` and belongs to
  M9's editor. See [ADR 0017](docs/adr/0017-the-write-gate-lives-in-the-store.md).
- **A write refusal is `ErrNotAllowed` and deliberately not `ErrNotFound`.** A read
  must not confirm a page exists; a write is a request about a page the caller
  already holds.
- **The read predicate's ownership test is real.** It was `1 = 0` for two
  milestones, because no principal owned a page; it is now an `EXISTS` over
  `principal_characters`, correlated on the page. The correlation is the whole of
  it — a subquery that compared against the *campaign* would admit every player to
  every `dm-and-owner` page in it. Never widen that file without reading its
  header.
- **`pages.body_public` is filled, so a page is findable by its prose.** It is
  `render.PublicText`: the page's text with its unrevealed secrets removed, as
  plain text rather than markdown, because the column is read by the tokenizer and
  by nothing else. A *revealed* secret stays — §9 says it is visible to everyone
  who can read the page, and the public search's rows are filtered by the read
  predicate, so every principal who can reach a hit may read it. A callout's
  *title* is in neither half: it is an attribute on the node, not text in it.
- **Nothing a caller typed is concatenated into SQL.** Every query clause is
  quoted with FTS5's own string quoting and the expression is a bound parameter.
  There is a corpus and a fuzzer for it, and a test that strips the literals back
  out and asserts nothing but the builder's own operators are left.
- **A share-link token cannot be printed.** `auth.Token` implements `String`,
  `GoString` *and* `Format`, because `%#v` does not consult `String` and `%x` on a
  struct hex-encodes its fields. The redacting logger is a *handler*, so a caller
  who logs a whole request struct is covered by the same rule as one who logs a
  token deliberately — and the named test greps the log output of a full flow.
  Note that a token's SHA-256 is 64 hex characters and so is a token, so the shape
  check redacts a hash along with a credential; that is the price and the log
  carries the principal id instead.
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

Next milestone: **M8 — Web shell**. The chi router, the layouts, the Datastar
components, the SSE hub, `/_/healthz`, and **the wiring for everything M6 and M7
built** — the cookie attributes ADR 0003 has been holding, the login and redeem
routes, and the first `TestSecretStrippedFromAllSurfaces`, which walks the
player-reachable routes and asserts a canary is absent from each raw response
body. It has no plugins yet; M11 brings the plugin host.

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

**The Go toolchain is pinned in the `Makefile` too, and it is pinned for
formatting.** `gofmt` may change its output in any release, on purpose, and
`make fmt-check` is a byte-for-byte comparison — so a developer's `gofmt` and
CI's disagreeing is a red build over nothing. `make fmt` and `make fmt-check`
run the *pinned* toolchain's `gofmt` (reached with `GOTOOLCHAIN` and
`go env GOROOT`, because `gofmt` is a separate binary from the `go` command and
`GOTOOLCHAIN` does not reach the one on `PATH`), and `make check-go-version`
fails if the pin and the `go` line in `go.mod` drift apart, since CI installs Go
from `go.mod` and formats with the pin. **Do not run a bare `gofmt` on this
repository** — use `make fmt`, or `make fmt-check` to check. One consequence to
know: a map or struct literal with a key far wider than its neighbours is
formatted differently by Go 1.25 and Go 1.26+, so keep test-table keys of a
similar width.

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
