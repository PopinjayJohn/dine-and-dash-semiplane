# Pitfalls

Constraints this codebase has already learned the hard way, and the mistake each
one is guarding against. Every claim here was earned by something going wrong:
a test that failed on its first run, a property that was quietly `1 = 0`, or a
document that had promised a control nobody was invoking.

`AGENTS.md` carries a one-line version of each of these, because it is the file
every agent reads. This file carries the argument. If the two disagree, this
file is the one to correct — see
[ADR 0025](adr/0025-the-documentation-authority-order.md).

Nothing here is a decision about *what* to build. Those are in
[the specification](spec.md) and the [ADRs](adr/); this is what a change here
must not quietly undo.

---

## The store and the read predicate

### The read predicate's ownership test correlates on the page's *owner*

`pc.character_page_id = p.owner_character_page_id` — the page's owner, not the
page. The version that correlated on the page itself gives a player the one file
they are bound to and not the twenty notes under it, which is the opposite of
what ownership is for. It was `1 = 0` for two milestones, because no principal
owned a page; it is now an `EXISTS` over `principal_characters`, correlated on
the page. The correlation is the whole of it: a subquery that compared against
the *campaign* would admit every player to every `dm-and-owner` page in it.

Never widen that file without reading its header.

- Tests: `TestStoreReadPredicateMatchesResolver`, `TestAnOwnedPageIsOwnedForThePageItBelongsTo`,
  `TestOwnerPageIDIsTheSameAnswerASyncGets`
- ADR: [0007](adr/0007-access-control-model.md)

### The audience test requires a role

`? = 'player' AND p.visibility = 'players'`, not `p.visibility = 'players'`
alone. Without the conjunct, a request that identified nobody read the whole
campaign — and `TestStoreReadPredicateMatchesResolver` found it on its first
run. The spec wrote the same clause; §8 now says otherwise.

- Tests: `TestAudienceTestNamesTheLevelsThatAdmit`, `TestAnUnidentifiedRequestReadsNothing`,
  `TestSearchPublicAppliesTheAudienceScope`

### The read predicate asks whose campaign the principal is of

Not just which campaign the caller asked about. Nothing had ever asked before the
web shell existed, because every caller was the sync engine passing
`AsDM(campaignID)` and is therefore always of the campaign it is reading. A
predicate that is only correct for callers who get their arguments right is a
predicate one handler away from a disclosure.

Tenancy is not authorisation, so it is deliberately **not** in `access.For`, and
the 36-cell matrix test is unaffected on purpose.

- Tests: `TestAPlayerOfOneCampaignReadsNothingInAnother`, `TestReadsByIdScopeToThePrincipalsCampaign`,
  `TestAPrincipalOfAnotherCampaignReadsNothing`

### The write gate is in the store, and it checks ownership

In the store because a handler can forget a line and a signature cannot. It
checks *ownership* because that is a question about two tables; the *position*
half of §8's rule is `internal/index`'s `OwnerOf`, and duplicating it would be a
third implementation of one rule.

- Test: `TestASyncThatWritesAsAPlayerIsRefusedByTheGate`, `TestTheWriteGateAgreesWithTheResolver`
- ADR: [0017](adr/0017-the-write-gate-lives-in-the-store.md)

### A write refusal is `ErrNotAllowed`, deliberately not `ErrNotFound`

A read must not confirm a page exists. A write is a request about a page the
caller already holds, so it may be refused in the open.

- Test: `TestAWriteRefusalIsNotNotFound`

### Nothing a caller typed is concatenated into SQL

Every query clause is quoted with FTS5's own string quoting and the expression is
a bound parameter. There is a corpus and a fuzzer for it, and a test that strips
the literals back out and asserts nothing but the builder's own operators are
left.

- Tests: `TestSearchMatchExpressionQuotesEveryClause`, `TestScopesHaveOneArgumentPerPlaceholder`,
  `TestSearchStatementsCarryTheirReadScope`

### The sync engine reads files and writes rows, and never writes a file

That is the property [ADR 0001](adr/0001-files-as-source-of-truth.md) is about,
it is the first thing a change here can break, and a test hashes every file's
contents *and* modification time before and after a sync to keep it that way.
The editor and the importer are the only things that write markdown.

- Tests: `TestSyncDoesNotTouchTheVault`, `TestWatchDoesNotTouchTheVault`,
  `TestTheFilesAreUnchangedByASaveThatIsNotOne`

### A page is "settled" when re-deriving it from its file would produce the row that is already there

Not when its content hash matches, and not only for the page row: the aliases,
the link graph and both search rows are compared the same way.

A hash says a file has not changed, which is a different thing. Settling on the
hash alone meant a row could rot in place unnoticed, and a change to how fields
are derived would leave every row stale with matching hashes. One function
decides it, so `Sync` and `Check` cannot disagree about what is out of step.

- Tests: `TestPageIndexMatches`, `TestAChangeOfOwnerIsNotSettled`,
  `TestSyncSettlesOnTheSearchRows`

### Ownership is resolved, and the page's owner is the character

A page is character-owned when its path begins with `characters/<slug>/` or its
frontmatter declares `character: <slug>`, and **the path wins where they
disagree**.

- Tests: `TestOwnerOf`, `TestOwnerOfNormalisesWhatTheFileSays`, `TestWriteIsScopedToTheCharactersOwn`

---

## Secrets and the render path

### A rendered page is a function of (content, campaign, decision, role)

`access.Decision` has a fifth field, `ReadsAll`, and the cache key has it with
it. On a `dm-and-owner` page the owner and the DM share a `CanSeeSecrets` and
resolve the page's links differently, so one cache class held two readers.

Two campaigns can each hold a `locations/rivergate` with byte-identical content
— two DMs who both started from the same template — and a shared entry would
serve one campaign's URLs inside the other's HTML. A page rendered with no
campaign resolves no links, which is the same fail-closed answer as everywhere
else and the reason a handler that forgets the field is a visible bug.

- Tests: `TestTheCacheIsKeyedByTheDecision`, `TestTheCacheIsKeyedByEverythingThatChangesTheOutput`,
  `TestTheCacheKeyCarriesEveryFieldThatChangesTheBytes`,
  `TestTheRenderersDecisionIsTheAccessOne`, `TestAPageWithNoCampaignResolvesNothing`
- ADR: [0014](adr/0014-secrets-leave-the-tree.md)

### The cache key names no principal

Adding a *principal* to the key would be wrong rather than wasteful — every
player would get an entry for identical bytes.

- Test: `TestTheCacheKeyNamesNoPrincipal`

### `pages.body_public` is filled, so a page is findable by its prose

It is `render.PublicText`: the page's text with its unrevealed secrets removed,
as plain text rather than markdown, because the column is read by the tokenizer
and by nothing else.

A *revealed* secret stays — §9 says it is visible to everyone who can read the
page, and the public search's rows are filtered by the read predicate, so every
principal who can reach a hit may read it. A callout's *title* is in neither
half: it is an attribute on the node, not text in it.

- Tests: `TestPublicText`, `TestPublicTextHoldsNoSecretWord`,
  `TestPublicTextAndSecretTextPartitionThePage`, `TestReplacePageIndexRecordsThePublicBody`

### A link resolves for its reader, and the reader is the DM no more

The resolver reads `domain.PrincipalFrom(ctx)` rather than `AsDM`, so a `[[link]]`
to a `dm-only` page is unresolved for a player and live for the DM.

Before this, a player could tell which paths *exist* by whether a link came back
resolved, and a search dropdown and a change log were two more ways to ask the
same question.

- Test: `TestALinkResolvesForTheReaderAndNotForTheDM`, `TestAnIndexThatCannotAnswerIsNotAnUnresolvedLink`
- ADR: [0020](adr/0020-link-resolution-is-campaign-wide.md)

### The link graph is still the DM's, deliberately

The graph answers "what does this file point at", which is the DM's question and
the sync's. A campaign holds both answers at once on purpose: the file says a
link exists, the render says whether *this* reader may follow it.

---

## Authentication and sessions

### A share-link token cannot be printed

`auth.Token` implements `String`, `GoString` *and* `Format`, because `%#v` does
not consult `String` and `%x` on a struct hex-encodes its fields.

The redacting logger is a *handler*, so a caller who logs a whole request struct
is covered by the same rule as one who logs a token deliberately — and the named
test greps the log output of a full flow.

Note the price: a token's SHA-256 is 64 hex characters and so is a token, so the
shape check redacts a hash along with a credential. The log carries the
principal id instead.

- Tests: `TestATokenPrintsRedacted`, `TestNoTokenInLogs`, `TestRedaction`, `TestRedactionIsNarrow`
- ADR: [0003](adr/0003-url-token-auth.md)

### The redaction survives a log format switch

`internal/auth` wraps whatever handler it is given and has no unwrapped
constructor, so adding a format cannot route around it. Everything goes through
`log/slog` with named fields; `--log-format` and `DDSP_LOG_FORMAT` accept `text`
or `json` and are **refused** otherwise.

- Tests: `TestTheRedactionSurvivesTheFormat`, `TestTheRedactionIsNotBypassedByTheFormat`,
  `TestAnInboundRequestIdIsKeptAndSanitised`

### The query string is not logged

`?k=<token>` is the share-link credential and `r.URL.String()` would put it in
every log line, in every proxy in front of the server, and in whatever a DM pastes
into a bug report.

- Test: `TestTheQueryStringIsNotLogged`

### The users page mints a link and shows it once, and the list shows no token, no hash and no hint

Four characters of a 32-byte credential is a fingerprint worth having in a list
of six people at a table.

- Tests: `TestAMintedLinkIsShownOnceAndOnlyOnce`, `TestUsersListShowsPrincipalsByID`,
  `TestAHintIsNotEnoughToBeAToken`

---

## Sync, editing and the writer

### `internal/edit` is the only writer, and it checks before it writes

The order is the design: read the file, compare its hash, **ask the store's gate
about the content**, write the file, re-derive the row, keep the old text.

The gate goes *before* the write because **the index watcher writes rows as the
DM**. A player's file on disk for the length of a rollback is a file the watcher
will index, as the DM, into a page the gate refused. A refused write laundered
into the index through the one path that writes rows without asking is the whole
failure this ordering prevents.

- Test: `TestTheFilesAreUnchangedByASaveThatIsNotOne`
- ADR: [0019](adr/0019-the-writer-checks-before-it-writes.md)

### The sync takes a principal, and the gate sees the *derived* owner

So a player cannot present somebody else's character as the owner of a page: the
derivation decides who the owner is and the gate checks that, and neither is a
value in a request. This closed the residual ADR 0017 left for the editor.

- Tests: `TestTheOwnerIsDerivedAndNotSupplied`, `TestASyncThatWritesAsAPlayerIsRefusedByTheGate`

### A write is gated even when it would change nothing

A sync writes only if the index is not already what the file says, so a player
re-saving unchanged content never reached the gate. A no-op is neither a refusal
nor a success.

- Test: `TestAWriteIsGatedEvenWhenItWouldChangeNothing`

### A preview is the save's own four steps

Parse, derive, decide, render — in the same function. It started as a handler
that rendered the whole file and produced an `<hr>` where the frontmatter fences
were. One render path, one place the answer is made.

- Tests: `TestAPreviewRendersThePageAndSavesNothing`, `TestThePreviewIsUnderTheReadersDecision`

### A conflict is three texts and no merge

A merge is a decision about somebody's prose, and a server that makes it silently
has edited a DM's page without asking.

The base is empty — **not guessed** — when this application has not kept the text
the edit was made from.

- Tests: `TestAConflictIsRefusedAndNothingIsWritten`, `TestAStaleSaveIsA409WithThreeTexts`,
  `TestTheBaseIsEmptyWhenThisApplicationHasNotKeptIt`

### A rename rewrites links by byte offset, never by re-rendering

A `WikiLink` carries no source segment, so an AST rewrite would have to
re-serialise the page, and re-serialising a DM's markdown is the one thing this
project must never do to a file. goldmark is used for the one thing it is
reliable about, which is where the code blocks are.

- Tests: `TestReplaceLinksIsAllOrNothing`, `TestTheRewriterIsIdempotent`,
  `TestTheRewriterTouchesNothingElse`
- ADR: [0013](adr/0013-frontmatter-parse-tree.md)

### An archive removes the file and keeps the row; a purge deletes the row

The pair is recoverable-and-not, and a purge asks for a typed confirmation
because a browser's `confirm()` is suppressed by a prefetch and is not announced
by a screen reader.

- Tests: `TestArchivingRemovesTheFileAndKeepsTheRow`, `TestAnArchiveIsIdempotentAndAPurgeIsNot`,
  `TestAPurgeIsNotOfferedWithoutSayingWhatItLoses`

---

## HTTP, headers and the stream

### `?raw=1` and `?stream=1` are query parameters, not path segments

A path segment would be a first-segment name the vault could not also use, and
the vault is the source of truth, so it wins any argument about what a URL may
look like.

The raw endpoint's body is `render.PublicText` under the same decision as the
HTML, which is what keeps it from being the place a secret leaks.

### The 500 page asks the store for nothing

The store is the thing that has just failed, and a store that *panics* rather
than errors takes the process down from inside the recovery handler, with every
other player's session on it.

- Test: `TestAPanicIsAPageAndNotADeadProcess`, `TestAStoreFailureIsAPageAndNotAStack`

### Every response carries a `Content-Security-Policy`, whatever the nonce

The header is unconditional and an empty nonce yields `script-src 'none'`.

This was M13's item, and the item was a **hole in a policy that had already
shipped**: a failed `crypto/rand` produced a response with no policy at all,
because the header was set only when a nonce had been produced — the exact
opposite of what `headers.go` argues for three functions below the code doing it.
The nonce source is `http.Config.Nonce` so the path is testable, because a
`crypto/rand` call inside a middleware is a failure path with no test, which is
how it stayed wrong for nine milestones.

- Tests: `TestEveryResponseCarriesAPolicyWhateverTheNonce`,
  `TestARequestWhoseNonceCannotBeGeneratedStillGetsAPolicy`,
  `TestTheContentSecurityPolicyIsPerResponseAndStrict`

### A rate limit is a 429 with a `Retry-After`, not the default 404

`auth.Limiter` and `auth.ClientIP` existed for seven milestones and
`TestRateLimitedRedemption` tested them for all of it, and `docs/security.md`
listed that test as *the control* against "the network, guessing share links".
Nothing invoked either. A test on a component nothing calls is a test of that
component, not a control on the route — and the route is where the attack is.

It is asked before a token is even read, and a script cannot tell a limit from a
wrong token, but a *person* can.

- Tests: `TestTheRedemptionRouteIsRateLimited`, `TestTheLimitIsReportedAsARateLimitNotAsAWrongToken`,
  `TestRedemptionIsRateLimitedEndToEnd`

### The SSE hub carries a notice, not a page

Each subscriber re-reads and re-renders under its own decision, through the same
`renderPage` the page route uses.

The obvious design — render once, hand the same component to every watcher — has
no correct version, because a DM and a player can be watching the same page and
the publisher can only pick one decision.

It is also what makes dropping a frame sound: a change is not a delta, so a
skipped notification is one the next read would have replaced anyway. A stream's
patch handler honours one selector and drops any other, so a hijacked stream can
at worst put a stale page on screen.

- Tests: `TestTheStreamRendersUnderTheReadersOwnDecision`,
  `TestTheNoticeGoesToTheReaderWhoMayNotSeeTheSecrets`,
  `TestASlowSubscriberLosesAFrameRatherThanThePublisher`, `TestAStreamIsOnlyAWholeReplacement`
- ADRs: [0006](adr/0006-sse-abstraction.md), [0021](adr/0021-one-reader-per-page.md)

---

## Search

### A search box is a box: the last term is a prefix

`fort` finds the fortified town, or a box shows a result only after the last
character of the word that finds it.

Only the *last* term, and only when asked for by name
(`search.WithPrefixLastTerm`), because the `*` is the one piece of FTS5 syntax
the match expression emits.

- Test: covered by the FTS expression corpus in `TestSearchMatchExpressionQuotesEveryClause`;
  the option itself has no named test of its own.

### `search.Hit` carries the audience

A policy that cannot see a hit's `Visibility` and `OwnerCharacterPageID` can only
say "hides everything", and the dropdown is a list of page *titles* typed one
character at a time.

- ADR: [0015](adr/0015-search-records-the-audience.md)

---

## Plugins

### A tree hook runs after the secret stripper, and an HTML hook before the sanitiser

Those two placements are the whole security argument.

A tree hook after the stripper is handed a tree with no secret text in it, so no
transform of it can put any back. A hook that ran *before* the stripper could lift
a `[!SECRET]` callout into the open body and the stripper would have nothing left
to remove.

An HTML hook after `Sanitise` would be a way for a plugin to put unsanitised HTML
on a page a player reads — "a plugin is not an author" is not an exception this
codebase can make.

- Test: `TestAPluginRunsBeforeTheSanitiserIsTheWholeArgumentForThisFile`,
  `TestATreeHookNeverSeesASecret`

### A plugin's output is filtered by the same allow-list as the DM's own markdown

Which is also why `house-rules` needed no sanitiser change: `callout-[a-z0-9-]+`
is the one class shape the sanitiser admits on purpose, and §12's callout type
turned out to be the seam the renderer left for this.

A plugin that needs an element or a class the core does not allow discovers it
cannot have one.

- Test: `TestAFieldsOutputGoesThroughTheOneSanitiser`, `TestAPluginFieldsOutputIsSanitised`

### A plugin may only *narrow* an access decision, and a failure denies

`access.Policies.Apply` ANDs the plugin's answer with the core's field by field,
so returning "granted" gets nothing.

The failure direction is the **opposite** of a render hook's: a panicking hook is
skipped because the render is still correct without it, and a panicking *policy*
denies because the answer it was going to give is the one keeping a page hidden.

- Tests: `TestAPluginPolicyCanOnlyTakeRightsAway`, `TestAPolicyThatCannotRunDeniesRatherThanFails`,
  `TestAFailedPolicyDeniesRatherThanDefaultingToTheCore`
- ADR: [0022](adr/0022-where-a-plugin-sits.md)

### A plugin's policy narrows what is served and listed, in Go, on top of the SQL predicate

The SQL is invariant 3 and a policy is a second, stricter layer, never a looser
one.

Three surfaces apply it, and **each was found by a test rather than by reading
the code**: the page route (which computes its decision *before* branching to
`?raw=1` and `?stream=1`, or the same page is served two ways under two
decisions), the editor (a save is a POST, and a POST is something a player can
send without loading the form), and the two listings.

- Test: `TestTheDropdownIsTheACLIsTheAnswer`, `TestAPluginPolicyHidesAPageARowReaches`

### A plugin's search field reaches the public index and nothing else

One shared `extra` column, because FTS5's column set is fixed at table creation
and a per-plugin column would be a per-plugin migration. A plugin that could
index into the private index could put a value in front of a principal the read
predicate never admitted.

- Test: `TestAPluginsFieldReachesTheExtraColumn`, `TestNoSearchFieldsIndexesExactlyWhatABuildWithoutPluginsIndexes`

### The render cache key carries no plugin field

A renderer's hooks are fixed at construction and each renderer owns its cache, so
two renderers with different plugins never consult the same map. What *would* be
unsafe is a persisted render, and nothing persists one.

`CacheKey.Type` was added late and for the same class of reason — a `type:`
change with an unchanged body is a change to the output, because a plugin's hook
may render a page differently according to it.

- Test: `TestTwoRenderersWithDifferentHooksDoNotShareACache`,
  `TestTheCacheIsKeyedByEverythingThatChangesTheOutput`

### There is no event-subscriber capability, and there is deliberately no caller for `Cache.Clear`

`house-rules` was going to demonstrate an event subscriber that invalidates the
render cache. The key already holds the content hash, so a saved page is a new
key, and `Cache.Clear` has no caller for the same reason.

A capability that exists only to be demonstrated is not a capability.

- No test: there is no capability to test.

---

## Fields and the 5e ruleset

### A field's value is redacted under the decision before a plugin sees it

With the same `PublicText` that fills `body_public` and the same condition the
body's stripper uses.

Redacting *unconditionally* — the version that looked right — hands the DM `[…]`
for a field the DM wrote and can read in the body of the same page. A field is
not a search index: `body_public` has no decision and a render does.

- Tests: `TestAFieldsValueIsRedactedBeforeAPluginSeesIt`, `TestAFieldsSecretDoesNotReachAPlayer`,
  `TestTheDmStillSeesTheFieldSecret`
- ADR: [0023](adr/0023-a-fields-value-is-redacted-under-the-decision.md)

### A field's HTML goes into the page's own buffer, so the one sanitiser runs over it

No exemption, no second sanitiser, no pre-rendered string on a row.

And **there is no fallback renderer** — a key nobody claimed renders as nothing,
because a page with a row for `created:` and `tags:` and everything else the DM
has ever typed is a page nobody asked for. ADR 0013's "unknown keys are
preserved" is about the *file*; preserving is not displaying.

- Test: `TestAFieldsOutputGoesThroughTheOneSanitiser`, `TestAnUnclaimedKeyRendersAsNothing`

### A claimed key is read through a seam that bypasses the vault's *write-side* closed set, and only the write side

ADR 0013 closed the set so that "a bug that writes `titel: Rivergate` into
somebody's campaign" cannot reach the filesystem. `Set` is still closed, nothing
in the seam can write, and `AddFieldType` refuses a core key so a plugin cannot
shadow `visibility:`.

The seam parses the frontmatter *text* a page row carries into a throwaway
`yaml.Node`, so ADR 0013's parse tree and its zero-byte-diff promise are not in
play.

- Test: `TestAPluginMayNotRedefineSomethingCoreOwns`, `TestCoreFieldsIsTheVaultsOwnList`

### A claim and a key the DM wrote are compared folded

Case, `_`, `.` and spaces all fold to `-`.

A plugin claims `casting-time` because claims are normalised like every other
name; a DM writes `casting_time` because that is what their editor produced. The
first version compared the strings, and a key that does not match is a key the
page does not have, so **nothing rendered and nothing errored**.

- Tests: `TestTheKeyIsFoldedOnBothSides`, `TestAClaimedFieldKeyIsNormalisedToLowerCase`

### One renderer, fifteen fields, dispatching on the page type

A 5e field means different things on different pages, so a renderer per key would
need the type anyway. A field of yours that does not apply returns `""` — which is
what stops `casting-time` appearing on a character page.

- Test: `TestAFieldThatDoesNotApplyToThePageRendersAsNothing`

### `Page.Fields` is not a cache key field, and `ContentHash` is why

A field lives in the frontmatter and the frontmatter is part of the file, so a
field change is already a content-hash change. A second field for a value
`ContentHash` covers would be a second answer to "has this page changed".

- Test: `TestAFieldsValueIsNotACacheKeyField`

### A field renderer that fails draws nothing and the page still renders

Exactly as a render hook's does. That is different from an access policy, which
*denies* on failure, and the asymmetry is ADR 0022's: a field is decoration on a
page a DM has to be able to read.

- Test: `TestAFieldRendererThatFailsRendersNothingAndThePageStillRenders`

### A claimed frontmatter key is rendered, and a claim without a renderer is refused

The original claim said a key would be "readable today". Readable turned out to
mean *readable and invisible*, and a claim with no renderer is a plugin that half
works in a way no test can see.

- Test: `TestTheFieldRendererIsHandedThePageAndTheDecision`

---

## Configuration, backup and release

### A missing config file is not an error, a broken one is, and a misspelled key is

The broken file names itself and the misspelled key names the key, because a
file that parses and applies nothing is a DM who changed a setting and has no way
to find out why.

The **environment beats the file**.

- Tests: `TestTheEnvironmentBeatsTheFile`, `TestAMisspelledKeyIsRefused`,
  `TestAConfigFileThatIsADirectoryIsAnError`

### `trusted_proxies` is a list of addresses or CIDRs, and a hostname is refused

A name would have to be resolved per request and can resolve somewhere else
tomorrow.

- Test: `TestTheTrustedProxyListDecidesWhetherAForwardedHeaderIsBelieved`, `TestAddressesAreNormalised`

### The database is copied with `VACUUM INTO`, and a backup does not take the serve lock

A `cp` of a WAL database gives you a main file consistent with *some* instant and
no `-wal` beside it: a database that opens and then quietly disagrees with the
vault, which is the one failure a backup must not have.

A backup that waits for the server to exit is a backup a DM cannot take *while
playing*, so the name collision is prevented with `O_EXCL` instead, which is
stronger.

- Tests: `TestTheDatabaseInsideTheArchiveIsAWholeDatabase`, `TestBackupsRestoreIdenticalIndex`,
  `TestTwoBackupsInOneSecondDoNotOverwriteEachOther`

### An export is a vault, and it is deterministic

Sorted entries, epoch timestamps, no directory entries, so two exports of an
unchanged vault are byte-identical and a DM can `git diff` one.

`wiki import obsidian` refuses a campaign that does not exist and writes nothing
until it has shown you what it would do, and `--force` does not exist in v1.

- Tests: `TestAnExportIsByteIdenticalTwiceOver`, `TestAnExportHoldsNoDatabase`,
  `TestAnImportRefusesACampaignThatDoesNotExist`, `TestAnImportWritesNothingUntilItIsToldTo`
- ADR: [0024](adr/0024-an-export-is-a-vault.md)

### `--lan` turns TLS on and does not offer to leave it off

It implies `--production` so the session cookie gets `Secure`, and it prints the
certificate's SHA-256 in `openssl` form so the browser warning can be *checked*
rather than dismissed.

- Tests: `TestTheLANFlagCannotServePlaintext`, `TestTheGeneratedCertificateIsUsableAndSaysSo`,
  `TestTheCertificateIsKeptSoTheWarningIsTheSameEveryVisit`
- ADR: [0011](adr/0011-single-binary-data-directory-backup-unit.md)

### The e2e test is behind a build tag and is not Playwright

It needs Node and a browser download, and ADR 0004 is "pure Go, no CGO". Adding a
Node toolchain to a project whose whole claim is one static binary would trade a
property this project has for one it does not.

`make e2e` builds the binary, boots it, mints a link, reads a page and takes a
backup.

- Test: `TestNothingIsFetchedFromTheNetworkAtRuntime` is the Go-side guard
