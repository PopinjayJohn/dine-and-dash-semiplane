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
  `.golangci.yml`, GitHub Actions, `.gitmessage` and
  `scripts/check-changelog.sh` all exist and CI is wired to run them. The
  Datastar spike that stood in for `internal/sse` was deleted in M10, and the
  formatting toolchain is now **templ as well as golangci-lint**: see
  `make generate` below.
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
- **Ownership is resolved, and the page's *owner* is the character.** A page is
  character-owned when its path begins with `characters/<slug>/` or its
  frontmatter declares `character: <slug>`, and the path wins where they
  disagree. The `dm-and-owner` ownership test correlates on the page's
  **owner**, which is not the same as correlating on the page: the version that
  correlates on the page gives a player the one file they are bound to and not
  the twenty notes under it.
- **The renderer's `Decision` is `access.Decision` and the render cache is keyed
  by it and by the campaign.** Two campaigns can each hold a
  `locations/rivergate` with byte-identical content — two DMs who both started
  from the same template — and a shared entry would serve one campaign's URLs
  inside the other's HTML. A page rendered with no campaign resolves no links,
  which is the same fail-closed answer as everywhere else and the reason a
  handler that forgets the field is a visible bug.
- **The read predicate asks whose campaign the principal is of, not just which
  campaign the caller asked about.** Nothing had ever asked before M8, because
  every caller was the sync engine passing `AsDM(campaignID)` and is therefore
  always of the campaign it is reading. A predicate that is only correct for
  callers who get their arguments right is a predicate one handler away from a
  disclosure. Tenancy is not authorisation, so it is not in `access.For`, and
  the 36-cell matrix test is unaffected on purpose.
- **The SSE hub carries a notice, not a page.** Each subscriber re-reads and
  re-renders under its own decision, through the same `renderPage` the page route
  uses. The obvious design — render once, hand the same component to every
  watcher — has no correct version, because a DM and a player can be watching
  the same page and the publisher can only pick one decision. It is also what
  makes dropping a frame sound: a change is not a delta, so a skipped
  notification is one the next read would have replaced anyway.
- **`?raw=1` and `?stream=1` are query parameters, not path segments.** A path
  segment would be a first-segment name the vault could not also use, and the
  vault is the source of truth, so it wins any argument about what a URL may
  look like. The raw endpoint's body is `render.PublicText` under the same
  decision as the HTML, which is what keeps it from being the place a secret
  leaks.
- **The 500 page asks the store for nothing.** The store is the thing that has
  just failed, and a store that *panics* rather than errors takes the process
  down from inside the recovery handler, with every other player's session on
  it. `TestAPanicIsAPageAndNotADeadProcess` is the named test.
- **The query string is not logged.** `?k=<token>` is the share-link credential
  and `r.URL.String()` would put it in every log line, in every proxy in front
  of the server, and in whatever a DM pastes into a bug report.
- **`internal/edit` is the only writer, and it checks before it writes.** The
  order is the design: read the file, compare its hash, **ask the store's gate
  about the content**, write the file, re-derive the row, keep the old text. The
  gate before the write because **the index watcher writes rows as the DM** — a
  player's file on disk for the length of a rollback is a file the watcher will
  index, as the DM, into a page the gate refused. A refused write is laundered
  into the index through the one path that writes rows without asking.
- **The sync takes a principal, and the gate sees the *derived* owner.** So a
  player cannot present somebody else's character as the owner of a page: the
  derivation decides who the owner is and the gate checks that, and neither is a
  value in a request. This closed the residual ADR 0017 left for the editor.
- **A write is gated even when it would change nothing.** A sync writes only if
  the index is not already what the file says, so a player re-saving unchanged
  content never reached the gate. A no-op is neither a refusal nor a success.
- **A preview is the save's own four steps** — parse, derive, decide, render —
  in the same function. It started as a handler that rendered the whole file and
  produced an `<hr>` where the frontmatter fences were. One render path, one
  place the answer is made.
- **A conflict is three texts and no merge.** A merge is a decision about
  somebody's prose, and a server that makes it silently has edited a DM's page
  without asking. The base is empty — not guessed — when this application has not
  kept the text the edit was made from.
- **A rename rewrites links by byte offset, never by re-rendering.** A
  `WikiLink` carries no source segment, so an AST rewrite would have to
  re-serialise the page, and re-serialising a DM's markdown is the one thing
  this project must never do to a file. goldmark is used for the one thing it
  is reliable about, which is where the code blocks are.
- **An archive removes the file and keeps the row; a purge deletes the row.** The
  pair is recoverable-and-not, and a purge asks for a typed confirmation because
  a browser's `confirm()` is suppressed by a prefetch and is not announced by a
  screen reader.
- **The users page mints a link and shows it once, and the list shows no token,
  no hash and no hint.** Four characters of a 32-byte credential is a
  fingerprint worth having in a list of six people at a table.
- **A link resolves for its reader, and the reader is the DM no more.** The
  resolver reads `domain.PrincipalFrom(ctx)` rather than `AsDM`, so a `[[link]]`
  to a `dm-only` page is unresolved for a player and live for the DM. Before
  this, a player could tell which paths *exist* by whether a link came back
  resolved, and a search dropdown and a change log were two more ways to ask
  the same question. See [ADR 0020](docs/adr/0020-link-resolution-is-campaign-wide.md).
- **A rendered page is a function of (content, campaign, decision, role).**
  `access.Decision` has a fifth field, `ReadsAll`, and the cache key has it
  with it: on a `dm-and-owner` page the owner and the DM share a
  `CanSeeSecrets` and resolve the page's links differently, so one cache class
  held two readers. Adding a *principal* to the key would be wrong rather
  than wasteful — every player would get an entry for identical bytes — and
  `TestTheCacheKeyNamesNoPrincipal` is the guard on that.
- **The link graph is still the DM's, deliberately.** The graph answers "what
  does this file point at", which is the DM's question and the sync's. A
  campaign holds both answers at once on purpose: the file says a link exists,
  the render says whether *this* reader may follow it.
- **The SSE hub still carries a notice and never content.** The session log is
  a subscriber on a campaign-wide topic rather than a feature, and a stream's
  patch handler honours one selector and drops any other — a hijacked stream
  can at worst put a stale page on screen.
- **A search box is a box: the last term is a prefix.** `fort` finds the
  fortified town, or a box shows a result only after the last character of the
  word that finds it. Only the *last* term, and only when asked for by name
  (`search.WithPrefixLastTerm`), because the `*` is the one piece of FTS5 syntax
  the match expression emits.
- The full plan lives in `docs/spec.md`. The milestone list is the last section
  of that file, and each shipped milestone's commit sequence is recorded there
  too.

- **M11 — Plugin framework is on `m11-plugins`.** `internal/plugin` is the
  compile-time registry [ADR 0002](docs/adr/0002-plugin-registry-in-process.md)
  described, with one accessor per capability rather than a `Capabilities` struct;
  `internal/plugin/contract` is the suite every plugin runs; `plugins/` holds the
  three the milestone names — `houserules`, `spoilerbox`, `wordcount` — and
  `cmd/wiki/plugins.go` is the compile-time list. `docs/plugins.md` is the
  authoring guide and [ADR 0021](docs/adr/0022-where-a-plugin-sits.md) is the
  decision.
- **A tree hook runs after the secret stripper, and an HTML hook before the
  sanitiser.** Those two placements are the whole of M11's security argument. A
  tree hook after the stripper is handed a tree with no secret text in it, so no
  transform of it can put any back; a hook that ran *before* the stripper could
  lift a `[!SECRET]` callout into the open body and the stripper would have
  nothing left to remove. An HTML hook after `Sanitise` would be a way for a
  plugin to put unsanitised HTML on a page a player reads — "a plugin is not an
  author" is not an exception this codebase can make.
- **A plugin's output is therefore filtered by the same allow-list as the DM's
  own markdown**, which is also why `house-rules` needed no sanitiser change:
  `callout-[a-z0-9-]+` is the one class shape the sanitiser admits on purpose,
  and §12's callout type turned out to be the seam M3 left for this. A plugin
  that needs an element or a class the core does not allow discovers it cannot
  have one.
- **A plugin may only *narrow* an access decision, and a failure denies.**
  `access.Policies.Apply` ANDs the plugin's answer with the core's field by
  field, so returning "granted" gets nothing. The failure direction is the
  opposite of a render hook's — a panicking hook is skipped because the render is
  still correct without it, and a panicking *policy* denies because the answer it
  was going to give is the one keeping a page hidden.
- **A plugin's policy narrows what is served and listed, in Go, on top of the
  SQL read predicate.** The SQL is invariant 3 and a policy is a second, stricter
  layer, never a looser one. Three surfaces apply it and each was found by a
  test rather than by reading the code: the page route (which computes its
  decision *before* branching to `?raw=1` and `?stream=1`, or the same page is
  served two ways under two decisions), the editor (a save is a POST and a POST
  is something a player can send without loading the form), and the two
  listings. `search.Hit` carries `Visibility` and `OwnerCharacterPageID` because
  a policy that cannot see a hit's audience can only say "hides everything", and
  the dropdown is a list of page *titles* typed one character at a time.
- **A plugin's search field reaches the public index and nothing else.** One
  shared `extra` column, because FTS5's column set is fixed at table creation and
  a per-plugin column would be a per-plugin migration. A plugin that could index
  into the private index could put a value in front of a principal the read
  predicate never admitted.
- **The render cache key carries no plugin field, and `cache.go` argues why:** a
  renderer's hooks are fixed at construction and each renderer owns its cache, so
  two renderers with different plugins never consult the same map. What *would*
  be unsafe is a persisted render, and nothing persists one. `CacheKey.Type` was
  added late and for the same class of reason — a `type:` change with an
  unchanged body is a change to the output, because a plugin's hook may render a
  page differently according to it.
- **`house-rules` was going to demonstrate an event subscriber that invalidates
  the render cache, and there is no such capability.** The key already holds the
  content hash, so a saved page is a new key and `Cache.Clear` has no caller for
  the same reason. A capability that exists only to be demonstrated is not a
  capability.

Next milestone: **M12 — D&D 5e plugin and character sheets.** The `dnd5e` plugin as
the first thing in this repository that is *about* a game rather than *for* one:
the ruleset plugin the spec names, the `statline` field type, and a character sheet
that is a page template with a field renderer. It is the first milestone whose
demonstration is a plugin a user would actually want, which is also the first
milestone where a plugin's *output* rather than a DM's markdown is the thing under
review.

**M10's optimistic fragments are the one piece of its list that is not finished.**
`web/static/wiki.js` has the toast region and the patch handler, and the editor's
autosave, but the save button does not yet show an optimistic state — the
`data-ds-*` attributes for it are M11's work, because an optimistic save is a claim
about what the server will do and M11 is where a plugin can change that. The
changelog says so where the milestone's scope is recorded.

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
- One concern per package, under `internal/`, except `cmd/wiki`, `plugins/` and
  `web/` — the last of which exists because `go:embed` cannot reach outside its
  own package directory and the static assets are in `web/static/`.
- Interfaces are declared by the consumer, never by the provider.
- Deterministic output. No `time.Now()` or `rand` outside an injected `Clock`
  or `IDGen` — this is what makes golden files work.

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
- Asking a store method for a page with a *different* campaign's id and a
  principal from this one, and trusting the role clause to notice.
- Rendering a page in the SSE publisher and handing the result to every
  subscriber, because that is the one place a DM's page and a player's page are
  the same string.
- Writing `page.Body` into a `?raw=1` response. It is the file as the DM wrote
  it, `[!SECRET]` blocks and all; `render.PublicText` under the request's
  decision is what goes out.
- Making the cookie's name a constant. A `__Host-` cookie without `Secure` is
  refused *silently*, so a local deployment that keeps the prefix is a wiki that
  forgets every player after every link.
