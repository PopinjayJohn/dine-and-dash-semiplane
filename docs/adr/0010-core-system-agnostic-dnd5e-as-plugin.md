# 0010. The core is system-agnostic; D&D 5e ships as a bundled plugin

- **Status:** Accepted
- **Date:** 2026-09-26
- **Relates to:** [0002](0002-plugin-registry-in-process.md),
  [0007](0007-access-control-model.md),
  [0009](0009-two-index-search-with-rrf.md)

## Context

The project exists because someone plays D&D 5e, and the obvious way to build
it is to put 5e in the core: page types for spells, creatures, feats and magic
items; a statline field; a character sheet template. It would work, and it
would be faster by a couple of weeks.

It is the wrong shape for three reasons, and the third is the one that decides
it.

1. **It makes "core" mean "5e".** Every later question becomes "is this core
   or is this 5e?" with no answer that is not a preference.
2. **The cost is paid at the wrong time.** 5e-specific *columns* in the pages
   table, or 5e branches in the search indexer, are not additions. When a
   second system appears they are migrations to a table that was never meant to
   be generic.
3. **The interesting requirement is not 5e, it is house rules.** The thing
   that will actually be asked for is "let me add a field", "let me change how
   something renders", "let me invalidate a cache when this happens". A plugin
   framework that can express 5e can express house rules, and the reverse
   framing — build for house rules, demo with 5e — is the one that makes the
   API general.

[ADR 0002](0002-plugin-registry-in-process.md) already decides the
*mechanism*: an in-process, compile-time registry with a narrow
`Capabilities` struct. It says nothing about **where the line is**. Without
that, "core is system-agnostic" is an aspiration that erodes one convenient
special case at a time.

## Decision

**Core owns the concepts that are true of every tabletop campaign. Everything
else is a plugin.**

### What core owns

Page types, all of which are meaningful with no ruleset attached:

```
note, location, npc, quest, item, faction, session-log, character
```

Plus the things that are security properties rather than content:

- **Ownership** and the `characters/<slug>/` subtree.
- **`[!SECRET]`** parsing and stripping, including the fail-closed default.
- **The access predicate** and the rights matrix.
- The vault format, the index, the search indexes, the render pipeline.

`character` is in core deliberately. It is the unit of ownership, and ADR 0007
depends on it being one thing rather than a ruleset entity.

### What a plugin owns

A plugin contributes page types with rich field schemas, new field kinds, render
hooks, goldmark extensions, routes, search fields, event subscribers and CLI
commands. The `dnd5e` plugin contributes `spell`, `creature`, `feat` and
`magic-item` as page types with field schemas, a `statline` field type, and a
character sheet template.

Crucially, **a page type is data, not a Go type.** A type is a name, a field
schema and a template. That is what keeps 5e out of the `pages` table: adding
`sell` needs a frontmatter field and a template, not a migration.

### Where the line is

A plugin may **add** to the capability surface in ADR 0002. It may not:

- **Add a visibility level or weaken a secret policy.** `access.Policies`
  composes extra rules; it cannot replace the resolver, and a plugin's default
  must deny rather than permit. Fail-closed is not negotiable and is not
  something a third-party extension gets to opt out of.
- **Register a second render path or a second secret stripper.** There is one
  render path (ADR 0007). A second is a second place to get stripping wrong.
- **Register a second FTS index or write to the secret index directly.** The
  two-index split in
  [ADR 0009](0009-two-index-search-with-rrf.md) is a security property, and the
  contract is that plugins add *search fields* to the public index rather than
  a table of their own.
- **Read the database.** Plugins go through `Store` with the same interfaces
  and the same ACL predicate as everyone else. There is no unfiltered
  read available to a plugin, because a plugin with one is a leak waiting for a
  third party.

### Why dnd5e is bundled

It is not an "official" plugin in the sense of being privileged. It is bundled
for three concrete reasons:

1. It is the reason the project exists, and the DM should not have to install
   anything to play the game they came for.
2. It is the **contract test suite**. The bundled plugins are compiled against
   the capability surface in CI, so the API cannot drift from a shape that
   actually works. A plugin API with no real plugin is a guess.
3. `house-rules`, `spoilerbox` and `wordcount` are bundled alongside it, and
   they exercise the *other* capabilities — render hooks, event subscribers,
   `access.Policies` contributors, search fields, CLI. 5e alone would not.

## Consequences

**Good**

- The core stays small enough to hold in one's head, and every 5e-specific
  concept is in a file that is obviously 5e's.
- A second system is a plugin, not a fork. This is the property that makes the
  extensibility claim real rather than aspirational.
- The capability surface is exercised by shipped code on every CI run, so it
  cannot accumulate fields nothing uses.
- The DM gets D&D 5e with no installation step, and gets house rules from a
  documented example rather than from reading the source.

**Bad**

- **The core page types have to be good enough on their own.** This is the
  real cost. `npc` and `location` are the DM's most-used pages, and if they
  feel thin the DM is pushed toward installing a plugin for basic use, which
  breaks the premise. `location`, `npc` and `session-log` get explicit
  treatment in M8 for this reason.
- Every core change has to be reviewed against the capability surface, because
  a core change can break a bundled plugin. That is the price of compiling them
  in, and it is paid deliberately.
- A plugin cannot be distributed separately, so a DM who wants a fifth plugin
  needs a new release. Accepted in ADR 0002; repeated here because it is the
  most likely thing to be regretted.
- "System-agnostic" is a discipline, not a guarantee. Nothing mechanically stops
  a 5e concept from being added to core. The rule that catches it is review,
  and the review question is always "does this make sense with no ruleset?".
