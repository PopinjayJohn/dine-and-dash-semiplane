# 0017. The write gate lives in the store, and it checks ownership rather than position

- **Status:** Accepted
- **Date:** 2026-09-27
- **Relates to:** [0007](0007-access-control-model.md),
  [0015](0015-search-records-the-audience.md), [0001](0001-files-as-source-of-truth.md)

## Context

Invariant 3 in AGENTS.md says no `Store` method may return a page without the
access predicate. M6 finished the read half: every page-returning method takes a
principal. M7's own milestone row says "edit enforcement", which is the other
half, and the two halves are not symmetric in their risk.

A read that is too permissive shows a page a player was not entitled to. A
*write* that is too permissive shows the same page — and, worse, a player can
write a page they have never been able to read, and the next sync indexes it, and
it appears in the next player's page tree. So a read-only predicate with an open
write path is a building with a locked front door and an unlocked back one.

§8's rights matrix has the write columns, and `access.For` answers them. The
question is who asks.

## Decision

**`UpsertPage` takes a principal and asks in the store. It checks *ownership*; the
*position* half of §8's rule stays `internal/index`'s, and the file says so.**

### The gate is in the store, not in each handler

The other shape is the one every web application has: the handler reads the page,
asks `access.For`, and calls the same `UpsertPage`. It is a shape where the check
that must not be forgotten is a line of code in a handler — so it is a line of
code somebody can forget, in a file whose other job is turning a request into HTML,
and there is nothing about that line that a test would fail without.

In the store, it is a parameter that does not compile away, and the cost is one
indexed lookup on a path that already has a transaction open. The read side made
the same argument for the same reason, and the two being in the same place is what
lets `TestTheWriteGateAgreesWithTheResolver` compare them.

### Ownership, not position

§8's rule is "players may create pages only within their own character subtree or
carrying their own `character:` frontmatter". That is two clauses, and the store
implements one of them:

- **Ownership** — "does *this* principal own the page?" — the store answers it,
  because it is a binding against `owner_character_page_id` and it has to be
  answered the same way the predicate answers it. Two implementations of the same
  question in one package is one too many, which is why `owns` is a single
  function and `TestTheWriteGateAgreesWithTheResolver` compares it with
  `access.For`.
- **Position** — "is the path inside the character?" — stays `internal/index`'s,
  because that is `OwnerOf`, the same function the sync uses to *decide* the owner
  in the first place. Duplicating it here would be a third implementation of §8's
  rule in one repository.

The residual is named in `internal/store/writes.go` rather than left to be
discovered: a caller that supplied its own character as the owner of a page
somewhere else would be caught by the position check and not by this one. No
caller can do that today — the only writer is the sync, which reads as the DM, and
the player-facing one arrives with the editor, which is where the position check
belongs.

### A refusal is `ErrNotAllowed`, and not `ErrNotFound`

Asymmetric with the read side on purpose. A read must not confirm that a page
exists, because "there is a `dm-only` page at this path" is itself a disclosure. A
write is a request about a page the caller already holds — they have its path
because they have it — and a player told "there is nothing here" goes to the DM
with a different question than the one they asked.

`SetPageVisibility` reads the page as the DM for the same reason: the gate is the
*write* check, and reading as the caller would turn "you may not reveal this" into
"there is nothing here" for every principal who cannot read the page. The one
thing that could leak is the existence of a page to somebody who does not know its
path, and the only caller with a path to offer is the DM's own session.

### Reveal only ever loosens

§8: "Reveal" is the owner moving a page one level less strict — a level change,
not a separate mechanism. So `SetPageVisibility` asks two questions for a player:
do you own it, and is this move strictly less strict? The second is checked
rather than trusted, because a method that could *tighten* a page's audience is a
method a player could use to make their own notes vanish from the DM's page tree,
which is a denial of service against the campaign rather than a privacy setting.

A DM may set any level. A bad level is a plain error and not `ErrNotAllowed`,
because a caller that wrote one has a bug rather than a denied request, and the
two deserve different handling.

## Consequences

**Good**

- The write gate cannot be left out, because the signature does not allow it.
- The refusal tells the caller the truth about what happened.
- The store has two implementations of "does this principal own this page" and not
  three, and a test compares the two.
- A player cannot un-see their own pages by closing them.

**Bad**

- **The gate is a gate, not a wall, and that is the price of not duplicating
  `OwnerOf`.** The residual is documented in the file and fixed by M9's editor, not
  by M7's store. A reviewer looking for "can a player write outside their own
  subtree?" has to read two files and both comments to get the answer.
- **`SetPageVisibility` reads as the DM**, which is the one place in the package
  that does not filter by the caller's principal. The comment says why, and the
  alternative — filtering — gives a worse error for a question the caller already
  has the path to.
- The write side is now coupled to `principal_characters`, so a campaign whose
  bindings are wrong refuses writes. Bindings are the DM's to get right, and
  before M7 a wrong binding cost a player a page they could read; now it can also
  cost them a page they could write. Both are the same bug and the second is
  louder.

**Neutral**

- The sync is exempt because the DM's row of the matrix is yes six times. That is
  an exemption and not an exception, and it is in `checkMayWrite` where a reader
  can see it.

## Alternatives rejected

**Leave the gate in the handlers.** Rejected above. It is the shape the rest of the
industry uses, and the industry is not running a wiki on somebody's laptop with one
DM and a `sqlite3` file.

**Filter by path in the store, so the gate is complete on its own.** Rejected
because it is a second `characterFromPath` in a different package from the first,
and §8's rule is exactly the kind of rule that two implementations drift on. The
symptom would be a player who can write a page inside their subtree by one route
and not by the other, and it would be reported as "the editor is broken".

**Make a *read* refusal also `ErrNotAllowed`.** Rejected: it tells a player that a
page exists, which for a `dm-only` page is the disclosure the predicate exists to
prevent. The asymmetry is the design.

**Return `ErrNotFound` from a write refusal, for consistency with the read side.**
Rejected above. Consistency here would be a leak dressed as tidiness.
