// Package render turns a page's markdown into the HTML the wiki serves.
//
// # One path
//
// There is exactly one render path, and it takes the whole body plus a
// Decision. There is no "public render" and no preview: a second path would be
// a second set of golden files and a second chance to leak, and the rule that
// makes this package safe is that there is nowhere else for a secret to hide
// (ADR 0007).
//
// # The order of operations
//
//	markdown -> goldmark AST -> [wiki links, callouts] -> strip secrets
//	         -> HTML -> sanitise -> out
//
// The stripping happens on the tree, before anything writes HTML, so a secret's
// text is never rendered and then removed: it is never rendered. The sanitiser
// runs last on the HTML and runs on the DM's pages as well as a player's,
// because a DM is the highest-value target and sanitising "only untrusted
// authors" would leave that target on the weakest path.
//
// # What this does not know yet
//
// A Decision here is not `access.Decision`, because access control is M7. It is
// the one field the renderer needs, and its zero value is the safe one: a caller
// that has not decided anything gets no secrets. M7 feeds the real decision in,
// and the cache is keyed by it, so a render made for a DM can never be handed to
// a player.
package render

import (
	"bytes"
	"context"
	"fmt"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

// Version is the type of RendererVersion, so the cache key and the index column
// cannot drift apart over what a version is.
type Version = int

// RendererVersion is the version of the render path this binary implements.
//
// Bump it when the output changes for the same input: a new callout class, a
// different heading anchor, a sanitiser policy change. Every page whose stored
// version differs is re-rendered, so one constant invalidates the whole cache at
// once rather than a cache going quietly stale.
const RendererVersion Version = 1

// Renderer renders pages. It is safe for concurrent use, because a DM and three
// players will render at the same time.
type Renderer struct {
	md goldmark.Markdown
}

// New returns a renderer with the goldmark pipeline this project commits to.
//
// GFM is on because a DM's notes contain tables and task lists. Footnotes are on
// because they are the most common way to write a note at the bottom of a page
// in Obsidian, and a page whose footnotes become literal text is a page the DM
// has to fix by hand.
//
// Unsafe HTML is rendered, and the sanitiser removes what is not allowed. The
// alternative -- dropping raw HTML at the renderer -- turns a DM's notes into a
// page with holes in it, and the sanitiser is the thing that gets tested
// against an XSS corpus.
func New() *Renderer {
	return &Renderer{md: goldmark.New(
		goldmark.WithExtensions(
			extension.GFM,
			extension.Footnote,
		),
		goldmark.WithParserOptions(
			// The id a heading gets is what the table of contents links to and
			// what the HTML carries, so it is generated once, here, and read
			// from the tree rather than guessed twice.
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(),
			html.WithXHTML(),
			// Hard line breaks are deliberately *off*. A DM's notes are
			// soft-wrapped, and turning every newline in the file into a <br>
			// would break sentences at whatever column their editor wrapped
			// at. Obsidian's live preview does not do it either; the setting
			// that does is called "strict line breaks" and is off by default.
		),
	)}
}

// Render turns a page into HTML.
//
// The Decision is taken even though nothing in this milestone reads it yet: it
// is the parameter the rest of the pipeline hangs off, and a signature that
// grows a security-relevant argument later is a signature that callers get
// wrong in the meantime.
func (r *Renderer) Render(ctx context.Context, page Page, decision Decision) (Result, error) {
	source := []byte(page.Body)

	doc := r.md.Parser().Parse(text.NewReader(source))

	toc := buildTOC(doc, source)

	var rendered bytes.Buffer
	if err := r.md.Renderer().Render(&rendered, source, doc); err != nil {
		return Result{}, fmt.Errorf("rendering %s: %w", page.Path, err)
	}

	return Result{HTML: rendered.String(), TOC: toc}, nil
}
