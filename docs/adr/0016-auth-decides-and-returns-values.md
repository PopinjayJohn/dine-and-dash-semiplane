# 0016. Auth decides and returns values, and there is no comparison to make constant-time

- **Status:** Accepted
- **Date:** 2026-09-27
- **Relates to:** [0003](0003-url-token-auth.md),
  [0015](0015-search-records-the-audience.md), [0007](0007-access-control-model.md)

## Context

[ADR 0003](0003-url-token-auth.md) decides that authentication is a capability URL
exchanged exactly once for a session cookie, and §10 of the spec lists the
hardening it requires. Two of those requirements do not fit the way the spec words
them, and M6 is the milestone that has to either satisfy them or say why not.

**There is no `net/http` in M6.** The milestone's own scope says "cookie exchange
and scrub redirect", which is HTTP: a `Set-Cookie` header and a 303. The project's
pattern since M2 has been that no milestone adds an `internal/http` until M8, the
web shell, and that pattern is written down.

**The spec asks for "constant-time comparison on the token hash".** There is no
comparison to make constant-time, because redemption never compares a presented
token to a stored one.

## Decision

**`internal/auth` returns values and M8's router turns them into a response. And
the redemption path has no token comparison, so it has no constant-time
comparison; what is asserted instead is the property that makes the absence safe.**

### The exchange is a decision and a set of bytes, not a handler

`Redeem` takes a campaign slug and a token and returns a `Redeemed`: the principal,
a session id, an expiry, and a redirect that does not contain the token. It does
not take a `*http.Request` and does not return a `*http.Response`.

The two halves of that are separable on purpose. The interesting part of a
redemption — five checks, in an order where each one is a place a naive
implementation gets it wrong — is a function that can be called from a test with two
arguments. The uninteresting part is four bytes of a `Set-Cookie` header, and the
part that is easy to get wrong and never to look at twice.

The alternative was worse than "a bit more plumbing". If `Redeem` took a request,
then every property in §10 would be testable only by standing up a server:
`TestNoTokenInLogs` would have to spin one up and capture its output,
`TestRateLimitedRedemption` would have to make real connections, and the
`Secure`-in-production rule would be either untestable or tested by inspecting a
response a test constructed. So M8 owns the response and M6 owns the decision, and
the two meet at one struct whose every field is a thing a handler has to put in a
response.

The cost is that M8 has a little wiring to do, and that the cookie's attributes —
`__Host-`, `HttpOnly`, `SameSite=Lax`, `Secure` in production — are not written
down anywhere in M6. They are in ADR 0003 and they are M8's, and this ADR is the
place to say that the omission is deliberate rather than an oversight.

### The ownership branch and the binding table, together

Not in this ADR — it is the tail of [ADR 0015](0015-search-records-the-audience.md)
and the reason is the same one. What is recorded here is that the two shipped
together: an ownership test that is `1 = 0` and a binding table that nothing reads
is a predicate nobody has run, and the tests that would catch it are tests written
against a page a DM has not written yet.

### No constant-time comparison, and what is asserted instead

Redemption is: hash the presented token, look the hash up in a `UNIQUE` column,
compare nothing. There is no presented value and no stored value being compared, so
there is no comparison whose duration depends on where the first differing character
is. The lookup is a b-tree search on an index, not a scan, so it does not leak the
hash either.

`crypto/subtle` is not imported. Adding it to a path with nothing to compare would
be a function that looks like a control and is not one, and the cost of a fake
control is that the next reader trusts the path.

What is asserted, and what the test is for:

- a token that is the wrong length, is not hex, belongs to nobody, is a prefix of a
  real one, is a truncation of a real one, or differs by one character, is refused;
- a real link for another campaign is refused;
- **the two halves have to agree about what a token is.** The minting side hashes 32
  raw bytes and the redemption side parses 64 hex characters, and the version of
  that agreement where each side hashes whatever it happens to be holding is a bug no
  unit test finds and every player does: every link in every campaign fails with "that
  share link is not valid". `ParseToken` is the one function both halves go through,
  and its existence is the test.

## Consequences

**Good**

- Every hardening item in §10 except the cookie attributes is tested with two
  arguments and a store, not a server.
- The exchange's decisions are readable in one file, and the file is the one a
  security review would read.
- The absence of a constant-time comparison is stated where somebody would otherwise
  add one, with the reasoning for why there is nothing to add it to.
- The mint/redeem agreement is one function, so it cannot drift.

**Bad**

- **M8 has to do the wiring**, and the cookie's attributes are decided there rather
  than here. A milestone's worth of security-critical header values live in a
  document rather than in the code that sets them until M8. That is the real cost of
  this decision and it is a cost, not a saving.
- Two halves of one flow cannot be exercised as one flow until M8. A bug that needs
  a real `Set-Cookie` round trip — a cookie not being sent back because of
  `SameSite`, a `__Host-` prefix rejected because the host is not `localhost` — is
  not findable in M6's tests. Those are exactly the failures a capability-URL design
  is prone to, and M8 is where they will show up.
- `ParseToken` rejects a token of the wrong length before touching the database,
  which is right, and also means a redemption with a two-character token does no
  work. That is a small denial-of-service saving and mostly a reason the test can
  assert it.

**Neutral**

- A rate limiter in one process is a rate limiter per process. A DM runs one binary
  on one machine; if this is ever run as several, the limit is per-process and the
  fix is in one place, which the comment in that file says.

## Alternatives rejected

**Give M6 a minimal `net/http` handler.** Rejected for the reasons above, and
because it would have made M8's real work — middleware ordering, the layout, the
page view, `/healthz` — start as "and the auth handler that was already there",
which is how a milestone ends up doing two.

**Issue a `net/http`-shaped interface from M6** — `type Handler func(*http.Request)
(http.Response, error)`. Rejected: it is the same coupling wearing a hat, and it
would make M8's router a pass-through for a signature this project chose two
milestones early and for reasons that will not apply once there is a router.

**Keep `1 = 0` and ship the binding table.** Rejected: a predicate nobody has run
against a page that exists is a predicate that does not work, and the tests that
would find out are tests a DM has not written yet.

**Add `subtle.ConstantTimeCompare` for show.** Rejected above, and it is worth
naming because it is the most likely future change to this file: somebody reading
"no constant-time comparison" will feel that something is missing, and the right
response to that feeling is to read this ADR rather than to add a function.
