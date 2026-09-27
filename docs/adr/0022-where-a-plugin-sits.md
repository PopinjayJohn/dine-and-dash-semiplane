# 0022 — Where a plugin sits, and what it may take away

- **Status**: Accepted
- **Date**: 2026-09-27
- **Decided by**: M11, building on [ADR 0002](0002-plugin-registry-in-process.md)

## Context

[ADR 0002](0002-plugin-registry-in-process.md) chose an in-process plugin registry
and sketched it as a `Plugin` with a `Setup(*Registry) error`, handing the plugin a
`Capabilities` value with nine fields. M11 built it. The sketch's *shape* was right
and four of its *answers* were not, and the three reasons are the same three places
the code turned out to need a decision the sketch had left open.

The four:

1. `BeforeRender` was sketched as `BeforeRender(ctx, p, md goldmark.Markdown)
   (goldmark.Markdown, error)`, and the goldmark pipeline parses the source into a
   tree *before* any hook would run. Returning a different `goldmark.Markdown` from
   that hook has no effect on the tree that is about to be rendered.
2. `AfterRender` was sketched as `AfterRender(ctx, p, out *bytes.Buffer)`, with no
   statement about where in the pipeline it sits. There are two places it could sit
   and the difference is a project's worth.
3. `RenderHook` was sketched as one interface with both methods, and the two halves
   have nothing in common but the page they are given.
4. The registry was sketched as a type with a `Capabilities` value, and the
   capabilities that turned out to matter most are the ones that *cannot* be fields
   of a struct without a caller being able to forget them.

## Decision

**1. A tree hook receives the AST after the secret stripper has run.**

`render.BeforeRenderer` is `BeforeRender(ctx, page, decision, doc ast.Node)
(ast.Node, error)`. It runs at the point in `Renderer.Render` where the links are
resolved and the `[!SECRET]` subtrees have already been unlinked.

The alternative — before the stripper — is a capability rather than a bug. A hook
running first could lift the contents of a secret callout into the open body, and
the stripper would then have nothing left to remove. Running after means the tree
the hook is handed contains no secret text and **no transform of it can put any
back**. The cost is that a hook cannot see a secret, and the DM, who can, is who
writes house rules.

**2. An HTML hook runs before the sanitiser, never after it.**

`render.AfterRenderer` is `AfterRender(ctx, page, decision, out *bytes.Buffer) error`,
called on the rendered buffer immediately before `render.Sanitiser.Sanitise`.

Invariant 4 says rendered markdown is sanitised for every author, DM included. An
`AfterRender` *after* `Sanitise` is a way for a plugin to put unsanitised HTML on a
page a player reads, and "a plugin is not an author" is not an exception this
codebase can make. Before the sanitiser, a plugin's output is filtered by exactly
the allow-list the DM's own markdown is filtered by.

That is also why §12's callout type mattered: `callout-[a-z0-9-]+` is the one class
shape the sanitiser admits by design, so a plugin ships markup by emitting a callout
of its own type, and a plugin that needs an element or a class the core does not
already allow discovers it cannot have one. `house-rules` needed no sanitiser change
for exactly that reason.

**3. Two interfaces, not one.**

`BeforeRenderer` and `AfterRenderer` are separate, and a `render.RenderHook` record
carries either, both or neither. A hook that only post-processes the HTML — which
is what both of M11's hook-using bundled plugins do — would otherwise have to write a
`BeforeRender` that returns the tree it was given: a no-op with a signature somebody
has to get right, in a plugin written by somebody who does not read the source.

**4. The registry exposes one accessor per capability, and the composition is in the consumer.**

There is no `Capabilities` struct. `Registry` has `RenderHooks()`, `SearchFields()`,
`Policies()`, `Routes()`, `Commands()`, `Events()`, `PageTypes()` and `FieldTypes()`.

A struct is a list of fields, and "every field added here is a permanent
compatibility promise" is a promise a struct makes badly: a caller can read the
value and forget a field, and the forgotten field is the one nobody notices is
missing. Eight accessors cannot be half-used. What the sketch called `Capabilities`
is the *set of accessors*, and adding one is a new method rather than a field.

`Capabilities.Markdown` and `Capabilities.Render` are merged: they are both
goldmark-level and they are wired in at the same moment, and a plugin contributing
an extension and then forgetting the hook is a plugin that half works.

**5. A plugin's access policy may only narrow, and a failure denies.**

`access.Policies.Apply` ANDs the plugin's answer with the core's, field by field.
`narrow` is written out one field per line rather than looped over a list of names,
because a loop needs somewhere to hold the answers and a map with a `true` default
would leave a sixth `Decision` field *allowed*.

The failure direction is the opposite of a render hook's, and deliberately so:

| | panicking hook | panicking policy |
|---|---|---|
| job | contribute to a render | take a right away |
| answer without it | the render is still correct | the right comes back |
| so | log and skip | **deny** |

A policy that errored and returned "unchanged" would honour itself by accident, on
the pages where it was supposed to be hiding something.

**6. A plugin's policy is applied after `access.For` and in Go, never in SQL.**

The read predicate is SQL (`internal/store/acl.go`) and is invariant 3. A policy is a
Go function and a `WHERE` clause cannot call one, so a policy narrows the *rows* a
listing is built from and the decision a single page is served under, on top of the
predicate. That is the safe side of the line: the SQL is the invariant and a policy
is a second, stricter layer, never a looser one.

The consequence, stated because a reader will want it: a store method called by
something other than this codebase — a plugin with a store handle, a future command
— sees the core predicate and nothing else.

Narrowing a listing costs an ownership query, memoised *by owner* rather than by
page: a campaign has one owner per character, so three hundred pages under five
characters is five queries, and a build with no plugins asks for none. This is also
why `search.Hit` grew `Visibility` and `OwnerCharacterPageID` — a policy that cannot
see a hit's audience can only say "hides everything", and the dropdown is a list of
page *titles* typed one character at a time.

**7. A plugin's search field reaches the public index and nothing else.**

`migrations/0007_plugin_fields` adds one `extra` column to `pages_fts`. One column
rather than one per plugin because FTS5's column set is fixed at table creation, so a
per-plugin column would be a per-plugin migration and "a plugin may contribute an
indexed field" would be true only for plugins whose migrations happen to have been
written.

The boundary is a security property. A plugin that could index into the private index
could put a value in front of a principal the read predicate never admitted, and no
redaction afterwards would help because the value was never in a body to redact. A
plugin's value is treated as markdown and run through `render.PublicText`, which
catches the accident — indexing a slice of the body, callouts and all — and not the
intent.

**8. The render cache key does not grow a plugin field.**

A `Renderer`'s hook set is fixed at construction and every `Renderer` owns its own
`Cache`, so two renderers with different plugins never consult the same map. A key
field naming them would separate entries that were never in the same bucket.

What *would* be unsafe is a persisted render, and nothing persists one: the files are
the source of truth and the cache dies with the process. A future capability that
varies a render by anything outside the seven key fields has to answer this
paragraph rather than assume it.

`CacheKey.Type` was added later, for the same class of reason and by this ADR's
logic: a `type:` change with an unchanged body *is* a change to the output, because a
plugin's hook may render a page differently according to it. A hook that can only see
a path is guessing with a regexp.

## Consequences

- `render.BeforeRenderer` and `render.AfterRenderer` are stable API. A new hook point
  is a new interface, and putting it in the pipeline is a change to `Renderer.Render`
  with a named test saying where it sits relative to the stripper and the sanitiser.
- A plugin cannot be granted a sanitiser extension, a new element, a new class or a
  new `Decision` field it may set. Each of those would be a widening of the boundary
  above, and each would need its own ADR.
- A plugin that panics costs a log line and a page that renders without its
  contribution. A plugin that *cannot decide* costs the page. Both are covered by
  `internal/safe`, at the call site, because a hook does not panic while it is being
  registered — it panics while a DM is reading a page.
- `internal/access.PageMeta` carries `Type` and `Path`, which the rights matrix does
  not read. `TestForDecisionMatrix` is unchanged by their existence, and the fields
  are there because a policy that cannot see what kind of page it is looking at can
  only write a rule about visibility.
- There is no cache-invalidation capability, and there was a plan for one:
  `house-rules` was going to demonstrate an event subscriber that clears the render
  cache. The cache key already contains the content hash, so a saved page is a new
  key, and `Cache.Clear` has no caller for the same reason. A capability that exists
  only to be demonstrated is not a capability, and the demo was the wrong thing to
  have been asked for.
