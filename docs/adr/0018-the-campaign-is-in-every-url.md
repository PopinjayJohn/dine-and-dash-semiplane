# 0018. The campaign is in every URL, and the hub carries a notice rather than a page

- **Status:** accepted
- **Date:** 2026-02-14
- **Supersedes/amends:** ADR 0003 (the cookie's name), ADR 0006 (four functions,
  and the type's name), ADR 0008 (the CSP's answer, and when `internal/sse` lands)
- **Milestone:** M8

## Context

M8 is the milestone that puts a browser in front of a campaign, and three things
about it were not settled by the ADRs that came before.

**One.** The renderer built a resolved link as `/c/<path>`. ADR 0003 writes the
redemption URL as `https://host/c/<campaign-slug>/?k=<token>`, so the campaign has
been in the URL scheme since the first ADR — but the renderer's link has no
campaign in it, and a data directory holds several campaigns (ADR 0011). A DM with
two campaigns had a wiki whose links pointed at whichever campaign the reader
happened to be in.

**Two.** A live page needs somebody to tell the stream that the page changed. The
obvious shape is that the publisher — the index watcher — renders the changed page
once and hands the result to everyone watching it. That shape has no correct
version, and the reason is the one thing this project is about: a DM and a player
can be watching the same page at the same time, the publisher can only pick one
decision, and so either the DM's page arrives in a player's stream or the player's
arrives stripped in the DM's.

**Three.** ADR 0003 fixed the cookie's attributes and this milestone set them. The
`__Host-` prefix is not a decoration: a browser *rejects* a `__Host-` cookie
without `Secure`, silently. A DM running the wiki on their own laptop over plain
HTTP could therefore not log in at all, and the symptom is a wiki that has quietly
forgotten them.

## Decision

### A link carries its campaign, and the campaign is in the render cache key

`render.Page` grows a `Campaign`, and a resolved link is `/c/<campaign>/<path>`.
An empty campaign resolves nothing: the other available answer is a link to a
plausible wrong place, and this project fails closed everywhere else. A page with
no campaign is visibly wrong rather than subtly wrong, which is what makes a
handler that forgets the field a bug somebody finds.

The campaign is in the cache key, beside the decision. Two campaigns can each hold
`locations/rivergate` with byte-identical content — two DMs who both started from
the same template — and a shared entry would have served one campaign's URLs
inside the other's HTML. Same class of mistake as the decision not being in the
key, and found by writing down the test that asks it.

`render.PageURL` is exported because four things link to a page — a rendered wiki
link, the page tree, a backlink and a search result — and the second
implementation of "where does a page live" is the second thing to get wrong in
production.

**Consequences.** `RendererVersion` is 2, because the output changed for the same
input. A page rendered with no campaign has unresolved links, which is a visible
failure rather than a silent one.

### A link's auxiliary forms are query parameters, not path segments

`?raw=1` is the page's markdown and `?stream=1` is the page as a stream. They are
query parameters because a path segment would be a first-segment name the vault
could not also use, and the vault is the source of truth (ADR 0001), so it wins
any argument about what a URL may look like.

**Consequences.** `/c/blackwater/raw/notes/foo` is a page called `raw/notes/foo`,
which is what a DM with such a page would expect. Nothing is reserved.

### The hub carries a notice, and the subscriber renders

`Hub.Publish` takes a topic and nothing else. A subscriber registers a *builder* at
`Subscribe`, and that builder re-reads the page and re-renders it under its own
decision, through the same `renderPage` the page route uses.

This is the security property, not a convenience: a decision never crosses the
hub, so a DM's render cannot reach a player's stream even by accident. The cost is
one store read and one render per reader per change, and the render is cached by
content hash and decision, so four players watching one page is one render of the
DM's view and one of a player's, not four of either.

It also makes dropping a frame sound, which is why `Publish` is so small: a change
is not a delta, so a skipped notification is one the subscriber would have replaced
with a newer read anyway. A hub carrying deltas — or ordered events, such as a
chat log — cannot drop, and a caller who needs that must say so rather than
inherit the guarantee by accident.

**Consequences.** A subscriber whose builder returns an error is finished: its page
has gone, or its decision no longer admits it, and there is nothing to keep
sending. The hub carries no content, so it has no way to log what changed; the
caller says it.

### The cookie's name follows the deployment

`__Host-wiki_session` in production, `wiki_session` over plain HTTP.

The prefix is worth having: it is what makes a browser reject a credential that a
sibling subdomain might otherwise rewrite. It is also the reason a plain-HTTP
deployment cannot use it, because that rejection is silent — the failure mode is a
wiki whose every redemption succeeds and whose every page is empty, which looks
exactly like lost links.

`HttpOnly`, `SameSite=Lax`, `Path=/` and an explicit `Max-Age` are ADR 0003's and
are unchanged. `Max-Age` is the session's *absolute* bound and not the idle
window: the idle window is the store's, checked on every request, and a cookie
carrying it would log an active player out every twelve hours no matter how
recently they had used the wiki.

**Consequences.** The local name gives up the subdomain protection, which is a
real cost. It is the right trade for a laptop on a network the DM owns, and the
cost is written on the constant rather than assumed away.

### ADR 0006's four functions, with two amendments

The type is `Sender` and the constructor kept the name: `func Stream(w, r)
*Sender`. Go has no room for a function and a type of the same name in one
package, so the constructor gave, because it is the name a handler writes.

The keep-alive is a comment frame written by hand in the HTTP layer.
`internal/sse` has four functions and none of them is a comment, and adding a
fifth is a change to this ADR rather than a detail. The handler is the one place in
the application that knows the framing's spelling, and it says so.

**Consequences.** `internal/sse` lands in M8 rather than M10, because M8 ships the
first consumer. `spike/datastar` is still deleted in M10, when the interactive
work makes its bound and drain tests worth writing against both implementations.

### The CSP has a per-response nonce, and ADR 0008's redirect is a page-load

`script-src 'nonce-<per response>'`, `style-src 'self'`, `default-src 'none'`,
`base-uri 'none'`, `frame-ancestors 'none'`. There is no `unsafe-inline` anywhere,
and a nonce that repeats is a nonce that authorises a script an attacker injected
into an earlier response.

A redirect in this application is a `303` in the HTTP layer, not a script element,
because a `303` works without JavaScript and cannot be blocked by a CSP. ADR 0008
left open whether the CSP would have to permit a script; the answer is that
nothing in this application needs it.

**Consequences.** A `dm-only` image from elsewhere renders as a broken image, which
is the honest outcome and the reason `_attachments/` exists.

## Consequences

- `TestStoreReadPredicateMatchesResolver` is unaffected by the tenancy conjunct, and
  must stay unaffected: the principals it builds are rows in the campaign it reads,
  so the conjunct is a constant `true` across all 36 cells. Tenancy is not
  authorisation, so it is deliberately not in `access.For`.
- A share link is a reusable bearer credential rather than a one-time code. ADR
  0003's five steps do not rotate the token, and that is deliberate: the case it
  serves is a player who clears their cookies or wants the wiki on a second
  device, and the alternative is a DM issuing a fresh link every time a browser
  forgets somebody. The threat model's answer is revocation and expiry, and
  `TestALinkIsRedeemableAgainUntilItIsRevoked` asserts that revocation stops it.
- `make cover` measures hand-written statements. Generated templ output is filtered
  out of the profile first, for the same reason `.golangci.yml` filters it out of
  linting: it is what the template compiler produced from the `.templ` files, and
  measuring it says something about the templates' element branches rather than
  about whether the application is tested. The filter is a filename and nothing
  else, and the raw profile is still written.
