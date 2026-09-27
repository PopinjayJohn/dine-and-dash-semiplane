# Writing a plugin

A plugin is a Go package in `plugins/` that implements three methods and lists
itself in `cmd/wiki/plugins.go`. It is compiled in, not loaded: there is no
`Register`, no `init()`, no scanning and no marketplace.
[ADR 0002](adr/0002-plugin-registry-in-process.md) recorded why, and
[ADR 0021](adr/0022-where-a-plugin-sits.md) recorded where in the application a
plugin is allowed to sit — which is the document to read before you write one.

## The shortest plugin that compiles

```go
package mything

import (
    "context"
    "io"

    "github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
)

type Plugin struct{}

func New() *Plugin { return &Plugin{} }

func (p *Plugin) Name() string    { return "mything" }
func (p *Plugin) Version() string { return "1.0.0" }

func (p *Plugin) Setup(reg *plugin.Registry) error {
    return reg.AddCommand(plugin.Command{
        Name:    "mything",
        Summary: "Count how many things there are.",
        Run:     run,
    })
}

func run(_ context.Context, args []string, stdout, stderr io.Writer) error {
    _, err := io.WriteString(stdout, "42\n")
    return err
}
```

Add it to the list:

```go
// cmd/wiki/plugins.go
func bundled(store *store.Store, logger *slog.Logger) []plugin.Plugin {
    return []plugin.Plugin{
        mything.New(),
    }
}
```

A plugin that cannot `Setup` stops the process at startup, with its name in the
message. That is deliberate: a plugin whose capabilities are a guess is a guess
about what a player may read.

## What a plugin may do

Nine things, and every one of them is additive. There is no way for a plugin to
*replace* anything, and the registry has no method that would let it try.

| | what it does | declared by |
|---|---|---|
| `AddRenderHook` | see the tree, or the HTML | `internal/render` |
| `AddGoldmarkExt` | change how markdown parses | goldmark |
| `AddAccessPolicy` | narrow a rights-matrix decision | `internal/access` |
| `AddSearchField` | add values to the public search index | `internal/index` |
| `AddSubscriber` | be told a page was saved or viewed | `internal/events` |
| `AddRoute` | mount a path in `/c/{slug}` | `internal/http` |
| `AddCommand` | add a `wiki` subcommand | `internal/plugin` |
| `AddPageType` | claim a `type:` name | `internal/domain` |
| `AddFieldType` | claim a frontmatter key | `internal/vault` |

The interfaces are declared by the packages that *consume* them, which is why this
table's right-hand column is the answer to "where do I find the type".

## The five rules

**1. A hook runs after the secrets are stripped, and before the sanitiser.**

`BeforeRender` is handed a tree with no secret text in it, and no transform of it
can put any back. `AfterRender` is handed the HTML *before* `Sanitiser.Sanitise`,
so a plugin's output goes through the same allow-list as the DM's own markdown.

This is why your markup has to be something the sanitiser already allows. A callout
is: `callout-[a-z0-9-]+` is the one class shape it admits on purpose, so

```html
<section class="callout callout-mything"><p>Hello</p></section>
```

survives and `<span class="mything">` does not. A plugin that wants a new element
or a new class finds out at implementation time, which is a better moment than after
a DM has written the plugin.

**2. `page.Body` is the whole file, secrets included.**

`render.Page.Body` is the pipeline's input and the pipeline's input does not change
for the convenience of a caller at the end of it. A hook that copies text out of the
body is responsible for what it copied. This is a contract rather than a boundary: a
plugin is compiled into the binary and can read the vault.

Note also that a goldmark text node is a pair of *offsets*, so a hook that wants to
read a node's text has to pass the source in:

```go
text := node.(*ast.Text).Segment.Value([]byte(page.Body))
```

**3. A policy may only take rights away.**

`Policies.Apply` ANDs your answer with the core's, field by field. Returning
`access.Granted()` gets you nothing; returning it for a page the matrix already
refused does not re-grant anything. And a policy that **errors or panics denies**,
which is the opposite of a render hook — a hook that crashes is skipped, because the
render is still correct without it.

**4. A search field reaches the public index and nothing else.**

Your values are treated as markdown and run through `render.PublicText` before they
go near the index. That catches the accident — indexing a slice of the body, callouts
and all — and not the intent, because `PublicText` removes blocks, not words.

One shared `extra` column holds every plugin's fields, so a value is findable two
ways: `extra:"words 1200"` and a bare `1200`. A bare value alone would be
indistinguishable from a word in the page's body.

**5. A route is not a render.**

A hook's output is filtered by the sanitiser. A route's response body goes through
**nothing**, so you escape your own values, and you set your own cache headers. See
`plugins/wordcount` for the shape.

## What you are given, and what you are not

You are given what your constructor takes, and nothing else. There is no package
level store, no package level clock and no ambient context: `wordcount.New(store)`
is a plugin that needs a store, and `spoilerbox.New()` is a plugin that does not.

You are *not* given a way to reach core's internals. There is no "the page route,
but different" and no "publish an event" — an event is a notice that something
happened, and nothing is rendered in a subscriber, which is why there are two
things rather than one.

## Naming

`Name()` is normalised through the same `domain.NewSlug` a campaign's is, so it is
safe in a URL, in a log line and in a `class="callout-<name>"` without a second
escaping step. `../../etc/passwd` normalises to `etc-passwd`; a name that reduces to
nothing is refused at startup.

A command name is a single lower-case word, because it appears in a shell
completion, in `wiki help` and in a log line. A plugin may not take a core command's
name, and may not redefine a core page type (`note`, `location`, `npc`, `quest`,
`item`, `faction`, `session-log`, `character`) or a core frontmatter key (`title`,
`aliases`, `tags`, `type`, `visibility`, `created`, `updated`, `character`).

## Order

Your capabilities run in `(Priority, Name)` order. Implement `Priority() int` to
ask for a place:

```go
func (p *Plugin) Priority() plugin.Priority { return plugin.PriorityLast }
```

It is optional, and `Plugin` itself stays three methods so that adding an ordering
knob to it would be a change to every plugin that exists. The name is the tie-break,
so a hook whose winner would otherwise be decided by a linker cannot be tested — and
`TestCapabilitiesRunInPriorityThenNameOrder` is that test.

## When it goes wrong

A panicking hook is recovered, logged and skipped, and the page renders without its
contribution. A panicking policy **denies**, and the page is a 404. A panicking
subscriber is logged and the next one still runs.

The log line names your plugin and your hook, because "recovered, logged and
skipped" is only useful if it says *which* one.

## Testing a plugin

Every plugin runs the contract suite, and it is one line:

```go
func TestMythingPassesTheContract(t *testing.T) {
    t.Parallel()
    contract.Run(t, New())
}
```

It checks that you name yourself, that you register cleanly, and that you tolerate
another plugin beside you. It does not check that you are *useful* — a word counter
that counted letters would pass it — so the rest is your own tests.

For a hook, the tests worth writing are the two questions ADR 0021 is about: *does
the DM get what they asked for* and *what does a reader who may not see the secrets
get*. `plugins/houserules` and `plugins/spoilerbox` are short worked examples of
both.

For a policy, a table over the `dm-and-owner` cells specifically. A plain player on a
`players` page never sees secrets either way, so a case like that passes with no
policy registered at all — `TestThePolicyTakesTheSecretsAndNothingElse` is careful
about that and says why.

## The bundled three

Read these before writing your own; between them they use every capability.

- **`houserules`** — a render hook, an event subscriber and a command. Turns
  `> [!houserule]` callouts into a list on the page, and counts them as pages are
  saved. It needed no goldmark extension and no sanitiser change, which is the
  point of the example.
- **`spoilerbox`** — an access policy and a render hook. A `spoiler-note` is a page
  the table may read whose secrets stay with the DM, *even for the player who owns
  the character it is about*. Narrower than the core's `dm-and-owner`, on purpose.
- **`wordcount`** — a search field, a route and a command. Makes a page's length
  findable by search, reportable by `wiki wordcount` and visible at
  `/c/<campaign>/wordcount`. It is also the one that says out loud that a count leaks
  a little, and why that is acceptable.
