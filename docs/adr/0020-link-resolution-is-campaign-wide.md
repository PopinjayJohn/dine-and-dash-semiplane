# 0020. Link resolution is campaign-wide, and this records what that cost

- **Status:** accepted, and **implemented in M10**
- **Date:** 2026-02-14
- **Amends:** ADR 0003 (no effect on a session), ADR 0007 (the read predicate)
- **Milestone:** M9 (found), M10 (fixed)

## Context

`index.Resolver` read every lookup as `store.AsDM(campaignID)`. Link resolution in
a rendered page was therefore campaign-wide: a `[[link]]` to a `dm-only` page
**resolved**, and the rendered page differed from what a player would see if the
link had not resolved.

The visible difference is the `unresolved` class. A link to a page that exists
gets `href="/c/<slug>/<path>"`; a link to a page that does not is a `<span
class="wiki-link unresolved">`. So a reader could tell which paths exist, and
could build an inventory of a campaign's private page tree by trying paths and
watching for links that came back resolved.

M9 found this while building the editor's preview, and recorded it as a decision
*not* to fix: the fix was available and each available form cost more than the
finding was worth, and the milestone already had one change to the write path in it.

## What it cost, now that it is fixed

**A context value.** `domain.WithPrincipal` / `domain.PrincipalFrom`, set once by
the session middleware and read by the resolver. Not a parameter on `Render`,
because a reader on every call site in the project is a reader somebody forgets;
not a per-principal resolver, because that is a renderer per player per campaign.

**A fifth decision field.** `access.Decision.ReadsAll`, which is "may read every
page in the campaign" and is true for a DM and false for everybody else. It
matters because the *output* now depends on the reader's role, and the render
cache is keyed by the decision: on a `dm-and-owner` page the owner and the DM share
a `CanSeeSecrets` and resolve the page's links differently, so without a second
axis the cache serves whichever rendered first — a disclosure one way, and a DM
served their own link as unresolved the other, which would be reported as a broken
wiki rather than as a security problem.

That is the cost I predicted wrong when I wrote "the cache key does not need a
principal, and here is the two-class argument". The argument was right about
`CanSeeSecrets` and wrong about `dm-and-owner`, and the page whose owner is a
*player* rather than a *character* is the one that finds it.

**A test that guards the other direction.** `TestTheCacheKeyNamesNoPrincipal`,
because adding a principal to the key would not be wasteful but *wrong*: every
player would get their own entry for byte-identical output, so the busiest page in
a campaign fills the cache once per player.

## Decisions

### A link resolves for the reader in the context

`internal/domain` owns the key, because it owns `Principal` and a key defined in
`internal/http` and read in `internal/index` is two packages agreeing on an
unexported value.

A context with no principal in it is nobody, and nobody reads anything. So a
render that is not a request — the goldens in `internal/render`, a plugin's render
hook, a command rendering to a terminal — gets unresolved links for every link
rather than a second behaviour to reason about.

### The link graph stays the DM's

The graph is built under an explicit DM principal, in `derive.go`, and says so.
The graph answers "what does this file point at", which is the DM's question and
the sync's: backlinks work, a page whose link now resolves gets re-indexed, and
the DM can see their campaign's structure. A graph built under a player's decision
would be missing every edge that leaves a `dm-only` page, which is most of them.

**A campaign holds both answers at once, deliberately.** The file says a link
exists; the render says whether *this* reader may follow it. Collapsing them would
lose one of the two, and the losing one is either the DM's backlinks or a
player's ability to see what they may not.

## Consequences

- The search dropdown and the session log are ACL by construction rather than by
  filtering, and both were the same disclosure arriving by another route — so both
  have a test whose negative half asserts the strings that must not be in the
  response.
- `TestStoreReadPredicateMatchesResolver` and the 36-cell matrix are unaffected:
  tenancy and audience were never this finding's subject.
- A rendered page is now a function of (content, campaign, decision, role) and
  nothing else. `TestTheCacheKeyIsStillComplete`'s argument, corrected, is the
  two-axis version of that.
