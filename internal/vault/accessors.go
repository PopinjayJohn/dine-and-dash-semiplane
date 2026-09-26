package vault

import (
	"fmt"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The typed reads below are conveniences over Value, one per key the
// application owns. They exist so that a caller does not have to remember
// which key holds a list and which holds a word, and so that an unusable value
// is reported next to the key it is in.

// Value returns a key's value as the DM wrote it: a string for a single value,
// a []string for a list, and false when the key is not in the file.
//
// A key the file does not have is not an error. A file with no frontmatter at
// all, or a file from before the DM used tags, is a normal file.
func (d *Document) Value(key Key) (any, bool, error) {
	if !key.Valid() {
		return nil, false, fmt.Errorf("%w: %q is not a key the application owns", ErrKey, key)
	}
	return d.front.value(key)
}

// Title returns the frontmatter title, which is empty when the file has none.
//
// It is empty rather than a fallback to the file name, because the file name is
// a path and the path is the page's identity: a page with no title is titled
// after where it lives, and only the caller knows where that is. Obsidian does
// the same thing, with the same reason.
func (d *Document) Title() string {
	text, _, err := d.scalar(KeyTitle)
	if err != nil {
		return ""
	}
	return text
}

// Aliases returns the alternative names a page answers to, which is how
// Obsidian's own link resolution finds a page from a name the DM used once in
// a session log.
func (d *Document) Aliases() []string {
	return d.list(KeyAliases)
}

// Tags returns the page's tags. A file with no tags gets an empty slice rather
// than nil, so a tag cloud can range over it without a nil check.
func (d *Document) Tags() []string {
	return d.list(KeyTags)
}

// PageType returns the page's type, or the empty string when the file does not
// declare one. An unknown type is not an error: ADR 0010 makes page types
// data, and a plugin's type is as valid as core's.
func (d *Document) PageType() domain.PageType {
	text, _, err := d.scalar(KeyType)
	if err != nil {
		return ""
	}
	return domain.PageType(text)
}

// Character returns the slug of the character a page belongs to, which is how
// a page under `characters/aria/` and a page anywhere else carrying
// `character: aria` end up owned by the same character.
func (d *Document) Character() string {
	text, _, err := d.scalar(KeyCharacter)
	if err != nil {
		return ""
	}
	return text
}

// Visibility returns who may read the page.
//
// A file with no visibility key gets VisibilityPlayers, which is the column's
// default in migration 0002 and the value the spec's own example carries: a DM
// who has never thought about visibility has a campaign every player may read.
//
// A file with a visibility the application does not recognise is an error, not
// a default. "plyers" has to be a page that refuses to be indexed rather than
// a page that publishes itself, because the permissive reading of a typo in a
// visibility key is exactly how a `[!SECRET]` block reaches a player. M4 turns
// this error into a sync failure naming the file.
func (d *Document) Visibility() (domain.Visibility, error) {
	text, ok, err := d.scalar(KeyVisibility)
	if err != nil {
		return "", err
	}
	if !ok || text == "" {
		return domain.VisibilityPlayers, nil
	}

	visibility, err := domain.ParseVisibility(text)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrFrontmatter, err)
	}
	return visibility, nil
}

// CreatedAt returns the created timestamp the DM wrote, and false when the file
// does not have one. The date-only form is accepted, because a DM writing
// `created: 2026-02-14` means that day.
func (d *Document) CreatedAt() (time.Time, bool, error) {
	return d.timestamp(KeyCreated)
}

// UpdatedAt returns the updated timestamp the DM wrote, and false when the file
// does not have one.
func (d *Document) UpdatedAt() (time.Time, bool, error) {
	return d.timestamp(KeyUpdated)
}

func (d *Document) timestamp(key Key) (time.Time, bool, error) {
	text, ok, err := d.scalar(key)
	if err != nil || !ok || text == "" {
		return time.Time{}, ok, err
	}

	stamp, err := parseTime(text)
	if err != nil {
		return time.Time{}, true, fmt.Errorf("%w: %s: %w", ErrFrontmatter, key, err)
	}
	return stamp, true, nil
}

func (d *Document) scalar(key Key) (string, bool, error) {
	value, ok, err := d.front.value(key)
	if err != nil || !ok {
		return "", ok, err
	}

	text, isString := value.(string)
	if !isString {
		return "", true, fmt.Errorf("%w: %s holds a list, not one value", ErrFrontmatter, key)
	}
	return text, true, nil
}

func (d *Document) list(key Key) []string {
	value, ok, err := d.front.value(key)
	if err != nil || !ok {
		return []string{}
	}

	items, isList := value.([]string)
	if !isList {
		return []string{}
	}
	return items
}

// Set replaces the value of a key the application owns, and marks the document
// modified. Setting a key the file does not have appends it; setting one it has
// keeps its position, its comments and the style of everything around it.
//
// A file with no frontmatter block gets one. That is the app's business: a DM
// who writes prose and then renames the file in the app should get a title, and
// adding `title:` to the top of a file that had no frontmatter is the smallest
// possible surprise.
func (d *Document) Set(key Key, value any) error {
	if err := checkValue(key, value); err != nil {
		return err
	}

	if d.front == nil {
		d.front = &frontmatter{}
	}
	if err := d.front.set(key, normaliseValue(value)); err != nil {
		return err
	}

	d.modified = true
	return nil
}

// Remove deletes a key the application owns. Removing a key the file does not
// have is not an error, and does not mark the document modified: the caller
// wanted it gone and it is, so there is nothing to write.
func (d *Document) Remove(key Key) error {
	if !key.Valid() {
		return fmt.Errorf("%w: %q is not a key the application owns", ErrKey, key)
	}

	removed, err := d.front.remove(key)
	if err != nil {
		return err
	}
	if removed {
		d.modified = true
	}
	return nil
}

// normaliseValue turns the values a caller is likely to pass into what the YAML
// encoder should write: a defined type over string becomes the string it spells,
// so the file carries the word and not a Go type's name. Timestamps are left
// alone, because a timestamp key knows to write one in the file's own format.
func normaliseValue(value any) any {
	if text, ok := textValue(value); ok {
		return text
	}
	return value
}
