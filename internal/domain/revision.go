package domain

import (
	"fmt"
	"time"
)

// PageRevision is one historical state of a page: the whole file, including
// its frontmatter.
//
// Revisions are a projection, not the backup. The vault's own `_history`
// directory is where revisions live for edits made in Obsidian; this table
// exists so the application can list and restore the ones it made itself. A
// DM who edits in Obsidian and a DM who edits in the app therefore have two
// histories, and ADR 0001 says the files win.
type PageRevision struct {
	ID     string
	PageID string

	// Rev numbers the revisions of one page, starting at 1 and increasing by
	// one. A zero rev means "the next one": the store assigns it under the
	// write connection, which is what keeps the numbering correct when a DM
	// and a player save the same page at the same moment.
	Rev int

	// Markdown is the whole file, frontmatter fences included.
	Markdown    string
	ContentHash string

	// AuthorPrincipalID is the principal whose session made the change, and
	// is empty for one the sync engine found on disk: a file edited in
	// Obsidian has no author as far as the application is concerned.
	AuthorPrincipalID string

	// Message is the DM's or player's note about the change. Optional: an
	// autosave has nothing to say about itself.
	Message string

	CreatedAt time.Time
}

// Validate reports whether r is a revision that may be persisted.
func (r PageRevision) Validate() error {
	switch {
	case r.ID == "":
		return required("revision ID")
	case r.PageID == "":
		return required("revision page ID")
	case r.ContentHash == "":
		return required("revision content hash")
	}

	if r.Rev < 0 {
		return fmt.Errorf("revision number: %d, must not be negative", r.Rev)
	}
	return nil
}
