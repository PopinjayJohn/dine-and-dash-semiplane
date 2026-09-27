package render

import (
	"slices"

	"github.com/yuin/goldmark"
)

// Hooks is everything a plugin may put between a page's markdown and its HTML:
// the goldmark extensions it contributes and the hooks it registered.
//
// It is a value and not an interface, and a struct of two slices rather than a list
// of one, because the two halves are wired in at different moments and for different
// reasons. The extensions go into the goldmark pipeline at construction, once, and a
// hook runs on every page. Folding them together would mean either running the
// extensions on every page or constructing a pipeline per hook, and both are worse
// than a field.
//
// The zero value is the honest one: no plugins, no extensions, and byte-identical
// output to a build before plugins existed. That is not a convenience — it is what
// lets every existing golden file, every existing test and every existing call site
// stay valid, and a hook mechanism that changed the bytes of a page nobody had
// asked it to change would be a hook mechanism nobody could review.
type Hooks struct {
	// Exts are goldmark extensions, registered in order. They see the source
	// before the core's own extensions have finished with it, which is goldmark's
	// arrangement and not something this package reshapes.
	Exts []goldmark.Extender

	// Hooks are the before/after records, in the order they run.
	Hooks []RenderHook
}

// RenderHook is one registered hook: the plugin that owns it and what it does.
//
// One record with two optional halves rather than two slices, so that a plugin's
// position in the pipeline is a property of the hook rather than of which method it
// happens to implement. The registry fills `Plugin` from its own record of who is
// running Setup, because a plugin cannot be trusted to spell its own name and a log
// line that names the wrong plugin sends somebody to the wrong source file.
type RenderHook struct {
	// Plugin is the name of the plugin that registered this hook, for the panic
	// log and for `wiki help plugins`. It is the normalised slug, so it is safe
	// to interpolate.
	Plugin string

	// Before is the tree hook, or nil for a hook that only post-processes.
	Before BeforeRenderer

	// After is the HTML hook, or nil for a hook that only transforms the tree.
	After AfterRenderer
}

// IsEmpty reports whether there is nothing to run, so that a caller can skip the
// loop entirely rather than iterating an empty slice on every page.
//
// It is a method rather than a `len(h.Hooks) == 0` at the call site because the
// extension slice has to be checked too, and there are three call sites that would
// otherwise each remember that.
func (h Hooks) IsEmpty() bool { return len(h.Exts) == 0 && len(h.Hooks) == 0 }

// Contributing returns the names of the plugins that changed this renderer's
// output, sorted and deduplicated.
//
// Nothing in the render path needs it and it is not here for the cache — the cache
// key does not grow a plugin field, and [cache.go] says why. It is for the one
// place that has to answer "what is in this binary" without opening the registry: a
// `?debug=1` footer and `wiki help plugins`. A hook list that could contain the same
// plugin twice (a plugin with both halves) is reported once, because the question is
// "which plugins are in this page's output", not "how many objects are in a slice".
func (h Hooks) Contributing() []string {
	seen := make(map[string]bool, len(h.Hooks))
	names := make([]string, 0, len(h.Hooks))
	for _, hook := range h.Hooks {
		if hook.Plugin == "" || seen[hook.Plugin] {
			continue
		}
		seen[hook.Plugin] = true
		names = append(names, hook.Plugin)
	}
	slices.Sort(names)
	return names
}
