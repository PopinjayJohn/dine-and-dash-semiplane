package render

import (
	"strings"

	"github.com/yuin/goldmark/ast"
)

// The table of contents is the headings in document order, with the same anchor
// each heading's HTML carries.
//
// It is built from the tree rather than from the HTML, and that is the whole
// point of the Anchor field: a table of contents and a link to a heading are
// built from the same id by the same code, so they cannot drift. A table of
// contents assembled by parsing the rendered HTML would be right until the
// renderer changed, and then wrong quietly for every page in the campaign.

// buildTOC collects the headings from a parsed document. source is the markdown
// the document was parsed from, because a heading's text lives in the source
// and the tree only says where to find it.
func buildTOC(doc ast.Node, source []byte) []Heading {
	toc := []Heading{}

	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		heading, isHeading := node.(*ast.Heading)
		if !isHeading {
			return ast.WalkContinue, nil
		}

		toc = append(toc, Heading{
			Level:  heading.Level,
			Text:   headingText(heading, source),
			Anchor: headingAnchor(heading),
		})
		return ast.WalkContinue, nil
	})

	return toc
}

// headingText is the text of a heading, with the markdown formatting of it
// removed.
//
// A table of contents that says `**Rivergate**` or `[the toll](rivergate)` is a
// table of contents nobody clicks, and the plain string is the one form the
// reader wants everywhere else on the page.
func headingText(heading *ast.Heading, source []byte) string {
	var text strings.Builder
	appendText(&text, heading, source)

	// A heading written as `# Rivergate   ` has the spaces in its text, and the
	// id it is given does not, so this trims them too.
	return strings.TrimSpace(text.String())
}

// appendText collects the readable text of a node and everything under it.
//
// It recurses rather than switching over the inline types, because a heading may
// contain emphasis, a code span, a link, an image or anything a plugin adds, and
// a list of the types that happen to exist today is a list that is wrong
// tomorrow. Two types are handled explicitly: text, and the string a plugin or a
// transformer put in the tree directly.
func appendText(text *strings.Builder, node ast.Node, source []byte) {
	switch typed := node.(type) {
	case *ast.Text:
		text.Write(typed.Segment.Value(source))
		return
	case *ast.String:
		text.Write(typed.Value)
		return
	}

	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		appendText(text, child, source)
	}
}

// headingAnchor is the id goldmark generated for a heading, which is what the
// renderer puts in the HTML.
//
// A heading with no id gets an empty anchor rather than a guessed one: a table
// of contents entry that links to nothing is visibly wrong, and a guessed id
// that is wrong is not.
func headingAnchor(heading *ast.Heading) string {
	value, present := heading.AttributeString("id")
	if !present {
		return ""
	}

	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case string:
		return typed
	default:
		return ""
	}
}
