package render

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// SecretText is a body's unrevealed secret text, and how many secrets it came out
// of. It exists for the search index that holds secrets (ADR 0009) and for
// nothing else.
//
// The reason it is here and not in the indexer is that the `[!SECRET]` grammar
// has to be read the same way twice. The renderer strips a secret off the tree
// and the indexer has to decide which side of the split it goes on; two
// implementations of the same syntax is two answers waiting for a DM to write the
// callout that separates them. So the tree is the only place the syntax is
// parsed, and this is the second thing that reads it.
//
// It is *unrevealed* text. A `[!SECRET]{.revealed}` block is one the DM chose to
// show, which makes it public body rather than secret text, and putting it here
// would mean the private index holds a word a player could already read. That
// half of the split is the redaction milestone's, because knowing what a
// *principal* may see is an access-control question rather than a parsing one.
//
// The rules, in the order they apply:
//
//   - Every unrevealed `[!SECRET]` callout's text is included.
//   - A revealed one is not, and the walk carries on *into* it, because a
//     revealed block can contain a secret that is not revealed.
//   - A blockquote the parser could not read but which has a secret's shape is
//     included, for the same fail-closed reason the stripper removes it.
//   - A secret inside a secret is counted once. The outer one already carries the
//     inner one's text, and indexing it twice would make a page that happened to
//     nest them rank differently from one that did not.
func SecretText(body string) (string, int) {
	source := []byte(body)

	doc := parseWithPipeline(body)

	collector := &secretCollector{source: source, lastEnd: -1}
	// walkChildren reports an error so that its mutating caller can use one
	// signature for both uses; the visitors here return a nil error always, so
	// there is nothing to report and nothing to do about it if there were.
	_ = walkChildren(doc, collector.visit)

	return strings.TrimSpace(collector.text.String()), collector.count
}

// secretCollector gathers the text of the secrets a page has.
type secretCollector struct {
	source []byte
	text   strings.Builder
	count  int

	// lastEnd is where in the source the segment just written ended, and -1
	// before the first one. gapBefore needs it to see a line break the inline
	// parser left as a hole in the tree rather than as a node.
	lastEnd int
}

// visit decides what to do with one node, and whether to go into it.
func (c *secretCollector) visit(node ast.Node) (bool, error) {
	switch typed := node.(type) {
	case *Callout:
		if !isSecretCallout(typed) {
			return true, nil
		}
		if calloutIsRevealed(typed) {
			// The block is public, so its text belongs in the public half — but
			// there may be a secret inside it, so the walk continues.
			return true, nil
		}
		c.collect(typed)
		// The text is in hand; descending would count a nested secret twice and
		// put its words in the index a second time.
		return false, nil

	case *ast.Blockquote:
		// A blockquote with a secret's shape that the parser could not read. The
		// only safe reading of `[!SECRET` with the bracket unclosed is that it is
		// one, and a secret that is not indexed is a secret the DM cannot find.
		if isMalformedSecret(typed) {
			c.collect(typed)
			return false, nil
		}
	}

	return true, nil
}

// collect adds one secret's text to the collection.
//
// The boundary is the first thing written, because a secret's first text segment
// carries no leading space of its own — the newline in front of it belongs to
// the block structure, which was walked past to get here — and two secrets
// written one after the other would otherwise run together into one token at the
// join.
func (c *secretCollector) collect(secret ast.Node) {
	c.count++
	c.boundary()
	c.writeSubtree(secret)
}

// writeSubtree adds every piece of text under a node.
//
// Segments are written as the source had them, because that spacing is the only
// record of where the boundaries were. A word split across two inline nodes
// arrives as two segments with the space in the first one; adding a separator of
// one's own would widen every space in the secret, and a phrase whose spaces are
// wrong is a phrase that does not match.
//
// Four node kinds hold text, and between them they hold all of it:
//
//   - `*ast.Text` for a paragraph's words, and for a code span's contents.
//   - `*ast.String` for an autolink's URL and an image's title.
//   - `*ast.FencedCodeBlock` and `*ast.CodeBlock`, which need their own case
//     because goldmark keeps a code block's lines *off* the child nodes: they
//     live in the block's own Lines(), which only the renderer reads. A walk
//     that looks for String children finds nothing there, and a secret whose
//     credential is in a code block would index as an empty string.
//   - a line break the inline parser did not turn into a node, found by gapBefore
//     from the source offsets. That is the one a DM's soft-wrapped prose hits:
//     a secret that is one paragraph over two source lines arrives as Text, Text
//     with three bytes between them and nothing to say why.
//
// Raw HTML is not read, which is deliberate -- a DM who wrote `<b>Ilithya</b>`
// inside a secret has still written the word `Ilithya`, and a raw HTML node's
// tags are not text a DM is going to search for.
func (c *secretCollector) writeSubtree(node ast.Node) {
	c.writeChildren(node)
}

func (c *secretCollector) writeChildren(node ast.Node) {
	first := true

	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		switch typed := child.(type) {
		case *ast.Text:
			c.writeSegment(typed.Segment)
		case *ast.String:
			c.write(typed.Value)
		case *ast.FencedCodeBlock:
			c.writeLines(typed.Lines())
		case *ast.CodeBlock:
			c.writeLines(typed.Lines())
		default:
			if !first {
				c.boundary()
			}
			c.writeChildren(child)
		}
		first = false
	}
}

// writeLines adds a code block's lines.
func (c *secretCollector) writeLines(lines *text.Segments) {
	if lines == nil {
		return
	}
	for i := range lines.Len() {
		c.writeSegment(lines.At(i))
	}
}

// writeSegment adds one segment of the source, after checking whether the source
// had a line break in the gap before it.
func (c *secretCollector) writeSegment(segment text.Segment) {
	c.gapBefore(segment.Start)
	c.write(segment.Value(c.source))
	c.lastEnd = segment.Stop
}

// gapBefore writes a boundary where the source had a line break between the last
// segment and this one and the tree has no node to say so.
//
// Reading it out of the source rather than out of the tree is what makes it
// robust: a DM soft-wraps their notes, so every wrapped line in every secret
// arrives as two Text nodes with nothing between them, and without this a
// two-line secret is indexed as one run-on line whose phrases match nothing.
func (c *secretCollector) gapBefore(start int) {
	if c.lastEnd < 0 || start <= c.lastEnd || c.lastEnd > len(c.source) || start > len(c.source) {
		return
	}
	if !bytes.ContainsRune(c.source[c.lastEnd:start], '\n') {
		return
	}
	c.boundary()
}

// boundary writes the break between two pieces of a secret, and does not double
// one that a segment already ended with.
func (c *secretCollector) boundary() {
	if c.text.Len() == 0 {
		return
	}
	if strings.HasSuffix(c.text.String(), " ") {
		return
	}
	c.text.WriteByte(' ')
}

// write appends one piece of text, exactly as the source had it.
func (c *secretCollector) write(segment []byte) {
	if len(segment) == 0 {
		return
	}
	c.text.Write(segment)
}
