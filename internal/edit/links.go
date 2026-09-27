package edit

import (
	"bytes"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// # Rewriting the links that point at a page
//
// A rename moves a page, and §5 says the links that pointed at it move with it:
// "changing the path is an explicit, DM-only action that rewrites inbound links
// atomically". Without that, every `[[locations/rivergate]]` in the campaign
// becomes an unresolved link the moment somebody renames a page, and a DM's
// campaign is a graph of links rather than a folder of files.
//
// # Why this is a scanner and not a walk of the parse tree
//
// The obvious implementation is to parse the source, walk the tree and rewrite
// each link node's target. It is not possible here, and the reason is in the
// renderer's own parser: **a `WikiLink` carries no source segment.** goldmark's
// inline nodes cannot hold one — `BaseInline.Lines` panics — and the wiki-link
// parser consumes the construct and keeps only the attributes. So the tree knows
// what a page contains and not *where in the page* anything was written. An AST
// rewrite would therefore have to re-render the page, and re-rendering a DM's
// markdown is the one thing this project must never do to a file: a renderer
// change, a whitespace difference, a lost line break, and a vault with a
// thousand diffs in it that say nothing about the rename.
//
// So: goldmark is used for what it is reliable about — *where* the code blocks
// are — and the substitutions are made on the bytes by offset. Every byte the
// rewriter does not touch is the byte the DM wrote.
//
// # What it rewrites
//
//   - `[[from]]`, `[[from|alias]]`, `[[from#heading]]` and the embed forms: the
//     target part only, so an alias and a heading survive.
//   - `[text](from)`, and the image form: the destination only.
//
// And what it does *not* rewrite, because rewriting it would be wrong:
//
//   - a link inside a fenced or indented code block, which is prose about a
//     link rather than a link;
//   - a link whose target merely *ends with* the old path, or is a URL, or is a
//     different page with a similar name. The target is compared whole.
//
// The comparison is on the *normalised* target — trimmed, with a leading `./` and
// a trailing slash removed — because `[[rivergate]]`, `[[./rivergate]]` and
// `[[/locations/rivergate]]` are three spellings of one link and only the first
// of them is what a path comparison would match.

// linkRewrite is one substitution: the byte range to replace and what to put
// there. The ranges are absolute offsets into the source, and a rename's whole
// output is produced by applying them back to front, so no two of them can
// interact.
type linkRewrite struct {
	start, stop int
	with        string
}

// rewriteLinks returns `source` with every link to `from` pointing at `to`.
//
// It is a pure function of the bytes: the same source and the same pair give the
// same output on every run, which is what lets a rename be tested by comparing
// files rather than by comparing a report.
func rewriteLinks(source []byte, from, to string) []byte {
	code := codeRegions(source)

	rewrites := wikiLinkRewrites(source, code, from, to)
	rewrites = append(rewrites, markdownLinkRewrites(source, code, from, to)...)

	if len(rewrites) == 0 {
		return source
	}

	// Sorted by position and de-overlapped, then applied back to front so an
	// earlier rewrite's offsets are still valid. Two substitutions can only
	// overlap if the two scanners found the same bytes, and the skip is the safe
	// answer rather than an error: one of them is already rewriting that range to
	// the same path.
	sort.Slice(rewrites, func(i, j int) bool { return rewrites[i].start < rewrites[j].start })
	kept := rewrites[:0]
	for _, r := range rewrites {
		if len(kept) > 0 && r.start < kept[len(kept)-1].stop {
			continue
		}
		kept = append(kept, r)
	}
	rewrites = kept

	out := make([]byte, len(source))
	copy(out, source)
	for i := len(rewrites) - 1; i >= 0; i-- {
		r := rewrites[i]
		var next bytes.Buffer
		next.Write(out[:r.start])
		next.WriteString(r.with)
		next.Write(out[r.stop:])
		out = next.Bytes()
	}
	return out
}

// codeRegions is the byte ranges that are code, and therefore prose about links
// rather than links.
//
// It is goldmark's parse tree used for one thing it is reliable about. A fenced
// block and an indented one both report their lines, and a link inside one of
// them is text a DM wrote to talk about the syntax.
func codeRegions(source []byte) []span {
	doc := goldmark.New().Parser().Parse(text.NewReader(source))
	parser.WithContext(parser.NewContext())

	var spans []span
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node.Kind() {
		case ast.KindFencedCodeBlock, ast.KindCodeBlock, ast.KindRawHTML:
			for i := range node.Lines().Len() {
				segment := node.Lines().At(i)
				spans = append(spans, span{start: segment.Start, stop: segment.Stop})
			}
		}
		return ast.WalkContinue, nil
	})
	return spans
}

// span is a byte range in a page's source.
type span struct{ start, stop int }

func (s span) contains(offset int) bool { return offset >= s.start && offset < s.stop }

// inAny reports whether an offset is inside one of the ranges.
func inAny(spans []span, offset int) bool {
	for _, s := range spans {
		if s.contains(offset) {
			return true
		}
	}
	return false
}

// wikiLinkRewrites finds `[[from...]]` and `![[from...]]`.
//
// The construct is scanned rather than parsed, which means the scanner has to be
// as careful as the parser about the things the parser is careful about: an
// unclosed `[[` is not a link, a link with an empty target is not a link, and a
// link that runs to the end of the line is a link the DM is in the middle of
// typing.
func wikiLinkRewrites(source []byte, code []span, from, to string) []linkRewrite {
	var rewrites []linkRewrite

	for i := 0; i+1 < len(source); i++ {
		if source[i] != '[' || source[i+1] != '[' || inAny(code, i) {
			continue
		}
		// The `!` of an embed is left where it is: the substitution replaces the
		// *target*, which is inside the brackets either way, so the construct's
		// own punctuation is never touched.
		closing := bytes.Index(source[i+2:], []byte("]]"))
		if closing < 0 {
			break // an unclosed construct: nothing after it is a link either
		}
		innerStart, innerStop := i+2, i+2+closing

		inner := string(source[innerStart:innerStop])
		target, _, _ := cutWikiTarget(inner)
		if sameTarget(target, from) {
			rewrites = append(rewrites, linkRewrite{
				start: innerStart,
				stop:  innerStart + len(target),
				with:  to,
			})
		}
		i = innerStop
	}
	return rewrites
}

// cutWikiTarget splits a wiki link's inside into the target and the rest.
//
// The rest is an alias after `|` and a heading after `#`, in either order, and
// both survive a rename: `[[from|the bridge]]` becomes `[[to|the bridge]]` and the
// heading is a fragment on a page that did not move.
func cutWikiTarget(inner string) (target, alias, heading string) {
	head, rest, _ := strings.Cut(inner, "|")
	target, heading, _ = strings.Cut(head, "#")
	return target, rest, heading
}

// sameTarget is whether two link targets name the same page.
//
// The normalisation is the one a reader would apply: surrounding space is not
// part of the name, a leading `./` is noise, and a trailing slash is the same
// page spelled by somebody whose fingers slipped.
func sameTarget(written, path string) bool {
	return normaliseTarget(written) == normaliseTarget(path)
}

func normaliseTarget(target string) string {
	trimmed := strings.TrimSpace(target)
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.TrimPrefix(trimmed, "/")
	return strings.TrimSuffix(trimmed, "/")
}

// markdownLinkRewrites finds `[text](from)` and `![alt](from)`.
//
// The destination is whatever is between the parentheses, trimmed, and it is
// compared as a page path: a URL is not a page, and a path with a fragment on it
// keeps its fragment.
func markdownLinkRewrites(source []byte, code []span, from, to string) []linkRewrite {
	var rewrites []linkRewrite

	for i := 0; i < len(source); i++ {
		if inAny(code, i) {
			continue
		}
		if source[i] != '(' {
			continue
		}

		closing := bytes.IndexByte(source[i+1:], ')')
		if closing < 0 {
			break
		}
		destinationStart, destinationStop := i+1, i+1+closing

		// A destination may be `<bracketed>` and may carry a title after the
		// space, and neither is a page path this rewriter touches.
		destination := string(source[destinationStart:destinationStop])
		if strings.ContainsAny(destination, " \t<>(") {
			continue
		}

		path, fragment, hasFragment := strings.Cut(destination, "#")
		if !sameTarget(path, from) {
			continue
		}

		// The `(` is not part of what is replaced, so a bracket-only link keeps its
		// brackets and an angle-bracketed one is left alone entirely.
		with := to
		if hasFragment {
			with = to + "#" + fragment
		}
		rewrites = append(rewrites, linkRewrite{
			start: destinationStart,
			stop:  destinationStop,
			with:  with,
		})
		i = destinationStop
	}
	return rewrites
}
