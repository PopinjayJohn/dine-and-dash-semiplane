# Security model

What this application protects, from whom, and — more usefully — what it does
**not** protect against. Every claim here points at the test that enforces it,
because a security property nobody tests is a comment.

Read alongside `docs/adr/0007-access-control-model.md` (the access rules) and
`docs/adr/0003-url-token-auth.md` (authentication).

## What is being defended

A D&D campaign is a long-lived collection of unreleased plot. The value at risk
is not money and not accounts; it is **the DM's ability to run the game**. A
player's character sheet leaking is embarrassing. The villain's identity
leaking ends the campaign.

## Assets, in priority order

| # | Asset | If it leaks |
|---|---|---|
| 1 | Unrevealed `[!SECRET]` content | The campaign ends |
| 2 | `dm-only` pages | The campaign ends |
| 3 | The DM's browser, via script in player-authored markdown | The campaign *and* the machine |
| 4 | A player's share-link token | That player's access, for that campaign |
| 5 | The vault on disk | Everything above, at rest |

Asset 3 is the one that makes this product unusual. Players author content, so
the **DM is the highest-value target**: the one browser with the whole campaign
in it, on the one machine that is not hardened. There is no reason to trust
your own input more than a player's.

## Threat actors

| Actor | Capability | Mitigations |
|---|---|---|
| A player, malicious | Authors pages, reads own character's pages | `[!SECRET]` stripping, the ACL on every read, HTML sanitisation, ownership limits on writes |
| A player, careless | Pastes their link into Discord, or shares the wrong one | Scoped to one campaign, revocable instantly, `docs/adr/0003-url-token-auth.md` |
| Someone who obtains a leaked link | Full read of one campaign as that player | Revocation is a row delete, effective on the next request; optional expiry |
| A DM's own machine, compromised | Everything | Out of scope. Not addressable in software that runs there |
| The network, on a LAN | Sees plaintext HTTP if `--lan` without TLS | Self-signed TLS offered by `wiki serve --lan`; `--lan` prints the URL so the DM knows what they chose |
| A third-party plugin | Compiled into the binary, running as the app | See the limits in [ADR 0010](adr/0010-core-system-agnostic-dnd5e-as-plugin.md): no raw DB access, no secret-policy override, no second render path |

## Controls, and where each is tested

| Control | Test |
|---|---|
| Secret content is absent from the **raw response bytes**, not hidden | `TestSecretStrippedFromAllSurfaces` |
| Secrets never appear in hits, snippets or counts | `TestSearchNeverRanksOrQuotesSecrets` |
| The SQL view and the Go resolver agree, for every cell of the matrix | `TestStoreReadPredicateMatchesResolver` |
| Every cell of the rights matrix behaves as documented | `TestForDecisionMatrix` |
| Rendered markdown is sanitised **for the DM too** | `TestPlayerAuthoredXSSSanitisedForDM` |
| A player cannot create a world page | `TestPlayerCannotCreateWorldPage` |
| A player cannot reveal another player's secret | `TestPlayerCannotRevealOthersSecret` |
| A share-link token appears in no log line after a full auth flow | `TestNoTokenInLogs` — and the log's redaction is a handler, so logging a request struct is covered by the same rule as logging a token |
| Redemption is refused past the limit, and the limit counts the attempts it refuses | `TestRateLimitedRedemption` |
| A revoked link stops working on the *next* request, and every session it made dies with it | `TestRevokingALinkStopsTheBrowserOnItsNextRequest` |
| A role or character-binding change ends every session of that principal | `TestARoleChangeEndsEverySession`, `TestABindingChangeEndsEverySession` |
| A player can read their own `dm-and-owner` page, and nobody else's | `TestABoundPrincipalReadsTheirOwnCharacterPage` |
| A player can search their own character's secret text, and nobody else's | `TestABoundPrincipalReadsTheirOwnSecretText` |
| A vault backup plus `reindex --full` rebuilds a byte-identical index | `TestBackupsRestoreIdenticalIndex` |
| The app boots and serves with no network | `TestOfflineBoot` |

The first row is the one that matters most. The assertion is
`bytes.Contains(body, canary) == false` over the raw body. A DOM inspection, or
a CSS rule, or an `aria-hidden` attribute would all pass a weaker test and all
leak. See [ADR 0007](adr/0007-access-control-model.md) for why there is no CSS
hiding anywhere in this project.

## Accepted limitations

These are not oversights. Each is a decision, and each is written down so that
nobody discovers it at a table.

1. **A share link is a credential for one player, scoped to one campaign.** If
   it is pasted in a Discord channel, treat that player as compromised and
   revoke. The mitigations are one link per player, instant revocation,
   optional expiry and an "active now" view — not prevention. There is no
   second factor, because a player who has to do something at the table is a
   player who does not do it.
2. **A DM cannot share a secret with two players and not a third.** Visibility
   is per page and per block, not per principal. Per-principal ACLs are the
   natural extension and are not in v1.
3. **Players can read each other character pages' public content.** This is
   deliberate and confirmed. Only `[!SECRET]` blocks and `dm-and-owner` pages
   are private.
4. **An unrevealed secret is findable by the DM's own search, and only by
   principals the ACL admits.** The split is described in
   [ADR 0009](adr/0009-two-index-search-with-rrf.md). The corollary is that a
   player *cannot* search for a secret they cannot read — which is correct, and
   is also a small amount of information ("that word is not findable") that this
   project does not try to hide.
5. **The DM's machine is trusted.** The binary runs there, the vault is on it,
   the database is on it. A wiki cannot defend a compromised host and does not
   pretend to.
6. **Self-signed TLS produces a browser warning.** `--lan` is a convenience for
   playing at a table, not a secure channel across the internet. Do not expose
   a self-hosted instance to the open internet.

## Reporting

Report a suspected disclosure to the maintainer privately, with the route and
the principal that triggered it. Include the build version from
`wiki version` and, if the vault and the index disagree, the output of
`wiki sync --check`.
