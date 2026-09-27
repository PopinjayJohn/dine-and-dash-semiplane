// Package spoilerbox is a plugin that adds a page type whose secrets stay with the
// DM, and says so on the page.
//
// # What it demonstrates
//
// An `access.Policies` contributor and a render hook — the two capabilities whose
// interesting question is the same one: *where in the pipeline does this run, and
// what happens when it fails?* Both answers are in the packages that own them
// (`internal/access` and `internal/render`) and neither is reargued here.
//
// # The rule
//
// A `spoiler-note` is a page the whole table may read and whose `[!SECRET]` blocks
// are still the DM's, even for the player who owns the character the page is about.
//
// That is narrower than the core's own `dm-and-owner`, and narrower on purpose: the
// core's answer lets the owner see the secrets, because a character page is written
// *about* the player who owns it. A plot note is not. A DM writing "who really paid
// the toll" on a page their players can read does not want the player to read it,
// and `visibility: players` cannot say that — the page genuinely is for everybody.
//
// So the plugin claims a page *type* and a policy: a `spoiler-note` takes the
// secrets away from everybody but the DM. It is a narrowing and nothing else, which
// is what `access.Policies` guarantees structurally rather than by asking.
//
// # What it is not
//
// It is not a way to keep a whole page from a player. `CanRead` is untouched: a
// player can read a `spoiler-note`, and the render hook says on the page that there
// is more underneath. A plugin that hid the page would have a much better demo and
// a much worse one — a page that vanishes with no explanation is the disclosure
// shape ADR 0020 spent a milestone removing from a link resolution.
package spoilerbox

import (
	"bytes"
	"context"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// PageType is the type a `spoilerbox` page carries in its frontmatter.
const PageType domain.PageType = "spoiler-note"

// Plugin is the spoilerbox plugin.
type Plugin struct{}

// New returns the plugin. It has no state and no dependencies, which is the shape
// most plugins should have and the one a plugin author should copy: a plugin that
// needs the store takes it as a field and a plugin that needs nothing has nothing
// to hold.
func New() *Plugin { return &Plugin{} }

// Name is the plugin's identity.
func (p *Plugin) Name() string { return "spoilerbox" }

// Version is this plugin's own version.
func (p *Plugin) Version() string { return "1.0.0" }

// Setup registers the policy, the hook, and the page type the policy narrows.
//
// The page type is registered *and* used by the policy in the same Setup, and the
// order is the argument for the claim existing at all: a plugin that narrows a type
// it has not claimed could be narrowing a type another plugin also claims, and
// `AddPageType` refuses a core type, so the policy below can rely on `spoiler-note`
// being this plugin's.
func (p *Plugin) Setup(reg *plugin.Registry) error {
	if err := reg.AddPageType(PageType, "A note the table may read whose secrets stay with the DM."); err != nil {
		return err
	}

	if err := reg.AddAccessPolicy(p.policy); err != nil {
		return err
	}

	return reg.AddRenderHook(render.RenderHook{Plugin: "spoilerbox", After: p})
}

// policy takes the secrets off everybody but the DM, on this plugin's page type.
//
// The whole rule is three lines and each one is a decision:
//
//   - the DM is unaffected, because a DM's own page is the one place the secrets
//     must be readable;
//   - anybody else loses `CanSeeSecrets` and keeps everything else, because a
//     player who cannot read the page at all is a *different* plugin and a
//     different policy — and one that this demo would blur;
//   - a page that is not a `spoiler-note` is untouched, because a policy that fired
//     on every page would be a policy that says nothing.
//
// It cannot widen anything, and that is not this function's doing: `access.Policies`
// ANDs whatever it returns with what the matrix already said.
func (p *Plugin) policy(
	_ context.Context, principal access.Principal, page access.PageMeta, _ access.Decision,
) (access.Decision, error) {
	if principal.Role == domain.RoleDM || page.Type != PageType {
		return access.Granted(), nil
	}

	// CanRead, CanEdit and CanReveal are all still true; only the secrets go.
	return access.Decision{
		CanRead:   true,
		CanEdit:   true,
		CanReveal: true,
	}, nil
}

// AfterRender says, on the page, that there is more underneath.
//
// It is the notice that makes the policy a feature rather than a disappearance. A
// player reading a `spoiler-note` sees a line saying the DM is holding something
// back, which is the whole content of a spoiler box in a game — and it is *true*,
// because the render that carries it is the one that ran with the policy applied.
//
// The condition is the page type *and* the reader not seeing the secrets. For the DM
// it would be a lie, and for a player on an ordinary page it would be noise.
func (p *Plugin) AfterRender(
	_ context.Context, page render.Page, decision render.Decision, out *bytes.Buffer,
) error {
	if decision.CanSeeSecrets {
		return nil
	}
	if page.Type != PageType.String() {
		// The page *type*, not a path prefix. `render.Page` carries the type for
		// exactly this: a DM keeps their spoiler notes wherever they keep them, and a
		// plugin that had to guess with a path prefix would be showing a spoiler
		// notice on `locations/gm-notes.md` because of where somebody filed it.
		return nil
	}

	out.WriteString(`<section class="callout callout-spoilerbox">` +
		`<p><strong>The DM is holding something back on this page.</strong> ` +
		`Ask them at the table.</p></section>` + "\n")
	return nil
}
