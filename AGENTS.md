# AGENTS.md

Guidance for AI coding agents — and humans — working in this repository. Tool
agnostic on purpose: nothing here depends on which assistant you are using.

## What this project is

A TTRPG wiki for DMs and their players. Markdown files on disk are the source
of truth and are Obsidian-compatible; SQLite is a rebuildable index that powers
search and access control. A DM hosts it on their own machine and shares one
link per player.

The technical stack is Go, chi, Datastar, templ, goldmark and SQLite.

**Read [`docs/spec.md`](docs/spec.md) before writing any code.** It is the
authoritative specification. The ADRs in `docs/adr/` record decisions already
made; do not re-litigate one without a new ADR that supersedes it. This file is
a summary and a set of guard rails — where the two disagree, the spec and the
ADR win, and the correction belongs here. See
[ADR 0025](docs/adr/0025-the-documentation-authority-order.md).

## Where things live

| Package | Owns | Constrained by |
|---|---|---|
| `internal/domain` | the model: campaigns, pages, principals | — |
| `internal/vault` | the bytes on disk; frontmatter as a parse tree | [ADR 0013](docs/adr/0013-frontmatter-parse-tree.md), invariant 1 |
| `internal/index` | the walk from files to rows, drift, the watcher | [ADR 0001](docs/adr/0001-files-as-source-of-truth.md) |
| `internal/store` | the SQLite projection and the read predicate | invariant 3, [ADR 0007](docs/adr/0007-access-control-model.md) |
| `internal/access` | `For`, the rights matrix — pure, no store, no clock | [ADR 0007](docs/adr/0007-access-control-model.md) |
| `internal/render` | the one render path: extensions, stripper, sanitiser, cache | invariants 2, 4, 5, [ADR 0014](docs/adr/0014-secrets-leave-the-tree.md) |
| `internal/edit` | the writer, and the gate that goes before the file | [ADR 0019](docs/adr/0019-the-writer-checks-before-it-writes.md) |
| `internal/auth` | minting, redeeming, sessions, the redacting logger | [ADR 0003](docs/adr/0003-url-token-auth.md), [ADR 0016](docs/adr/0016-auth-decides-and-returns-values.md) |
| `internal/http` | the router, the middleware, the headers, the templates | — |
| `internal/sse` | four functions and a hub | [ADR 0006](docs/adr/0006-sse-abstraction.md), [ADR 0021](docs/adr/0021-one-reader-per-page.md) |
| `internal/search` | the query language and `Run` — all pure | [ADR 0009](docs/adr/0009-two-index-search-with-rrf.md), [ADR 0015](docs/adr/0015-search-records-the-audience.md) |
| `internal/plugin` | the compile-time registry, one accessor per capability | [ADR 0002](docs/adr/0002-plugin-registry-in-process.md), [ADR 0022](docs/adr/0022-where-a-plugin-sits.md) |
| `internal/fields` | the seam between a plugin's claims and a page's keys | [ADR 0023](docs/adr/0023-a-fields-value-is-redacted-under-the-decision.md) |
| `internal/events` | the bus a plugin subscribes to | [ADR 0002](docs/adr/0002-plugin-registry-in-process.md) |
| `internal/config` | `config.yaml` and the `DDSP_*` settings | [ADR 0011](docs/adr/0011-single-binary-data-directory-backup-unit.md) |
| `internal/datadir` | where a DM's data directory is | [ADR 0011](docs/adr/0011-single-binary-data-directory-backup-unit.md) |
| `internal/lockfile` | one writer per campaign, on disk | [ADR 0011](docs/adr/0011-single-binary-data-directory-backup-unit.md) |
| `internal/safe` | recovering a panic from code the application does not own | invariant 6 |
| `internal/clock` | the injected source of time | — |
| `internal/idgen` | the only source of identifiers | — |
| `internal/logfmt` | the `text` and `json` `log/slog` handlers | — |
| `internal/version` | the build version | — |
| `internal/store/testsuite` | the contract every `Store` runs | — |
| `internal/plugin/contract` | the suite every plugin runs | — |
| `migrations/` | the versioned schema and the runner | [ADR 0012](docs/adr/0012-migration-runner-in-repo.md) |
| `cmd/wiki` | the CLI, and the compile-time plugin list | — |
| `plugins/` | the bundled plugins: `houserules`, `spoilerbox`, `wordcount`, `dnd5e` | [ADR 0010](docs/adr/0010-core-system-agnostic-dnd5e-as-plugin.md) |
| `web/` | the embedded static assets | [ADR 0008](docs/adr/0008-datastar-release-and-client-pin.md) |

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

## Known gaps

- **Next milestone: none planned.** M13 closed `v0.1.0` and the spec's table
  ends there. The next work is whatever the first user of this software
  reports, and [`docs/spec.md` §16](docs/spec.md#16-milestones) is the place to
  write it down.
- **The save button has no optimistic state.** `web/static/wiki.js` has the toast
  region, the patch handler and the editor's autosave, but not the `data-ds-*`
  attributes that would show a save as pending before the server answers. An
  optimistic save is a claim about what the server will do, and plugins can
  change what a save does, so the attributes belong with the plugin framework.
  The changelog records this where M10's scope is.

## Ways to get this wrong

Each line is a guard rail this project has already paid for. The reasoning, the
bug it caught and the test that holds it are in
[`docs/pitfalls.md`](docs/pitfalls.md) — read that file when a change touches
any of these.

### [The store and the read predicate](docs/pitfalls.md#the-store-and-the-read-predicate)

- The ownership test correlates on the page's **owner**, not on the page.
- The audience test needs its role conjunct, or an unidentified request reads
  the whole campaign.
- The predicate asks whose *campaign* the principal is of, not merely which one
  the caller asked about.
- Every query over `pages` carries the predicate, including the page tree and
  the tag cloud. A handler is not the only caller.
- The write gate is in the store, and it checks ownership rather than position.
- A write refusal is `ErrNotAllowed`, deliberately not `ErrNotFound`: a read must
  not confirm a page exists.
- No handler issues a raw `sqlite` `UPDATE`; everything goes through `Store`.
- Nothing a caller typed is concatenated into SQL — quote with FTS5's own
  quoting and bind the expression.
- The sync engine writes rows and never writes a file.
- A page is settled when re-deriving it would produce the row that is already
  there, not when the content hash matches: the aliases, the link graph and both
  search rows are compared the same way.
- A page's owner is the *character*, and the path wins over the frontmatter.

### [Secrets and the render path](docs/pitfalls.md#secrets-and-the-render-path)

- The render cache is keyed by content, campaign, decision and role — and by no
  principal, which would be wrong rather than merely wasteful.
- A page rendered with no campaign resolves no links. That is the fail-closed
  answer, and a handler that forgets the field should be a visible bug.
- `pages.body_public` is `render.PublicText`: plain text, unrevealed secrets
  removed, revealed ones kept because everyone who can read the page can read
  those.
- A `[[link]]` resolves for its reader, and the reader is not the DM.
- The link graph is the DM's, on purpose. The file says a link exists; the
  render says whether *this* reader may follow it. Both are true.
- Secrets are stripped from the tree, not filtered out of the HTML afterwards.
- A secret is never hidden with a CSS class, a draft either.

### [Authentication and sessions](docs/pitfalls.md#authentication-and-sessions)

- A token cannot be printed: `String`, `GoString` *and* `Format`, because `%#v`
  consults neither of the first two and `%x` on a struct hex-encodes its fields.
- The redacting logger is a *handler*, so a caller who logs a whole request
  struct is covered by the same rule, and there is no unwrapped constructor to
  route around.
- The query string is not logged. `?k=<token>` is the share-link credential.
- The cookie's name is not a constant: a `__Host-` cookie without `Secure` is
  refused *silently*, which is a wiki that forgets every player after every link.
- A minted link is shown once, and the list shows no token, no hash and no hint.

### [Sync, editing and the writer](docs/pitfalls.md#sync-editing-and-the-writer)

- `internal/edit` is the only writer, and the gate runs **before** the file is
  written — the index watcher writes rows as the DM, so a refused write that
  touched the disk is laundered into the index.
- The sync takes a principal, and the gate sees the *derived* owner, so a player
  cannot present somebody else's character as an owner.
- A write is gated even when it would change nothing.
- A preview is the save's own four steps — parse, derive, decide, render — in
  the same function.
- A conflict is three texts and no merge, and the base is empty rather than
  guessed when this application has not kept the text.
- A rename rewrites links by byte offset, never by re-rendering, and a page is
  never renamed because its title changed.
- Serialising frontmatter never drops keys the application does not understand.
- An archive removes the file and keeps the row; a purge deletes the row, and
  says what it loses before it is asked for.

### [HTTP, headers and the stream](docs/pitfalls.md#http-headers-and-the-stream)

- `?raw=1` and `?stream=1` are query parameters, because the vault owns the path
  namespace and a segment would be a first-segment name it could not use.
- A `?raw=1` body is `render.PublicText` under the request's decision, never
  `page.Body`.
- The 500 page asks the store for nothing. The store is the thing that just
  failed, and one that panics takes the process down from inside the recovery
  handler.
- Every response carries a `Content-Security-Policy`, whatever the nonce did. A
  failed `crypto/rand` yields `script-src 'none'`, never no header.
- A rate limit is a 429 with a `Retry-After`, not the default 404.
- The SSE hub carries a notice, and each subscriber re-renders under its own
  decision. The publisher never renders once and hands the result to everyone.

### [Search](docs/pitfalls.md#search)

- The last term is a prefix, and only when asked for by name — the `*` is the
  one piece of FTS5 syntax the match expression emits.
- `search.Hit` carries the audience, or a policy can only hide everything.
- Secrets are filtered in SQL, not in Go after a snippet has been built.

### [Plugins](docs/pitfalls.md#plugins)

- A tree hook runs **after** the secret stripper; an HTML hook runs **before**
  the sanitiser. Both placements are the security argument.
- A plugin's output is filtered by the same allow-list as the DM's own markdown.
- A plugin may only *narrow* an access decision, and a failing policy **denies**.
  A failing render hook is skipped instead — the asymmetry is deliberate.
- A plugin's policy narrows in Go, on top of the SQL predicate, and never the
  other way round.
- A plugin's search field reaches the public index and nothing else.
- The render cache key carries no plugin field: each renderer owns its cache.
- There is no cache-invalidation capability, and `Cache.Clear` has no caller on
  purpose. A capability that exists only to be demonstrated is not a capability.
- A plugin's command names cannot collide with the core's.

### [Fields and the `dnd5e` ruleset](docs/pitfalls.md#fields-and-the-5e-ruleset)

- A field's value is redacted under the decision before a plugin sees it.
- Field HTML goes into the page's own buffer, so the one sanitiser runs over it.
- There is no fallback renderer: a key nobody claimed draws nothing.
- A claim and a key the DM wrote are compared folded.
- `Page.Fields` is not a cache key field — `ContentHash` already covers it.
- A field renderer that fails draws nothing and the page still renders.
- A claimed key is read through a seam that bypasses the vault's *write-side*
  closed set, and only the write side.

### [Configuration, backup and release](docs/pitfalls.md#configuration-backup-and-release)

- A missing config file is fine; a broken one and a misspelled key are both
  errors that name themselves, and the environment beats the file.
- `trusted_proxies` takes addresses and CIDRs. A hostname is refused, because it
  can resolve somewhere else tomorrow.
- The database is copied with `VACUUM INTO`, and a backup does not take the
  serve lock.
- An export is a vault and is deterministic, so two exports of an unchanged
  campaign diff clean.
- An import writes nothing until it has shown you what it would do.
- `--lan` turns TLS on and prints the certificate's SHA-256 in `openssl` form,
  so the browser warning can be checked rather than dismissed.
- The e2e test is behind a build tag and is not Playwright: the claim is one
  static binary and no C toolchain.

## Conventions

- Module path: `github.com/popinjayjohn/dine-and-dash-semiplane`.
- Conventional Commits, with scopes: `vault`, `index`, `search`, `access`,
  `auth`, `render`, `plugin`, `http`, `sse`, `cli`, `dnd5e`.
- Every commit touching `internal/`, `cmd/`, `plugins/`, `migrations/` or
  `web/` must touch `CHANGELOG.md` in the same commit. CI enforces it.
- One milestone is one branch, one PR, one changelog section.
- Commits are small and independently green. A commit that breaks the build
  does not get pushed, not even "just to push it and fix it next".
- One concern per package, under `internal/`, except `cmd/wiki`, `plugins/` and
  `web/` — the last of which exists because `go:embed` cannot reach outside its
  own package directory and the static assets are in `web/static/`.
- Interfaces are declared by the consumer, never by the provider.
- Deterministic output. No `time.Now()` or `rand` outside an injected `Clock`
  or `IDGen` — this is what makes golden files work.
- **A lesson learned about this codebase goes in `docs/pitfalls.md`, not here.**
  This file is read by every agent on every task, so it carries the guard rail
  and the link. A paragraph of retrospective is an archival file's job.

## Commands

`make help` lists these. `make check` is the one to run before every push; CI
runs the same targets, so a green local `make check` is a green CI.

```
make check    # fmt-check, generate-check, vet, lint, go test -race -shuffle=on ./...
make test     # tests only
make lint     # golangci-lint run
make cover    # coverage report, fails below the 80% gate
make fuzz     # short fuzz runs
make run      # build and serve
make reindex  # wiki reindex --full against the local data dir
make generate # regenerate the templ templates from their .templ sources
make install-tools  # the pinned golangci-lint and templ
```

The pinned tool versions live in the `Makefile` and nowhere else; CI reads them
from there with `make print-<tool>-version`.

**The templ templates are committed as generated code, and `make check` fails if
they are stale.** `internal/http/templates_templ.go` is what `go build` compiles
and what the reviewer reads, so it is committed rather than built — but a `.templ`
edited without regenerating it is a template and a `_templ.go` that disagree,
and the disagreement is invisible until the page renders the old thing.
`make generate` rewrites them and `make generate-check` is in `make check` and in
CI. The generated files are excluded from `.golangci.yml` and from the coverage
profile, for the same reason: they are not code anybody wrote, and measuring them
says something about the templates' element branches rather than about whether the
application is tested.

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

**Prose in this repository is British.** `.golangci.yml` sets `misspell` to
`locale: UK`, so `sanitiser`, `serialise`, `normalise` and `recognise` are not
stylistic preferences — the other spellings are a lint failure.

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
- A change to a document that moves a `§N` reference or a link has kept
  `docs/doclinks_test.go` green. That test is the reason a citation in a code
  comment cannot rot into a pointer to nothing.

## Further reading

| Document | What it is for |
|---|---|
| [`docs/spec.md`](docs/spec.md) | the authoritative specification, §1–§15 |
| [`docs/adr/`](docs/adr/) | the decisions, and why each was made |
| [`docs/pitfalls.md`](docs/pitfalls.md) | the constraint behind each guard rail above, and its test |
| [`docs/security.md`](docs/security.md) | the threat model, and the test that enforces each control |
| [`docs/plugins.md`](docs/plugins.md) | writing a plugin |
| [`docs/search.md`](docs/search.md) | the search query language |
| [`docs/milestones.md`](docs/milestones.md) | a historical record: what each shipped milestone did |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | the workflow and the bar a change has to clear |
| [`CHANGELOG.md`](CHANGELOG.md) | what each version was |
