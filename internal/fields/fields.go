// Package fields is the seam between a plugin's claims and a page's frontmatter.
//
// # Why it is a package
//
// Three parties need it and none of them can own it. `internal/http` has a page row
// and needs a `render.Page`; `internal/edit` has a file and needs the same; and
// `internal/plugin` knows which keys a build claimed but must not be imported by the
// HTTP layer, because the HTTP layer is the thing plugins are attached *to*.
//
// Put it in `render` and `render` learns about frontmatter documents. Put it in
// `vault` and `vault` learns which keys a plugin claimed. Put it in `plugin` and
// `plugin` has to be imported by `internal/http`, which is the dependency the whole
// of M11 is arranged to run the other way. So it is here: a package of one function,
// importing the two it serves.
//
// # What it does, and does not, decide
//
// It decides **which** keys and **in what order**. It does not decide what a value
// looks like — that is the plugin's — and it does not decide whether a value may be
// shown, which is the redaction inside `internal/render` and happens under the
// decision. Nothing here reads a clock, touches a store, or makes a rights
// decision, and the reason is that a function with all three would be a second
// answer to "what may this reader see" and the invariant says there is one.

package fields

import (
	"sort"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// Of is the claimed fields a page's frontmatter carries, in the order the DM wrote
// them.
//
// The zero value of `render.Hooks` carries no claims, so a build with no plugins
// returns nil and the render path skips the field block entirely — which is what
// keeps a build without plugins byte-identical to a build before plugins existed.
//
// A frontmatter block that is not a mapping, or a page with none, is nil. So is a
// page whose claimed keys are all absent. All three are the ordinary case for most
// pages in most campaigns, and a nil here costs one length check on the render path.
func Of(hooks render.Hooks, frontmatter string) []render.Field {
	if len(hooks.Fields) == 0 || frontmatter == "" {
		return nil
	}

	keys := make([]vault.Key, 0, len(hooks.Fields))
	for name := range hooks.Fields {
		keys = append(keys, vault.Key(vault.NormaliseFieldKey(name)))
	}
	// The map is unordered and `vault.Fields` walks the *file*, so this sort does not
	// decide the order the fields render in. It is here because a slice of keys
	// handed to a function that promises an order should be a slice somebody could
	// have predicted, and because a test comparing two calls should not be comparing
	// map iteration.
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	values, order := vault.Fields(frontmatter, keys)
	if len(order) == 0 {
		return nil
	}

	claimed := make([]render.Field, 0, len(order))
	for _, key := range order {
		spec, isClaimed := hooks.Fields[vault.NormaliseFieldKey(string(key))]
		if !isClaimed {
			// Unreachable: `keys` was built from the same map. The skip is here
			// because a field with no renderer would render as nothing anyway, and a
			// nil dereference in a render path is a worse way to find that out.
			continue
		}
		claimed = append(claimed, render.Field{
			// The *claim's* name, not the file's. A renderer matching on `Level`
			// when the file says `level` is a plugin that works on somebody's
			// machine.
			Name:  vault.NormaliseFieldKey(string(key)),
			Value: values[key],
			Kind:  spec.Kind,
		})
	}

	if len(claimed) == 0 {
		return nil
	}
	return claimed
}
