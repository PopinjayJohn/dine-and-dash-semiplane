# 0002. Extensibility is an in-process plugin registry, not Go's plugin system

- **Status:** Accepted
- **Date:** 2026-09-26

## Context

The system must be extensible: game systems, house rules, rendering tweaks and
small utilities should be addable without forking the core. In Go there are
three common shapes for this, and they fail very differently.

1. **A `switch` over plugin names in the core.** Simple, no interfaces, but
   every plugin is a fork. Not a plugin system.
2. **Go's `buildmode=plugin`** (`.so` files loaded at runtime). Requires the
   plugin and the host to be built with the *exact same* Go toolchain and a
   compatible dependency graph, panics on mismatch, and is unsupported on
   Windows. For a tool distributed as a single static binary to a DM on Linux,
   macOS and Windows, this is a non-starter.
3. **An interface plus a compile-time registry.** Plugins are ordinary Go
   packages imported by `cmd/wiki/plugins.go`. Full type safety, works with
   `go test`, no startup cost, no runtime loading.

A fourth option — out-of-process plugins over a JSON-RPC protocol — is the only
shape that allows third-party, non-Go, or separately-versioned plugins. It is
also a protocol to design, document, secure, version and support.

## Decision

Use option 3: a narrow capability surface handed to each plugin's `Setup`
method, with plugins registered by a compile-time list in `cmd/wiki/plugins.go`.

```go
// internal/plugin/plugin.go
package plugin

// Plugin is the unit of extensibility. Implement it, import it in
// cmd/wiki/plugins.go, done.
type Plugin interface {
	Name() string
	Version() string
	Setup(*Registry) error
}
```

`Capabilities` is the entire surface a plugin may touch. It is deliberately
small, because every field added to it is a permanent compatibility promise.

```go
// internal/plugin/caps.go
package plugin

type Capabilities struct {
	Pages    PageTypes    // page types, field schemas, templates
	Fields   FieldTypes   // new field kinds (dice, statline, ref, ...)
	Render   RenderHooks  // before/after markdown render
	Markdown GoldmarkExts // contribute goldmark extensions
	HTTP     Routes       // mount chi routes
	Access   Policies     // compose extra visibility rules
	Search   SearchFields // extra indexed fields
	Events   Subscribe    // PageSaved, PageViewed, ShareLinkUsed, ...
	CLI      Commands     // `wiki <plugin> <subcommand>`
}
```

Guarantees the registry provides:

- **Deterministic order.** Capabilities are sorted by `(Priority, Name)`, so
  two plugins cannot nondeterministically win a hook.
- **Failure isolation.** A panicking hook is recovered, logged and skipped; the
  page still renders.
- **Loud startup.** Duplicate plugin names fail startup with a readable error.
- **No ambient globals.** Everything flows through the `*Registry` passed to
  `Setup`.
- **Composition, not override.** Plugins may *add* to the access-policy
  resolver; they cannot replace it.

Out-of-process plugins are explicitly **out of scope for v1**. The interface is
kept narrow and explicit so that adding an RPC transport later is additive
rather than a redesign.

## Consequences

**Good**

- Works identically on every platform, from a single static binary.
- Every plugin is unit-testable with the standard tools, and the bundled
  reference plugins double as the contract test suite.
- The plugin API cannot drift from reality, because the shipped plugins are
  compiled against it in CI.

**Bad**

- Plugins are compiled in. Adding one requires a rebuild and a new release.
  For the intended audience — a DM who wants a house-rules plugin — that is
  acceptable; for a public extension ecosystem it would not be.
- The capability surface must be curated. Additions are additive changes to
  `Capabilities`, never changes to what an existing field means.
