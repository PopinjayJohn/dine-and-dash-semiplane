package render

import (
	"bytes"
	"context"
	"log/slog"

	"github.com/yuin/goldmark/ast"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/safe"
)

// runBefore and runAfter are the two loops that put a plugin between a page's
// markdown and its HTML, and they are the reason the pipeline in render.go reads
// as six steps rather than four.
//
// Both have the same shape, and the shape is the guarantee:
//
//   - the caller's usable value is assigned **before** the hook is called, so a
//     hook that panics or returns an error contributes nothing and leaves the
//     render exactly where the previous plugin left it;
//   - every hook is isolated by `safe.Guard`, so a panic is a log line and a
//     missing contribution rather than a 500 for the DM or a dead process for
//     everybody at the table;
//   - the failure is *logged and skipped*, never propagated. A render that failed
//     because a house-rules plugin had a nil map would be a wiki with no pages,
//     and the alternative — serving the page without the hook — is the whole
//     point of isolating it.
//
// An error from a hook is treated exactly as a panic from one, deliberately. The
// two are the same event from the render's point of view ("this hook did not
// manage to contribute") and the alternative — propagating the error and failing
// the page — would make a plugin capable of denial of service against every page in
// the campaign.

// runBefore runs every registered tree hook in order and returns the tree to
// render.
//
// The loop is not short-circuited on error, because "this hook contributed nothing"
// and "stop asking the others" are different decisions and only the first one is
// this package's to make.
func (r *Renderer) runBefore(ctx context.Context, page Page, decision Decision, doc ast.Node) ast.Node {
	for _, hook := range r.hooks.Hooks {
		if hook.Before == nil {
			continue
		}

		// The named return is the fallback. Assigning it before the call is what
		// makes a panic a no-op rather than a nil tree: `safe.Guard` recovers and
		// the function returns whatever `doc` held a moment ago.
		func() {
			defer safe.Guard(r.log, hook.Plugin, "BeforeRender")

			replaced, err := hook.Before.BeforeRender(ctx, page, decision, doc)
			switch {
			case err != nil:
				r.logHook(hook.Plugin, "BeforeRender", err)
			case replaced != nil:
				doc = replaced
			}
		}()
	}

	return doc
}

// fields is the field block: every claimed field the page carries, rendered by the
// plugin that claimed it, in the order the DM wrote the keys.
//
// The order comes from [Page.Fields] and not from the registry, and the difference
// matters to a reader: a spell whose frontmatter says `level` then `school` shows
// level then school however the plugins happen to be registered, and a page whose
// fields rearrange themselves between builds is a page nobody can screenshot.
//
// A field nobody claimed is not in [Page.Fields] at all, so there is no fallback
// path here and no "render it as plain text" branch. The claim is the switch.
//
// The redaction is on this path and not at the call site that built [Page.Fields],
// because it is the only place that has the decision. A page's field list is
// assembled by a handler that knows the principal, but the *render* is the thing
// that decides what may be shown, and a field redacted anywhere else would be a
// field redacted under somebody else's decision.
func (r *Renderer) fields(ctx context.Context, page Page, decision Decision, out *bytes.Buffer) {
	if len(r.hooks.Fields) == 0 || len(page.Fields) == 0 {
		return
	}

	for _, field := range page.Fields {
		spec, claimed := r.hooks.Fields[field.Name]
		if !claimed || spec.Renderer == nil {
			// Unreachable while the claim is the switch, and a skip rather than an
			// error because the alternative is a page that will not render because
			// one field's owner went missing.
			continue
		}

		// The redaction, under the decision, exactly as the body is. See rule 1 in
		// field.go: this is the only thing between a DM's
		// `mood: "[!SECRET] he is lying"` and a player's browser, and a plugin is
		// code compiled into this binary so it cannot be a promise a plugin makes.
		//
		// The `CanSeeSecrets` test is the whole of the difference, and it is the
		// difference the body also makes. Redacting unconditionally -- with
		// `PublicText` and no test -- would hand the DM `[...]` for a field the DM
		// wrote and can see in the body of the same page, which is a field that
		// stops being true for the one person it was written for.
		public := field.Value
		if !decision.CanSeeSecrets {
			public, _ = PublicText(field.Value)
		}

		html := r.callFieldRenderer(ctx, spec.Renderer, field.Name, page, decision, Field{
			Name:  field.Name,
			Value: public,
			Kind:  spec.Kind,
		})
		if html == "" {
			continue
		}

		out.WriteString(html)
	}
}

// callFieldRenderer runs one renderer and recovers its panic, returning the HTML or
// the empty string.
//
// The same shape as the two hook loops and for the same reason: the caller's usable
// value is assigned before the call, so a panic is a field that renders as nothing
// rather than a page that will not render. A field is decoration on top of a page a
// DM has to be able to read, and a broken field renderer is a bug in a build somebody
// can fix.
func (r *Renderer) callFieldRenderer(
	ctx context.Context, renderer FieldRenderer, name string, page Page, decision Decision, field Field,
) string {
	html := ""

	func() {
		defer safe.Guard(r.log, "field:"+name, "RenderField")

		produced, err := renderer.RenderField(ctx, page, decision, field)
		if err != nil {
			r.logHook("field:"+name, "RenderField", err)
			return
		}
		html = produced
	}()

	return html
}

// runAfter runs every registered HTML hook in order, against the buffer.
//
// The buffer is shared, so one hook's output is the next hook's input and the
// order is the plugins' `(Priority, Name)` order — the same order everything else
// uses, and the reason two plugins cannot disagree about who ran first.
func (r *Renderer) runAfter(ctx context.Context, page Page, decision Decision, out *bytes.Buffer) {
	for _, hook := range r.hooks.Hooks {
		if hook.After == nil {
			continue
		}

		func() {
			defer safe.Guard(r.log, hook.Plugin, "AfterRender")

			if err := hook.After.AfterRender(ctx, page, decision, out); err != nil {
				r.logHook(hook.Plugin, "AfterRender", err)
			}
		}()
	}
}

// logHook is one line saying which plugin's hook declined to contribute and why.
//
// It is at Error rather than Warn because a hook that errors on every page is a
// wiki that is quietly missing a feature, and the DM is the only person who can fix
// it. The page renders, so the alternative — a 500 — is worse; the log line is what
// stops it being silent.
func (r *Renderer) logHook(plugin, hook string, err error) {
	r.log.LogAttrs(context.Background(), slog.LevelError, "plugin hook failed; rendering without it",
		slog.String("plugin", plugin),
		slog.String("hook", hook),
		slog.String("error", err.Error()),
	)
}
