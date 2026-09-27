# 0015. Search records a page's audience, and the public index starts empty

- **Status:** Accepted
- **Date:** 2026-09-27
- **Relates to:** [0009](0009-two-index-search-with-rrf.md),
  [0007](0007-access-control-model.md), [0001](0001-files-as-source-of-truth.md)

## Context

[ADR 0009](0009-two-index-search-with-rrf.md) decides that search reads two FTS5
indexes: a public one fed from `pages.body_public`, and a private one holding
`[!SECRET]` text. It does not say where `body_public` comes from, and the answer
turns out to be the whole difficulty of shipping search before access control.

There are three things to work out, and each has a wrong answer that looks like a
working feature.

**Where the public body comes from.** It is the body with every secret block
replaced by a marker. Producing it means knowing which blocks a *particular
principal* may be shown, which is `access.Decision` — and `internal/access` is the
next milestone. The tempting shortcut is to fill it in from the fail-closed
stripper, which removes every secret: that produces a body with no secret in it,
and then feeds it to the public index, and the index is readable by anybody. The
shortcut is a disclosure. So the only safe value before access control exists is
the empty string, and search is findable by title, alias, tag and type and not by
prose until M7 lands.

**Where the audience comes from.** A read predicate has to filter on something, and
M4's sync already read every `visibility` key, refused a value it did not
recognise, and then threw the readable ones away. A page written
`visibility: dm-only` was indexed as a page anyone could read, because there was
nowhere to put the value. Validating a security field and discarding it is the
shape of a bug nobody finds, and the predicate cannot be written without the
column.

**Whether the public index needs the predicate at all.** ADR 0009 says the public
search path has "no ACL in it at all", and read literally that means a
`dm-only` page's *title* is in a list a player can read. The ADR is about secret
text and it is right about secret text; invariant 3 in AGENTS.md is about pages
and it names search. Both hold at once because the index splits *text* and the
predicate filters *pages*, and a `dm-only` page's public body is safe to index
without being safe to list.

## Decision

**The audience column arrives with search, the public body stays empty until
access control can compute it, and both indexes are read through the predicate.**

### The audience is recorded, not enforced

`pages.visibility` and `domain.Page.Visibility` land in this milestone. The sync
writes the value it read, a blank one is `players`, and a value the application
does not recognise is refused rather than defaulted.

Enforcing it is the next milestone. The predicate in `internal/store/acl.go`
exists now so that there is one place to edit and so that the search queries are
written correctly from the first commit; what it does not yet do is stop
`GetPage` from returning a `dm-only` page to anybody. That is recorded in the
store's package comment and is the next milestone's work.

### The ownership branch is present and false

`aclOwnership` is `1 = 0` until the table that answers it exists. The branch is
written down rather than left out, because leaving it out silently widens the
audience of every `dm-and-owner` page, and admitting every player to one is a
disclosure the moment a DM writes one. A test asserts the branch is still there,
which is what stops it being tidied away by somebody who has not noticed the table
is missing.

### The public body is empty, and saying so is part of the decision

`body_public` exists from this milestone and is written as the empty string. The
search tests that depend on that are named: a word in a page's public body is not
findable yet, and the test that asserts it says which assertion moves on the day
the redaction lands.

## Consequences

**Good**

- Search ships with a predicate that is already the finished shape, so M7 is an
  edit to one line rather than a rewrite of two queries.
- The audience is no longer validated and discarded, which was a live bug the
  moment a `dm-only` page existed.
- The fail-closed direction is explicit and tested, rather than being a property
  of a column that does not exist yet.
- A DM can find their own secrets by content today, which is the feature ADR 0009
  exists for and the one part of it that does not need access control.

**Bad**

- **A search that cannot find prose is a disappointing milestone.** It finds
  titles, aliases, tags and types, which is most of what a DM types, and it does
  not find a word in the middle of a paragraph. The alternative is a public index
  holding bodies, which is a disclosure.
- The schema is now ahead of the code: a column that is written, checked and
  compared, and one enforcement path out of seven. A reader who stops at
  `pages.visibility` will assume more than is true, which is why the store's
  package comment says exactly which queries filter and which do not.
- The migration names in the spec are wrong. `0002_access.sql` was to hold
  `visibility`, `owner_character_page_id` and `body_public` together; they are now
  split across `0003_search` and `0004_visibility`, with the owner and the
  bindings still to come.

**Neutral**

- `1 = 0` will look like a placeholder. It is a value, and it is the safe one.

## Alternatives rejected

**Fill `body_public` from the fail-closed stripper, so prose is searchable now.**
Rejected: every secret is stripped, so the index holds no secret, and it is
readable by everybody. The text would be safe and the reasoning would be wrong,
which is the worst combination available — a leak with a comment above it
explaining why it cannot leak.

**Fill `body_public` with the body minus *unrevealed* secrets only.** Also
rejected, and it is the subtle one. A revealed secret is public, so this is
closer to right — but "revealed" is per-*principal*: ADR 0007 makes a revealed
secret visible to its owner, and the character page that owns it is a `players`
page. Without ownership there is no way to ask the question, and the only answer
available is a global one, which is the wrong one for a shared campaign.

**Leave the audience column to M7 and filter search by nothing.** Rejected: the
predicate would be unwritable, and shipping two queries with no audience test and a
comment saying one is coming is how a `dm-only` page's title ends up in a player's
search results during the gap.

**Enforce the audience in the store now.** Tempting, and it is the next
milestone's work. Doing it here would mean giving `GetPage`, `ListPages`,
`Backlinks` and the target lookups a principal, which is a change to seven call
sites that all currently have tests written against the campaign-scoped shape.
Those tests are the specification of what M7 has to change, and rewriting them
before the `access` package exists would mean writing them twice.
