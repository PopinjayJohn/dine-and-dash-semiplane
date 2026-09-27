package render

import "github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"

// The renderer takes an `access.Decision`.
//
// M3 shipped a placeholder with one field and a comment saying M7 would fill it
// in. This is that: the renderer's decision *is* `access.Decision`, aliased so
// that the name at the call site says what it is, rather than a
// `render.Decision` that somebody has to keep in step with the real one. A
// one-field type of our own is a second answer to "may this principal see the
// secrets on this page", and the whole of the renderer's safety is that there is
// one place that answers it.
//
// `Decision`'s zero value permits nothing, which is the only safe zero and is the
// property `access.Decision` was built with: a caller that forgot to ask gets a
// page with no secrets in it, which is a missing feature and not a disclosure.
type Decision = access.Decision

// NewDecision is the renderer's own name for the zero value, so that a caller
// writing a test does not have to know that a struct literal is the safe default.
func NewDecision() Decision { return access.Decision{} }

// Grant is the decision for a principal who may see everything on the page: a DM,
// or the player who owns a character-owned page (ADR 0007).
func Grant() Decision { return access.Granted() }
