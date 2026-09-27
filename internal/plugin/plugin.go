// Package plugin is the compile-time plugin registry: the seam through which a
// piece of code that is not core contributes to a render, an access decision, a
// route, a search index and a command.
//
// # Why in-process
//
// [ADR 0002](../../docs/adr/0002-plugin-registry-in-process.md) chose an
// interface plus a compile-time list over Go's `buildmode=plugin` and over a
// `switch` on plugin names, and recorded why: a `switch` makes every plugin a
// fork, and `.so` files need a byte-identical toolchain, panic on a mismatch and
// do not exist on Windows. This package is the thing that decision describes.
//
// # What a plugin may reach
//
// Everything it is given, and nothing else. There is no package-level registry, no
// init-time registration and no global: a plugin that needs the store is handed it
// through the [Registry] its Setup is called with, and a test can build a registry
// with one plugin in it and know that nothing else in the process is reachable from
// it. That is the "no ambient globals" guarantee, and it is the reason this
// package has no `Register` at init time and never will.
//
// # The guarantees
//
// Four, each with a named test beside it.
//
//   - **Deterministic order.** Capabilities run in (Priority, Name) order, so two
//     plugins cannot win a hook by which one the linker happened to place first.
//     The sort happens once, in [Registry.Add], and everything after it reads an
//     already-ordered slice.
//   - **Failure isolation.** A panicking hook is recovered, logged and skipped, and
//     the page still renders. The recovery is `internal/safe`, because it has to
//     happen at the call site rather than here: a hook does not panic while it is
//     being registered, it panics while a DM is reading a page.
//   - **Loud startup.** A duplicate name, a name that is not a name, a redefinition
//     of something core owns, and a Setup that returns an error are all refused by
//     [Registry.Add] and stop the process. None of them is resolved by "whoever was
//     added first", because that is a coin toss decided by an import graph.
//   - **Composition, not override.** A plugin may *narrow* an access decision and
//     may never widen one. That one is not a property of this package — it is a
//     property of `access.Policies`, which is where a plugin's access capability
//     lands, and it is enforced there because that is the only place that can
//     enforce it structurally.
//
// # What is *not* here
//
// A dynamic loader, a plugin marketplace, a version negotiation, and any way for a
// plugin to replace core behaviour rather than add to it. The last of those is the
// one that would matter: every capability in this package is additive by
// construction, and a capability that could override one would make every
// compatibility promise below it unenforceable.
package plugin

import (
	"errors"
)

// Plugin is the unit of extensibility. Implement it, import it in
// cmd/wiki/plugins.go, and it is part of the build.
//
// A Plugin is a value, not a global: `Setup` is called once, by [Registry.Add], on
// the registry the process built. A plugin that needs the store, the clock or the
// data directory takes them as fields and is constructed with them, which is what
// makes a plugin testable without a server.
type Plugin interface {
	// Name is the plugin's identity, and it is normalised into a [domain.Slug]
	// before anything else looks at it.
	//
	// Normalising rather than rejecting is the same trade the campaign slug makes
	// and for the same reason: "House Rules" and "house-rules" are one plugin,
	// and a name that has reduced to nothing is a mistake worth stopping for. The
	// consequence that matters downstream is that a name is safe in a URL, in a
	// log line and in a `class="callout-<name>"` without a second escaping step
	// anywhere.
	Name() string

	// Version is the plugin's own version, and nothing in this package compares it
	// to anything. It is here so that a log line and a `wiki help plugins` can say
	// which build of a plugin is in the binary, and so that a plugin whose output
	// changes can say so in the place a reader will look.
	Version() string

	// Setup registers the plugin's capabilities. It is called exactly once, from
	// [Registry.Add], before the registry is read by anything.
	//
	// Returning an error refuses the plugin and stops startup. A plugin that
	// cannot configure itself is a plugin whose capabilities are a guess, and a
	// guess in the render path is a guess about what a player may read.
	Setup(*Registry) error
}

// The ways [Registry.Add] can refuse a plugin. They are sentinels rather than
// formatted strings so that a caller — `cmd/wiki`, and a test — can tell a
// duplicate from a reserved name, and so that a future refusal is a new sentinel
// rather than a new sentence to match on.
var (
	// ErrDuplicateName is a second plugin, page type or frontmatter key with a
	// name the registry has already taken. It is a startup failure and not a
	// last-one-wins: two things claiming one name means one of them is not going
	// to run, and a DM has no way to tell which.
	ErrDuplicateName = errors.New("plugin: name already registered")

	// ErrInvalidName is a name that is not a usable slug at all — empty, or
	// nothing but punctuation.
	ErrInvalidName = errors.New("plugin: name is not a usable slug")

	// ErrNoVersion is a plugin that named itself and then could not say which
	// build it is. Not fatal in any interesting sense, and refused anyway: the
	// registry exists to answer "what is in this binary", and a row with a blank
	// in it is a row nobody can use.
	ErrNoVersion = errors.New("plugin: no version")

	// ErrReservedName is a page type or frontmatter key that belongs to core. It
	// is a separate error from ErrDuplicateName because the two have different
	// fixes: a duplicate is two plugins disagreeing, and a reserved name is a
	// plugin disagreeing with the application it is compiled into.
	ErrReservedName = errors.New("plugin: name is reserved by the core")
)
