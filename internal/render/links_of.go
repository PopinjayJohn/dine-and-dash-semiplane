package render

import (
	"fmt"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// The link graph and the rendered links must agree, and the only way to be sure
// of that is for both to come from the same walk of the same tree.
//
// This is that walk. It says what a page links to, as the DM wrote it, and it
// resolves nothing: resolution is the index's business, and a resolver here
// would be a resolver that cannot be tested against a store.
//
// The pipeline it parses with is the same one Render uses, extensions and all.
// Two configurations would parse the same bytes into different trees, and the
// graph and the page would then disagree in a way nobody would notice.

// LinkRef is one outgoing link a page has, exactly as the DM wrote it.
type LinkRef struct {
	// Target is the path or name, without a `#heading` fragment.
	Target string

	// Heading is the fragment, when the DM wrote one.
	Heading string

	// Alias is the text after a `|`, when the DM wrote one.
	Alias string

	// Embed is true for the `![[...]]` form and for an image, both of which pull
	// the target's content in rather than linking to it.
	Embed bool
}

// pipeline is the shared goldmark configuration. It is built once because
// building a pipeline is not free, and it is the same one New() uses.
var pipeline = New()

// LinksOf returns the links a body has, in the order they appear.
//
// Wiki links and markdown links to a page path both count, because a DM writes
// both and a wiki that resolved one and not the other has a link that works in
// one editor and not in the other. A link to a URL does not count: it is not a
// page in this vault, and a row in the link graph pointing at it would point at
// nothing.
func LinksOf(body string) ([]LinkRef, error) {
	source := []byte(body)

	doc := pipeline.md.Parser().Parse(text.NewReader(source))

	return linksFromTree(doc)
}

// linksFromTree collects the links of an already-parsed document.
func linksFromTree(doc ast.Node) ([]LinkRef, error) {
	collector := &linkCollector{seen: map[string]bool{}}

	err := ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch typed := node.(type) {
		case *WikiLink:
			collector.add(LinkRef{
				Target:  attributeString(typed, AttrWikiTarget),
				Heading: attributeString(typed, AttrWikiHeading),
				Alias:   attributeString(typed, AttrWikiAlias),
				Embed:   isEmbed(typed),
			})
		case *ast.Link:
			if path, heading, isPagePath := pageDestination(typed.Destination); isPagePath {
				collector.add(LinkRef{Target: path, Heading: heading})
			}
		case *ast.Image:
			if path, heading, isPagePath := pageDestination(typed.Destination); isPagePath {
				collector.add(LinkRef{Target: path, Heading: heading, Embed: true})
			}
		}

		return ast.WalkContinue, nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the links: %w", err)
	}

	return collector.refs, nil
}

// linkCollector accumulates the links of one document.
type linkCollector struct {
	refs []LinkRef

	// seen keeps the collector from recording one destination twice. A page that
	// links to a page in a sentence and again in a footnote is one edge, and the
	// link graph's primary key is (source, destination). An embed and a link to
	// the same page are two edges, which is why the embed marks the key.
	seen map[string]bool
}

func (c *linkCollector) add(ref LinkRef) {
	if ref.Target == "" {
		return
	}

	key := ref.Target
	if ref.Embed {
		key = "!" + key
	}
	if c.seen[key] {
		return
	}
	c.seen[key] = true

	c.refs = append(c.refs, ref)
}
