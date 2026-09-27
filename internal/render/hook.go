package render

import (
	"bytes"
	"context"

	"github.com/yuin/goldmark/ast"
)

// The two ends of a render hook, and the reason they are two interfaces rather
// than one with two methods.
//
// ADR 0002 and §12 both sketch a single `RenderHook` with a `BeforeRender` and an
// `AfterRender`, and the sketch is worth taking apart. A hook that only post-
// processes the HTML — which is what both of M11's own bundled plugins do, and what
// a plugin that adds a footer or a count will always do — would have to write a
// `BeforeRender` that returns the tree it was given. That is a no-op with a
// signature somebody has to get right, in a plugin written by somebody who does not
// read this file, and the two halves have genuinely different lifetimes anyway: a
// plugin that can see the AST is contributing *structure*, and a plugin that can see
// the HTML is contributing *presentation*. So they are two interfaces and a
// [RenderHook] record that may carry either, both or neither.
//
// # BeforeRender runs after the secrets are gone
//
// This is the load-bearing placement in the whole file and it is not negotiable. The
// hook is handed the parsed tree at a point where:
//
//   - the links have already been resolved, so a hook that rewrites an href is
//     rewriting a link whose destination the read predicate already admitted;
//   - the secrets have already been stripped, so the tree contains no secret text
//     and **no tree transform can put any back**. A hook that ran *before* the
//     stripper could take the contents of a `[!SECRET]` callout and move them into
//     the open body, and the stripper would then have nothing to strip. That is not
//     a bug in a plugin; it is the capability the placement would hand it.
//
// The cost of the placement is that a hook cannot see a secret, and that is the
// intended cost. A plugin that needs a secret has the DM, who sees it anyway.
//
// # The body is the whole file
//
// `page.Body` is the file as the DM wrote it, `[!SECRET]` blocks and all, because
// `render.Page.Body` is the input to the pipeline and the pipeline's input does not
// change for the convenience of a caller at the end of it. A hook that copies text
// out of the body and into the output is responsible for what it copied.
//
// That is a contract rather than a hole. A plugin is code compiled into this
// binary: it can read the vault file and open the database, and a guard around it
// would be a guard around code that is already in the process. What the core owes
// the reader is one boundary, and that boundary is [AfterRenderer]'s placement
// rather than than the body's.
//
// # The tree has no source in it
//
// A goldmark text node is a pair of offsets into the source, and `Segment.Value`
// takes the source as an argument — the tree does not carry it. So a hook that wants
// to read what a node says calls `node.(*ast.Text).Segment.Value([]byte(page.Body))`,
// and the fixture in `hook_test.go` does exactly that.
//
// It is written down because the first version of that fixture called `Value(nil)`,
// and the renderer's own recovery caught the resulting slice-bounds panic — which
// meant the test asserting "a tree hook never sees a secret" was passing for the
// wrong reason. The hook had contributed nothing because it had crashed, not because
// the secret had been stripped.

// BeforeRenderer is a hook that sees the page's tree before it is written to HTML.
//
// It is an interface rather than a func type because a plugin will usually be a
// struct with configuration in it, and because a method name is what appears in the
// panic log line when one of these goes wrong.
type BeforeRenderer interface {
	// BeforeRender is handed the tree and returns the tree to render. Returning
	// the tree it was given, or a nil tree, means "I changed nothing"; returning
	// an error means the hook contributed nothing and the render carries on
	// without it, which is the same outcome as a panic and is the reason the two
	// are treated the same way.
	//
	// The decision travels with it for the same reason it travels with [Renderer.Render]:
	// it is the only thing that says what may be shown, and a hook that redacts
	// something needs it to know what the reader was already given.
	BeforeRender(ctx context.Context, page Page, decision Decision, doc ast.Node) (ast.Node, error)
}

// AfterRenderer is a hook that sees the page's HTML before it is sanitised.
//
// "Before it is sanitised" is the whole of this interface's contract, and it is
// where ADR 0002's sketch was ambiguous. The alternative — a hook that runs *after*
// [Sanitiser.Sanitise] — is a way for a plugin to put unsanitised HTML on a page a
// player reads, in a project whose fourth invariant is that rendered markdown is
// sanitised for every author, and "a plugin is not an author" is not an exception
// this codebase can make. Before the sanitiser, a plugin's output is filtered by
// exactly the allow-list the DM's own markdown is filtered by, which is also why a
// plugin that needs a new element or a new class discovers that it cannot have one.
type AfterRenderer interface {
	// AfterRender is handed the buffer the HTML has been written into and may
	// append to it, replace it, or leave it alone. Returning an error means the
	// hook contributed nothing beyond what it has already written, which is
	// deliberate: a partially-applied hook is harder to reason about than an
	// absent one, and the buffer is the only state the render has left.
	AfterRender(ctx context.Context, page Page, decision Decision, out *bytes.Buffer) error
}
