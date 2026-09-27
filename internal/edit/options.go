package edit

import "github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"

// Options is what an editor is given besides the three things it cannot work
// without.
//
// It is one field today and it is a struct anyway, because this is the third place
// a plugin's capabilities have to be threaded (after the HTTP layer's renderer and
// its router) and three functions with a growing parameter list is how one of them
// is missed. A missed thread is a plugin that works on the page and not on the
// preview, which is the kind of bug that is reported as "the preview lies".
type Options struct {
	// Hooks are the plugins' render hooks. The zero value previews exactly what a
	// build without plugins previews.
	Hooks render.Hooks
}
