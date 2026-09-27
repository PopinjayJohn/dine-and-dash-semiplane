package plugin

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yuin/goldmark"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// The capabilities that are declarations — a name, and what it means — and the two
// that reach into a render and an index.
//
// They are in one file because they share the one thing that makes them safe, which
// is that **none of the registration functions below takes a lock.** [Registry.Add]
// holds the write lock for the whole of a plugin's `Setup`, so a registration that
// took the read lock to ask "is this name taken?" would deadlock, exactly as
// [Registry.Owner] would. They are therefore lock-free by construction, and that is
// only sound because a registry is *built* by one goroutine before anything reads
// it — which is the same rule `Add` documents and the same one the mutex exists to
// make a concurrent read wait rather than observe half of a build.
//
// The accessors at the bottom of the file do take the read lock, because those are
// the ones a handler calls while other goroutines are rendering pages.

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
	// Name is the frontmatter key, lower case and hyphenated like the core keys it
	// sits beside.
	Name vault.Key

	// Kind is what a value in the key is: "text", "number", "list", "ref", or
	// whatever a plugin's own field renderer wants to call its own. It is a free
	// string because ADR 0010 fixes page types as data and this is the same
	// argument applied to field kinds — a plugin is not required to be in the
	// binary before a page may use it.
	Kind string

	// Summary is one line, for a `wiki help` and for the authoring guide. It is not
	// optional because a claim nobody can read the meaning of is a claim nobody will
	// make correctly.
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
// plugin that claimed `character` would make that function answer about a page type
// whose character-ownership rule does not apply to it. That is not a cosmetic
// collision — it is a page whose owner is derived by a rule meant for a different
// kind of page.
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

// AddFieldType claims a frontmatter key for the plugin currently running Setup, and
// hands the core the renderer that will draw it.
//
// **A claim and a renderer are one argument in M12 and were two in M11**, and the
// change is the milestone. M11 let a plugin claim a key and stop there, because
// nothing rendered fields; the doc comment said a claimed key was "readable today
// and writable by nobody", and "readable" turned out to mean *readable and invisible*.
// A claim with no renderer is a key that renders as nothing, which is a plugin that
// half works in a way no test can see. So the renderer is required, and a plugin that
// wants a claim it does not draw does not have one to register.
//
// The reservation check is unchanged and it is the reason this function exists in
// this shape rather than as a field on a plugin struct: a collision between two
// plugins and a collision between a plugin and the core are the same accident with
// different consequences, and both have to be found before startup rather than by
// whichever page a DM happens to write first.
func (r *Registry) AddFieldType(field FieldType, renderer render.FieldRenderer) error {
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
	if isNil(renderer) {
		// A claim with no renderer is a key that renders as nothing, and refusing it
		// at startup is the difference between a DM finding out from a log line and
		// a DM finding out from an empty space on a page.
		return fmt.Errorf("plugin: frontmatter key %q needs a renderer", field.Name)
	}

	// Lower-cased here rather than left to the caller, because `AC:` and `ac:` being
	// two keys would be a page whose frontmatter says both — and a key the vault
	// does not own is a key the vault has never compared against anything.
	field.Name = vault.Key(strings.ToLower(string(field.Name)))

	if _, taken := r.fields[field.Name]; taken {
		return fmt.Errorf("%w: frontmatter key %q", ErrDuplicateName, field.Name)
	}

	r.fields[field.Name] = field
	r.specs[string(field.Name)] = render.FieldSpec{Kind: field.Kind, Renderer: renderer}

	return nil
}

// AddSearchField contributes values to the public search index.
//
// It is the one capability here that reaches into a table, and two things come with
// it for free: the values land in the *public* index only, so they go through the
// same redaction a body does, and they go into one `extra` column rather than a
// column of their own, so a plugin adding a field is not a migration. Both are
// argued where they happen, in `internal/index/fields.go`.
func (r *Registry) AddSearchField(indexer index.Indexer) error {
	if r == nil {
		return errors.New("plugin: adding a search field to a nil registry")
	}
	if isNil(indexer) {
		return errors.New("plugin: a search field needs an indexer")
	}

	r.indexers = append(r.indexers, index.SearchField{Plugin: r.current, Indexer: indexer})
	return nil
}

// AddRenderHook contributes a goldmark extension and a render hook in one call.
//
// They are one registration because they are one plugin's contribution to one
// pipeline, and the alternative is a plugin registering an extension and then
// forgetting the hook — a plugin that half works, in a way nobody can see from
// either half.
//
// The hook's `Plugin` field is filled in from the registry's own record of who is
// running, because a plugin cannot be trusted to spell its own name and a log line
// that names the wrong plugin sends somebody to the wrong source file.
func (r *Registry) AddRenderHook(hook render.RenderHook) error {
	if r == nil {
		return errors.New("plugin: adding a render hook to a nil registry")
	}
	if hook.Before == nil && hook.After == nil {
		return errors.New("plugin: a render hook needs something to do")
	}

	if hook.Plugin == "" {
		hook.Plugin = r.current
	}

	r.hooks = append(r.hooks, hook)
	r.exts = append(r.exts, hook.Extensions...)
	return nil
}

// AddGoldmarkExt contributes a goldmark extension and nothing else.
//
// It is a separate registration from [Registry.AddRenderHook] because an extension
// is a *parse* capability and the failure-isolation guarantee is deliberately not
// extended to one: there is no per-extension call site to recover at, because an
// extension participates in the parse itself, and a panic there is a bug in a build
// somebody can fix rather than a fact about a DM's markdown. That is a different
// guarantee from a hook's and it is not one this package can make.
func (r *Registry) AddGoldmarkExt(extension goldmark.Extender) error {
	if r == nil {
		return errors.New("plugin: adding a goldmark extension to a nil registry")
	}
	if extension == nil {
		return errors.New("plugin: a goldmark extension cannot be nil")
	}

	r.exts = append(r.exts, extension)
	return nil
}

// RenderHooks is every plugin's render contribution, in `(Priority, Name)` order.
//
// The order is the plugins' order rather than the registration order because a
// plugin's position is decided in [Registry.Add]: one that asks for `PriorityLast`
// runs after one that does not, whatever order they were registered in. The two
// orders are the same today because the registry sorts on Add and every capability
// is appended after that, but the sort is what makes it true and this accessor is
// the one place that reads it.
func (r *Registry) RenderHooks() render.Hooks {
	if r == nil {
		return render.Hooks{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return render.Hooks{Exts: slices.Clone(r.exts), Hooks: slices.Clone(r.hooks)}
}

// SearchFields is every plugin's search contribution, in the same order.
func (r *Registry) SearchFields() []index.SearchField {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	return slices.Clone(r.indexers)
}

// PageTypes returns the page types the registered plugins contribute, keyed by the
// name a page's frontmatter would use, with each one's summary.
//
// A nil registry contributes nothing and returns an empty map, so a caller can range
// over the answer without asking whether there were plugins. The map is a copy: it
// is built during Add and read afterwards by a sync and a handler, and a map handed
// out by reference is a map two goroutines are one write away from a race.
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
