# 0003. Authentication is a per-player share link, exchanged for a cookie

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The product requirement is that a DM hands each player one URL and the player
does nothing else: no account, no password, no registration. That rules out
session cookies issued by a login form as the *entry point*, and rules out
anything that requires the player to store a credential before first use.

The naive implementation accepts the long-lived credential from the query string
on every request:

```
https://host/c/rivergate?k=<token>    on every single request
```

That keeps a bearer credential in the address bar indefinitely, which leaks it
through the `Referer` header to every outbound link on the page, through browser
history and bookmarks, through screenshots and screen shares, and into any proxy
or access log that records the request line.

The deployment target is a DM's own machine or a self-hosted instance, so there
is no registration funnel and no email channel to recover accounts through. The
DM *is* the account authority.

## Decision

Authentication is a **capability URL**, exchanged exactly once for an opaque
session cookie.

**Link issuance.** The DM clicks "New player link". The server generates 32
bytes from `crypto/rand`, stores only `sha256(token)` plus a four-character hint
for the UI, and displays the URL exactly once. The plaintext token is never
persisted and is not recoverable afterwards.

```
https://host/c/<campaign-slug>?k=<token>
```

**Link redemption.** Middleware, on the first request carrying `k`:

1. `sha256` the token, look up `principals.token_hash` (indexed).
2. Reject if revoked, expired, or the campaign slug does not match.
3. Create a `sessions` row and `Set-Cookie` an opaque session id (`__Host-`
   prefix, `HttpOnly`, `SameSite=Lax`, `Secure` in production).
4. Respond `303` to `/c/<campaign-slug>/` — **the token leaves the URL here.**
5. Record `share_link_used` in `audit_log`.

Subsequent requests authenticate with the cookie only.

**Revocation** is immediate because sessions are rows rather than signed
blobs: revoking a player deletes the principal and its sessions in one
transaction, and that player's existing browser stops working on the next
request.

**Roles** are `dm` and `player`. There is no `editor` role; edit rights are
derived from page ownership (see [0007](0007-access-control-model.md)). A second
DM is simply another principal with `role = 'dm'`.

**Hardening**, each with a named test in `docs/spec.md` § Authentication:

- Constant-time comparison on the token hash.
- A redacting logger. The token is never written to any log; a test runs a full
  auth flow and greps the captured log output for it.
- `Referrer-Policy: no-referrer`, `Cache-Control: no-store` and
  `X-Robots-Tag: noindex` on every campaign response.
- Rate limiting on redemption: 10 attempts/minute/IP.
- CSRF protection on all mutations: `SameSite=Lax` plus a double-submit token,
  carried by Datastar in a header rather than a query parameter.
- Session rotation whenever role or character binding changes.

## Consequences

**Good**

- The player does nothing but click a link. One-time friction only.
- The long-lived credential appears in exactly one request line, then is gone
  from the address bar, history and referrers.
- Per-player revocation and rotation are trivial and instant.
- The audit log records which link was used when, so "whose link leaked" is
  answerable.

**Bad**

- A link pasted in a Discord channel, or shared across a table, is equivalent
  to a leaked account for that player, scoped to a single campaign. This is
  documented in `docs/security.md` with mitigations: one link per player,
  revocation, optional expiry, and an "active now" view.
- The redemption redirect costs one extra round trip on first load. Accepted.
- Link tokens are not human-typable. The DM shares links, not codes, so this
  does not matter in practice.
- There is no recovery path for a lost link. The DM mints a new one, which is
  the correct behaviour anyway.
