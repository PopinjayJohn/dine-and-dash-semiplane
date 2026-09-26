package domain

// LinkKind distinguishes a link from an embed. Both put a target path into
// the graph; only an embed also inlines the target's content, so the renderer
// treats them differently and backlinks may want to as well.
type LinkKind string

// The two kinds, matching the CHECK constraint on page_links.kind.
const (
	LinkKindLink  LinkKind = "link"
	LinkKindEmbed LinkKind = "embed"
)

// ParseLinkKind returns the kind for a stored value, or an error naming the
// values that would have been accepted. Rows are written by this codebase, so
// an unknown kind means the schema and the code have drifted apart.
func ParseLinkKind(s string) (LinkKind, error) {
	switch kind := LinkKind(s); kind {
	case LinkKindLink, LinkKindEmbed:
		return kind, nil
	default:
		return "", oneOf("link kind", s, string(LinkKindLink), string(LinkKindEmbed))
	}
}

// Valid reports whether k is one of the two kinds the schema allows.
func (k LinkKind) Valid() bool {
	return k == LinkKindLink || k == LinkKindEmbed
}

// String returns the kind as it is stored.
func (k LinkKind) String() string {
	return string(k)
}

// PageLink is one edge of the link graph: a source page, the target it names,
// and — once the target exists — the target's page id.
//
// DstPath is always known and DstPageID often is not. An Obsidian vault is
// full of links to pages that do not exist yet, and the DM writes them on
// purpose; the graph has to hold them, or every page would have to be
// re-resolved from scratch each time a single file is saved.
type PageLink struct {
	SrcPageID string

	// DstPath is the target as the source page wrote it, which is not
	// necessarily a path that exists.
	DstPath string

	// DstPageID is empty while the target is unresolved.
	DstPageID string

	Kind LinkKind
}

// Validate reports whether l is a link that may be persisted. An unresolved
// target is valid: that is the interesting case, not a mistake.
func (l PageLink) Validate() error {
	switch {
	case l.SrcPageID == "":
		return required("link source page ID")
	case l.DstPath == "":
		return required("link destination path")
	}

	if !l.Kind.Valid() {
		return oneOf("link kind", l.Kind.String(), string(LinkKindLink), string(LinkKindEmbed))
	}
	return nil
}

// Resolved reports whether the target of this link exists in the index.
func (l PageLink) Resolved() bool {
	return l.DstPageID != ""
}
