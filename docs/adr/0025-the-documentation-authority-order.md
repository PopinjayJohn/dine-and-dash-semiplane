# 0025 — The documentation authority order

- **Status**: Accepted
- **Date**: 2026-09-27
- **Relates**: [0001](0001-files-as-source-of-truth.md),
  [0007](0007-access-control-model.md),
  [0013](0013-frontmatter-parse-tree.md)
- **Supersedes**: the authority rule stated in `AGENTS.md` and `docs/spec.md`
  before this date

## Context

Two documents had grown past the job they were doing, and both had grown the same
way: by accumulating history.

`AGENTS.md` was 549 lines, and 394 of them — 72% — were a section called *Current
state*: a milestone-by-milestone retrospective of work that had already shipped,
carrying eight branch names that no longer existed. It is the file every coding
agent reads on every task, so that 72% was a cost paid continuously to describe
milestones that closed. `docs/spec.md` was 1298 lines, and §16 was 516 of them:
a table, thirteen commit sequences, and about 150 lines explaining why each one
went the way it did.

The two files also overlapped each other, and both overlapped `CHANGELOG.md` and
the twenty-four ADRs — the same reasoning about the read predicate appeared in the
spec, in AGENTS.md, in ADR 0007 and in `internal/store/acl.go`.

So this ADR is not choosing between file layouts. It is answering a question the
existing rule did not cover: **`docs/spec.md` says "where the two disagree, the ADR
wins and this document should be corrected".** That rule resolves a disagreement
between an ADR and the spec. It says nothing about a disagreement between the spec
and a *new* document, and nothing about a code comment, which is a fourth thing and
is a sentence someone wrote once.

There was a real content at risk in cutting this down. Roughly 55 of the claims in
*Current state* were not history at all; they were durable correctness
constraints, written as "*X* is real, and here is the bug it caused" — a share-link
token cannot be printed, a page is settled by re-derivation and not by a content
hash, a plugin's policy may only narrow. Deleting the section would have deleted
them with it. Moving the whole section verbatim to an archive would have left
`AGENTS.md` exactly as long as before.

## Decision

**1. The authority order is: ADR → `docs/spec.md` §1–§15 → `docs/pitfalls.md` →
`docs/milestones.md` → code comments.**

Each document is authoritative for its own subject and subordinate to the ones
above it for everything else.

- An **ADR** records a *decision* and why it was made. When one disagrees with
  anything, the ADR wins and the other document is corrected — the rule that
  already existed, now written down as a position in a sequence.
- **`docs/spec.md` §1–§15** is the *design*: what the application is, what the
  rights matrix says, what the schema holds, what the tests are named. §16 is the
  live milestone list, and the next piece of work is written down there.
- **`docs/pitfalls.md`** is the *reasoning* behind each guard rail: the claim, the
  bug it caught, the test that holds it. It is loaded on demand, by an agent whose
  change touches the guard rail in question.
- **`docs/milestones.md`** is a *historical record*. It is not maintained, it is
  not authoritative for anything, and `git log` is what a commit sequence is
  actually checked against.
- **Code comments** cite, and are cited by name. A comment that contradicts the
  spec is wrong, and the correction belongs in the comment.

**2. `AGENTS.md` is a read-first summary, and its one-line guard rails are a
pointer rather than a second source.**

A guard rail in `AGENTS.md` states the constraint in one line and links to its
entry in `docs/pitfalls.md`. Where the two differ, `docs/pitfalls.md` is right and
`AGENTS.md` is the thing to fix — because the line in `AGENTS.md` is a summary
written for speed, and the argument is in the file it points at.

This is the one place in the sequence where a lower document is authoritative
over a higher one, and it is deliberately narrow: it covers the *reasoning* for a
guard rail, not the rule the guard rail states. Invariant 1 is invariant 1 in both
files or it is a bug in one of them.

**3. The six invariants stay in `AGENTS.md`, numbered 1 through 6, in place.**

Five Go comments cite "invariant 3" by number. The numbering is part of the
codebase's vocabulary, so the section does not move and does not renumber. A
seventh invariant gets a number and a named test here, like the other six.

**4. A citation that cannot be resolved is a test failure, not a defect somebody
finds later.**

`docs/doclinks_test.go` walks every markdown file in the repository and asserts
that each relative link resolves, that each `docs/spec.md §N` citation names a
section that exists, and that each `invariant N` citation names an invariant that
exists. A document move that breaks a pointer fails `make check` rather than
producing a comment that quietly points at nothing.

That is this project's usual argument applied to its own prose: a property
nobody checks is a property that rots, and a pointer into an archive is exactly
the kind of thing that rots quietly. The test is the reason the moves are safe,
and it is why a future move does not have to be afraid.

## Consequences

- `AGENTS.md` is 335 lines, down from 549, and every line in it is either
  orientation, an invariant, a guard rail, a command, or a link. The always-loaded
  cost of a task is paid on live guidance only.
- `docs/pitfalls.md` and `docs/milestones.md` exist and are not optional reading.
  An agent that changes the read predicate reads `docs/pitfalls.md` first; that is
  the entire argument for the split, because the argument is what would otherwise
  have been deleted.
- `docs/spec.md` no longer carries a commit sequence. A milestone's commits are in
  `git log`, which is the thing that cannot drift, and `docs/milestones.md` says
  so at the top.
- **A new document needs its place in the order stated here, explicitly.** The
  failure this ADR exists to prevent is a fifth document appearing with an
  authority level nobody assigned it.
- Six claims in the documents were false at the time of writing and are now
  corrected, most notably `docs/spec.md` §14's reference to a
  `internal/testutil` package that was never built. Surgical correction means
  correcting what is false, not re-tenseing normative prose that is already
  correct — rewriting a security specification for style risks changing a
  requirement, which is a worse outcome than an uneven voice.
- The retrospective that was in *Current state* is gone from a read-first file and
  is not replaced anywhere in full. That is the deliberate part: `CHANGELOG.md` and
  the ADRs already hold the decisions, and `docs/milestones.md` holds the
  sequences. What was not already held — the ~55 constraints — is preserved, and
  that is the line the split was drawn along.
