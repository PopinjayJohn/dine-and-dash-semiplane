# Specification — Dine-and-Dash-SemiPlane

> A TTRPG wiki for DMs and their players. Obsidian-compatible storage,
> database-powered search, one shared link per player.

**Status:** agreed design, pre-implementation
**Last updated:** 2026-09-26
**Companion documents:** `AGENTS.md` (invariants and conventions),
`CONTRIBUTING.md` (workflow), `docs/adr/` (decisions and their rationale)

Decisions in this document are recorded as ADRs. Where the two disagree, the
ADR wins and this document should be corrected.

---

## 1. Problem and personas

| Persona | Needs | Without this |
|---|---|---|
| **DM** | Author lore quickly, reveal it gradually, never lose it, open it in Obsidian on a laptop, hand each player one link | A vault scattered across machines, shared by copying files |
| **Player** | Look up their character, the town, last session's notes, with zero friction — one link, no account, no login | Asking the DM in Discord |

**Non-goals for v1:** character sheets as a live rules engine, dice roller,
combat tracker, map editor, multi-tenant billing, public plugin ecosystem,
out-of-process plugins.

## 2. Decisions, agreed

| # | Decision | ADR |
|---|---|---|
| 1 | Module path `github.com/popinjayjohn/dine-and-dash-semiplane` | — |
| 2 | Files are the source of truth; SQLite is a rebuildable index | 0001 |
| 3 | In-process plugin registry, not `buildmode=plugin` | 0002 |
| 4 | Auth is a per-player share link, exchanged for a cookie | 0003 |
| 5 | Pure-Go SQLite driver, no CGO | 0004 |
| 6 | Obsidian compatibility is a defined subset | 0005 |
| 7 | SSE isolated behind a four-function internal package | 0006 |
| 8 | Three-level visibility, character ownership, secrets stripped at render | 0007 |
| 9 | Core is system-agnostic; D&D 5e ships as a bundled plugin | 0010 |
| 10 | Players author character-owned pages; the DM creates the character and binds the player | 0007 |
| 11 | Local / self-hosted deployment: one static binary plus a SQLite file and a vault directory | 0011 |
| 12 | Datastar v1 GA, vendored, no CDN | 0006, 0008 |
| 13 | Search splits into two FTS5 indexes, merged with Reciprocal Rank Fusion | 0009 |
| 14 | Migrations run through a runner in this repository rather than golang-migrate | 0012 |
| 15 | A document is the bytes it was read as; frontmatter is a parse tree | 0013 |
| 16 | Secrets are removed from the parse tree; the cache is keyed by the decision | 0014 |

## 3. Technology

| Concern | Choice | Rationale |
|---|---|---|
| Language | Go 1.25+ | Single binary, excellent stdlib testing. 1.25 because `modernc.org/sqlite` requires it from v1.47, and the driver is the one non-negotiable choice here |
| Routing | `github.com/go-chi/chi/v5` | Small, `net/http` native, good middleware model |
| Front end | Datastar v1 GA | Hypermedia over SSE; composes with templ since both emit HTML |
| Templates | `github.com/a-h/templ` | Compile-time checked components; reusable as SSE fragments |
| Markdown | `github.com/yuin/goldmark` + extensions | AST extension points for wiki links and callouts |
| YAML | `go.yaml.in/yaml/v3` | Frontmatter as a parse tree, so a DM's keys, order and comments survive a rewrite — [ADR 0013](adr/0013-frontmatter-parse-tree.md) |
| Database | SQLite via `modernc.org/sqlite` | Pure Go, so tests run anywhere without a C toolchain |
| Migrations | `migrations/`, a runner in-repo | Versioned SQL embedded with `go:embed`; golang-migrate was available and pure-Go, and was passed over — [ADR 0012](adr/0012-migration-runner-in-repo.md) |
| Sanitisation | `github.com/microcosm-cc/bluemonday` | HTML allow-list for rendered markdown |
| Misc | `github.com/google/uuid`, `golang.org/x/crypto` | IDs, constant-time comparison |

**Resolved risk — Datastar.** The Go server module was not verified at design
time. The M0 spike has since confirmed it: client v1.0.4, server
`datastar-go` v1.2.2, both fitting behind the four-function interface.
[ADR 0008](adr/0008-datastar-release-and-client-pin.md) records the pins
and the two defects the spike found.
[ADR 0006](adr/0006-sse-abstraction.md) confines the dependency to one
package with four functions.

## 4. Architecture

```
                  +------------- chi router -------------+
  browser --HTTP-> | request-id - logging - recovery       |
        <--SSE---  | security headers - session - ACL     |
                  +---------+----------------+-----------+
                            |                |
                   +--------v-------+  +-----v--------+
                   | internal/http  |  |   plugins/   |  compiled in
                   +--------+-------+  +-----+--------+
                            |                |
       +--------------------+----------------+--------------------+
       |                    |                                     |
+------v------+    +--------v---------+                  +----------v--------+
|   render/   |    |    index/  sync  |                  |      plugin/       |
| goldmark +  |    |    files <-> DB  |                  | registry + caps   |
| wiki-links  |    +--------+---------+                  +-------------------+
| SECRET strip|             |
+------+------+      +------v------+
       |             |   vault/   |  Obsidian-compatible .md
       |             | atomic IO  |  <-- SOURCE OF TRUTH
       |             +------+-----+
       |                    |
       |             +------v-------------------+
       +------------->|         store/            |
                     | pages, FTS, links, revisions|
                     | principals, sessions, audit|
                     +---------------------------+
```

The three flows that matter:

**Read** — request → session → `access.Decision` → page row (through
`page_acl_read`) → render with the decision → sanitise → templ → response.

**Write** — request → ETag check → ACL decision → atomic file write → revision →
index update → audit → response.

**Index** — file change → hash → changed? → skip : parse → derive ownership →
resolve links → upsert page + FTS rows.

## 5. Content model

```sql
-- migrations/0001_init.sql

CREATE TABLE campaigns (
  id         TEXT PRIMARY KEY,
  slug       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  system     TEXT NOT NULL DEFAULT 'dnd5e',
  vault_dir  TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE pages (
  id             TEXT PRIMARY KEY,
  campaign_id    TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  path           TEXT NOT NULL,      -- 'locations/rivergate', no extension, no leading /
  title          TEXT NOT NULL,
  type           TEXT NOT NULL,      -- 'npc' | 'location' | 'character' | 'session-log' | ...
  frontmatter    TEXT NOT NULL,      -- canonical YAML, unknown keys preserved
  body           TEXT NOT NULL,      -- markdown, frontmatter removed
  content_hash   TEXT NOT NULL,      -- sha256 of the whole file
  renderer_version INTEGER NOT NULL,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  is_deleted     INTEGER NOT NULL DEFAULT 0,
  UNIQUE (campaign_id, path)
);
CREATE INDEX pages_campaign_path ON pages(campaign_id, path);

CREATE TABLE page_revisions (
  id                 TEXT PRIMARY KEY,
  page_id            TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  rev                INTEGER NOT NULL,
  markdown           TEXT NOT NULL,   -- whole file including frontmatter
  content_hash       TEXT NOT NULL,
  author_principal_id TEXT,
  message            TEXT,
  created_at         TEXT NOT NULL,
  UNIQUE (page_id, rev)
);

CREATE TABLE page_links (           -- the link graph
  src_page_id  TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  dst_path     TEXT NOT NULL,
  dst_page_id  TEXT REFERENCES pages(id) ON DELETE SET NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('link','embed')),
  PRIMARY KEY (src_page_id, dst_path)
);
CREATE INDEX page_links_dst ON page_links(dst_page_id);
```

Search tables are defined in §7. Principals, sessions and audit are in §9.

```sql
-- migrations/0005_character_bindings.sql  (M6)

CREATE TABLE principal_characters (
  principal_id      TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  character_page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  PRIMARY KEY (principal_id, character_page_id)
);
CREATE INDEX principal_characters_page ON principal_characters(character_page_id);
```

```sql
-- migrations/0004_access.sql  (M7)

ALTER TABLE pages ADD COLUMN owner_character_page_id TEXT
  REFERENCES pages(id) ON DELETE SET NULL;
CREATE INDEX pages_owner ON pages(owner_character_page_id);

CREATE TABLE principal_characters (
  principal_id      TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  character_page_id TEXT NOT NULL REFERENCES pages(id) ON DELETE CASCADE,
  PRIMARY KEY (principal_id, character_page_id)
);
```

`principal_characters` moved earlier than planned, into
`migrations/0005_character_bindings.sql`, with the reason in
[ADR 0016](adr/0016-auth-decides-and-returns-values.md): an ownership test of
`1 = 0` beside a binding table nothing reads is a predicate nobody has run.

Two of the other three columns moved earlier than planned too, because
search cannot be written correctly without them and the reasons are in
[ADR 0015](adr/0015-search-records-the-audience.md):

- `visibility` is in `migrations/0004_visibility.sql`. M4's sync already read
  every page's `visibility` key and refused the values it did not recognise; it
  then discarded the ones it did, because there was nowhere to put them, which left
  a `dm-only` page indexed as a page anybody could read. A read predicate has to
  filter on something.
- `body_public` is in `migrations/0003_search.sql`, because the public index is
  fed from it and the index is what the milestone ships. It is written as the empty
  string until access control can compute it; a body that reached the public index
  with a secret in it is a disclosure, and a body that did not is a missing
  feature.

**Page identity is the path.** Renaming a title never moves the file. Changing
the path is an explicit, DM-only action that rewrites inbound links atomically.

## 6. Obsidian compatibility

```
data/vaults/<campaign-slug>/
|-- .obsidian/                       # written on export only
|-- _attachments/                    # images, referenced by relative path
|-- _history/<path>/<n>-<iso8601>.md # revisions
|-- campaign.md
|-- characters/aria/<slug>.md         # type: character, bound to a principal
|-- characters/aria/backstory.md      # character-owned by path
|-- locations/rivergate.md
|-- npcs/garros-ironbar.md
`-- sessions/2026-02-14-dragon-heist.md
```

```markdown
---
title: Rivergate
aliases: [The Bridge Town, Flussport]
tags: [location, hub, revealed]
type: location
visibility: players
created: 2026-02-14T19:03:00Z
updated: 2026-02-16T20:11:00Z
---

A fortified town at the confluence of the [[Blackwater]] and the [[Thorn]].

> [!warning] The toll-collector
> Captain Vell has not been seen since the winter.

>![[map-rivergate.png]]
```

The committed subset is enumerated in
[ADR 0005](adr/0005-obsidian-compat-subset.md). The load-bearing
requirements:

- **Frontmatter** — YAML between `---` fences. Unknown keys preserved
  verbatim, never dropped. Parse → serialise → parse is idempotent.
- **Wiki links** — `[[target]]`, `[[target|alias]]`, `[[target#heading]]`,
  `![[embed]]`, resolved through the index. Unresolved links render with an
  `unresolved` class.
- **Callouts** — `> [!type] Title` with `-`/`+` fold markers, nesting, and the
  `{.revealed}` attribute form.
- **Encoding** — UTF-8, LF, no BOM, one trailing newline, deterministic
  serialisation. Rewriting an untouched file produces a zero-byte diff.
- **Commands** — `wiki import obsidian <dir>`, `wiki export --zip` (M12).

## 7. Search

```sql
-- Public index: every page, secrets already redacted from body_public.
CREATE VIRTUAL TABLE pages_fts USING fts5(
  page_id UNINDEXED, title, aliases, body, tags, kind,
  tokenize = "unicode61 remove_diacritics 2"
);

-- Private index: only [!SECRET] text. Consulted exclusively through page_acl_read.
CREATE VIRTUAL TABLE pages_secrets_fts USING fts5(
  page_id UNINDEXED, secret_text,
  tokenize = "unicode61 remove_diacritics 2"
);
```

A single index cannot be safe here: a row can be visible to a principal while
the *match* sits inside a secret block on that page. So the index splits.

- Every principal searches `pages_fts`. Safe unconditionally, no predicate
  needed, because secret text was redacted at index time.
- Privileged hits come from a second query over `pages_secrets_fts` joined to
  `page_acl_read`, which is where the ACL is applied.
- The two ranked lists merge with **Reciprocal Rank Fusion**,
  `score = sum(1 / (60 + rank))`, so two different scorers never need
  normalising against each other.
- Excerpts (`snippet()`) come from the index the match came from, so a secret
  hit can only ever produce a secret excerpt for a principal allowed to see it.
- `search.Run(q, principal) []Hit` is a pure function, table-tested against a
  fixture corpus containing one secret, one public page that mentions it, and
  one `dm-only` page that mentions it.

Query language: bare words are ANDed, `"exact phrase"`, `tag:`, `type:`,
`is:dm-only`. User input is always parameterised, never concatenated — FTS5
syntax is an injection surface with its own test corpus.

## 8. Access control

The riskiest subsystem, because players now author content. One resolver, one
decision type, one render choke point.

### Visibility levels

| `visibility`   | Visible to                            | Who may set it   |
|----------------|---------------------------------------|------------------|
| `dm-only`      | DMs                                   | DM only          |
| `dm-and-owner` | DMs and the player(s) owning the page | DM, or the owner |
| `players`      | DMs and every bound player            | DM, or the owner |

`dm-only` is absolute. Ownership never unlocks it and a player can never set
it. "Reveal" is the owner moving a page one level less strict — a level change,
not a separate mechanism.

### Ownership

A page is character-owned when its path begins with `characters/<slug>/` or its
frontmatter declares `character: <slug>`. Both resolve to
`pages.owner_character_page_id`. A character is not a new entity: it is a page
of type `character`, and a binding is a row in `principal_characters`.

The DM creates the character page and binds the player. The player authors
everything beneath it. Players may create pages only within their own character
subtree or carrying their own `character:` frontmatter.

**Confirmed:** players can read each other's character pages' public content.
Only `[!SECRET]` blocks and `dm-and-owner` pages are private.

### Rights matrix

| Principal | Read `dm-only` | Read `dm-and-owner` (own) | Read it (other's) | Edit DM-owned | Edit own char | Edit others' |
|---|:-:|:-:|:-:|:-:|:-:|:-:|
| `dm`    | yes | yes | yes | yes | yes | yes |
| `player`| no  | yes | no  | no  | yes | no  |

### The resolver

```go
// internal/access/decision.go
package access

// Decision is the per-(principal, page) verdict, computed once per request and
// threaded through the renderer, the search excerpter and the SSE payloads.
type Decision struct {
	CanRead       bool
	CanEdit       bool
	CanReveal     bool // may move visibility one level less strict
	CanSeeSecrets bool // may see [!SECRET] content on this page
}

// For is pure: no DB, no clock, no mocks. Exhaustively table-testable.
func For(p Principal, page PageMeta) Decision
```

```sql
-- internal/store/acl.go
--
-- A *parameterised* fragment, not a view: SQLite has no view taking a campaign,
-- a role and a principal, and the three all differ per request. The fragment
-- lives in Go so that a query which forgot it does not compile.

p.is_deleted = 0
  AND p.campaign_id = :campaign
  AND ( ( :role = 'player' AND p.visibility = 'players' )
     OR :role = 'dm'
     OR ( :role = 'player' AND p.visibility = 'dm-and-owner'
          AND EXISTS (SELECT 1 FROM principal_characters pc
                      WHERE pc.character_page_id = p.owner_character_page_id
                        AND pc.principal_id = :principal) ) )
```

**The role is a conjunct of the first and third clauses, and that is not
decoration.** The specification originally wrote the first clause as
`p.visibility = 'players'` on its own, which admits a `players` page to *anybody* —
and "anybody" includes a request that identified nobody, because a session that
could not be found produces a principal with an empty role, and the `OR` does not
look at the role before it answers true. That is what
`TestStoreReadPredicateMatchesResolver` found, on its first run, and it is a
correction to this document as much as to the code.

Search, listings, the page tree, backlinks and tag clouds all filter through
this predicate. A test asserts the predicate and `For` agree for every cell of
the matrix, because a disagreement is a leak in one direction and a missing
feature in the other.

## 9. Secrets

A `[!SECRET]` callout, which is valid Obsidian:

```markdown
> [!SECRET] The toll-collector's real name
> Captain Vell is actually **Ilithya Marrow**, sworn to the Umbral Court.
>
> - She engineered the bridge collapse.
> - She answers to [[the-whisperer]].
```

Revealed, still valid Obsidian:

```markdown
> [!SECRET] The toll-collector's real name {.revealed}
> Captain Vell is actually **Ilithya Marrow**, sworn to the Umbral Court.
```

| Page kind | `[!SECRET]` visible to |
|---|---|
| DM-owned, any `visibility` | DMs |
| Character-owned | DMs **and the owning player** |
| — with `{.revealed}` | everyone who can read the page |

### The rule that makes it safe

> **Secret content is removed from the response bytes, server-side, at render
> time. There is no CSS hiding, anywhere.**

```
body --> goldmark AST --> [wiki-link, callout, SECRET plugins] --> sanitiser --> HTML
             ^
             +-- SecretStripper(policy)   <-- removes blocks Decision does not permit
```

- **One render path.** Full body plus an `access.Decision`. No second "public
  render" path — that would mean a second set of golden tests and a second
  place to get the stripping wrong.
- `body_public` exists **only** to keep secret text out of the public FTS
  index. It is never rendered.
- **Fail closed.** The stripper takes a `Policy` interface whose default
  implementation permits *nobody*. M3 therefore leaks nothing while M7 is still
  in progress.
- Surfaces in scope: page HTML, search results and excerpts, the raw-markdown
  endpoint, revision diffs, SSE frames, `$signals` state, canonical metadata,
  the page tree, the tag list.

The test is a forbidden-substring assertion over the raw response body, never a
DOM inspection:

```go
// internal/http/secret_leak_test.go
for _, path := range playerReachableRoutes {
    body := get(t, srv, path, nonOwnerPrincipal)
    require.False(t, bytes.Contains(body, []byte(secretCanary)),
        "%s leaked secret content to a non-owner", path)
}
```

### DM and player actions

- **DM:** "Reveal page", "Make DM-only", per-block "Reveal", and a Campaign →
  Secrets panel listing every unrevealed block and where it lives.
- **Player:** "Reveal page" and per-block "Reveal" on their own pages; disabled
  with an explanation everywhere else.
- Both go through the `access.Policies` plugin capability, so the 5e plugin can
  add rules without touching core.

**Known limitation:** a DM cannot share a secret with two players but not a
third. Accepted for v1; per-principal ACLs are the natural extension.

## 10. Authentication

A capability URL, exchanged exactly once for an opaque session cookie.
Full rationale in [ADR 0003](adr/0003-url-token-auth.md).

**Issuance** — the DM clicks "New player link". The server generates 32 bytes
from `crypto/rand`, stores only `sha256(token)` plus a four-character hint, and
shows the URL once. The plaintext is never persisted.

```
https://host/c/<campaign-slug>?k=<token>
```

**Redemption** — on the first request carrying `k`:

1. `sha256` the token, look up `principals.token_hash` (indexed).
2. Reject if revoked, expired, or the campaign slug does not match.
3. Create a `sessions` row; `Set-Cookie` an opaque id (`__Host-`, `HttpOnly`,
   `SameSite=Lax`, `Secure` in production).
4. Respond `303` to `/c/<campaign-slug>/` — **the token leaves the URL here.**
5. Record `share_link_used` in `audit_log`.

```sql
CREATE TABLE principals (
  id            TEXT PRIMARY KEY,
  campaign_id   TEXT NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  label         TEXT NOT NULL,       -- "Alice (Ranger)"
  role          TEXT NOT NULL CHECK (role IN ('dm','player')),
  token_hash    TEXT NOT NULL UNIQUE,
  token_hint    TEXT NOT NULL,
  created_at    TEXT NOT NULL,
  expires_at    TEXT,
  revoked_at    TEXT,
  last_used_at  TEXT
);

CREATE TABLE sessions (
  id           TEXT PRIMARY KEY,
  principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  user_agent   TEXT
);

CREATE TABLE audit_log (
  id            INTEGER PRIMARY KEY,
  campaign_id   TEXT,
  principal_id  TEXT,
  action        TEXT NOT NULL,
  page_id       TEXT,
  at            TEXT NOT NULL,
  detail        TEXT
);
```

Roles are `dm` and `player`; there is no `editor`, because edit rights come
from ownership. A second DM is another principal with `role = 'dm'`.
Revocation is immediate: sessions are rows, so deleting the principal ends
every existing browser's access on the next request.

**Hardening**, each with a named test: constant-time comparison; a redacting
logger with a test that greps captured output for the token;
`Referrer-Policy: no-referrer`, `Cache-Control: no-store` and
`X-Robots-Tag: noindex` on campaign responses; 10 redemption attempts per
minute per IP; CSRF via `SameSite=Lax` plus a double-submit token carried in a
header rather than a query parameter; session rotation on role or binding
change.

**Documented threat model:** a link pasted in Discord or shared across a table
is equivalent to a leaked account for that player, scoped to one campaign.
Mitigations: one link per player, instant revocation, optional expiry, an
"active now" view for the DM.

## 11. Rendering

```
markdown file
  -> frontmatter split (unknown keys preserved)
  -> goldmark: GFM, footnotes, wiki-link ext, callout ext, SECRET ext
  -> AST transform: resolve [[links]] via the index, strip disallowed secrets
  -> render to HTML
  -> bluemonday sanitise
  -> templ wraps in the page shell
  -> cache keyed by (content_hash, renderer_version, can_see_secrets, path)
```

The cache key carries the decision because a render made for a DM and one made
for a player are different bytes, and one of them contains secrets: see
[ADR 0014](adr/0014-secrets-leave-the-tree.md), which also records why the
stripping happens on the tree and not on the way out.

- **Wiki-link extension** resolves against the index: exact path, then alias,
  then case-insensitive filename — Obsidian's resolution order.
- **Sanitiser** applies to every author, DM included. Sanitising "only untrusted
  authors" would leave the highest-value target on the weakest path.
- **CSP** with a per-response nonce, no `unsafe-inline`, vendored assets.
- **Render cache** invalidated on save or on a `renderer_version` bump.

## 12. Plugin architecture

Compile-time registry, in-process. See
[ADR 0002](adr/0002-plugin-registry-in-process.md).

```go
// internal/plugin/plugin.go
package plugin

type Plugin interface {
	Name() string
	Version() string
	Setup(*Registry) error
}

// internal/plugin/caps.go

// Capabilities is the entire surface a plugin may touch. Every field added
// here is a permanent compatibility promise.
type Capabilities struct {
	Pages    PageTypes    // page types, field schemas, templates
	Fields   FieldTypes   // new field kinds: dice, statline, ref, ...
	Render   RenderHooks  // before/after markdown render
	Markdown GoldmarkExts // contribute goldmark extensions
	HTTP     Routes       // mount chi routes
	Access   Policies     // compose extra visibility rules
	Search   SearchFields // extra indexed fields
	Events   Subscribe    // PageSaved, PageViewed, ShareLinkUsed, ...
	CLI      Commands     // `wiki <plugin> <subcommand>`
}
```

```go
// internal/plugin/renderhook.go
package plugin

type RenderHook interface {
	BeforeRender(ctx context.Context, p *wiki.Page, md goldmark.Markdown) (goldmark.Markdown, error)
	AfterRender(ctx context.Context, p *wiki.Page, out *bytes.Buffer) error
}
```

Guarantees: deterministic `(Priority, Name)` ordering; a panicking hook is
recovered, logged and skipped; duplicate plugin names fail startup loudly; no
ambient globals; plugins compose into the access policy and cannot replace it.

Core owns the generic page types (`note`, `location`, `npc`, `quest`, `item`,
`faction`, `session-log`, `character`), ownership, `[!SECRET]` and the ACL.

| Plugin | Demonstrates | Ships |
|---|---|---|
| `dnd5e` | page types with rich field schemas (`spell`, `creature`, `feat`, `magic-item`), a `statline` field type, a character-sheet template | M12 |
| `house-rules` | render hook + event subscriber that invalidates the render cache | M11 |
| `spoilerbox` | `access.Policies` contributor + render hook | M11 |
| `wordcount` | Datastar fragment + `SearchFields` + CLI command | M11 |

Each is also a test fixture, so the plugin API can never drift from reality.

## 13. Deployment

Local or self-hosted. See [ADR 0011](adr/0011-single-binary-data-directory-backup-unit.md)
for the shape and the backup unit, [ADR 0006](adr/0006-sse-abstraction.md)
for the offline requirement and [ADR 0004](adr/0004-pure-go-sqlite.md) for
the connection settings. The threat model is in
[`docs/security.md`](security.md).

- **One static binary.** `templ` output and `web/static/**` (CSS, fonts,
  pinned `datastar.js`) are embedded with `go:embed`. Nothing is fetched at
  runtime.
- **The data directory is the backup unit:**

```
~/.local/share/dine-and-dash-semiplane/
|-- config.yaml
|-- campaigns.db
|-- locks/serve.lock      # prevents two servers on one data dir
|-- backups/
`-- vault/<campaign>/...  # Obsidian-openable
```

- Config via `config.yaml` with `DDSP_*` environment overrides: `DDSP_LISTEN`
  (default `127.0.0.1:8080`), `DDSP_BASE_URL`, `DDSP_TRUSTED_PROXIES`,
  `DDSP_DATA_DIR`.
- SQLite: WAL, `foreign_keys=ON`, `busy_timeout=5000`,
  `synchronous=NORMAL`, single-connection write pool plus a pooled read pool.
- `wiki backup [--prune]` writes a timestamped archive of the database (via
  `.backup`) and the vault. The vault can equally be a git repository, which is
  the natural thing for the DM to do anyway.
- `wiki serve --lan` prints the LAN URL and offers self-signed TLS for playing
  around a table.
- SSE streams are capped and drained on shutdown; `SIGINT` closes the database
  cleanly.

## 14. Testing

Coverage floor 80%, and CI fails on regression. `-race` on everything, always.

| # | Layer | Contents |
|---|---|---|
| 1 | Unit | slug and path rules, frontmatter parse/serialise, `access.For`, token mint/verify, migrations |
| 2 | Property | frontmatter round-trip idempotent; render never panics; slug never yields `..`, empty or absolute; path sanitiser resists traversal and symlink escape |
| 3 | Fuzz | frontmatter parser, goldmark extensions, path sanitiser, FTS query builder |
| 4 | Golden | `testdata/render/*.md` ↔ `*.html`, regenerated with `-update`, diffs reviewed |
| 5 | XSS corpus | ~60 payloads (script, `javascript:`, onerror, svg/onload, `data:`, obfuscated) must all be neutralised |
| 6 | Store contract | one suite `testsuite.Store(t, factory)` run against every `Store` implementation; asserts dual-write consistency, idempotency, drift repair |
| 7 | Migration | up-from-zero and up-from each shipped golden database snapshot; asserts schema version |
| 8 | HTTP integration | full chi app, real SQLite in `t.TempDir()`, `httptest.NewServer`; auth, CRUD, search, ACL |
| 9 | SSE | a hand-written SSE client asserting fragment ids, event ordering, retry directives, cancellation, reconnect |
| 10 | Plugin contract | the same harness run against every bundled plugin |
| 11 | Race and coverage | `go test -race -shuffle=on -covermode=atomic ./...` plus a coverage gate |
| 12 | E2E (optional) | Playwright smoke, behind a `//go:build e2e` tag so it never blocks CI |

Determinism helpers in `internal/testutil`: injected `Clock` and `IDGen`, a
temp-vault builder, golden helpers, `NewTestApp(t)`.

CI runs: `go vet`, `golangci-lint`, build, tests with race, the coverage gate,
fuzz smoke, migration tests, the changelog check, and a smoke test that boots
the binary and hits `/healthz`.

### Named security and access tests

| Test | Asserts |
|---|---|
| `TestForDecisionMatrix` | all 36 cells of the rights matrix, pure table test |
| `TestStoreReadPredicateMatchesResolver` | the SQL view and `For` agree for every cell |
| `TestSecretStrippedFromAllSurfaces` | forbidden-substring sweep across every player-reachable route |
| `TestSearchNeverRanksOrQuotesSecrets` | the canary is absent from all hits, snippets and counts |
| `TestRRFMergeOrdering` | golden ordering across both indexes |
| `TestPlayerAuthoredXSSSanitisedForDM` | player script is neutralised in the **DM's** view |
| `TestConcurrentEditDMAndPlayerProducesConflict` | 409 plus three-way diff, both texts intact |
| `TestPlayerCannotCreateWorldPage` | denial half of the matrix |
| `TestPlayerCannotRevealOthersSecret` | denial half of the matrix |
| `TestObsidianRoundTripPreservesSecretReveal` | `[!SECRET]{.revealed}` survives parse → serialise → parse |
| `TestBackupsRestoreIdenticalIndex` | database backup plus vault rebuild to a byte-identical index |
| `TestOfflineBoot` | `serve` with no network returns a working application |
| `TestNoTokenInLogs` | a full auth flow leaves the token in no captured log line |
| `TestRateLimitedRedemption` | redemption is refused past the limit |

## 15. Process

- **`CHANGELOG.md`** — Keep a Changelog plus SemVer. `## [Unreleased]` always
  first; released sections are never retro-edited. Every commit touching
  `internal/`, `cmd/`, `plugins/`, `migrations/` or `web/` must touch the
  changelog in the same commit, enforced by `scripts/check-changelog.sh`.
- **Conventional Commits** with the scopes listed in `AGENTS.md`, and a
  template in `.gitmessage`.
- **ADRs** in `docs/adr/NNNN-short-title.md` with Status, Date, Context,
  Decision, Consequences. Numbers are sequential and never reused; a superseded
  ADR is marked, not deleted.
- **Version** comes from the git tag via `-ldflags`, and is surfaced at
  `/healthz` and in the UI footer.

## 16. Milestones

Each milestone is one branch, one PR, one changelog section.

| M | Goal | Ships | Key tests |
|---|---|---|---|
| **M0** | Foundation | `go.mod`, `Makefile`, golangci-lint, GitHub Actions, `.gitmessage`, CI skeleton, **Datastar v1 spike** | CI green on an empty repo |
| **M1** | Domain and store | domain types, store interfaces, `0001_init.sql`, migration runner, injected `Clock`/`IDGen` | store contract, migration from zero, pragma assertions |
| **M2** | Obsidian storage | frontmatter parse/serialise, safe paths, atomic writes, content hash, `_history`, attachments | round-trip, fuzz, traversal fuzz, atomicity under simulated crash |
| **M3** | Renderer | pipeline, wiki-link extension, callout extension, **`[!SECRET]` parsing and fail-closed stripper**, TOC, sanitiser, render cache | golden, fuzz no-panic, XSS corpus, cache invalidation |
| **M4** | Sync engine | incremental and full reindex, ownership resolution, drift detection, `fsnotify`, `sync --check` | idempotency over N runs, drift injection, external-edit detection |
| **M5** | Search | FTS5 schema, query builder, BM25, **two indexes plus RRF**, ACL in SQL | relevance, FTS-injection corpus, "the secret never appears", benchmarks |
| **M6** | Auth and principals | token mint/verify, cookie exchange and scrub redirect, roles, sessions, rate limits, revocation, audit log, **character binding**, a redacting logger | every hardening item in §10 as a named test |
| **M7** | Access control | `access` package, the read predicate corrected to the spec's form, **`body_public` redaction**, the owner column, edit enforcement, a principal on every page-returning store method | the §14 access table, `TestStoreReadPredicateMatchesResolver` |
| **M8** | Web shell | router, middleware, layout, page view, browse tree, 404/500, embedded assets, `/_/healthz`, **the cookie attributes ADR 0003 held back, the login route, and a live page** | httptest render tests, route coverage, HTML smoke assertions, `TestSecretStrippedFromAllSurfaces` |
| **M9** | Editing | editor, autosave, preview, ETag and 409 plus three-way diff, archive and purge, rename, revisions and restore, **player editing of own character pages**, and the `users new` / `users revoke` buttons that mint and take back share links | CRUD flows, conflict detection, restore fidelity, DM/player races |
| **M10** | Datastar | `internal/sse` abstraction, search-as-you-type, live session log, toasts, optimistic fragments | SSE client tests, ordering, reconnect, cancellation, goroutine drain |
| **M11** | Plugin framework | `internal/plugin` and capabilities, `house-rules`, `spoilerbox`, `wordcount`, authoring guide | contract suite, ordering, panic isolation, duplicate rejection |
| **M12** | DX and release | full CLI, `import obsidian`, `export --zip`, `users new`/`revoke`, the **`dnd5e` plugin**, Dockerfile, backup and restore, CSP and structured logs, full docs, **v0.1.0** | CLI tests, e2e smoke behind a build tag, release dry run |

### M0 commit sequence

```
chore: initialise Go module and tooling
build: add Makefile with test, lint, cover, fuzz targets
ci: add GitHub Actions workflow (vet, lint, race tests, coverage gate)
chore: add commit template and changelog enforcement script
docs(adr): record Datastar release and SSE client pin from the M0 spike
```

### M1 commit sequence

```
feat: inject the sources of time and identity
feat: describe the world the wiki is about
feat: give the database a schema and a way to reach it
feat: open the database the way ADR 0004 says, and check
feat: store campaigns, pages, revisions and links
test: write the store's contract down, once
docs(adr): record the migration runner, and correct the spec
chore: record where the project actually is
build: measure coverage across the module, not per package
build: make the fuzz target actually run
build: take the newest pure-Go SQLite and the Go it needs
feat(cli): add a manual wiki migrate
docs(adr): correct a false claim in ADR 0012
docs: record the commits M1 actually took
```

Four of those are not postscripts. Choosing a migration runner departed
from §3 and needed recording; the two `build:`
commits fix tools that the first code commits made insufficient — the
coverage gate was measuring each package with its own test binary, which
reports 0% for a package that only runs inside another package's tests,
and `make fuzz` was exiting zero having fuzzed nothing; the driver bump
is the Go version the pure-Go SQLite translation needs. And one corrects
ADR 0012, which had claimed the library had no pure-Go SQLite driver. It
has one. The decision to keep an in-repo runner stands on the corrected,
weaker grounds the ADR now gives, and §3 says so rather than repeating
the error.

### M2 commit sequence

```
feat: read a DM's markdown without touching it
feat: check a path by resolving it, not by reading it
feat: write a page atomically, through a handle rather than a path
feat: keep the history and the attachments where Obsidian expects them
docs(adr): record why a document is the bytes it was read as
chore: record where the project actually is
```

The first commit is the milestone. A file the application did not change
comes back out byte for byte, which cannot be tested for and has to be
structural — see [ADR 0013](adr/0013-frontmatter-parse-tree.md). The
third one moved every file operation onto an `os.Root`, which is a
directory handle rather than a name: the kernel then refuses anything that
would leave the vault, a symlink followed included, and there is no
window between checking a path and opening it. The second commit had
already done the string-level work that the handle makes unnecessary, and
it is the one whose fuzz targets state the invariant the rest stands on.

### M4 commit sequence

```
feat(store): hold the names a page answers to, and look them up
feat(index): put a resolver behind the interface the renderer asked for
feat(index): read a vault into the index, and say what it did
feat(index): notice drift, and rebuild when asked
feat(index): work out who owns a page, and check the answer
feat(lock): one campaign, one writer
feat(index): watch the vault, so a saved page is a seen page
feat(cli): wiki sync, and wiki reindex --full
chore: record where the project actually is
```

The second commit is the one that makes the third possible: the renderer
finished M3 with a `LinkResolver` and nothing behind it, and the link graph
has to be derived by the same walk as the rendered links or a link can resolve
in the page and not in the graph. The third is the milestone, and its own
tests found the design error worth finding — settling a page on its content
hash, which a row that rots in place and a change to how fields are derived
both defeat. The fifth ships the ownership *rule* without its column, because
the rule decides which subtree a player may write in and the column arrives
with access control in M7.

### M5 commit sequence

```
feat(search): a query language, and a fuzzer for it
feat(store): the column a public search reads, and both FTS5 indexes
feat(access): record a page's audience, and write the read predicate in SQL
feat(search): run a query against the public index, ACL in SQL
feat(render): lift a page's secret text off the tree, for the index that holds it
feat(search): merge the two ranked lists with reciprocal rank fusion
feat(index): keep both search indexes in step with a sync
docs(adr): record the audience arriving before access control, and the empty body
```

The first is the milestone in miniature: a value with no handle in it, which is
what makes the language fuzzerable and the relevance tests runnable without a
database. Its fuzzer found two things worth fixing — a byte that is not valid
UTF-8 riding through into a clause, which would have made any response echoing
the query invalid JSON, and a NUL inside a term ending it for the tokenizer and
not for the string.

The third is a decision that had to be taken to write the fourth, and it is
recorded in [ADR 0015](adr/0015-search-records-the-audience.md): a read predicate
filters on an audience, M4 was already reading and discarding one, and the
ownership branch is written down as `1 = 0` rather than left out — leaving it out
silently widens every `dm-and-owner` page, and admitting every player to one is a
disclosure the moment a DM writes one.

The fifth ships the *secret* half of the split and not the public half, because
which text is secret is a parsing question and which text a *principal* may be
shown is an access-control one. So a DM can find their own secrets today and
nobody can find a word in the middle of a paragraph: a missing feature, in the
safe direction, with the test that names it.

The sixth is where the benchmark earned its place. A `tag:` filter was asking
whichever index it was reading for a `tags` column, and the private index has no
`tags` column, so `tag:hub` was a SQL error on every secret search. A unit test
with a single tag would have found it; the one that did not exist yet was a
benchmark over a real campaign, and that is the argument for having one.

The last commit is the one that writes the language down, in `docs/search.md`,
and corrects the two documents that were wrong about how it arrived. Where a
milestone's own documentation lands in the ADR commit is a matter of taste; what
is not a matter of taste is that a design decision with a security consequence —
the audience arriving before the code that enforces it, and the public body
arriving empty — is recorded rather than discovered by the next reader.

### M6 commit sequence

```
feat(auth): the tables a share link needs
feat(auth): mint a share link, and keep the token out of the database
feat(auth): redeem a link for a cookie, and take the token out of the URL
feat(auth): rate limit redemption
feat(auth): a logger that cannot leak a token
feat(access): bind a character to a principal, so the ownership test is no longer false
docs(adr): record what M6 decided, and what it deliberately did not
docs: record where the project actually is
```

The second one is a type that cannot print itself, and the reason is in the commit
body: `%#v` does not consult `String` and `%x` on a struct hex-encodes its fields,
so a `Token` with only a `String` hands out the whole credential to
`t.Errorf("%#v", err)`. Both were found by a test that checks every verb, and both
are why it also implements `GoString` and `Format`.

The third one had a bug that no unit test found and every player would have: the
minting side hashed 32 raw bytes and the redemption side hashed the 64 hex
characters, so no link in any campaign would have worked. One function,
`ParseToken`, is what both halves go through now, and its existence is the test.

The fifth is the named test `TestNoTokenInLogs`, and it runs a *full* auth flow and
greps everything it produced. The redactor had two bugs of its own: `Redacted`
checked the whole remaining string for hex rather than the 64-character window, so a
token at the start of a sentence was not recognised; and a token's SHA-256 is 64 hex
characters and so is the token, so the shape check cannot tell a hash from a
credential.

The sixth is the one the previous two milestones were waiting for. The read
predicate's ownership test stops being `1 = 0` and becomes the `EXISTS` over
`principal_characters`, and a player can for the first time find their own
character page's secrets — which is the rule ADR 0007 has described since M0 and
which nothing could reach until a principal owned a page.

### M7 commit sequence

```
feat(domain): an owner on a page — the column, and the sync resolving it
feat(access): the resolver, and all 36 cells of the rights matrix
feat(access): the predicate reads the owner, and the two agree for every cell
feat(store): a principal on every page-returning method
feat(index): fill body_public, so a page is findable by its prose
feat(access): refuse an edit the principal may not make
feat(render): the real decision reaches the stripper
docs(adr): record what M7 decided, and what it deliberately did not
docs: record where the project actually is
```

The first is a two-milestone debt discharged: M4 resolved a page's owner on every
sync, validated it, reported the problems, and threw it away. It also found an M4
bug on the way — a `character:` key resolved to a page at `<slug>` rather than at
`characters/<slug>`, which was only a spurious report while the answer was used
for nothing and became a character nobody owned once it was used for an id.

The second **caught a disclosure in the resolver two minutes after it was
written**: the ownership shortcut ran before the `dm-only` check, so a player bound
to a character page the DM had marked `dm-only` could read it, edit it and see its
secrets. §8 says `dm-only` is *absolute*, and writing all the cells out by hand is
the only reason anybody noticed.

The third is `TestStoreReadPredicateMatchesResolver`, the named test this project
has been deferring since M5, and it **found a second disclosure on its first
run**: the predicate's first clause admitted a `players` page to a request that
identified nobody. The spec wrote the same clause, so the correction is to the
document as well as to the code.

The fifth is the last piece of "a missing feature, in the safe direction", and it
became safe to fill only because the public search's rows are filtered by the read
predicate — every principal who can reach a hit may read the page, so the index
holds exactly what a reader of that page may read.

The sixth moves the gate for writes into the store, where a handler cannot leave
it out, and checks *ownership* rather than *position* — which is a partial check,
deliberately, and ADR 0017 is mostly about the residual.

### M3 commit sequence

```
feat: render a page, and put the output under a golden file
feat: render wiki links, and say when one does not resolve
feat: strip secrets from the tree, not from the output
feat: sanitise every page, for the DM as much as for a player
feat: cache renders by everything that can change one
docs(adr): record where a secret leaves, and what the cache is keyed by
chore: record where the project actually is
```

The third commit is the milestone. The last two are the ones whose wrong answers
look like working features rather than like bugs: a sanitiser applied to
"untrusted authors only" leaves the DM on the weakest path, and a cache keyed
by content hash alone is a channel from a DM's render to a player. Both are
written down in [ADR 0014](adr/0014-secrets-leave-the-tree.md), which also
corrects §11's two-field cache key.

### M10 commit sequence

```
fix(resolve): a link resolves for its reader, and the reader is not the DM
feat(http): search as you type, and the candidates a reader may see
feat(http): the session log, which is the hub with a wider topic
chore: delete the Datastar spike, which internal/sse has replaced
docs(adr): record what M10 decided, and what it found in its own fixture
docs: record where the project actually is
```

M10 is a milestone of one decision with everything else resting on it, and it is
the fix [ADR 0020](adr/0020-link-resolution-is-campaign-wide.md) decided in M9
and did not build. Everything else in the milestone — a search box, a live page, a
change log — is a *reader*, and each of them was a way for the same disclosure to
arrive again: a `dm-only` page's title in a dropdown, a DM's edit announced in a
log. So the fix came first, and the three features then had a property to be
tested against rather than a shape to be drawn.

The cost was not the one ADR 0020 predicted. It said the render cache would need
the principal in its key, and worked out that it would not, on a two-class
argument. The argument was right about `CanSeeSecrets` and wrong about
`dm-and-owner`: a page whose owner is a *player* rather than a *character* gives
its owner and the DM the same `CanSeeSecrets` and resolves its links differently,
so `access.Decision` grew a fifth field — `ReadsAll` — and the cache key grew it
with it. **A rendered page is now a function of (content, campaign, decision,
role) and nothing else**, and `TestTheCacheKeyNamesNoPrincipal` is the guard on the
other direction, because a principal in the key would not be wasteful but wrong:
every player would get their own entry for byte-identical output.

The search work found two things in the fixture that were worth more than the
search itself. The test fixture used to write rows with `UpsertPage` and files
with hand-written frontmatter, and **the two had drifted** — `type:` was in the
struct and missing from every block, and one title contained a colon that is a YAML
error, so that page did not parse and was skipped entirely. Nothing failed loudly,
because the rows came from the struct and the files were never read by anything.
The fixture is built through the derivation now, like a real campaign, and that is
what let the search test notice it was searching an index of nonsense.

### M9 commit sequence

```
feat(index): a sync that writes as somebody, so the store's gate still runs
feat(edit): the writer, and the gate that has to come before the file
feat(http): the editor, and a preview that reuses the render path
feat(http): users new and users revoke, so a DM can hand somebody a link
docs(adr): record what M9 decided, and what it found and did not fix
docs: record where the project actually is
```

M9 is the first milestone in which anything writes a markdown file, and the whole
milestone is about **the order of the steps**. The sequence begins on M7's work
rather than on M9's because the editor's write had to be threaded through the
sync as a principal: ADR 0017 put the write gate on `UpsertPage`, and until now
every writer was the sync, which writes as the DM because indexing a DM's own
vault is what a sync *is* — so the gate had never refused anything.

The finding that shaped the second commit is in
[ADR 0019](adr/0019-the-writer-checks-before-it-writes.md): the index watcher
writes rows as the DM, so a save that writes the file and *then* asks whether the
write was allowed leaves a player's file on disk for the length of the rollback,
and the watcher will index it. **A refused write is laundered into the index
through the one path that writes rows without asking.** The gate therefore runs
while the content is still bytes, and the file is the last thing that changes.

The third commit is where a preview stopped being a second rendering path. It
started as a handler that had the whole file's bytes and rendered them whole, which
put an `<hr>` where the frontmatter fences were and a heading out of the `title:`
line. §9's "there is exactly one render path" applies to a route nobody thought
about when the rule was written, and the preview is now one function in
`internal/edit` that parses, derives, decides and renders — the same four steps the
save takes, in the same code.

The fourth commit is the missing half of handing somebody a link: §10 has said since
M0 that the DM clicks "new player link" and sees the plaintext once, and until now
there was no button. The minted link is a 303 to the page with the link on it, the
page says it will not be shown again, and the list shows no token and no
fingerprint.

### M8 commit sequence

```
fix(store): the predicate asks whose campaign the principal is of
feat(render): the campaign is in the link, because two campaigns share a server
feat(sse): the four functions ADR 0006 promised, and a hub to drive them
feat(web): the assets, embedded, and nothing fetched at runtime
feat(http): the web shell, the cookie ADR 0003 held back, and the login
feat(sse): a page that updates itself, and the hub's first consumer
feat(cli): wiki serve, and the lock that keeps two servers off one directory
docs(adr): record what M8 decided, and what it deliberately did not
docs: record where the project actually is
```

The first is the one that matters and it is a `fix` on M7's work rather than on
M8's: the read predicate asked which campaign the *caller* wanted and never
whether the caller belongs to it, so a `GetPage` for a page in Thornford made with
a session for the Blackwater was answered by the role clause alone. Nothing had
ever asked, because every caller so far was the sync engine, which passes
`AsDM(campaignID)` and is therefore always of the campaign it is reading. **A
predicate that is correct for callers who get their arguments right is a predicate
one handler away from a disclosure**, and M8 is the first caller with a real
principal. Twelve test fixtures turned out to be building principals that cannot
exist — a role and no campaign, which the schema refuses to store — and the new
conjunct is what made that visible rather than a matter of taste.

The second is the same idea one layer up. A resolved link said
`/c/locations/rivergate`, and a data directory holds several campaigns, so a link
pointed at whichever campaign the reader was already in. §9's "a link into a page
of another campaign" is this.

The fourth is ADR 0006's offline requirement and ADR 0011's reason for it
disagreeing, resolved the way both wanted: a `datastar.js` fetched at runtime is a
script the application did not write, running with a player's session cookie, and
it is also a request that fails on a table's wifi.

The fifth is the milestone, and its five findings are the reason the sequence has
a `fix` at the front of it and a `docs(adr)` at the end of it:

- The **500 page asked the store for a page tree**, which is the thing that has
  just failed. A store that *panicked* rather than errored took the process down
  from inside the recovery handler, with every other player's session on it.
- The **uptime was a package variable reading `time.Now()`** while everything else
  used the injected clock, so a fixed-clock test got minus five thousand hours. It
  was a duration and it was a string, which is how that kind of wrong survives
  review.
- The **404, the 500, the 403 and the 405 each built their shell by hand**, and
  three of them had already disagreed about whether the CSRF token was in it. The
  404 carried a logout form whose token was the empty string: a form that could
  never be submitted.
- **ADR 0003's five steps do not rotate the token**, so a share link is a reusable
  bearer credential. The test that asked found it, and the answer is that this is
  right and is now written down in [ADR 0018](adr/0018-the-campaign-is-in-every-url.md).
- The **cookie's name could not be ADR 0003's on a laptop**, because a browser
  refuses a `__Host-` cookie without `Secure` and says nothing when it does. A DM
  on plain HTTP got a wiki that forgot them after every link.

The sixth changes what the SSE hub carries, and it is the security decision of the
milestone: the hub carries a *notice* and each subscriber re-renders under its own
decision, so a DM's render cannot reach a player's stream. The obvious design —
render once, hand the same component to everyone — has no correct version, because
a DM and a player can be watching the same page and the publisher can only pick
one decision.

### Definition of Done

Every milestone:

- `go test -race -shuffle=on ./...` green
- `go vet ./...` and `golangci-lint run` clean
- coverage not below the gate
- documentation updated
- an ADR written if a decision was made
- `CHANGELOG.md` updated
- no TODOs left unlinked to an issue
