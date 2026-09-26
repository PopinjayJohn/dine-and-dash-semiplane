package domain

import (
	"fmt"
	"slices"
	"time"
)

// PageType is the kind of a page: a string, not a Go type, because ADR 0010
// fixes page types as data. A plugin adds one by registering a name, so there
// is no closed set here and no way to validate a page type against one.
type PageType string

// The page types core owns. A ruleset plugin adds its own — spell, creature,
// feat, magic-item for dnd5e — and a campaign may use both kinds in the same
// vault.
const (
	PageTypeNote       PageType = "note"
	PageTypeLocation   PageType = "location"
	PageTypeNPC        PageType = "npc"
	PageTypeQuest      PageType = "quest"
	PageTypeItem       PageType = "item"
	PageTypeFaction    PageType = "faction"
	PageTypeSessionLog PageType = "session-log"
	PageTypeCharacter  PageType = "character"
)

// corePageTypes is the set behind IsCore, in the order the spec lists them.
var corePageTypes = []PageType{
	PageTypeNote,
	PageTypeLocation,
	PageTypeNPC,
	PageTypeQuest,
	PageTypeItem,
	PageTypeFaction,
	PageTypeSessionLog,
	PageTypeCharacter,
}

// IsCore reports whether t is one of the eight types core owns, as opposed to
// one contributed by a plugin. The distinction matters where core has to act
// on its own types: a character page can be character-owned, a spell page
// never is.
func (t PageType) IsCore() bool {
	return slices.Contains(corePageTypes, t)
}

// String returns the type as it appears in frontmatter and in a directory
// name.
func (t PageType) String() string {
	return string(t)
}

// Page is one markdown file, as the index sees it.
//
// The path is the identity. Retitling a page does not move it, and changing
// the path is a separate, explicit action that rewrites inbound links — see
// the rename work in M9. Everything else here is a projection of the file
// that may be thrown away and rebuilt by `wiki reindex --full`.
//
// A page carries the whole body, including any [!SECRET] block. Redaction is a
// render-time decision, not a storage decision: the DM's secret has to survive
// a reindex, or the vault on disk and the index would disagree about the
// content of a file. What a given principal may see of this body is decided
// by access.Decision, which does not exist yet.
type Page struct {
	// ID is the primary key. A zero ID means "mint one", which the store
	// does; see store.Options.IDGen.
	ID string

	CampaignID string

	// Path is the page's address within the campaign vault: forward slashes,
	// no leading slash, no file extension. Whether the path escapes the vault
	// is not decided here — internal/vault owns that rule, and a page that
	// arrives from anywhere but the vault sanitiser is not to be trusted.
	Path string

	Title string
	Type  PageType

	// Frontmatter is the canonical YAML block with the fences removed and
	// unknown keys preserved verbatim. It is text here on purpose: parsing it
	// is the vault's job, and a serialisation round trip that drops a key the
	// application does not understand is how a DM loses their own notes.
	Frontmatter string

	// Body is the markdown with the frontmatter removed.
	Body string

	// ContentHash is the SHA-256 of the whole file, frontmatter included. It
	// is what lets the sync engine answer "did this change?" without parsing,
	// and it is required rather than optional: a row with no hash cannot be
	// compared to a file, and a file that cannot be compared is silently
	// reindexed on every run.
	ContentHash string

	// RendererVersion is the renderer this page's cached HTML was built
	// against, per ADR 0009. Bumping the renderer bumps the number, and every
	// page whose stored value differs from the current one is re-rendered.
	RendererVersion int

	CreatedAt time.Time
	UpdatedAt time.Time

	// IsDeleted marks a page that has been archived. The row stays, because
	// revisions and inbound links reference it, and because an archived page
	// is recoverable. Every read path filters it out.
	IsDeleted bool
}

// Validate reports whether p is a page that may be persisted.
func (p Page) Validate() error {
	switch {
	case p.ID == "":
		return required("page ID")
	case p.CampaignID == "":
		return required("page campaign ID")
	case p.Path == "":
		return required("page path")
	case p.Title == "":
		return required("page title")
	case p.Type == "":
		return required("page type")
	case p.ContentHash == "":
		return required("page content hash")
	}

	if p.RendererVersion < 0 {
		return fmt.Errorf("page renderer version: %d, must not be negative", p.RendererVersion)
	}
	return nil
}
