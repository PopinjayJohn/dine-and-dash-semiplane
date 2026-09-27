package render

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Wiki links: `[[target]]`, `[[target|alias]]`, `[[target#heading]]` and the
// embed form `![[target]]`.
//
// They are a DM's links, and they are not markdown links, so they are parsed as
// their own inline node rather than rewritten into markdown before goldmark sees
// them. The alternative -- a pre-pass over the source turning `[[x]]` into
// `[x](x)` -- loses the distinction between a wiki link and a markdown link,
// which is the distinction the resolver needs: a markdown link to a page is a
// page link too, and a link to https://example.invalid is not.

// WikiLink is one wiki link: the target the DM wrote, the alias they gave it,
// the heading they pointed at, and whether it is an embed.
//
// It is an ast.Link in the tree whatever the syntax was, because that is what
// the HTML renderer knows how to draw, and the parts resolution needs are
// attributes on it. A later walk rewrites the destination without having to know
// which syntax the link came from.
type WikiLink struct {
	ast.BaseInline
}

// Kind is the node kind, and Dump is here because ast.Node requires them. The
// tree is the parser's, so a dump is a debugging aid and nothing more.
func (n *WikiLink) Kind() ast.NodeKind {
	return kindWikiLink
}

// Dump writes the node and its children to standard output, for debugging.
func (n *WikiLink) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"WikiLink": "WikiLink"}, nil)
}

// wikilinkText is the text a wiki link shows, and therefore the text a reader
// searching for it would type.
//
// The alias when they gave one, and the target when they did not, because that is
// what the renderer puts between the anchor tags and so what a page *reads* as
// containing. It is one function so that the renderer and the search index agree
// on it: a page that displays "the collector" and indexes "the toll collector"
// is a page a DM cannot find by what they can see on it.
func wikilinkText(n *WikiLink) string {
	if alias, found := n.AttributeString(AttrWikiAlias); found {
		if text, isString := alias.(string); isString && text != "" {
			return text
		}
	}
	if target, found := n.AttributeString(AttrWikiTarget); found {
		if text, isString := target.(string); isString {
			return text
		}
	}
	return ""
}

// The attributes a wiki link carries, as the resolver and the tests read them.
const (
	// AttrWikiTarget is the path or name the DM wrote, before resolution.
	AttrWikiTarget = "data-wiki-target"

	// AttrWikiAlias is the alias they gave it, or empty.
	AttrWikiAlias = "data-wiki-alias"

	// AttrWikiHeading is the `#heading` part, or empty.
	AttrWikiHeading = "data-wiki-heading"

	// AttrWikiEmbed says whether the link was written `![[...]]`.
	AttrWikiEmbed = "data-wiki-embed"

	// AttrWikiTitle is the resolved page's title, which is what a link shows
	// when the DM gave it no alias.
	AttrWikiTitle = "data-wiki-title"
)

// The classes a wiki link carries.
const (
	// ClassWikiLink marks every wiki link, so the front end can style them and
	// a test can find them.
	ClassWikiLink = "wiki-link"

	// ClassUnresolved is on a link whose target the index does not know.
	//
	// A class and not an error, and not a 404: a DM writes links to pages they
	// intend to write, and a wiki that rendered those as broken links would be
	// unusable on the day it is most useful. The link is still readable and
	// still says where it was going.
	ClassUnresolved = "unresolved"

	// classEmbed is on the `![[...]]` form, which pulls the target's content in
	// rather than linking to it.
	classEmbed = "embed"
)

// kindWikiLink is the node kind of a wiki link, registered with the parser so
// that goldmark knows what the inline parser it holds produces.
var kindWikiLink = ast.NewNodeKind("WikiLink")

// WikiLinks is the extension that teaches the pipeline about wiki links.
//
// It is a goldmark extension rather than a bare parser because that is what
// goldmark's own extensions are, and a plugin in M5 that wants to contribute a
// link syntax will do it this way. The name is plural because it is a
// collection: a parser and a renderer.
type WikiLinks struct {
	parser wikiLinkParser
}

// NewWikiLinks returns the wiki-link extension.
func NewWikiLinks() *WikiLinks {
	return &WikiLinks{}
}

// SetParserOption registers the inline parser.
//
// The priority is above goldmark's own link parser, because a wiki link begins
// with the same character and whichever parser claims it first wins.
func (e *WikiLinks) SetParserOption(config *parser.Config) {
	parser.WithInlineParsers(util.Prioritized(e.parser, wikiLinkPriority)).SetParserOption(config)
}

// SetRendererOption is part of being an extension and does nothing here: the
// renderer is registered in Extend, because the renderer config is not an Option
// and pretending otherwise would be a lie in the method's name.
func (e *WikiLinks) SetRendererOption(*renderer.Config) {}

// wikiLinkPriority is *below* goldmark's link parser at 200, because goldmark
// sorts parsers ascending and asks them in that order: a lower number is asked
// first. A wiki link begins with the same character as a markdown link, so
// whoever is asked first wins the bracket.
const wikiLinkPriority = 150

// wikiLinkRenderPriority is the node renderer, which is consulted by node kind
// rather than by character, so it only has to be distinct.
const wikiLinkRenderPriority = 700

// Extend registers the parser and the renderer.
//
// The renderer is registered through its own config rather than through the
// extension, because goldmark's renderer config is not an Option: the parser
// and the renderer are two different shapes of the same idea and the library
// keeps them apart.
func (e *WikiLinks) Extend(md goldmark.Markdown) {
	md.Parser().AddOptions(e)
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&wikiLinkRenderer{}, wikiLinkRenderPriority),
	))
}

// wikiLinkParser parses `[[...]]` and `![[...]]`.
type wikiLinkParser struct{}

// Trigger is the character that starts a wiki link. `!` as well as `[`, because
// the embed form is `![[...]]` and the bang is part of the construct.
func (wikiLinkParser) Trigger() []byte {
	return []byte{'[', '!'}
}

// Parse reads one link, if there is one here.
func (p wikiLinkParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, segment := block.PeekLine()
	if len(line) == 0 {
		return nil
	}

	embed := line[0] == '!'
	if embed {
		// The bang is consumed here rather than pushed back, because the
		// brackets are the start of the construct either way, and a construct
		// that can begin two ways is one parser rather than two.
		line = line[1:]
		segment = segment.WithStart(segment.Start + 1)
	}

	// `[[` and nothing else: a single bracket is markdown's, and `[]` is an
	// empty link label.
	if len(line) < 2 || line[0] != '[' || line[1] != '[' {
		return nil
	}

	closing := bytes.Index(line[2:], closingBrackets)
	if closing < 0 {
		return nil
	}

	// The segment starts after the two opening brackets: the value is what is
	// between them, not the whole construct.
	innerSegment := segment.WithStart(segment.Start + 2)
	inner := block.Value(innerSegment.WithStop(innerSegment.Start + closing))
	// A wiki link has to name something. `[[]]` and `[[  ]]` are four
	// characters a DM typed, not a link, and rendering them as a link to nothing
	// would put an empty anchor in the page.
	if strings.TrimSpace(string(inner)) == "" {
		return nil
	}

	consumed := 2 + closing + len(closingBrackets)
	if embed {
		consumed++
	}

	// The node carries no source segment. goldmark's inline nodes cannot hold
	// one -- BaseInline.Lines panics -- and the renderer draws the label from
	// the attributes, so the only thing the text was needed for is the
	// attributes themselves.
	node := &WikiLink{}
	attachLinkAttributes(node, string(inner), embed)

	block.Advance(consumed)

	return node
}

var closingBrackets = []byte("]]")

// attachLinkAttributes records what the DM wrote, in the parts a wiki link has: a
// target, an optional alias after `|`, and an optional heading after `#`.
//
// The order matters and is Obsidian's: the alias is split off first, so a target
// that contains a `#` keeps its heading with it rather than losing it to the
// alias split.
func attachLinkAttributes(node *WikiLink, inner string, embed bool) {
	target, alias := inner, ""
	if before, after, found := strings.Cut(inner, "|"); found {
		target, alias = before, after
	}

	path, heading, _ := strings.Cut(target, "#")

	node.SetAttributeString(AttrWikiTarget, strings.TrimSpace(path))
	node.SetAttributeString(AttrWikiAlias, strings.TrimSpace(alias))
	node.SetAttributeString(AttrWikiHeading, strings.TrimSpace(heading))
	node.SetAttributeString(AttrWikiEmbed, embed)

	// The attributes the HTML renderer draws from, so goldmark's own link
	// rendering does the drawing and this package decides what the link says.
	//
	// The href starts empty on purpose. A link to a page this index does not
	// know has nowhere to go, and an href to a path that 404s is worse than a
	// link a reader can see is unresolved.
	node.SetAttributeString("class", wikiLinkClasses(embed, false))
	node.SetAttributeString("href", "")
}

// wikiLinkClasses is the class list a wiki link carries.
func wikiLinkClasses(embed, resolved bool) string {
	classes := []string{ClassWikiLink}
	if embed {
		classes = append(classes, classEmbed)
	}
	if !resolved {
		classes = append(classes, ClassUnresolved)
	}
	return strings.Join(classes, " ")
}

// wikiLinkRenderer draws a wiki link.
//
// It is goldmark's own `<a>` with two differences: the label is assembled from
// the alias, the resolved title or the target as it was written, and a link with
// no destination is not a link at all. A wiki link to a page this index has not
// seen has nowhere to go, and an `<a href="">` points at the current page, so
// an unresolved one is a span that says where it was going and is visibly not a
// link.
type wikiLinkRenderer struct{}

// RegisterFuncs registers the renderer for a wiki link.
func (r *wikiLinkRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindWikiLink, r.render)
}

func (r *wikiLinkRenderer) render(writer util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}

	link, isLink := node.(*WikiLink)
	if !isLink {
		return ast.WalkContinue, nil
	}

	href := attributeString(link, "href")

	// A link the index does not know is a span and not an <a>. An anchor
	// without an href is not a link -- it is not focusable, not clickable, and
	// invalid HTML -- and rendering one anyway would be saying "this goes
	// somewhere" in the markup while the class says it does not. The span is
	// visibly the thing it is: a piece of text that was a link to a page the DM
	// has not written yet.
	element := "a"
	if href == "" {
		element = "span"
	}

	_, _ = writer.WriteString("<")
	_, _ = writer.WriteString(element)
	if href != "" {
		_, _ = writer.WriteString(` href="`)
		_, _ = writer.Write(util.EscapeHTML([]byte(href)))
		_ = writer.WriteByte('"')
	}
	_, _ = writer.WriteString(` class="`)
	_, _ = writer.Write(util.EscapeHTML([]byte(attributeString(link, "class"))))
	_ = writer.WriteByte('"')
	_ = writer.WriteByte('>')

	_, _ = writer.Write(util.EscapeHTML([]byte(linkLabel(link))))

	_, _ = writer.WriteString("</")
	_, _ = writer.WriteString(element)
	_, _ = writer.WriteString(">\n")

	return ast.WalkContinue, nil
}
