# 0005. Obsidian compatibility is a defined subset, not full emulation

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The DM is an Obsidian user. The strongest form of Obsidian compatibility would
reproduce Obsidian's markdown dialect and semantics exactly. That is neither
achievable nor desirable: Obsidian's behaviour is defined by an undocumented
runtime with its own plugin ecosystem, and chasing parity creates a maintenance
burden with no user-visible payoff.

What matters is a specific, testable promise: **a DM can point Obsidian at
`vault/<campaign>/`, nothing they do there breaks, and everything they write
there shows up in the wiki.**

## Decision

Commit to the following subset, and treat anything outside it as out of scope
until a new ADR says otherwise.

**Frontmatter.** YAML between `---` fences. Unknown keys are **preserved
verbatim** and never dropped on rewrite — the app is a guest in the DM's files.
Parse → serialise → parse is idempotent, and that is a property test.

**Wiki links.** `[[target]]`, `[[target|alias]]`, `[[target#heading]]` and the
embed form `![[target]]`. Implemented as our own goldmark extension rather than
downgraded to standard markdown links, because link *resolution* is the
interesting part: resolution consults the index, and unresolved links render
with a visible `unresolved` class instead of silently degrading to plain text.

**Callouts.** `> [!type] Title` with `-`/`+` fold markers, including our custom
`[!SECRET]` type (see [0007](0007-access-control-model.md)) and the
`{.revealed}` attribute form. Callouts nest.

**Attachments.** Files live in `_attachments/` inside the vault and are
referenced by relative path, so a vault stays portable when zipped or committed
to git.

**File identity.** A page's identity is its campaign-relative path without the
extension. Renaming a page's *title* never renames the file, so `[[rivergate]]`
links keep working. Changing the *path* is an explicit action that rewrites
inbound links atomically, and is DM-only.

**Revisions.** Written to `_history/<path>/<n>-<rfc3339>.md`, close enough to
the Obsidian File Recovery plugin's layout to be recognised.

**Encoding.** UTF-8, LF line endings, no BOM, exactly one trailing newline. The
serialiser is deterministic, so rewriting a file the app did not change
produces a zero-byte diff.

Deliberately **not** implemented: Obsidian plugins, canvas files, block-level
transclusion (`![[page#^block]]`), Dataview queries, and callout types beyond a
documented set.

`wiki import obsidian <dir>` ingests an existing vault and `wiki export --zip`
produces a portable one. Both land in M12.

## Consequences

**Good**

- The compatibility promise is written down and testable, so "does it work in
  Obsidian" becomes a golden-file question rather than an opinion.
- Round-tripping a file the app did not author must be a no-op, which prevents
  the app from mangling the DM's own notes.
- The markdown stays legible and portable to any other tool.

**Bad**

- A DM using a transclusion-heavy workflow will hit unimplemented features.
  Each is a candidate for a goldmark extension in a later milestone, and
  several make good plugin exercises.
- Fidelity is bounded by our list, not by Obsidian. The list is the contract.
