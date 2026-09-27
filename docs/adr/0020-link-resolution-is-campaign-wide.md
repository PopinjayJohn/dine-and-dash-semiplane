# 0020. Link resolution is campaign-wide, and this records what that costs

- **Status:** accepted (as a finding, with the fix deliberately not taken yet)
- **Date:** 2026-02-14
- **Amends:** ADR 0003 (no effect on a session), ADR 0007 (the read predicate)
- **Milestone:** M9 (found while building the editor's preview)

## Context

`index.Resolver` reads every lookup as `store.AsDM(r.campaignID)`. Link
resolution in a rendered page is therefore campaign-wide: a `[[link]]` to a
`dm-only` page **resolves**, and the rendered page differs from what a player would
see if the link did not resolve.

The visible difference is the `unresolved` class. A link to a page that exists
gets `href="/c/<slug>/<path>"`; a link to a page that does not is a `<span
class="wiki-link unresolved">`. So a reader can tell which paths exist, and can
enumerate them by trying.

This was found in M9 while building the editor's preview, and it was not fixed
there, for reasons that are about cost rather than about the finding being wrong.

## Why it is not a content disclosure, and why it is still a finding

**It is not a content disclosure.** The link's *text* is what the DM wrote in a
page the reader may read, and the target is a path the DM also wrote. Nothing the
reader could not already read is in the rendered bytes, and §9's concern — the
*content* of a page a principal may not see — is not what this leaks.

**It is still a finding**, for two reasons. A player can build an inventory of a
campaign's page tree, and a TTRPG campaign's page tree is a map of what the DM has
planned: `sessions/09-the-dragon-heist-arc-two` existing is a spoiler that a page
that does not exist yet is not. And the "existence" distinction is one a reader can
only draw by trying paths, which means the *rendered* page depends on the index
being complete — a page the sync has not reached yet reads differently from the
same page a second later.

## Why the fix was not taken in M9

The three available fixes and what each costs:

1. **Resolve under the reader's decision.** The renderer holds a `LinkResolver` and
   the store's lookups take a principal, so the resolver would need the reader's
   principal — and a renderer is built per campaign and shared, so it would have to
   come from the context, or a renderer per principal per campaign. The cache is
   keyed by `CanSeeSecrets` and that *is* a complete discriminator for the output
   (two principals rendering the same page produce the same bytes, because the only
   principal-dependent thing a renderer can observe is secrets and link
   resolution), so a context-carried principal would not multiply the cache. But it
   is a change to the render boundary, and the derivation is in `internal/index`
   while the reader is in `internal/http`, so the two would have to agree on a
   context key.
2. **A per-principal renderer cache.** One renderer and one 500-entry LRU per
   player per campaign, for a campaign with four players. Rejected as a cost with
   no property in return.
3. **Leave it, and say so.** This ADR.

**Why leave it is defensible right now**: the leak is existence, not content; the
campaign is a small closed group whose members know the table; and the alternative
is a change to the render boundary in the same milestone that changed the write
path. Both changes at once, in the milestone where a player first gets to write
markdown, is how one of them goes unreviewed.

**What makes it M10's problem**: M10 makes links *more* visible. Search-as-you-type
resolves candidates against the index, the client will show a title for a target,
and a `dm-only` page's title in a search dropdown is a larger disclosure than a
resolved `href` in a page body. The fix and the feature arrive together, which is
the right time to have the conversation about what a reader may be told exists.

## The fix, decided but not yet built

`domain.WithPrincipal(ctx, principal)` — the domain owns the `Principal` type, so
the context key belongs to it. The session middleware sets it; the resolver reads it
when it has no explicit principal. The renderer is unchanged and so is the cache
key, because `CanSeeSecrets` already discriminates the output completely; the
derivation in `internal/index` changes in exactly one function.

**Consequences when it is built.** A page that links to a `dm-only` page renders
its link unresolved for a player and resolved for a DM, which is what §9's
"unresolved links render with an `unresolved` class" was always assumed to mean. The
golden files in `internal/render` do not change, because the renderer takes a
resolver as a parameter and the tests' resolvers are unaffected; the change is
visible only in `internal/index` and in an integration test that says a player's
render of a page with a `dm-only` link is byte-different from a DM's.

**Consequences now, while it is not fixed.** Any test that asserts on a rendered
page containing a link to a page the reader may not see is asserting the *current*
behaviour, and such a test does not exist — which is the point: nothing depends on
it, so nothing breaks when it changes.
