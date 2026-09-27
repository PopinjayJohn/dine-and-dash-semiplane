package render

import (
	"context"
)

// # A field a plugin renders
//
// A *field* is a frontmatter key somebody claimed, and its value. M11 shipped the
// claim and this is what the claim was for: `AddFieldType` refused a core key and
// said a plugin "cannot set it through the editor, and a DM can set it with their
// own editor". This milestone is the other half — a claimed key is **visible** on the
// page, rendered by the plugin that claimed it.
//
// # The three rules, and they are the same three as the hooks'
//
// **1. The value is redacted under the decision, before a plugin sees it.**
// [Renderer.fields] runs the same `PublicText` over the value that fills
// `body_public` — so a field a DM marked with a `[!SECRET]` block reaches a plugin
// with the block removed, and the secret text never leaves the core.
//
// It is not defence in depth and it is not belt and braces: it is the *only* thing
// between a DM's `mood: "[!SECRET] he is lying"` and a player's browser, and a plugin
// is code compiled into this binary, so the redaction has to happen in the core
// rather than being a promise a plugin makes about itself.
//
// The redaction is **conditional on the decision**, and the condition is the same one
// the body's stripper uses. Redacting unconditionally is the version of this that
// looked right and was wrong: `PublicText` alone hands the DM `[...]` for a field the
// DM wrote and can read in the body of the same page, which is a field that stops
// being true for the one person it was written for. `TestTheDmStillSeesTheFieldSecret`
// is the test that would have caught it.
//
// **2. The output goes through the one sanitiser.** A field's HTML is written into
// the same buffer as the page's body, before the body, and `Sanitiser.Sanitise` runs
// over the result. There is no second sanitiser for fields, no "trusted plugin
// output" exemption, and no way for a plugin to reach the response without passing
// the same allow-list the DM's own markdown passes.
//
// **3. There is no fallback.** A frontmatter key nobody claimed renders as nothing,
// so a page does not grow a row for `created:` and `tags:` and every key the DM has
// ever typed. The claim is the switch, and that is the same answer ADR 0013's
// "unknown keys are preserved" is about: preserved, not displayed.
//
// # What is *not* here
//
// A field is not a slot in a schema, and a plugin does not get to say where on the
// page its value goes. The field block is at the top of the page, before the body,
// in the order the DM wrote the keys — because "at the top" is the only answer that
// works for both a spell's casting time and a character's hit points, and a plugin
// that wants its value somewhere else uses a render hook, which has the page.

// FieldRenderer is a hook that turns one field's value into the page's HTML.
//
// One field per call rather than "render all the fields you own", because a field's
// value is the unit a plugin reasons about: a spell's `level` is a number and its
// `components` is a list and neither is a page. It is also what makes the claim a
// *claim*: the core looks up who owns the key and asks only that plugin.
type FieldRenderer interface {
	// RenderField returns the HTML for one field, or the empty string for "this is
	// not one of mine".
	//
	// Returning an empty string and returning an error are the same outcome — the
	// field renders as nothing — and the error is logged. They are the same because
	// the core has already decided which plugin owns the key, so a plugin saying
	// "not mine" is a plugin that has misread its own Setup, and either way the
	// answer for the reader is the same.
	RenderField(ctx context.Context, page Page, decision Decision, field Field) (string, error)
}
