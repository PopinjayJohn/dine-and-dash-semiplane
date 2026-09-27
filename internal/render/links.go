package render

import (
	"context"
	"strings"

	"github.com/yuin/goldmark/ast"
)

// Link resolution is a separate concern from rendering, and the renderer asks
// about it rather than doing it: the index knows which paths and aliases exist,
// the renderer knows how to draw a link, and neither of them should be reaching
// into the other.
//
// The order is Obsidian's, because a DM's links are Obsidian's links: an exact
// path, then an alias, then a case-insensitive filename. A DM who has written
// `[[rivergate]]` for six sessions and then renames the page to
// `Rivergate-after-the-flood` should not be told their link broke.

// LinkResolver answers what a wiki link points at.
//
// The index implements it in M4. A nil resolver is legal and resolves nothing,
// which is what a page rendered with no index should do: every link is
// unresolved, and nothing panics.
type LinkResolver interface {
	// Resolve returns the page a target names, and whether it was found.
	//
	// target is what the DM wrote: a path without extension, an alias, or a
	// filename. heading is the `#heading` part, which does not affect whether
	// the page exists.
	//
	// The error is for an index that could not answer -- a database failure, a
	// closed store -- and is deliberately not folded into the boolean. A wiki
	// full of unresolved links because the database was briefly busy is a bug
	// report about links that do not exist, which is a much worse thing to be
	// handed than a 500.
	Resolve(ctx context.Context, target, heading string) (Link, bool, error)
}

// Link is a resolved wiki link target.
type Link struct {
	// Path is the page the target names, campaign-relative and without
	// extension. It is what an href has to become.
	Path string

	// Title is the page's title, which is what an unresolved link shows instead
	// of a path, because "Rivergate" reads better than
	// "locations/rivergate" in a sentence.
	Title string
}

// hrefFor is the URL a resolved link points at.
func (l Link) hrefFor() string {
	return pageURL + l.Path
}

// pageURL is the prefix every page link carries, so one change moves every link
// in the application. It is a constant rather than configuration because a link
// in a rendered page has to be the same link the DM would get from the
// application, and a configurable prefix is one more thing to get wrong in
// production.
const pageURL = "/c/"

// resolveLinks walks the tree and rewrites every wiki link the resolver knows,
// and marks the ones it does not.
//
// It is a walk rather than a parse-time lookup because a link may be to a page
// that the index has not seen yet -- M4 is building the index from these very
// files -- and because a markdown link to a page is resolved the same way.
func resolveLinks(ctx context.Context, doc ast.Node, links LinkResolver) error {
	if links == nil {
		return nil
	}

	return ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}

		switch typed := node.(type) {
		case *WikiLink:
			resolved, ok, err := lookup(ctx, links, linkTarget(typed))
			if err != nil {
				return ast.WalkStop, err
			}
			applyResolution(typed, resolved, ok)
		case *ast.Link:
			// A markdown link whose destination is a page path is a wiki link
			// as far as this application is concerned, because a DM writes
			// both and the index knows about both. A link to a URL is not, and
			// asking the resolver about one is how a campaign page called
			// "https" would come to be.
			target, ok, err := rewritePageLink(ctx, links, typed.Destination)
			if err != nil {
				return ast.WalkStop, err
			}
			if ok {
				typed.Destination = []byte(target)
			}
		case *ast.Image:
			target, ok, err := rewritePageLink(ctx, links, typed.Destination)
			if err != nil {
				return ast.WalkStop, err
			}
			if ok {
				typed.Destination = []byte(target)
			}
		}

		return ast.WalkContinue, nil
	})
}

// rewritePageLink resolves a markdown link or image whose destination names a
// page in this vault, and reports the destination it should have.
func rewritePageLink(ctx context.Context, links LinkResolver, current []byte) (string, bool, error) {
	path, heading, isPagePath := pageDestination(current)
	if !isPagePath {
		return "", false, nil
	}

	resolved, ok, err := lookup(ctx, links, destination{path: path, heading: heading})
	if err != nil || !ok {
		return "", false, err
	}
	return resolved.hrefFor(), true, nil
}

// destination is a target and its heading, which is what the resolver takes.
type destination struct {
	path    string
	heading string
}

// lookup asks the resolver about a link and returns what it found, treating a
// resolver that returns nothing for a target it claims to know as not found.
//
// A resolver that answers with an empty path is a bug in the resolver, and
// resolving a link to an empty href is a link to the current page, so the empty
// answer is refused here.
func lookup(ctx context.Context, links LinkResolver, target destination) (Link, bool, error) {
	if target.path == "" {
		return Link{}, false, nil
	}

	resolved, ok, err := links.Resolve(ctx, target.path, target.heading)
	if err != nil {
		return Link{}, false, err
	}
	// A resolver that answers with an empty path has not found a page, whatever
	// its boolean said, and turning that into an href would point at the current
	// page.
	if !ok || resolved.Path == "" {
		return Link{}, false, nil
	}
	return resolved, true, nil
}

// applyResolution rewrites a wiki link's href and classes, or leaves it
// unresolved.
// applyResolution rewrites a wiki link's href and drops the unresolved class.
//
// The target attribute is deliberately *not* rewritten. It is what the DM wrote,
// it is what the link's text is, and a link that changed its own label the
// moment the index learned something new is a link a DM cannot search for.
func applyResolution(node *WikiLink, resolved Link, ok bool) {
	if !ok {
		return
	}

	node.SetAttributeString("class", wikiLinkClasses(isEmbed(node), true))
	node.SetAttributeString("href", resolved.hrefFor())
}

// linkTarget reads the target and heading a wiki link carries, which is what the
// resolver is asked about.
func linkTarget(node *WikiLink) destination {
	return destination{
		path:    attributeString(node, AttrWikiTarget),
		heading: attributeString(node, AttrWikiHeading),
	}
}

// isEmbed reads the embed attribute back.
func isEmbed(node ast.Node) bool {
	flag, ok := node.AttributeString(AttrWikiEmbed)
	if !ok {
		return false
	}
	embed, isBool := flag.(bool)
	return isBool && embed
}

func attributeString(node ast.Node, name string) string {
	value, present := node.AttributeString(name)
	if !present {
		return ""
	}
	text, isText := value.(string)
	if !isText {
		return ""
	}
	return text
}

// pageDestination decides whether a markdown link's destination is a page in this
// vault, and if so what path it names.
//
// The test is deliberately narrow: a destination is a page path when it is
// relative, has no scheme, no host and no `..`. A destination that looks
// absolute is a link to the internet and is left alone, and a destination with a
// `..` in it is refused rather than resolved, because the sanitiser would remove
// the link and the resolver should not be asked to make a page out of it.
func pageDestination(destination []byte) (path, heading string, isPagePath bool) {
	target := string(destination)
	if target == "" {
		return "", "", false
	}

	// A scheme or a protocol-relative URL is not a page. The colon test covers
	// `https:`, `mailto:` and `C:` without needing a URL parser.
	if strings.Contains(target, "://") || strings.Contains(target, "#") && !strings.Contains(target, "/") {
		return "", "", false
	}
	if idx := strings.Index(target, "#"); idx >= 0 {
		heading = target[idx+1:]
		target = target[:idx]
	}
	if strings.HasPrefix(target, "/") || strings.Contains(target, "..") {
		return "", "", false
	}
	if strings.Contains(target, ":") {
		return "", "", false
	}

	return target, heading, target != ""
}

// linkLabel is the text a link shows: the alias the DM gave it, or the target
// as they wrote it.
//
// Never the resolved page's title. Obsidian does not prettify a link's text
// either, and a link that says something other than what the DM typed is a link
// a DM cannot find in their own notes with a search.
func linkLabel(node *WikiLink) string {
	if alias := attributeString(node, AttrWikiAlias); alias != "" {
		return alias
	}
	return attributeString(node, AttrWikiTarget)
}
