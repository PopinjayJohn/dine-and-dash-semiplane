package render

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Callouts are Obsidian's blockquote variant: `> [!type] Title`, with `-` or
// `+` on the marker to say whether the body starts folded, and the `{.revealed}`
// attribute form this project adds on top (ADR 0007).
//
//	> [!warning] The winter
//	> The winter is not discussed further here.
//
// `[!SECRET]` is one of the types, and it is the reason this file exists rather
// than a general-purpose blockquote renderer: a secret callout's body is removed
// from the tree before anything writes HTML, so the text is never rendered and
// then removed. It is never rendered.

// Callout is one callout: its type, its title, whether it starts folded, and
// whatever is inside it.
type Callout struct {
	ast.BaseBlock
}

var kindCallout = ast.NewNodeKind("Callout")

// Kind is the node kind, and Dump is here because ast.Node requires them.
func (n *Callout) Kind() ast.NodeKind {
	return kindCallout
}

// Dump writes the node and its children to standard output, for debugging.
func (n *Callout) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Callout": "Callout"}, nil)
}

// The attributes a callout carries.
const (
	// AttrCalloutType is the type after `[!`, lower-cased.
	AttrCalloutType = "data-callout-type"

	// AttrCalloutTitle is the title after the type on the first line.
	AttrCalloutTitle = "data-callout-title"

	// AttrCalloutFoldable is "-" for open, "+" for closed, and empty when the
	// callout has no fold state.
	AttrCalloutFoldable = "data-callout-foldable"

	// AttrCalloutRevealed is true for the `{.revealed}` attribute form, which
	// is how a DM marks a secret they have chosen to show.
	AttrCalloutRevealed = "data-callout-revealed"
)

// CalloutSecret is the type whose body is a secret.
//
// It is a constant and not a string in the middle of a function because three
// places have to agree on it -- the parser, the stripper and the replayer -- and
// a typo in any of them is a leaked secret.
const CalloutSecret = "secret"

// calloutOpen is the callout types this application knows how to draw, besides
// `secret`.
//
// A type outside this list still renders as a callout, with the type as a class,
// because a DM may have types this build has never heard of and a callout that
// renders as a plain blockquote is a worse answer than a callout with a class
// nothing styles. What the list is for is the *documented* set ADR 0005 commits
// to, which is a promise about what a DM can rely on.
var knownCalloutTypes = map[string]bool{
	// secret is this project's own type (ADR 0007), and it is drawn by the
	// stripper's own renderer rather than this one, so it is here only to stop
	// it being reported as a type this build has never heard of.
	CalloutSecret: true,

	"note": true, "abstract": true, "summary": true, "tldr": true,
	"info": true, "todo": true, "tip": true, "hint": true, "important": true,
	"success": true, "check": true, "done": true,
	"question": true, "help": true, "faq": true,
	"warning": true, "caution": true, "attention": true,
	"failure": true, "fail": true, "missing": true,
	"danger": true, "error": true,
	"bug": true, "example": true, "quote": true, "cite": true,
}

// calloutParser turns a blockquote whose first line starts with `[!type]` into
// a callout.
type calloutParser struct{}

// Trigger is `>`, which is also what blockquotes use.
func (calloutParser) Trigger() []byte {
	return []byte{'>'}
}

// Callouts is the extension that teaches the pipeline about callouts.
type Callouts struct {
	parser calloutParser
}

// NewCallouts returns the callout extension.
func NewCallouts() *Callouts {
	return &Callouts{}
}

// Extend registers the block parser and the node renderer.
func (e *Callouts) Extend(md goldmark.Markdown) {
	md.Parser().AddOptions(e)
	md.Renderer().AddOptions(renderer.WithNodeRenderers(
		util.Prioritized(&calloutRenderer{}, wikiLinkRenderPriority),
	))
}

// SetParserOption registers the block parser.
//
// The priority is *below* goldmark's blockquote parser at 700, because goldmark
// sorts parsers ascending and asks them in that order: whoever is asked first
// gets the `>`.
func (e *Callouts) SetParserOption(config *parser.Config) {
	parser.WithBlockParsers(util.Prioritized(e.parser, calloutPriority)).SetParserOption(config)
}

// SetRendererOption does nothing: the renderer is registered in Extend, because
// the renderer config is not an Option.
func (e *Callouts) SetRendererOption(*renderer.Config) {}

// calloutPriority is below goldmark's blockquote parser at 700.
const calloutPriority = 650

// Open opens a callout, or hands the `>` back.
//
// There are three answers. A well-formed callout gives a Callout and its
// children. A blockquote whose first line has the *shape* of a callout and not
// the syntax -- `[!SECRET` with the bracket unclosed -- gives a Blockquote marked
// as a malformed secret, because the stripper has to be able to find it. Anything
// else gives nothing at all, and nothing consumed, so goldmark's own blockquote
// parser opens the quote: an ordinary quote stays an ordinary quote.
//
// The line is looked at before any of it is consumed. A parser that consumes and
// then decides cannot decide to decline, and a block parser that opens a block
// without advancing the reader is a loop.
func (p calloutParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	prefix, rest, quoted := quotePrefix(reader)
	if !quoted {
		return nil, parser.NoChildren
	}

	if header, isHeader := parseCalloutHeader(rest); isHeader {
		consumeQuoteMarker(reader, prefix)
		// The rest of the line is the header, not the body, so it is consumed
		// too. What is left of the line is the newline.
		reader.Advance(len(rest) - 1)
		return newCallout(header), parser.HasChildren
	}

	if looksLikeMalformedSecret(rest) {
		consumeQuoteMarker(reader, prefix)
		quote := ast.NewBlockquote()
		quote.SetAttributeString(AttrMalformedCallout, true)
		return quote, parser.HasChildren
	}

	return nil, parser.NoChildren
}

// Continue asks whether the next line belongs to this block, which is
// goldmark's question for a blockquote and the same one for a callout: a line
// that starts with `>`, or a blank line inside the block.
func (p calloutParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	prefix, _, quoted := quotePrefix(reader)
	if !quoted {
		return parser.Close
	}
	consumeQuoteMarker(reader, prefix)
	return parser.Continue | parser.HasChildren
}

// Close ends the callout.
func (p calloutParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {}

// quotePrefix reports how many bytes of the current line are the quote marker and
// its padding, and what is left of the line. It consumes nothing.
//
// The rule is goldmark's: an indented `>` is a code block, and an unindented one
// starts a quote. A space or a tab after the marker is padding, and belongs to
// the prefix rather than to the content.
func quotePrefix(reader text.Reader) (prefix int, rest []byte, quoted bool) {
	line, _ := reader.PeekLine()
	if line == nil {
		return 0, nil, false
	}

	width, offset := util.IndentWidth(line, reader.LineOffset())
	if width > 3 || offset >= len(line) || line[offset] != '>' {
		return 0, nil, false
	}

	prefix = offset + 1
	rest = line[prefix:]
	if len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t') {
		prefix++
		rest = rest[1:]
	}

	return prefix, rest, true
}

// consumeQuoteMarker consumes the marker and its padding, which is what
// quotePrefix measured.
//
// A tab is padding whose width depends on the column, and goldmark's own
// blockquote parser sets it rather than counting bytes, so this does too: a
// nested block inside a callout that kept the wrong column would be a list
// where a DM wrote a paragraph.
func consumeQuoteMarker(reader text.Reader, prefix int) {
	_, _ = reader.PeekLine()

	// The byte before the content is the padding, if the prefix had any.
	line, _ := reader.PeekLine()
	if padding := paddingWidth(line, prefix); padding > 0 {
		reader.AdvanceAndSetPadding(padding, util.TabWidth(reader.LineOffset())-padding)
		return
	}

	reader.Advance(prefix)
}

// paddingWidth is how many bytes of padding follow the `>`, or 0.
func paddingWidth(line []byte, prefix int) int {
	offset := prefix - 2 // the marker and the one byte before the padding
	if offset < 0 || offset >= len(line) {
		return 0
	}
	switch line[offset] {
	case ' ':
		return 1
	case '\t':
		return 1
	default:
		return 0
	}
}

// CanInterruptParagraph says a callout cannot start in the middle of a
// paragraph, which is right: a callout is a block, and a `>` in the middle of a
// sentence is a character a DM typed.
func (p calloutParser) CanInterruptParagraph() bool {
	return true
}

// CanAcceptIndentedLine says an indented line cannot continue a callout, so a
// code block after one is a code block.
func (p calloutParser) CanAcceptIndentedLine() bool {
	return false
}

// calloutHeader is the metadata on a callout's first line.
type calloutHeader struct {
	kind     string
	title    string
	foldable string
	revealed bool
}

// newCallout builds the node a header describes.
func newCallout(header calloutHeader) *Callout {
	callout := &Callout{}
	callout.SetAttributeString(AttrCalloutType, header.kind)
	callout.SetAttributeString(AttrCalloutTitle, header.title)
	callout.SetAttributeString(AttrCalloutFoldable, header.foldable)
	callout.SetAttributeString(AttrCalloutRevealed, header.revealed)
	return callout
}

// AttrMalformedCallout marks a blockquote whose first line has the shape of a
// secret callout and not the syntax. The stripper removes it, because the only
// safe reading of a `[!SECRET` nobody can parse is that it is one.
//
// It is marked as a malformed *callout* rather than as a malformed secret
// because that is what it is: a callout the parser could not build. Which callout
// it was is the stripper's business.
const AttrMalformedCallout = "data-malformed"

// looksLikeMalformedSecret reports whether a line is a secret callout written
// without its closing bracket.
//
// This is the fail-closed rule in the parser, and it is deliberately paranoid.
// A DM who meant a secret and mistyped the bracket gets their secret hidden
// rather than shown, because ADR 0007 asks for exactly that trade: a mistyped
// secret is a missing secret, and a secret that appears is a disclosure nobody
// can undo.
//
// Only the *first* line of the block is examined. A `[!SECRET]` quoted inside a
// paragraph is prose about a callout, and is not touched.
func looksLikeMalformedSecret(line []byte) bool {
	return strings.HasPrefix(strings.ToLower(string(line)), "[!secret")
}

// parseCalloutHeader reads `[!type] Title` off the front of a line, and reports
// whether there is one.
//
// Everything about a callout is on its first line: the type, the fold marker,
// the title, and the `{.revealed}` attribute. A line without one of those is
// prose, and saying so is what lets an ordinary quote stay an ordinary quote.
func parseCalloutHeader(line []byte) (calloutHeader, bool) {
	rest := string(line)
	if !strings.HasPrefix(rest, "[!") {
		return calloutHeader{}, false
	}

	marker, after, closed := strings.Cut(rest, "]")
	if !closed {
		// An unterminated `[!warning` is not a callout, and is not a secret
		// either: parseCalloutHeader says so, and the caller looks at the shape
		// of the line instead.
		return calloutHeader{}, false
	}

	body := strings.TrimSpace(after)

	// The attribute form comes first: `[!SECRET]{.revealed} Title`, and a title
	// beginning with `{.` is an attribute rather than a title. The attribute is
	// cut with its closing brace, because that is what the check expects to see.
	var revealed bool
	if strings.HasPrefix(body, "{") {
		if attribute, rest, isClosed := strings.Cut(body, "}"); isClosed && isRevealedAttribute(attribute+"}") {
			revealed = true
			body = strings.TrimSpace(rest)
		}
	}

	var foldable string
	if strings.HasPrefix(body, "-") || strings.HasPrefix(body, "+") {
		foldable = body[:1]
		body = strings.TrimSpace(body[1:])
	}

	kind := strings.ToLower(strings.TrimSpace(marker[2:]))
	if kind == "" {
		return calloutHeader{}, false
	}

	return calloutHeader{kind: kind, title: body, foldable: foldable, revealed: revealed}, true
}

// isRevealedAttribute reports whether an attribute list is the revealed one.
//
// An attribute list is a set of `.field` names between braces, so `{.revealed}`
// is one field named revealed and the leading dot is syntax rather than a field.
// The whole list has to be the revealed form and nothing else:
// `{.revealed .spoiler}` is not it, and treating it as the revealed form would
// show a secret because somebody wrote a second attribute beside it.
func isRevealedAttribute(attribute string) bool {
	trimmed := strings.TrimSpace(attribute)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return false
	}

	fields := strings.Split(trimmed[1:len(trimmed)-1], ".")
	found := false

	for _, field := range fields {
		switch name := strings.TrimSpace(field); name {
		case "":
			// The dot before the first name, or a doubled one.
		case "revealed":
			found = true
		default:
			return false
		}
	}

	return found
}

// calloutType reads a callout's type, which is what decides whether it is a
// secret.
func calloutType(node ast.Node) string {
	return strings.ToLower(attributeString(node, AttrCalloutType))
}

// calloutTitle reads a callout's title.
func calloutTitle(node ast.Node) string {
	return attributeString(node, AttrCalloutTitle)
}

// calloutIsRevealed reports whether the DM marked the callout revealed.
func calloutIsRevealed(node ast.Node) bool {
	value, present := node.AttributeString(AttrCalloutRevealed)
	if !present {
		return false
	}
	revealed, isBool := value.(bool)
	return isBool && revealed
}
