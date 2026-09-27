package plugin_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
)

// The registry's guarantees, one named test each. They are named after the
// property rather than the function because a test called TestAdd that fails
// halfway through a table does not say which guarantee broke.

// stub is a plugin that does whatever the test tells it to, because the
// registry's own tests are about the registry and not about any real plugin.
type stub struct {
	name    string
	version string
	order   plugin.Priority
	setup   func(*plugin.Registry) error
}

func (s *stub) Name() string    { return s.name }
func (s *stub) Version() string { return s.version }

// Priority is implemented on the value, not the pointer, so that a stub which
// wants the default does not have to spell out an ordering it does not have.
func (s *stub) Priority() plugin.Priority { return s.order }

func (s *stub) Setup(reg *plugin.Registry) error {
	if s.setup == nil {
		return nil
	}
	return s.setup(reg)
}

func newStub(name string) *stub {
	return &stub{name: name, version: "1.0.0"}
}

// quiet returns a registry whose log line about registering a plugin goes
// nowhere, so a test's failure output is the assertion and not the startup.
func quiet(t *testing.T) *plugin.Registry {
	t.Helper()
	return plugin.New(slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError})))
}

// TestCapabilitiesRunInPriorityThenNameOrder is the determinism guarantee.
//
// The table is the interesting half: it registers the same five plugins in five
// different orders and asserts the same answer every time. A registry that
// appended and left it there would pass a one-order test and fail this one, and
// the difference between those two is the whole reason the guarantee exists.
func TestCapabilitiesRunInPriorityThenNameOrder(t *testing.T) {
	t.Parallel()

	// Five plugins, two priorities, names that do not sort in registration order.
	names := map[string]plugin.Priority{
		"zebra":  plugin.PriorityNormal,
		"alpha":  plugin.PriorityNormal,
		"middle": plugin.PriorityFirst,
		"beta":   plugin.PriorityLast,
		"gamma":  plugin.PriorityNormal,
	}

	orders := [][]string{
		{"zebra", "alpha", "middle", "beta", "gamma"},
		{"gamma", "beta", "middle", "alpha", "zebra"},
		{"beta", "gamma", "zebra", "middle", "alpha"},
		{"middle", "zebra", "gamma", "beta", "alpha"},
		{"alpha", "beta", "gamma", "middle", "zebra"},
	}

	const want = "middle,alpha,gamma,zebra,beta"

	for i, order := range orders {
		reg := quiet(t)
		for _, name := range order {
			priority := names[name]
			if err := reg.Add(&stub{name: name, version: "1.0.0", order: priority}); err != nil {
				t.Fatalf("order %d: Add(%s): %v", i, name, err)
			}
		}

		got := strings.Join(reg.Names(), ",")
		if got != want {
			t.Errorf("registration order %v gave %q, want %q", order, got, want)
		}
	}
}

// TestAPluginNameIsNormalisedIntoItsIdentity is the other half of the order
// guarantee: two plugins whose names differ only in spelling are the same
// plugin, and the one that lost is told why.
func TestAPluginNameIsNormalisedIntoItsIdentity(t *testing.T) {
	t.Parallel()

	reg := quiet(t)
	if err := reg.Add(newStub("House Rules")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := reg.Names(); len(got) != 1 || got[0] != "house-rules" {
		t.Fatalf("Names() = %v, want [house-rules]", got)
	}

	err := reg.Add(newStub("house-rules"))
	if !errors.Is(err, plugin.ErrDuplicateName) {
		t.Errorf("Add of a differently-spelled duplicate = %v, want ErrDuplicateName", err)
	}
}

// TestADuplicateNameStopsStartup is the loud-startup guarantee, and the "twice"
// in the name is the point: one Add succeeds and the second is refused, so a
// caller that checked the error for the first cannot miss the second.
func TestADuplicateNameStopsStartup(t *testing.T) {
	t.Parallel()

	reg := quiet(t)
	if err := reg.Add(newStub("spoilerbox")); err != nil {
		t.Fatalf("first Add: %v", err)
	}

	err := reg.Add(newStub("spoilerbox"))
	if !errors.Is(err, plugin.ErrDuplicateName) {
		t.Fatalf("second Add = %v, want ErrDuplicateName", err)
	}
	if !strings.Contains(err.Error(), "spoilerbox") {
		t.Errorf("error %q does not name the plugin", err)
	}
}

// TestAPluginNameCannotCarryAPathOrASeparator is the claim the package doc makes
// about names: one that is safe in a URL, in a log line and in a
// `class="callout-<name>"` without a second escaping step anywhere.
//
// The table is hostile input rather than a demonstration, because the interesting
// case is the name that *looks* like an attack and normalises into something
// harmless, and the uninteresting case is the one that already worked.
func TestAPluginNameCannotCarryAPathOrASeparator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want string
	}{
		{name: "../../etc/passwd", want: "etc-passwd"},
		{name: "House Rules", want: "house-rules"},
		{name: "  spaced  out  ", want: "spaced-out"},
		{name: "CALL-OUTS", want: "call-outs"},
		{name: "9lives", want: "9lives"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reg := quiet(t)
			if err := reg.Add(newStub(test.name)); err != nil {
				t.Fatalf("Add(%q): %v", test.name, err)
			}

			got := reg.Names()
			if len(got) != 1 || got[0] != test.want {
				t.Fatalf("Names() = %v, want [%s]", got, test.want)
			}

			// The same three properties the sanitiser's class pattern and a URL
			// path need, asserted rather than described: a-z, 0-9 and single
			// interior hyphens, and nothing else.
			if strings.ContainsAny(got[0], "./\\ \t\n\"'<>") {
				t.Errorf("normalised name %q still contains something a path or a class attribute cannot hold", got[0])
			}
			if strings.HasPrefix(got[0], "-") || strings.HasSuffix(got[0], "-") {
				t.Errorf("normalised name %q starts or ends with a hyphen", got[0])
			}
		})
	}
}

// TestTheRegistryRefusesAThingItCannotName is the third refusal, and it is a
// table because the three reasons a name is unusable are three different bugs in
// whoever wrote the plugin.
func TestTheRegistryRefusesAThingItCannotName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		plugin  plugin.Plugin
		wantErr error
	}{
		{
			name:    "no name at all",
			plugin:  &stub{name: "", version: "1.0.0"},
			wantErr: plugin.ErrInvalidName,
		},
		{
			name:    "nothing but punctuation",
			plugin:  &stub{name: "...", version: "1.0.0"},
			wantErr: plugin.ErrInvalidName,
		},
		{
			name:    "no version",
			plugin:  &stub{name: "wordcount", version: "  "},
			wantErr: plugin.ErrNoVersion,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := quiet(t).Add(test.plugin)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("Add = %v, want %v", err, test.wantErr)
			}
		})
	}
}

// TestANilPluginIsRefusedRatherThanPanicking is the case a `p == nil` check
// misses, and the reason isInil exists: a slice of plugin pointers with a nil in
// it is the ordinary way a plugin list is written.
func TestANilPluginIsRefusedRatherThanPanicking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		plugin plugin.Plugin
	}{
		{name: "an untyped nil", plugin: nil},
		{name: "a nil pointer in an interface", plugin: (*stub)(nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// The point is that this returns at all. A registry that panicked here
			// would take down `wiki serve` before it had printed its address.
			if err := quiet(t).Add(test.plugin); err == nil {
				t.Error("Add(nil) = nil, want an error")
			}
		})
	}
}

// TestSetupFailureStopsStartupAndNamesThePlugin: a plugin that cannot configure
// itself is refused, and the message carries the plugin's name — because the
// alternative is a log line that says "setting up: already registered" and a DM
// reading it has no way to know which of their four plugins did it.
func TestSetupFailureStopsStartupAndNamesThePlugin(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("no dice table configured")
	reg := quiet(t)

	err := reg.Add(&stub{
		name:    "dice",
		version: "2.1.0",
		setup:   func(*plugin.Registry) error { return sentinel },
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Add = %v, want it to wrap %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "dice") {
		t.Errorf("error %q does not name the plugin", err)
	}
	if got := reg.Names(); len(got) != 0 {
		t.Errorf("Names() = %v, want none: a plugin whose Setup failed is not registered", got)
	}
}

// TestTheRegistryHasNoAmbientState is the "no ambient globals" guarantee, and
// the only way to test it is to build two registries and show that they do not
// see each other. A package-level registry would fail the second half of this
// immediately.
func TestTheRegistryHasNoAmbientState(t *testing.T) {
	t.Parallel()

	first := quiet(t)
	if err := first.Add(newStub("house-rules")); err != nil {
		t.Fatalf("Add: %v", err)
	}

	second := quiet(t)
	if got := second.Names(); len(got) != 0 {
		t.Errorf("a fresh registry sees %v, want nothing", got)
	}
	if err := second.Add(newStub("house-rules")); err != nil {
		t.Errorf("re-adding the same plugin to a second registry = %v, want nil", err)
	}
}

// TestAnEmptyRegistryIsTheZeroConfiguration: every accessor handles a nil
// receiver, so a caller can pass a registry it does not have and get the
// application the DM would get with no plugins at all.
func TestAnEmptyRegistryIsTheZeroConfiguration(t *testing.T) {
	t.Parallel()

	var reg *plugin.Registry

	if !reg.IsNil() {
		t.Error("IsNil() on a nil registry = false, want true")
	}
	if got := reg.Names(); len(got) != 0 {
		t.Errorf("Names() = %v, want none", got)
	}
	if got := reg.Infos(); len(got) != 0 {
		t.Errorf("Infos() = %v, want none", got)
	}
	if got := reg.PageTypes(); len(got) != 0 {
		t.Errorf("PageTypes() = %v, want none", got)
	}
	if got := reg.FieldTypes(); len(got) != 0 {
		t.Errorf("FieldTypes() = %v, want none", got)
	}
	if got := reg.Owner(); got != "" {
		t.Errorf("Owner() = %q, want empty outside Setup", got)
	}
	if err := reg.Add(newStub("anything")); err == nil {
		t.Error("Add on a nil registry = nil, want an error")
	}
}

// TestOwnerNamesThePluginRunningSetup: a registration that logs, recovers or
// attributes something needs the plugin's name, and the plugin cannot supply it
// because a name-taking constructor can be called with the wrong one.
func TestOwnerNamesThePluginRunningSetup(t *testing.T) {
	t.Parallel()

	var seen string
	reg := quiet(t)

	err := reg.Add(&stub{
		name:    "Spoiler Box",
		version: "1.0.0",
		setup: func(r *plugin.Registry) error {
			seen = r.Owner()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if seen != "spoiler-box" {
		t.Errorf("Owner() during Setup = %q, want %q", seen, "spoiler-box")
	}
	if got := reg.Owner(); got != "" {
		t.Errorf("Owner() after Setup = %q, want empty", got)
	}
}

// TestInfosReportsWhatIsInTheBinary: the two places that print a plugin list are
// diagnostics, and a diagnostic with the priority missing is half a diagnostic.
func TestInfosReportsWhatIsInTheBinary(t *testing.T) {
	t.Parallel()

	reg := quiet(t)
	plugins := []*stub{
		{name: "wordcount", version: "0.3.0"},
		{name: "house-rules", version: "1.2.0", order: plugin.PriorityLast},
	}
	for _, p := range plugins {
		if err := reg.Add(p); err != nil {
			t.Fatalf("Add(%s): %v", p.name, err)
		}
	}

	want := []plugin.Info{
		{Name: "wordcount", Version: "0.3.0", Priority: plugin.PriorityNormal},
		{Name: "house-rules", Version: "1.2.0", Priority: plugin.PriorityLast},
	}

	got := reg.Infos()
	if len(got) != len(want) {
		t.Fatalf("Infos() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Infos()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
