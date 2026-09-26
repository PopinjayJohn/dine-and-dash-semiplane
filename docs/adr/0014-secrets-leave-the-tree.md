# 0014. Secrets leave the tree, and the cache is keyed by the decision

- **Status:** Accepted
- **Date:** 2026-09-26
- **Relates to:** [0007](0007-access-control-model.md),
  [0005](0005-obsidian-compat-subset.md),
  [0004](0004-pure-go-sqlite.md),
  [0013](0013-frontmatter-parse-tree.md)

## Context

ADR 0007 settles *when* a secret is removed — server-side, at render time, in
the response bytes, never with CSS. It does not settle *where* in the render
path, and it does not settle what happens to a render once it has been made.

Both of those are decisions with a wrong answer that looks like a working
feature:

- Strip on the way out, with a regexp over the rendered HTML or the source, and
  the stripper is only as good as its worst case. A callout nested in a list
  item, quoted inside a quote, written with a fold marker, or written with an
  unclosed bracket are four ways to be wrong, and one of them is a leak.
- Cache a render by its content hash, and the cache is a channel: a page
  rendered for a DM, served to a player, because they asked for the same bytes.
  This is the mistake the cache's key is shaped to make hard rather than the one
  a reviewer's eye is likely to catch.

## Decision

**A secret is removed from the parse tree, and the callout is replaced by a
placeholder node.**

The stripper is a walk over the tree, and a `[!SECRET]` callout's children are
replaced before anything writes HTML. The secret's text is never rendered and
then removed, because "rendered and then removed" is a statement about a
sequence of events, and a sequence of events is a thing that can be
interrupted, cached halfway, logged, or reordered by a future refactor. A tree
that does not contain the secret cannot be rendered into a response that
contains it.

The placeholder is a node rather than a deletion so the shape of the page
survives: a player can see that a secret is there and ask about it after the
session. It holds no reference to the body it replaced, and the node's children
are unlinked, so there is no path from the tree to the text.

**The walk is the stripper's own.** goldmark's `ast.Walk` advances with
`child.NextSibling()` after the visitor returns, and a child that has just been
spliced out of its parent has no next sibling left to offer. A stripper built on
it removes the first secret on a page, reports that it removed six, and serves
five. The stripper collects its children before visiting them, because a
mutating walk is a different thing from a reading one.

**Three things fail closed, and one does not.**

| Input | Result |
| --- | --- |
| `[!SECRET]` | body removed, placeholder in its place |
| `[!SECRET]{.revealed}` | shown: the DM chose to show it |
| `[!SECRET` with the bracket unclosed | body removed, for anybody who may not read secrets |
| a malformed secret, for a reader who *may* | shown: the DM wrote it, and the DM is the one who can fix it |

A mistyped secret is a missing secret. A secret that appears is a disclosure
nobody can undo, and ADR 0007 asks for that trade explicitly. The last row is
the other direction: a rule that hid the DM's own words from the DM would be a
bug, not a safety.

**The render cache is keyed by (content hash, renderer version, decision,
path).**

Each field earns its place by naming what it costs to drop:

- without the **content hash**, a saved page serves the previous version;
- without the **renderer version**, a renderer upgrade keeps serving old HTML
  for every cached page, which is what the version constant exists to prevent;
- without the **decision**, a render made for a DM is served to a player;
- without the **path**, two pages with identical bodies share an entry, and the
  next thing to need the path — relative link resolution — would give one of
  them the other's links.

**The sanitiser runs last, on every page, for every author.** Not for
untrusted authors and not for players. A DM is the highest-value target in this
application, and "sanitise what a player wrote" leaves the DM's own notes on the
weakest path: a DM pastes a snippet from a forum, and a forum is a place scripts
come from.

## Consequences

**Good**

- There is no code path from a secret to a response except a decision that says
  it may be read. That is a claim about the *absence* of paths, which is the
  kind of claim a design can support and an implementation review cannot.
- The stripper visits every callout there is, because they are all nodes,
  including the ones nested in blocks a text pass would miss.
- The canary tests are cheap and exact: a fixture with one string in it, and the
  string is either in the output or it is not. A fuzz target puts the same
  string in a well-formed secret callout and appends whatever it likes.
- The cache cannot serve a DM's render to a player, and the test for that is the
  first one in the file because it is the one that matters.

**Bad**

- A page that is read with the wrong decision is served from a cache entry that
  was made for the right one, and nothing in the renderer notices. M7's
  `access.Decision` is the only thing standing between the two, which is one
  more place for a bug to live and one more thing to test.
- The malformed-secret rule hides a mistyped secret from a player, silently. The
  DM sees it, so the fix is theirs to make, but they have to notice.
- The placeholder means the page's structure is not the file's structure: a
  stripped secret leaves an element where a paragraph was, so anything that
  counts blocks has to know about placeholders.
- `Get` does not update the cache's recency, so eviction is
  least-recently-*stored*. That is a weaker guarantee than LRU and is stated as
  such in the code and pinned by a test rather than called LRU anyway.

`docs/spec.md` §11 is corrected by this ADR in two places: the render cache key
gains the decision and the path, and the stripping is on the tree rather than on
the way out. Everything else in §11 stands.

## Alternatives rejected

**Strip the source before parsing.** A regex or a line scan for `[!SECRET]`,
counting brackets to find where the block ends. It is the simplest thing that
could possibly work and it is wrong in four shapes, three of which a DM writes
without noticing.

**Strip the rendered HTML.** Blelemonday cannot do it: the secret is text inside
an allowed element, so removing it means a policy that knows what a secret is,
which is the same problem one step later with less information.

**Hide it with CSS.** Explicitly forbidden by ADR 0007, and it is the shape of
the bug the whole design avoids: the bytes are in the response.

**A second render path for players.** The fastest possible thing to write, and
exactly what invariant 5 in AGENTS.md forbids: a second set of golden files is a
second set of tests nobody reads, and a second path is a second thing to keep
correct.
