# Contributing

Thanks for helping. This document covers the workflow, the conventions, and
the bar a change has to clear before it is done.

## Before you start

Read, in this order:

1. `AGENTS.md` — invariants, the guard rails, and the commands.
2. `docs/spec.md` — the authoritative specification.
3. `docs/adr/` — decisions already made. Do not re-litigate one without a new
   ADR that supersedes it.
4. `docs/pitfalls.md` — the constraint behind each guard rail, the bug it caught
   and the test that holds it. Read this when your change touches one of them.

If a change seems to require breaking an invariant in `AGENTS.md`, one of two
things is true: the change is wrong, or the invariant needs to change and that
deserves its own ADR and an explicit decision by the maintainer.

**Where a lesson goes.** `AGENTS.md` is loaded into every agent's context on
every task, so it carries the one-line guard rail and nothing more. When you find
out why a thing is the way it is — the bug, the disclosure, the test that holds it
— that goes in `docs/pitfalls.md` with a link from the guard rail. It does not
go in `AGENTS.md`, and it does not go in the spec. `docs/doclinks_test.go` fails
the build when a citation or a link stops resolving, so a move that breaks one is
caught rather than discovered.

## Workflow

One milestone, one branch, one pull request:

```
git switch -c m6-auth-principals
# ... work ...
# run `make check`
git commit -am "feat(auth): exchange share links for session cookies"
git push -u origin m6-auth-principals
```

Commits are small and **independently green**. A commit that does not build
does not get pushed, not even temporarily. Prefer a sequence of honest commits
over one large commit with an unhelpful message.

## Commits

[Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<optional scope>): <subject>
```

**Types:** `feat`, `fix`, `docs`, `test`, `refactor`, `build`, `chore`, `perf`.

**Scopes:** `vault`, `index`, `search`, `access`, `auth`, `render`, `plugin`,
`http`, `sse`, `cli`, `dnd5e`.

Useful ones from recent work:

```
feat(access): strip [!SECRET] blocks during render
test(vault): add traversal fuzz corpus
fix(search): rank exact title matches before body matches
chore: initialise Go module and tooling
```

Set the template once per clone:

```
git config commit.template .gitmessage
```

## Changelog

Every commit that touches `internal/`, `cmd/`, `plugins/`, `migrations/` or
`web/` **must** update `CHANGELOG.md` in the same commit. CI enforces this
with `scripts/check-changelog.sh`; if it is fighting you, the script is a bug,
not the rule.

Add entries under `## [Unreleased]`. When a milestone completes, rename that
section to the milestone's version heading and add a fresh empty `[Unreleased]`
above it.

## Architecture decisions

Anything that constrains future work — a storage model, a security property, a
dependency choice, a rejected alternative — gets an ADR in `docs/adr/`. So does
a rule about how the documents themselves relate; ADR 0025 is one.

```
docs/adr/0008-short-slug.md
```

with `Status`, `Date`, `Context`, `Decision`, `Consequences`. Number them
sequentially, never reuse a number, and mark a superseded ADR
`Status: Superseded by ADR-NNNN` rather than deleting it.

## Testing

Every change ships with tests. The suite is a stated project priority; see
`docs/spec.md` § Testing for the full layered plan.

- New logic gets a table test. Anything parsing untrusted input gets a fuzz
  target. Anything rendering markdown gets golden files.
- Coverage must not drop below the gate. `make cover` reports and enforces.
- Regenerating golden files is a deliberate act. Review the diff — a surprise
  golden diff is a bug until proven otherwise.
- Run `make check` before every push. It is `fmt-check`, `vet`, `lint` and
  `go test -race -shuffle=on ./...`.

**Formatting.** Use `make fmt` and `make fmt-check`, never a bare `gofmt`.
`gofmt` may change its output in any release, on purpose, and `fmt-check` is a
byte-for-byte comparison, so a `gofmt` from a different Go than the one CI runs
will either fail the build over nothing or be quietly undone by the next
person's `make fmt`. The toolchain is pinned in the `Makefile` and both targets
use it, so this is a one-word rule rather than a thing to remember.

## Pull requests

- Describe the user-visible change in the first paragraph.
- List which invariants and ADRs the change touches, or state that it touches
  none.
- Include the changelog entry in the diff, not in the description.
- Include a screenshot for anything visual.

## Reporting bugs

Include the Go version, the OS, whether you are on a release or `main`, and the
`wiki sync --check` output if the vault and index disagree. A reproduction with
a two-file vault is worth ten paragraphs.
