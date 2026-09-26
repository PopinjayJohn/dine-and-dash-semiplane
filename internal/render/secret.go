package render

import "github.com/yuin/goldmark/ast"

// The secret stripper is the whole reason this milestone exists, and everything
// about it is arranged so that the wrong answer is a missing feature rather than
// a disclosure.
//
// The rules, in the order they apply:
//
//   - A `[!SECRET]` callout's body is removed from the tree, and the callout
//     becomes a StrippedSecret placeholder. The body is never rendered and then
//     removed; it is replaced before the renderer walks it.
//   - A callout marked `[!SECRET]{.revealed}` is shown, because the DM chose
//     to show it (ADR 0007).
//   - A callout the parser could not make sense of, in a place where a secret
//     would be, is treated as a secret. This is the fail-closed half: an
//     unparseable `[!SECRET` in a blockquote is a blockquote, and
//     `> [!secret` with the bracket unclosed is not a secret we can see, so the
//     only safe reading of the region is to drop it.
//   - Nothing else in this file can decide anything. A Decision that permits
//     secrets changes the first rule and nothing else, and the zero Decision
//     permits none.

// StrippedSecret is what a secret callout becomes when the decision does not
// permit it.
//
// It is a node rather than a deletion so that the shape of the page survives:
// a player can see that a secret is there, and the page's other paragraphs are
// not renumbered by a secret disappearing. It holds no reference to the body it
// replaced, because a node that could be unwound is a node that will be.
type StrippedSecret struct {
	ast.BaseBlock
}

// Kind is the node kind.
func (n *StrippedSecret) Kind() ast.NodeKind {
	return kindStrippedSecret
}

// Dump writes the node for debugging. There is nothing to dump: the node holds
// no content, and that is the point.
func (n *StrippedSecret) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"StrippedSecret": "StrippedSecret"}, nil)
}

// Revealed reports whether the callout was marked `{.revealed}`.
//
// A revealed secret is *not* stripped, so this is only ever true on a node the
// stripper replaced, which happens when a revealed secret is inside a region
// that was stripped whole. It stays as an attribute so that M9's "reveal" can
// find it and M7 can decide.
func (n *StrippedSecret) Revealed() bool {
	value, present := n.AttributeString(AttrCalloutRevealed)
	if !present {
		return false
	}
	revealed, isBool := value.(bool)
	return isBool && revealed
}

var kindStrippedSecret = ast.NewNodeKind("StrippedSecret")

// secretStripper walks a parsed page and removes what the decision does not
// permit.
//
// It is a walk rather than a text pass, and that is the whole design. A text
// pass -- a regexp over the source looking for `[!SECRET]` and counting braces
// -- gets a callout that is nested in a list, quoted inside a callout, or written
// with a fold marker wrong. A tree walk visits every callout there is, including
// the ones inside blockquotes inside list items, because they are all nodes.
type secretStripper struct {
	decision Decision

	// stripped counts what it removed, so a caller can log it and a test can
	// assert that a page with a secret was treated as one.
	stripped int
}

// strip removes the secrets a decision does not permit, and returns how many it
// removed.
//
// It has its own walk rather than using ast.Walk, because this pass *mutates*
// the tree it is walking. goldmark's walk advances with `child.NextSibling()`
// after the visit returns, and a child this pass has just been spliced out of
// its parent has no next sibling left to offer -- so the walk ends at the first
// secret on the page. The symptom is the worst kind: the stripper reports six
// removals and the HTML still has five secrets in it.
func (s *secretStripper) strip(doc ast.Node) int {
	_ = walkChildren(doc, func(node ast.Node) (bool, error) {
		switch typed := node.(type) {
		case *Callout:
			if !s.permits(typed) {
				s.replace(typed)
				// The node is out of the tree and its children are
				// unreachable. Descending into them would count a nested
				// secret twice and say something untrue about the page.
				return false, nil
			}
		case *ast.Blockquote:
			// A blockquote the parser marked as secret-shaped and could not
			// parse. The only safe reading of `[!SECRET` with the bracket
			// unclosed is that it is a secret, so it goes -- but only for
			// somebody who may not read secrets. A DM sees it, because the DM
			// wrote it and is the one who can fix it, and a rule that hid the
			// DM's own words from the DM would be a bug in the other direction.
			//
			// There is no revealed form here: a block that could not be parsed
			// cannot carry the attribute that would have said so.
			if isMalformedSecret(typed) && !s.decision.CanSeeSecrets {
				s.replace(typed)
				return false, nil
			}
		}

		return true, nil
	})

	return s.stripped
}

// walkChildren visits every node under a node, and recurses into the ones the
// visitor asks it to.
//
// The children are collected before any of them is visited, so a visitor that
// replaces a child does not change what the loop is about to look at.
func walkChildren(node ast.Node, visit func(ast.Node) (bool, error)) error {
	children := []ast.Node{}
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		children = append(children, child)
	}

	for _, child := range children {
		descend, err := visit(child)
		if err != nil {
			return err
		}
		if !descend {
			continue
		}
		if err := walkChildren(child, visit); err != nil {
			return err
		}
	}

	return nil
}

// permits reports whether this callout's content may be rendered.
//
// Three ways to be permitted, and no fourth:
//
//   - the decision says so,
//   - the callout is not a secret,
//   - the callout is a secret the DM marked revealed.
func (s *secretStripper) permits(node ast.Node) bool {
	if !isSecretCallout(node) {
		return true
	}
	if s.decision.CanSeeSecrets {
		return true
	}
	return calloutIsRevealed(node)
}

// isMalformedSecret reports whether the parser gave this blockquote back
// because its first line looked like a secret callout and was not one.
func isMalformedSecret(node ast.Node) bool {
	value, present := node.AttributeString(AttrMalformedCallout)
	if !present {
		return false
	}
	marked, isBool := value.(bool)
	return isBool && marked
}

// replace swaps a node for a StrippedSecret and then unlinks everything the
// secret held.
//
// The order is not a detail. goldmark's ReplaceChild finds the node by asking it
// who its parent is, so clearing the parent first makes the replacement a
// silent no-op: the stripper counts a secret, returns a page, and the secret is
// still in it.
//
// Detaching afterwards matters as much as replacing. The old children are still
// in memory, and a node that keeps a reference to a secret is a node whose
// subtree some later pass could render.
func (s *secretStripper) replace(node ast.Node) {
	parent := node.Parent()
	if parent == nil {
		// A callout with no parent is not in the tree, so there is nothing to
		// remove. It cannot be a leak either: the renderer never sees it.
		return
	}

	stripped := &StrippedSecret{}
	stripped.SetAttributeString(AttrCalloutRevealed, calloutIsRevealed(node))
	stripped.SetAttributeString(AttrCalloutType, CalloutSecret)

	parent.ReplaceChild(parent, node, stripped)
	unlink(node)
	s.stripped++
}

// unlink takes a node out of the tree: its parent, and every child, recursively.
//
// The recursion is the point. A secret nested in a list item is nested in a
// paragraph nested in a list item, and leaving any of those linked to the
// stripped node is leaving a path to the text.
func unlink(node ast.Node) {
	for child := node.FirstChild(); child != nil; {
		next := child.NextSibling()
		unlink(child)
		child.SetParent(nil)
		child.SetPreviousSibling(nil)
		child.SetNextSibling(nil)
		child = next
	}

	node.SetParent(nil)
}

// isSecretCallout reports whether a node is a `[!SECRET]` callout.
func isSecretCallout(node ast.Node) bool {
	callout, isCallout := node.(*Callout)
	if !isCallout {
		return false
	}
	return calloutType(callout) == CalloutSecret
}
