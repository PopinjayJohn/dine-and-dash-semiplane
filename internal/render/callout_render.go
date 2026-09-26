package render

import (
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// calloutRenderer draws a callout.
//
// A callout the stripper emptied is not drawn at all: the stripper replaces a
// secret's body with a placeholder node and marks it, and this renderer draws
// that placeholder as a small, obviously-empty box. There is no "draw it and
// hide it with CSS" path here, and there never will be one: ADR 0007 says the
// content is not in the response bytes, and a renderer that drew a secret and
// styled it away would be a CSS hiding path wearing a different hat.
type calloutRenderer struct{}

// RegisterFuncs registers the renderer for a callout, and for the placeholder a
// stripped one becomes.
//
// Both kinds go to the same renderer because the placeholder is a callout as far
// as this package is concerned: it is the same thing with nothing inside it.
func (r *calloutRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindCallout, r.render)
	reg.Register(kindStrippedSecret, r.render)
}

func (r *calloutRenderer) render(writer util.BufWriter, source []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		if stripped, isStripped := node.(*StrippedSecret); isStripped {
			return r.renderStripped(writer, stripped), nil
		}
		return r.renderOpen(writer, node), nil
	}

	_, _ = writer.WriteString("</div>\n")
	return ast.WalkContinue, nil
}

// renderOpen draws the opening tag and the title.
func (r *calloutRenderer) renderOpen(writer util.BufWriter, node ast.Node) ast.WalkStatus {
	calloutType := calloutType(node)

	classes := []string{"callout", "callout-" + calloutType}
	if !knownCalloutTypes[calloutType] {
		// A type this build has never heard of still renders as a callout,
		// with its type as a class. A DM's callout becoming a plain blockquote
		// is a worse answer than a callout that nothing has styled yet.
		classes = append(classes, "callout-unknown")
	}
	if calloutIsRevealed(node) {
		classes = append(classes, "revealed")
	}

	_, _ = writer.WriteString(`<div class="`)
	_, _ = writer.Write(util.EscapeHTML([]byte(strings.Join(classes, " "))))
	_, _ = writer.WriteString(`" data-callout-type="`)
	_, _ = writer.Write(util.EscapeHTML([]byte(calloutType)))
	_ = writer.WriteByte('"')

	if foldable := attributeString(node, AttrCalloutFoldable); foldable != "" {
		fold := "open"
		if foldable == "+" {
			fold = "closed"
		}
		_, _ = writer.WriteString(` data-callout-fold="` + fold + `"`)
	}

	_, _ = writer.WriteString(">\n")

	if title := calloutTitle(node); title != "" {
		_, _ = writer.WriteString(`<p class="callout-title">`)
		_, _ = writer.Write(util.EscapeHTML([]byte(title)))
		_, _ = writer.WriteString("</p>\n")
	}

	return ast.WalkContinue
}

// renderStripped draws what a secret callout looks like to somebody who may not
// read it: the shape of the thing, and nothing inside it.
//
// The class is the point. A player can see that the DM wrote a secret here and
// can ask about it after the session; what they cannot do is see what it says,
// and the bytes of what it says are not in the response at all.
func (r *calloutRenderer) renderStripped(writer util.BufWriter, stripped *StrippedSecret) ast.WalkStatus {
	_, _ = writer.WriteString(`<div class="callout callout-secret stripped" data-callout-type="secret"`)
	if stripped.Revealed() {
		_, _ = writer.WriteString(` data-callout-revealed="true"`)
	}
	_, _ = writer.WriteString(">\n<p class=\"callout-stripped\">A secret is hidden here.</p>\n")

	return ast.WalkContinue
}

var _ = strconv.Itoa
