package render

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// PublicText is a page's text with its unrevealed secrets removed, and how many
// it removed.
//
// This is what `pages.body_public` holds and therefore what the public search
// index is fed from, and the whole of ADR 0009's split is decided here. The
// private index holds the text of the unrevealed secrets; this holds everything
// else, as plain text.
//
// # Plain text, not markdown
//
// `body_public` is never rendered — it is read by the tokenizer and by nothing
// else — so there is nothing to gain from preserving the markdown and a great deal
// to lose. A markdown version would need the byte range of every secret callout,
// which the tree does not carry, so it would need a second parser for the same
// grammar, and ADR 0014's reason for parsing the grammar once is that two
// implementations of it is two answers waiting for a DM to write the callout that
// separates them. Plain text comes out of the tree I already have.
//
// # Why a revealed secret stays
//
// A `[!SECRET]{.revealed}` block is visible to everyone who can read the page
// (§9), and the public search's rows are filtered by the read predicate — so
// every principal who can reach a hit in this text may read the page, and may
// therefore read the revealed block on it. Stripping it would be removing text from
// the index that the reader is entitled to see.
//
// That is also the reason prose is findable at all. Before M5 the public search
// path applied no predicate, so the only safe text for the public index was a
// title. Now every row is filtered by the audience scope, so the public index
// holds exactly "what a reader of this page may read", and a search hit on a
// `dm-only` page is only ever produced for the DM.
const PublicSecretMarker = "[…]"

// PublicText walks a page once and returns its text with the unrevealed secrets
// removed.
//
// The count is returned because a caller that is surprised by a page with no
// public text at all needs to know whether that is because the page is all secret
// or because the page is empty, and the two are different problems to report.
func PublicText(body string) (string, int) {
	source := []byte(body)
	doc := pipeline.md.Parser().Parse(text.NewReader(source))

	collector := &publicCollector{source: source, lastEnd: -1}
	_ = walkChildren(doc, collector.visit)

	return strings.TrimSpace(collector.text.String()), collector.secrets
}

// publicCollector gathers a page's text, leaving out the unrevealed secrets.
type publicCollector struct {
	source  []byte
	text    strings.Builder
	secrets int

	// lastEnd is where in the source the last written segment ended, and -1
	// before the first. gapBefore needs it to see a line break the inline parser
	// left as a hole rather than as a node; the same rule as SecretText's, and for
	// the same reason.
	lastEnd int
}

// visit writes one node and says whether the walk should go into it.
//
// It is a visitor and not a recursion of its own, so the traversal is the one
// `walkChildren` does and the callout rules are the ones the renderer applies to
// the same tree. An earlier version recursed from here *and* returned false, which
// meant it never descended: a revealed block wrapping an unrevealed one kept the
// inner secret, and a wiki link's label went missing, and both looked like
// "the text is shorter than it should be" rather than like a traversal bug.
func (c *publicCollector) visit(node ast.Node) (bool, error) {
	switch typed := node.(type) {
	case *Callout:
		if isSecretCallout(typed) && !calloutIsRevealed(typed) {
			c.markSecret()
			// The block is gone; descending would put its words in the index.
			return false, nil
		}
		// A revealed block, or one that is not a secret: the walk goes into it,
		// because a secret nested inside a revealed one is still a secret.

	case *ast.Blockquote:
		// A blockquote with a secret's shape that the parser could not read. It is
		// treated as a secret, for the same fail-closed reason the stripper removes
		// it: the only safe reading of `[!SECRET` with the bracket unclosed is that
		// it is one.
		if isMalformedSecret(typed) {
			c.markSecret()
			return false, nil
		}

	case *WikiLink:
		// A wiki link has no children: the label and the destination are both
		// attributes, and the renderer builds the anchor from them. So the
		// collector has to read them the same way, or a page's links vanish from
		// its index and a DM searching for the name of a place they linked gets
		// nothing.
		c.writeString(wikilinkText(typed))
		return false, nil

	case *ast.Text:
		c.writeSegment(typed.Segment)
		return false, nil

	case *ast.String:
		// An autolink's URL, an image's title, a footnote's reference.
		c.write(typed.Value)
		return false, nil

	case *ast.FencedCodeBlock:
		c.writeLines(typed.Lines())
		return false, nil

	case *ast.CodeBlock:
		c.writeLines(typed.Lines())
		return false, nil
	}

	// A block: its first line starts a new line, which is a token boundary.
	// Without this, two paragraphs in a page would run together and a phrase
	// spanning them would match nothing.
	if node.ChildCount() > 0 {
		c.boundary()
	}
	return true, nil
}

// markSecret leaves the marker where a secret was.
func (c *publicCollector) markSecret() {
	c.secrets++
	if c.text.Len() > 0 {
		c.boundary()
	}
	// The marker tokenises to nothing, so it costs no index space and no
	// relevance; and it is legible in the database, so somebody looking at a row
	// and wondering why a page's body has holes gets an answer.
	c.text.WriteString(PublicSecretMarker)
	// Whatever came after the secret in the source is after it here too, so the
	// gap is where the secret was rather than at the end of the page.
	c.lastEnd = -1
}

// writeLines adds a code block's lines, which goldmark keeps in the block rather
// than in its children.
func (c *publicCollector) writeLines(lines *text.Segments) {
	if lines == nil {
		return
	}
	for i := range lines.Len() {
		c.writeSegment(lines.At(i))
	}
}

func (c *publicCollector) writeSegment(segment text.Segment) {
	c.gapBefore(segment.Start)
	c.write(segment.Value(c.source))
	c.lastEnd = segment.Stop
}

func (c *publicCollector) gapBefore(start int) {
	if c.lastEnd < 0 || start <= c.lastEnd || c.lastEnd > len(c.source) || start > len(c.source) {
		return
	}
	if !strings.ContainsRune(string(c.source[c.lastEnd:start]), '\n') {
		return
	}
	c.boundary()
}

func (c *publicCollector) boundary() {
	if c.text.Len() == 0 {
		return
	}
	if strings.HasSuffix(c.text.String(), " ") {
		return
	}
	c.text.WriteByte(' ')
}

// writeString adds a piece of text that is not a source segment, so there is no
// offset to record for it.
func (c *publicCollector) writeString(s string) {
	c.write([]byte(s))
}

func (c *publicCollector) write(segment []byte) {
	if len(segment) == 0 {
		return
	}
	c.text.Write(segment)
}
