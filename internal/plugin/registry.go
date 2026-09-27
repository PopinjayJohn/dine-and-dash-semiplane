package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// Registry holds the plugins a process was built with.
//
// # Ordering
//
// A Registry is *built* by one goroutine — `cmd/wiki`, during startup — and read by
// all of them afterwards. [Registry.Add] holds the write lock for the whole of its
// work, including the call to `Setup`, so a reader that arrives mid-build waits
// rather than observing a half-registered plugin; a caller that needs to be correct
// against a *partially* built registry has to build a second one, which is the only
// way to get one.
//
// A nil *Registry is a registry with no plugins in it. Every method handles it, so
// `http.Config{Registry: nil}` and `http.Config{}` are the same application — which
// is what lets a caller pass one in without a nil check, and what makes the zero
// configuration correct rather than merely convenient.
type Registry struct {
	mu  sync.RWMutex
	log *slog.Logger

	// order is the registered plugins in (Priority, Name) order, and every
	// accessor that needs to run a plugin reads it rather than the map below.
	order []*entry

	// seen is the set of taken plugin names. A map rather than a scan because the
	// duplicate check is on the path every Add takes and a registry holds a
	// handful of entries, so either would do; the map is the one that also
	// answers "is this registered" without handing out the slice.
	seen map[string]bool

	// pages is the page types the plugins contribute, keyed by the type name.
	pages map[domain.PageType]string

	// fields is the frontmatter keys the plugins claim, keyed by the key.
	fields map[vault.Key]FieldType

	// current is the name of the plugin whose Setup is running, and the empty
	// string at every other time.
	//
	// It is deliberately not behind the mutex. [Registry.Add] holds the write lock
	// while it calls Setup, and the only caller of [Registry.Owner] is a plugin
	// registering something from inside its own Setup — so the read is on the
	// goroutine that owns the write lock and no other goroutine can be inside Add
	// at the same time. Guarding it with the same mutex would deadlock: an
	// RWMutex is not reentrant.
	current string
}

// entry is what the registry keeps per plugin: the identity and the order.
type entry struct {
	info Info
}

// New returns a registry that logs through log.
//
// A nil logger means [slog.Default], for the same reason `http.New` does it: a
// caller that has a logger wants its own, and a caller that has not should still
// get a working application rather than a nil-pointer panic inside a hook.
func New(log *slog.Logger) *Registry {
	if log == nil {
		log = slog.Default()
	}
	return &Registry{
		log:    log,
		seen:   make(map[string]bool),
		pages:  make(map[domain.PageType]string),
		fields: make(map[vault.Key]FieldType),
	}
}

// Add registers a plugin, running its Setup.
//
// The checks are in the order of how much they cost to diagnose, and the last one
// is the only expensive one: a nil plugin and a bad name are wrong in a way the
// error message shows, and only a plugin that survives those two is worth calling
// into.
//
// An error from Add means the registry is in an undefined state — a plugin's Setup
// may have registered some of its capabilities before failing — so a caller that
// gets one must stop rather than carry on. `cmd/wiki` does, by exiting.
func (r *Registry) Add(p Plugin) error {
	if r == nil {
		return errors.New("plugin: adding to a nil registry")
	}
	if isNil(p) {
		return errors.New("plugin: Add was given a nil plugin")
	}

	// A plugin's name goes through the same slug rules as a campaign's, and the
	// slug is the identity. Normalising here rather than at each use is what
	// makes "a plugin name is safe in a URL, a log line and a class attribute"
	// true everywhere rather than true in three places that each remember to.
	name, err := normalise(p.Name())
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrInvalidName, p.Name(), err)
	}

	version := p.Version()
	if strings.TrimSpace(version) == "" {
		return fmt.Errorf("%w: %s", ErrNoVersion, name)
	}

	priority := PriorityNormal
	if ordered, isOrdered := p.(Prioritised); isOrdered {
		priority = ordered.Priority()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.seen[name] {
		return fmt.Errorf("%w: %s", ErrDuplicateName, name)
	}

	r.current = name
	defer func() { r.current = "" }()

	if err := p.Setup(r); err != nil {
		// A refusal from inside Setup — a reserved page type, a duplicate field
		// key — arrives here, and the plugin's name is added to it because
		// "setting up: name is reserved by the core" with no subject is a log line
		// a DM cannot act on.
		return fmt.Errorf("plugin %s: setting up: %w", name, err)
	}

	r.order = append(r.order, &entry{
		info: Info{Name: name, Version: version, Priority: priority},
	})
	slices.SortStableFunc(r.order, compareEntries)

	r.seen[name] = true

	r.log.LogAttrs(context.Background(), slog.LevelInfo, "plugin registered",
		slog.String("plugin", name),
		slog.String("version", version),
		slog.Int("priority", int(priority)),
	)

	return nil
}

// compareEntries is the (Priority, Name) order, and it is the whole of the
// determinism guarantee.
//
// The name is the tie-break rather than registration order, so two plugins that
// both ask for PriorityNormal are in the same relative order in every binary, every
// run and every test. A hook that is "last writer wins" is a hook whose winner is
// decided by a linker, and a hook whose winner is decided by a linker cannot be
// tested.
func compareEntries(a, b *entry) int {
	if a.info.Priority != b.info.Priority {
		return int(a.info.Priority) - int(b.info.Priority)
	}
	return strings.Compare(a.info.Name, b.info.Name)
}

// Owner is the name of the plugin whose Setup is running, and the empty string at
// every other time.
//
// A plugin registering a capability cannot supply its own name to the registration:
// a constructor that takes a name is a constructor that can be called with the wrong
// one, and the registry is the thing that has already normalised it. Every
// registration that logs, recovers or attributes something takes it from here.
//
// It reads `current` without taking the lock, and the reason is in that field's
// comment. Outside Setup the answer is "" and no caller is reading it.
func (r *Registry) Owner() string {
	if r == nil {
		return ""
	}
	return r.current
}

// ordered returns the registered plugins in (Priority, Name) order.
//
// It copies. The slice is built by Add and never reordered afterwards, so a caller
// that received it directly could reorder it and change the order the guarantee is
// about; two plugins and a copy is not an optimisation anybody needs here.
func (r *Registry) ordered() []*entry {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.order == nil {
		return nil
	}
	return slices.Clone(r.order)
}

// Names returns the names of the registered plugins, in the order they run.
func (r *Registry) Names() []string {
	entries := r.ordered()
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.info.Name
	}
	return names
}

// Infos returns every registered plugin, in the order they run.
func (r *Registry) Infos() []Info {
	entries := r.ordered()
	infos := make([]Info, len(entries))
	for i, e := range entries {
		infos[i] = e.info
	}
	return infos
}

// IsNil reports whether r is a nil registry, so that a caller assembling a
// configuration can tell "no plugins" from "a field I forgot to set".
func (r *Registry) IsNil() bool { return r == nil }

// normalise turns whatever a plugin called itself into the identity everything
// else uses.
//
// It is [domain.NewSlug] rather than a second rule, for the reason the campaign slug
// has: two normalisers are two answers to "what names are legal" and they will
// disagree about the third one. A name that reduces to nothing comes back as an
// error, which is the one answer a caller has to handle and cannot get wrong.
func normalise(name string) (string, error) {
	slug, err := domain.NewSlug(name)
	if err != nil {
		return "", err
	}
	return slug.String(), nil
}

// isNil reports whether a Plugin interface holds a nil pointer, which is the case a
// `p == nil` check misses.
//
// A plugin is very likely to be registered as `&houseRules{}` from a slice of
// pointers, and a slice of pointers is where a nil turns up. `isNil` is what turns
// that from a panic inside the first hook a DM's page reaches into a refusal at
// startup, on the line that names the plugin.
func isNil(p Plugin) bool {
	if p == nil {
		return true
	}
	value := reflect.ValueOf(p)
	// Every kind that can be nil behind an interface, and nothing else. A struct
	// plugin — `houseRules{}` rather than `&houseRules{}` — lands on the default and
	// is never nil, which is correct: there is no such thing as a nil struct value
	// behind an interface.
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return value.IsNil()
	default:
		return false
	}
}
