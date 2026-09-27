# Milestones

**This file is a historical record.** It is the commit sequence of every
milestone that shipped, with the note that explains why each one went the way it
did. Nothing here is maintained against the code, and the sequences are a copy
of `git log` — treat them as a record of what was decided, not as the current
state of anything.

For what the project *is*, read [`spec.md`](spec.md). For the live milestone
list and the next piece of work, read [`spec.md` §16](spec.md#16-milestones).
For why the code looks the way it does — the constraints that a change must not
quietly undo — read [`pitfalls.md`](pitfalls.md) and the [ADRs](adr/).

The sequences are here because a commit subject is a one-line decision record,
and thirteen of them add up to a paragraph of context each that no single file
in the repository otherwise holds. They were moved out of the specification when
the specification outgrew them, not because they were wrong.

Sequences are in milestone order rather than the order they happened to be
written down in.

---

## M0 — Foundation

```
chore: initialise Go module and tooling
build: add Makefile with test, lint, cover, fuzz targets
ci: add GitHub Actions workflow (vet, lint, race tests, coverage gate)
chore: add commit template and changelog enforcement script
docs(adr): record Datastar release and SSE client pin from the M0 spike
```

## M1 — Domain and store

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

Four of those are not postscripts. Choosing a migration runner departed from §3
and needed recording; the two `build:` commits fix tools that the first code
commits made insufficient — the coverage gate was measuring each package with its
own test binary, which reports 0% for a package that only runs inside another
package's tests, and `make fuzz` was exiting zero having fuzzed nothing; the
driver bump is the Go version the pure-Go SQLite translation needs. And one
corrects ADR 0012, which had claimed the library had no pure-Go SQLite driver.
It has one. The decision to keep an in-repo runner stands on the corrected,
weaker grounds the ADR now gives, and §3 says so rather than repeating the
error.

## M2 — Obsidian storage

```
feat: read a DM's markdown without touching it
feat: check a path by resolving it, not by reading it
feat: write a page atomically, through a handle rather than a path
feat: keep the history and the attachments where Obsidian expects them
docs(adr): record why a document is the bytes it was read as
chore: record where the project actually is
```

The first commit is the milestone. A file the application did not change comes
back out byte for byte, which cannot be tested for and has to be structural — see
[ADR 0013](adr/0013-frontmatter-parse-tree.md). The third one moved every file
operation onto an `os.Root`, which is a directory handle rather than a name: the
kernel then refuses anything that would leave the vault, a symlink followed
included, and there is no window between checking a path and opening it. The
second commit had already done the string-level work that the handle makes
unnecessary, and it is the one whose fuzz targets state the invariant the rest
stands on.

## M3 — Renderer

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
"untrusted authors only" leaves the DM on the weakest path, and a cache keyed by
content hash alone is a channel from a DM's render to a player. Both are written
down in [ADR 0014](adr/0014-secrets-leave-the-tree.md), which also corrects
§11's two-field cache key.

## M4 — Sync engine

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

The second commit is the one that makes the third possible: the renderer finished
M3 with a `LinkResolver` and nothing behind it, and the link graph has to be
derived by the same walk as the rendered links or a link can resolve in the page
and not in the graph. The third is the milestone, and its own tests found the
design error worth finding — settling a page on its content hash, which a row
that rots in place and a change to how fields are derived both defeat. The fifth
ships the ownership *rule* without its column, because the rule decides which
subtree a player may write in and the column arrives with access control in M7.

## M5 — Search

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
silently widens every `dm-and-owner` page, and admitting every player to one is
a disclosure the moment a DM writes one.

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

The last commit is the one that writes the language down, in
[`search.md`](search.md), and corrects the two documents that were wrong about how
it arrived. Where a milestone's own documentation lands in the ADR commit is a
matter of taste; what is not a matter of taste is that a design decision with a
security consequence — the audience arriving before the code that enforces it, and
the public body arriving empty — is recorded rather than discovered by the next
reader.

## M6 — Auth and principals

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

The second one is a type that cannot print itself, and the reason is in the
commit body: `%#v` does not consult `String` and `%x` on a struct hex-encodes its
fields, so a `Token` with only a `String` hands out the whole credential to
`t.Errorf("%#v", err)`. Both were found by a test that checks every verb, and
both are why it also implements `GoString` and `Format`.

The third one had a bug that no unit test found and every player would have: the
minting side hashed 32 raw bytes and the redemption side hashed the 64 hex
characters, so no link in any campaign would have worked. One function,
`ParseToken`, is what both halves go through now, and its existence is the test.

The fifth is the named test `TestNoTokenInLogs`, and it runs a *full* auth flow and
greps everything it produced. The redactor had two bugs of its own: `Redacted`
checked the whole remaining string for hex rather than the 64-character window,
so a token at the start of a sentence was not recognised; and a token's SHA-256 is
64 hex characters and so is the token, so the shape check cannot tell a hash from
a credential.

The sixth is the one the previous two milestones were waiting for. The read
predicate's ownership test stops being `1 = 0` and becomes the `EXISTS` over
`principal_characters`, and a player can for the first time find their own
character page's secrets — which is the rule ADR 0007 has described since M0 and
which nothing could reach until a principal owned a page.

## M7 — Access control

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
written**: the ownership shortcut ran before the `dm-only` check, so a player
bound to a character page the DM had marked `dm-only` could read it, edit it and
see its secrets. §8 says `dm-only` is *absolute*, and writing all the cells out by
hand is the only reason anybody noticed.

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

## M8 — Web shell

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
  right and is now written down in
  [ADR 0018](adr/0018-the-campaign-is-in-every-url.md).
- The **cookie's name could not be ADR 0003's on a laptop**, because a browser
  refuses a `__Host-` cookie without `Secure` and says nothing when it does. A DM
  on plain HTTP got a wiki that forgot them after every link.

The sixth changes what the SSE hub carries, and it is the security decision of the
milestone: the hub carries a *notice* and each subscriber re-renders under its own
decision, so a DM's render cannot reach a player's stream. The obvious design —
render once, hand the same component to everyone — has no correct version, because
a DM and a player can be watching the same page and the publisher can only pick
one decision.

## M9 — Editing

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

The fourth commit is the missing half of handing somebody a link: §10 has said
since M0 that the DM clicks "new player link" and sees the plaintext once, and
until now there was no button. The minted link is a 303 to the page with the link
on it, the page says it will not be shown again, and the list shows no token and
no fingerprint.

## M10 — Datastar

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
role) and nothing else**, and `TestTheCacheKeyNamesNoPrincipal` is the guard on
the other direction, because a principal in the key would not be wasteful but
wrong: every player would get their own entry for byte-identical output.

The search work found two things in the fixture that were worth more than the
search itself. The test fixture used to write rows with `UpsertPage` and files
with hand-written frontmatter, and **the two had drifted** — `type:` was in the
struct and missing from every block, and one title contained a colon that is a
YAML error, so that page did not parse and was skipped entirely. Nothing failed
loudly, because the rows came from the struct and the files were never read by
anything. The fixture is built through the derivation now, like a real campaign,
and that is what let the search test notice it was searching an index of nonsense.

## M11 — Plugin framework

```
feat(plugin): the registry, and the whole surface a plugin may touch
feat(render): a render hook, on the one path, and where in it
feat(access): a policy a plugin may compose with, and never replace
feat(index): a plugin's search field, and the one column they all share
feat(http): a route, a command and an event bus, which are requests
feat(plugins): the three the milestone names, and the contract they run
docs(adr): record where a plugin sits, and what it may take away
chore: record where the project actually is
```

M11 is a milestone of two placements, and the seven feature commits are
arrangement around them. **The first two are `internal/plugin` and the render
hook, and the interesting line in the second is "on the one path":** M11 is the
first milestone in which something sits *between* a page's markdown and its HTML,
so "there is exactly one render path" stops being a statement about a function
and becomes a statement about a boundary. The boundary is narrower than the
sketch in §12 said it would be, and the narrowing is the milestone.

The fourth and fifth are the two capabilities whose cost turned out to be a
store change and a router entry rather than an interface. `search.Hit` grew two
columns so a policy can narrow a hit, because the dropdown is a list of page
*titles* typed one character at a time and a policy that hid a page and left its
title would not have hidden it. A plugin's route hangs off `/c/{slug}` because that
is the only place it can hang, and behind the same three middlewares a page gets.

The seventh is the ADR, and it is the one worth reading first: four of §12's
answers were wrong and three of them were wrong the same way, which is that the
sketch left a placement open and the code had to close it.

## M12 — The `dnd5e` ruleset

```
feat(render): a field a plugin renders, and the value it is handed redacted
feat(plugins): dnd5e, which is a ruleset rather than a demonstration
docs(adr): record what a field's value is, and who redacts it
chore: record where the project actually is
```

M12 is a two-commit milestone and the smallness is the finding. §12's `Fields`
capability looked like a field *schema* and turned out to be one function: a
plugin claims a frontmatter key, hands over a renderer, and the core decides
which keys a page has, in what order, and whether a value may be shown.
Everything else — a spell's level becoming a word, a statline keeping the DM's own
rows, fifteen fields served by one renderer — is the plugin.

The security question is the one §9's rule is about, asked of a *plugin's* output
for the first time. A DM can mark a frontmatter field secret with a `[!SECRET]`
callout inside its value, and the field block is plugin-authored HTML on a page a
player reads. So the value is redacted **under the decision** before a plugin sees
it, and the plugin's output is written into the page's own buffer so the one
sanitiser runs over it. The first version redacted unconditionally and handed the
DM `[…]` for a field they could read in the body of the same page — which is the
asymmetry between a search index's `body_public` and a render's decision, and it
is worth a named test.

## M13 — DX and release

```
fix(plugin): the core command list reserved the wrong names, and the test its comment named was not written
fix(http): a response with no CSP header is a page that is open
feat(log): a log format a DM can choose, and a refusal for one they cannot
feat(config): config.yaml, the three DDSP_* settings, and the rate limit that was never called
feat(cli): wiki users new, revoke and list
feat(cli): wiki backup, and the test three documents have been naming
feat(cli): wiki serve --lan, with the TLS security.md has been promising
test(cli): TestOfflineBoot, which two documents have been listing
feat(cli): wiki export --zip and wiki import obsidian, and the ADR for both
build: a Dockerfile, a release workflow, make dist, and an e2e behind a tag
docs: the README a DM reads, and the v0.1.0 release
```

M13 is nine items wide and its commit sequence is eleven commits, because **the
first two are defects rather than features and they were in the milestone's own
dependencies.**

The rate limit is the one worth reading the sequence for. `auth.Limiter` and
`auth.ClientIP` were built in M6, `TestRateLimitedRedemption` has tested them
since, and [`security.md`](security.md) has listed that test as *the control*
against "the network, guessing share links" ever since. Nothing invoked either.
**A test on a component nothing calls is a test of that component, not a control
on the route** — and the route is where the attack is. The `trusted_proxies`
setting that M13 needed to read turned out to be the third argument
`auth.ClientIP` had been waiting for since M6, so the config and the limiter are
one commit rather than two.

The other defect is M11's, found by the test M11's own comment named and did not
have: the reserved list of core command names held three commands that do not
exist and omitted two that do, one of which is how a database's schema is
applied.

Two items were specified as one and arrived as one. **CSP was already shipped**
(§11's policy is in `internal/http/headers.go` with four named tests), so the item
was the one hole in it: a failed `crypto/rand` produced a response with *no* policy,
which is the opposite of what that file argues for three functions below the code
doing it. **Structured logs were already structured** — everything goes through
`log/slog` with named fields — so the item was a format switch, and the constraint
on adding a format is that `internal/auth`'s redaction has to survive it.

---

## The M12/M13 renumbering

The milestone table used to have one row, M12, carrying both "DX and release" and
the `dnd5e` plugin as one item in a list of nine. §12's plugin table had already
assigned `dnd5e` to M12, so the same number was doing two jobs and the running
state had to pick one.

It picked the plugin, because §12's table and `AGENTS.md` agreed with each other
and the milestone table did not, and because a milestone that is nine items wide
is not a milestone a commit sequence can be written for. The `dnd5e` work is done;
the other nine items are M13.
