# 0013. Frontmatter is a parse tree, and a document is the bytes it was read as

- **Status:** Accepted
- **Date:** 2026-09-26
- **Relates to:** [0005](0005-obsidian-compat-subset.md),
  [0001](0001-files-as-source-of-truth.md),
  [0004](0004-pure-go-sqlite.md)

## Context

ADR 0005 commits to two things that pull against each other in every
implementation:

- **Unknown frontmatter keys are preserved verbatim and never dropped on
  rewrite**, and parse → serialise → parse is idempotent.
- **Rewriting a file the application did not change produces a zero-byte
  diff**, and the serialiser is deterministic.

Put those together and the requirement is sharper than it first reads. A
serialiser that *always* re-renders the frontmatter cannot produce a zero-byte
diff for a file a DM hand-wrote, because no emitter agrees byte for byte with
every hand-written YAML: blank lines between keys, a double space before a
comment, an unusual indent, a long line folded at a different width. Each of
those is a whole-file diff in the DM's git, and the first time a DM sees that
they stop trusting the tool with their campaign.

So the zero-byte promise cannot be *tested for*. It has to be structural.

## Decision

**A Document keeps the bytes it was parsed from and hands them back unchanged
until something actually changes.**

`Bytes()` returns the original bytes for any document nothing has been
mutated, which makes a reindex, a render and a save of an untouched page all
produce a zero-byte diff by construction rather than by luck. Re-serialisation
happens only when `Set`, `Remove` or `SetBody` has been called, and
`Modified()` says which case a caller is in before it writes anything.

**The frontmatter is kept as a `yaml.Node` tree, not a `map[string]any`.**

This is the part that would be expensive to reverse. A map drops the order the
keys were written in, the comments beside them, the difference between `'one'`
and `"one"`, and the exact spelling of a timestamp — so a re-serialisation
from it rewrites a DM's notes on every save. The tree keeps all of it, so the
only thing a change to a key costs is the whitespace *between* keys. The keys
the application owns are a closed set of eight, and `Set` refuses anything
outside it, because the application is a guest in the DM's files (ADR 0005) and
a bug that writes `titel: Rivergate` into somebody's campaign is not a bug that
should be able to reach the filesystem.

**The YAML library is `go.yaml.in/yaml/v3`.** It is the maintained home of
go-yaml v3, it is pure Go, and its node API is the thing the decision above
rests on. `gopkg.in/yaml.v3` is the same code at the old path.

**Read is forgiving; write is conventional.** UTF-8, no BOM, one trailing
newline and LF are what the application *writes*. What a DM *wrote* is read as
it is: CRLF stays CRLF, a byte-order mark stays a byte-order mark, and a file
with no trailing newline keeps not having one. A DM on Windows is not a broken
DM, and normalising their whole vault on the first sync is not this
application's business. The exception is a document the application has
modified, which it writes in the committed encoding.

**What is refused rather than interpreted.** A file whose frontmatter is not
valid YAML, or is not a set of keys, or whose `visibility` is not a level the
application knows, stops the file from being indexed and says which file and
why. `plyers` has to be a page that will not index, not a page that publishes
itself: the permissive reading of a visibility key is how a `[!SECRET]` block
reaches a player. A file with *no* frontmatter is not an error — most pages in
a new vault have none, and a DM writing prose should not have to add YAML to be
indexed.

## Consequences

**Good**

- The zero-byte diff is a property of the design, and a golden test can prove
  it for every file in `testdata` rather than for the cases somebody thought of.
- Unknown keys, key order, comments and quote styles survive an edit to a key
  the application owns, which is the only version of "preserved verbatim" that
  means anything to the person who wrote them.
- A DM's own file survives every read path this application has, including
  paths that only want a hash.
- Two fuzz targets cover it: one for "parse anything without panicking and
  return what came in", one for "a change to a key survives a re-parse, keeps
  the unknown keys and leaves the body alone".

**Bad**

- A document the application *has* modified is normalised: blank lines between
  frontmatter keys are lost and indentation becomes two spaces. This is
  deliberate and bounded — it only happens when the DM asked for a change
  through the app — but it is a diff in a file they opened in Obsidian.
- The tree is mutable and therefore has to be trusted. Nothing outside this
  package may reach into it, which is why `Document` has no accessor for the
  node and the tree is unexported.
- `Set` validates a value against its key's shape, so `tags: rivergate` is an
  error rather than a one-element tag list. That is a rule to maintain, and a
  ninth key is a line in two places.
- Reading a timestamp means parsing a string ourselves rather than letting the
  YAML library hand back a `time.Time`, because `title: 42` becoming the number
  42 on the way into the database is a bug in the wrong direction.

## Alternatives rejected

**`map[string]any`.** The obvious implementation and the one every tutorial
shows. It cannot satisfy the zero-byte diff, and it drops the DM's comments on
the first write. Rejected on the comments alone.

**Re-serialise always, and make the emitter good enough.** There is no such
emitter for hand-written YAML, and every improvement is a bug someone else's
vault finds. Rejected as a promise that cannot be kept.

**Keep the raw text and do surgical edits to it.** Preserves everything,
including the blank lines, but a frontmatter value can be a block scalar or a
nested list, and finding "the lines of the `description` key" in the general
case is a YAML parser wearing a disguise. Rejected for two code paths where one
is correct.
