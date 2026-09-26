package domain

import (
	"fmt"
	"time"
)

// DefaultSystem is the ruleset a campaign gets when nothing says otherwise.
// It is a name, not a code path: ADR 0010 keeps the core ruleset-agnostic and
// ships dnd5e as a plugin, so this is the id a plugin registers under.
const DefaultSystem = "dnd5e"

// Campaign is one DM's world: a set of pages, a set of people who may read
// them, and a vault directory.
//
// The slug is the campaign's permanent identity. The name is a label the DM
// can change whenever they like, and the pages inside are addressed by path,
// never by title, so nothing else in the system has to move when either does.
type Campaign struct {
	// ID is the primary key. A zero ID means "mint one", which the store
	// does; see store.Options.IDGen.
	ID string

	// Slug is the campaign's URL key and directory name, and it does not
	// change once set.
	Slug Slug

	Name string

	// System names the ruleset plugin that owns this campaign's extra page
	// types. Empty means DefaultSystem.
	System string

	// VaultDir is where this campaign's markdown lives, relative to the data
	// directory. The vault is the source of truth; this row says which part of
	// it belongs to this campaign.
	VaultDir string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate reports whether c is a campaign that may be persisted.
func (c Campaign) Validate() error {
	switch {
	case c.ID == "":
		return required("campaign ID")
	case c.Name == "":
		return required("campaign name")
	case c.VaultDir == "":
		return required("campaign vault directory")
	}

	if err := c.Slug.Validate(); err != nil {
		return fmt.Errorf("campaign slug %w", err)
	}
	return nil
}

// SystemOrDefault returns the campaign's ruleset, substituting the default
// for the empty string the column carries when the DM never chose one.
func (c Campaign) SystemOrDefault() string {
	if c.System == "" {
		return DefaultSystem
	}
	return c.System
}
