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
// # What this knows about a page
//
// Two things and no more: the markdown it is turning into HTML, and the campaign
// and path that a link to the page has to be built from. A page's *audience* is
// not one of them, and neither is its secrets: those are an `access.Decision`,
// and the decision is threaded in beside the page rather than read off it.
package render

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/yuin/goldmark"
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
//
// It is 2 because a resolved link grew the campaign it is in: `/c/rivergate`
// became `/c/blackwater/rivergate`, which is a different string for the same
// input and therefore a different render.
const RendererVersion Version = 2

// Renderer renders pages. It is safe for concurrent use, because a DM and three
// players will render at the same time.
type Renderer struct {
	md goldmark.Markdown

	// parseMu serialises parses against this renderer's parser. See parse.go for
	// what it is defending and what it is not: it is defensive, and a render is
	// one call per HTTP request from M8 onwards, so this parser is shared.
	parseMu sync.Mutex

	// cache holds the renders, keyed by everything that can change one.
	cache *Cache

	// saniti is the last thing the HTML passes through.
	saniti *Sanitiser

	// links resolves what a wiki link points at. A nil resolver resolves
	// nothing, and every link in the output is then visibly unresolved.
	links LinkResolver

	// hooks are the plugins' goldmark extensions and render hooks. They are fixed
	// at construction and never change, which is the reason the cache key does
	// not carry a plugin field: see cache.go.
	hooks Hooks

	// log is where a panicking hook is reported, and it is never nil so that the
	// recovery cannot itself be the thing that fails.
	log *slog.Logger
}

// NewWithLinks returns a renderer that resolves wiki links through a resolver.
//
// It is separate from New because a nil resolver is a legitimate thing to render
// with -- a page read before the index has seen it, a test, a golden file -- and
// a constructor that took a resolver would either have to reject nil or have a
// second way of saying "no links", and both are worse than saying it at the call
// site.
func NewWithLinks(links LinkResolver) *Renderer {
	return NewWith(Options{Links: links})
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
//
// It is `NewWith(Options{})`, and it is kept because a golden file and a test that
// does not care about links or plugins should not have to name a zero value to say
// so.
func New() *Renderer {
	return NewWith(Options{})
}

// Render turns a page into HTML.
//
// The Decision is in the cache key as well as the arguments, because a render
// made for a DM and a render made for a player are different bytes and one of
// them contains secrets: see the cache key comment for the whole list of what a
// missing field would cost.
func (r *Renderer) Render(ctx context.Context, page Page, decision Decision) (Result, error) {
	key := CacheKey{
		ContentHash:   page.ContentHash,
		Version:       RendererVersion,
		CanSeeSecrets: decision.CanSeeSecrets,
		ReadsAll:      decision.ReadsAll,
		Campaign:      page.Campaign,
		Path:          page.Path,
		Type:          page.Type,
	}
	if cached, found := r.cache.Get(key); found {
		return cached, nil
	}

	result, err := r.render(ctx, page, decision)
	if err != nil {
		return Result{}, err
	}

	r.cache.Put(key, result)
	return result, nil
}

// render is the pipeline itself, with no cache in it. It is a separate function
// so the cache is an optimisation of one function rather than woven through it,
// and so a test can ask for the pipeline without a cache at all.
func (r *Renderer) render(ctx context.Context, page Page, decision Decision) (Result, error) {
	source := []byte(page.Body)

	doc := r.parseSource(source)

	// Resolution walks the tree rather than the source, so a link that a
	// resolver found is a link whose destination has been rewritten, not a
	// string that was replaced somewhere in the markdown.
	if err := resolveLinks(ctx, doc, r.links, page.Campaign); err != nil {
		return Result{}, fmt.Errorf("resolving the links in %s: %w", page.Path, err)
	}

	// The secrets go before the table of contents is built, because a heading
	// inside a stripped secret must not end up in a table of contents a player
	// can read: the *text* of a secret heading is secret, whatever the text of
	// the callout's own body is.
	stripped := (&secretStripper{decision: decision}).strip(doc)

	// A plugin's tree hook runs here, and the placement is the whole of the
	// security argument: the stripper has already unlinked every secret subtree, so
	// the tree this hook is handed contains no secret text and no transform of it
	// can put any back. Before the stripper, a hook could lift a `[!SECRET]`
	// callout's contents into the open body and the stripper would have nothing
	// left to remove. See hook.go.
	doc = r.runBefore(ctx, page, decision, doc)

	toc := buildTOC(doc, source)

	var rendered bytes.Buffer

	// The fields, before the body, into the same buffer. This is the whole of "a
	// field goes through the one sanitiser": there is no second buffer, no second
	// pass, and no path from a field renderer's output to the response that does not
	// go through the `Sanitise` call below.
	//
	// Before the body because both of the things this is for want to be looked at
	// first: a spell's casting time, a character's hit points. A field block under
	// three paragraphs of description is a field block nobody reads, and the DM
	// would stop filling it in.
	r.fields(ctx, page, decision, &rendered)

	if err := r.md.Renderer().Render(&rendered, source, doc); err != nil {
		return Result{}, fmt.Errorf("rendering %s: %w", page.Path, err)
	}

	// And a plugin's HTML hook runs *before* the sanitiser, never after it, so
	// that a plugin's bytes are filtered by the same allow-list the DM's own
	// markdown is. An `AfterRender` after `Sanitise` would be a way for a plugin
	// to put unsanitised HTML on a page a player reads.
	r.runAfter(ctx, page, decision, &rendered)

	// The sanitiser runs last, on every page, for every author. It is not a
	// filter for untrusted input: a DM is the highest-value target in this
	// application, and "sanitise only what a player wrote" is how the DM's own
	// notes become the way in.
	clean := r.saniti.Sanitise(rendered.String())

	result := Result{HTML: clean, TOC: toc}
	if stripped > 0 {
		// The count travels with the result so a caller can log it and a test
		// can assert that a page with a secret in it was treated as one. The
		// number is not in the HTML: how many secrets a page has is not
		// something a player may know.
		result.stripped = stripped
	}

	return result, nil
}
