// Package safe recovers a panic from code the application does not own.
//
// # Why
//
// A plugin is a hook called in the middle of a request. If it panics, the three
// things that can happen are all bad: the panic reaches the HTTP recovery
// middleware and a DM reads a 500 for a rendering bug in a house-rules plugin; it
// escapes it and every other player at the table loses their session; or somebody
// writes a `recover` in each of the five call sites and one of them is missing.
//
// This package is the one place that answer is written down, and it is a
// `defer`-shaped function rather than a wrapper so that it can be applied to a
// function whose signature is somebody else's to change.
//
// # What it is not
//
// It is not a boundary. A plugin is compiled into the binary: it can read the
// vault, it can open the database and it can format. Nothing here makes a plugin
// safe to *write* — what makes it survivable to *ship* is that a panic is a bug
// in a build somebody can fix, and the cost of the bug is a log line and a page
// that renders without that hook's contribution rather than a process that is
// down for everybody.
//
// The scope is deliberately the *hook* and not everything a plugin ships. A goldmark
// extension is not covered, because there is no per-extension call site to recover
// at: it participates in the parse itself, and a panic there is a bug in code that
// a rebuild fixes, not a fact about a DM's markdown.
package safe

import (
	"context"
	"log/slog"
	"runtime/debug"
)

// Guard recovers a panic and logs it. It is meant to be deferred:
//
//	defer safe.Guard(log, "house-rules", "BeforeRender")
//
// # What the caller gets
//
// The function's *current* return values. A hook that panics therefore contributes
// nothing: the page carries on with the tree the previous hook left, which is why
// the caller is expected to have already put a usable value in its named return
// before making the call. That is the whole of the "recovered, logged and skipped"
// guarantee, and it is why this is a `defer` and not a wrapper — a wrapper would
// have to invent a fallback value, and inventing one is how a skip turns into a
// page rendered with an empty tree.
//
// A nil logger means [slog.Default], because a nil-pointer panic inside a panic
// handler is the one outcome that is worse than the one being handled.
func Guard(log *slog.Logger, plugin, what string) {
	recovered := recover()
	if recovered == nil {
		return
	}

	if log == nil {
		log = slog.Default()
	}

	// The stack is the useful part. "house-rules BeforeRender panicked: index out
	// of range" tells a plugin author nothing about which line; the trace tells them
	// everything, and it is only expensive on the path that is already wrong.
	log.LogAttrs(context.Background(), slog.LevelError, "plugin hook panicked; skipping it",
		slog.String("plugin", plugin),
		slog.String("hook", what),
		slog.Any("panic", recovered),
		slog.String("stack", string(debug.Stack())),
	)
}
