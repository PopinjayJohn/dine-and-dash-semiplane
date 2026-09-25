# 0007. Access control: three-level visibility, character ownership, secrets stripped at render

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

Originally the DM was the only author and the only reader of private content,
so access control was a single boolean on a page. Players now author their own
character pages. That changes the threat model materially:

- The DM is the highest-value target. A player-authored page containing script
  is an attack against the person who administers the campaign.
- Content is authored by several people with differing privacy needs. A player
  must be able to write notes they intend to keep private, and later choose to
  reveal them.
- The DM should not have to remember who has seen what.

The design has to make "this text reaches the wrong person's browser" a
structurally difficult mistake rather than something to be careful about.

## Decision

### Page visibility is a three-level enum

| `visibility`   | Visible to                            | Who may set it   |
|----------------|---------------------------------------|------------------|
| `dm-only`      | DMs                                   | DM only          |
| `dm-and-owner` | DMs and the player(s) owning the page | DM, or the owner |
| `players`      | DMs and every bound player            | DM, or the owner |

`dm-only` is absolute. Character ownership never unlocks it, and a player can
never set it. The owner can move a page one level less strict, which is the
"reveal" action — a change of level, not a separate mechanism.

### Ownership

A page is character-owned when its campaign-relative path begins with
`characters/<slug>/`, or when its frontmatter declares `character: <slug>`.
Both resolve to the same `pages.owner_character_page_id` column. A `character`
is not a separate entity: it is a page of type `character`, and a binding is a
row in `principal_characters`.

The DM creates a character page and binds the player to it; the player then
authors everything beneath it. Players may create pages only within their own
character subtree, or carrying their own `character:` frontmatter.

Confirmed: players can read each other's character pages' public content. Only
`[!SECRET]` blocks and `dm-and-owner` pages are private.

### Secrets are blocks, not pages

A `[!SECRET]` callout, valid Obsidian, is private to DMs on DM-owned pages and
private to DMs plus the owning player on character-owned pages. A `{.revealed}`
attribute makes it public to everyone who can read the page.

### The load-bearing rule

**Secret content is removed from the response bytes, server-side, at render
time. There is no CSS hiding, anywhere.**

One render path takes the full page body plus an `access.Decision` and strips
what the decision does not permit:

```
body --> goldmark AST --> [wiki-link, callout, SECRET plugins] --> sanitiser --> HTML
             ^
             +-- SecretStripper(policy)   <-- removes blocks not permitted by Decision
```

Consequences that bind the implementation:

- There is exactly one render path. A second "public render" path would mean a
  second set of golden tests and a second place to get the stripping wrong.
- `pages.body_public`, stored with secret blocks replaced by a marker, exists
  **only** to keep secret text out of the public FTS index. It is never used
  for rendering.
- The stripper takes a `Policy` interface whose default implementation permits
  **nobody** to see secrets. M3 therefore ships fail-closed: an unfinished
  access-control feature leaks nothing, because the default denies.
- Every surface that could echo a secret is in scope: page HTML, search results
  and excerpts, the raw-markdown endpoint, revision diffs, SSE frames,
  `$signals` state, canonical metadata, the page tree and the tag list. The
  test is a forbidden-substring assertion over the raw response body, not a DOM
  inspection.
- A DM who wants a secret shared with two players but not a third has no
  mechanism for it. This limitation is accepted for v1; per-principal ACLs are
  the natural extension to `access.Policies`.

### One resolver, evaluated twice, tested for agreement

```go
// internal/access/decision.go
type Decision struct {
	CanRead, CanEdit, CanReveal, CanSeeSecrets bool
}

func For(p Principal, page PageMeta) Decision  // pure; no DB, no mocks
```

Search, listings, the page tree, backlinks and tag clouds all filter through
the `page_acl_read` view, which encodes the same rule in SQL. A test asserts
the view and the Go function agree for every cell of the matrix, because a
mismatch between them is a leak.

### Sanitisation applies to every author

Rendered markdown is sanitised for the DM's content as well as the players'.
Sanitising "only untrusted authors" would leave the highest-value target
defended by the weakest path, and the DM is exactly who a player would target.

## Consequences

**Good**

- "Reveal" is a single well-understood action at both page and block level.
- The DM gets a list of every unrevealed secret and where it lives.
- Secrets cannot leak via a forgotten CSS rule, because there is no CSS rule to
  forget.
- The rights matrix is a pure function, so all of it is table-testable.

**Bad**

- The access predicate is applied in at least three places — the Go resolver,
  the SQL view and the FTS join — and they must stay in agreement. Mitigated by
  the agreement test, but a real ongoing cost.
- Character ownership is partly defined by path, so moving a directory changes
  ownership. Moving pages is DM-only for the same reason.
- A player editing their own page can change its `visibility` from
  `dm-and-owner` to `players`. That is intended, and it is why "reveal" is a
  deliberate action rather than a side effect of saving.
