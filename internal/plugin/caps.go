package plugin

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// The capabilities that are pure declarations: a name, and what it means. They
// are first because they are the two that need nothing from the rest of the
// application — a page type and a frontmatter key are both strings, and refusing
// one of them to collide with a core name is a check this package can make on its
// own with no ordering, no recovery and no I/O.
//
// The capabilities that reach into a render, a decision, a route, an index or a
// command are declared by the package that consumes them and registered through
// the registry, and each arrives in the commit that gives the consumer its shape.

// FieldType is a frontmatter key a plugin claims, and what its value is.
//
// It is a *claim*, not a definition, and this milestone stops there on purpose. This
// application has no field renderer, and the key is carried through the frontmatter
// parse tree untouched either way (ADR 0013): a key the vault does not own is an
// *unknown* key, and ADR 0013's promise is that an unknown key survives untouched,
// which is exactly what a DM writing `mood: grim` in Obsidian needs. So a claimed
// key is readable today and writable by nobody — a plugin cannot set it through the
// editor, and a DM can set it with their own editor, which is the arrangement a
// vault already assumes.
//
// What the claim buys is a name that two plugins cannot both want, and a place to
// say what a value in that key means so that a later field renderer has something to
// read.
//
// The claim is refused for the keys core already reads, and the reason is not
// politeness. `visibility:` and `character:` decide what a player may read and who
// owns a page; a plugin that redefined either would be changing the access-control
// model by writing frontmatter, which is the one thing §12's "composition, not
// override" is about and the one thing a claim list is the right place to forbid.
type FieldType struct {
	// Name is the frontmatter key, lower case and hyphenated like the core keys
	// it sits beside.
	Name vault.Key

	// Kind is what a value in the key is: "text", "number", "list", "ref", or
	// whatever a plugin's own field renderer wants to call its own. It is a free
	// string because ADR 0010 fixes page types as data and this is the same
	// argument applied to field kinds — a plugin is not required to be in the
	// binary before a page may use it.
	Kind string

	// Summary is one line, for a `wiki help` and for the authoring guide. It is
	// not optional because a claim nobody can read the meaning of is a claim
	// nobody will make correctly.
	Summary string
}

// coreFields are the frontmatter keys core reads and gives a meaning to.
//
// The list is [vault.CoreKeys] rather than a copy of it. A copy would be a second
// answer to "which keys are core's", and it would be wrong the first time somebody
// added a key to the vault without adding it here — which is the moment a plugin
// would be allowed to redefine a key the core had just started reading.
var coreFields = vault.CoreKeys()

// CoreFields returns the frontmatter keys core owns, in the order a page's
// frontmatter lists them.
//
// It is exported because the authoring guide and `wiki help` both need to print the
// list, and a guide that has to be kept in step with a private slice is a guide that
// is out of step.
func CoreFields() []vault.Key {
	return slices.Clone(coreFields)
}

// AddPageType claims a page type name for the plugin currently running Setup.
//
// A type is refused when it is one core owns, and refused *loudly* rather than
// ignored: `domain.PageType.IsCore` already exists to tell the two apart, and a
// plugin that claimed `character` would make that function answer about a page
// type whose character-ownership rule does not apply to it. That is not a
// cosmetic collision — it is a page whose owner is derived by a rule meant for a
// different kind of page.
func (r *Registry) AddPageType(name domain.PageType, summary string) error {
	if r == nil {
		return errors.New("plugin: adding a page type to a nil registry")
	}
	if strings.TrimSpace(string(name)) == "" {
		return errors.New("plugin: a page type needs a name")
	}
	if name.IsCore() {
		return fmt.Errorf("%w: page type %q is a core type", ErrReservedName, name)
	}
	if _, taken := r.pages[name]; taken {
		return fmt.Errorf("%w: page type %q", ErrDuplicateName, name)
	}
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("plugin: page type %q needs a summary", name)
	}

	r.pages[name] = summary
	return nil
}

// AddFieldType claims a frontmatter key for the plugin currently running Setup.
//
// The reservation check is the one that matters, and it is the reason this
// function exists in this shape rather than as a field on a plugin struct: a
// collision between two plugins and a collision between a plugin and the core are
// the same accident with different consequences, and both have to be found before
// startup rather than by whichever page a DM happens to write first.
func (r *Registry) AddFieldType(field FieldType) error {
	if r == nil {
		return errors.New("plugin: adding a field type to a nil registry")
	}
	if strings.TrimSpace(string(field.Name)) == "" {
		return errors.New("plugin: a field type needs a name")
	}
	if isCoreField(field.Name) {
		return fmt.Errorf("%w: frontmatter key %q is a core key", ErrReservedName, field.Name)
	}
	if strings.TrimSpace(field.Kind) == "" {
		return fmt.Errorf("plugin: frontmatter key %q needs a kind", field.Name)
	}
	if strings.TrimSpace(field.Summary) == "" {
		return fmt.Errorf("plugin: frontmatter key %q needs a summary", field.Name)
	}

	key := strings.ToLower(string(field.Name))
	field.Name = vault.Key(key)

	if _, taken := r.fields[field.Name]; taken {
		return fmt.Errorf("%w: frontmatter key %q", ErrDuplicateName, field.Name)
	}

	r.fields[field.Name] = field
	return nil
}

// PageTypes returns the page types the registered plugins contribute, keyed by the
// name a page's frontmatter would use, with each one's summary.
//
// A nil registry contributes nothing and returns an empty map, so a caller can
// range over the answer without asking whether there were plugins. The map is a
// copy: it is built during Add and read afterwards by a sync and a handler, and a
// map handed out by reference is a map two goroutines are one write away from a
// race.
func (r *Registry) PageTypes() map[domain.PageType]string {
	if r == nil {
		return map[domain.PageType]string{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	copied := make(map[domain.PageType]string, len(r.pages))
	for name, summary := range r.pages {
		copied[name] = summary
	}
	return copied
}

// FieldTypes returns the frontmatter keys the registered plugins claim, keyed by
// the key itself.
func (r *Registry) FieldTypes() map[vault.Key]FieldType {
	if r == nil {
		return map[vault.Key]FieldType{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	copied := make(map[vault.Key]FieldType, len(r.fields))
	for name, field := range r.fields {
		copied[name] = field
	}
	return copied
}

// isCoreField reports whether a key is one core reads.
func isCoreField(key vault.Key) bool {
	return slices.Contains(coreFields, key)
}
