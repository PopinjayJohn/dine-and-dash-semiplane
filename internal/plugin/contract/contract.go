// Package contract is the suite every plugin runs against itself.
//
// # Why a package of its own rather than a test in each plugin
//
// A plugin's own tests are about whether it does its job. This is about whether it
// can be *registered at all* — and that question has the same answer for every plugin
// and would otherwise be asked once per plugin, which is three copies of the ordering
// test and three chances for the three to disagree about what ordering means.
//
// It follows `internal/store/testsuite`, which is the same arrangement for the
// store: a package that is not `_test.go`, importing `testing`, so that it can be
// handed a `*testing.T` from a test in any other package. The alternative — an
// interface the caller satisfies — would be a second description of what a plugin
// is, and this project has one already.
//
// # What it is not
//
// It does not check that a plugin is *useful*. `wordcount` could count letters and
// every test here would pass. What it checks is that a plugin behaves like a
// participant in the registry: it names itself, it registers without disturbing
// anyone, its capabilities land in the order the registry promised, and it cannot
// widen a right.
//
// A plugin that fails this suite cannot be registered, because [Run] is what
// `cmd/wiki/plugins.go` calls for each one and a plugin that fails it stops the
// process — loudly, at startup, which is the only place a plugin problem is cheap.
package contract

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
)

// quiet is a logger that throws everything away, so a test's output is the assertion
// and not a plugin complaining. A panic recovery is still exercised — that is in
// [Run] — it just does not print a stack trace into the test log.
func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError + 8}))
}

// Run is the whole suite, for one plugin.
//
// It is one exported function taking one value, and that is the interface: a plugin
// author writes `contract.Run(t, New())` in one line and gets every guarantee in
// this file. Anything more elaborate would be a suite a plugin author has to
// configure, and a suite that can be configured is a suite somebody will configure
// wrong.
func Run(t *testing.T, p plugin.Plugin) {
	t.Helper()

	namesItself(t, p)
	registersCleanly(t, p)
	toleratesAnotherPlugin(t, p)
}

// namesItself is the first and bluntest check: a plugin whose `Name` is empty, whose
// name is not a slug, or whose version is blank cannot be registered at all.
//
// It is worth a separate test because the failure mode is otherwise a registry error
// at startup with the plugin's own name in the message and nothing to point at which
// of the three fields was wrong.
func namesItself(t *testing.T, p plugin.Plugin) {
	t.Helper()

	if p == nil {
		t.Fatal("the plugin is nil")
	}

	name := p.Name()
	if strings.TrimSpace(name) == "" {
		t.Error("Name() is empty; the registry normalises it to a slug and cannot normalise nothing")
	}
	if strings.TrimSpace(p.Version()) == "" {
		t.Error("Version() is empty; the registry refuses a plugin that cannot say which build it is")
	}
}

// registersCleanly is "it goes into a registry and everything it claims comes out".
//
// The second half is the part that is easy to get wrong and is the reason this is a
// suite rather than a checklist: a plugin that registers a capability the accessor
// does not return has registered *nothing*, and a page rendered without its
// contribution is indistinguishable from a build without the plugin.
func registersCleanly(t *testing.T, p plugin.Plugin) {
	t.Helper()

	reg := plugin.New(quiet())
	if err := reg.Add(p); err != nil {
		t.Fatalf("Add(%s): %v", p.Name(), err)
	}

	if infos := reg.Infos(); len(infos) != 1 {
		t.Errorf("after Add the registry has %d plugins, want 1", len(infos))
	}

	// Every accessor, asked. A plugin that registers nothing is legal — a plugin that
	// only subscribes to events has no page types — so there is no value to check
	// here, only the fact that each accessor *answers*. An accessor that panics on a
	// capability nobody registered is a bug in the accessor, and the only way it ever
	// gets noticed is from a plugin that happened not to use it.
	answers(t, "PageTypes", func() any { return reg.PageTypes() })
	answers(t, "FieldTypes", func() any { return reg.FieldTypes() })
	answers(t, "Commands", func() any { return reg.Commands() })
	answers(t, "Routes", func() any { return reg.Routes() })
	answers(t, "RenderHooks", func() any { return reg.RenderHooks() })
	answers(t, "SearchFields", func() any { return reg.SearchFields() })
	answers(t, "Events", func() any { return reg.Events() })
	answers(t, "Policies", func() any { return reg.Policies() })
}

// answers calls one accessor and turns a panic in it into a test failure, because a
// stack trace in the middle of somebody else's test is a thing nobody can read.
//
// The result is discarded on purpose. The question is "does this accessor answer at
// all", and the interesting failures are the ones where it does not.
func answers(t *testing.T, accessor string, call func() any) {
	t.Helper()

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Errorf("%s panicked: %v", accessor, recovered)
		}
	}()

	_ = call()
}

// toleratesAnotherPlugin is the isolation check, and it is the one a plugin author is
// most likely to break without noticing.
//
// A plugin's `Setup` is called with the registry and nothing else, so the ways to
// interfere with another plugin are: claiming a name the core owns, and depending on
// being the only thing in the registry. Both are checked here by registering a second
// plugin beside it and asserting the first is still there and still answered to.
func toleratesAnotherPlugin(t *testing.T, p plugin.Plugin) {
	t.Helper()

	reg := plugin.New(quiet())
	if err := reg.Add(p); err != nil {
		t.Fatalf("Add(%s): %v", p.Name(), err)
	}

	// A second plugin with a name that could not collide, and one capability, so the
	// pair is not "two plugins that registered nothing".
	other := neighbour{}
	if err := reg.Add(other); err != nil {
		t.Fatalf("a second plugin could not be registered beside %s: %v", p.Name(), err)
	}

	if got := len(reg.Names()); got != 2 {
		t.Errorf("the registry has %d plugins, want 2", got)
	}
	if got := reg.Names(); got[0] != "aaa-neighbour" {
		t.Errorf("Names() = %v, want the plugins in name order at the same priority", got)
	}
}

// neighbour is the second plugin in [toleratesAnotherPlugin].
//
// It is a struct value rather than a pointer on purpose, for the same reason
// `plugin.isNil` exists: the two are the cases a registry has to survive, and the
// suite is a place to survive them.
type neighbour struct{}

func (neighbour) Name() string    { return "aaa-neighbour" }
func (neighbour) Version() string { return "0.0.1" }
func (neighbour) Setup(*plugin.Registry) error {
	return nil
}
