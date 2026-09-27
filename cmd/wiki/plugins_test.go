package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
)

// TestTheBundledPluginsRegister is the test that says a build with a broken plugin
// is a build that does not start, and it is the one test the whole of
// cmd/wiki/plugins.go exists to make possible.
//
// Without it, a plugin that fails `Setup` is a page rendered without its
// contribution, and a DM has no way to tell that from a plugin that was never
// written.
func TestTheBundledPluginsRegister(t *testing.T) {
	t.Parallel()

	registry, err := buildRegistry(nil, quietLogger(t))
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}

	// Sorted by name, because that is what `(Priority, Name)` means when every
	// plugin is at the default priority -- and an assertion written in
	// registration order would be a test that a reorder commit would break for no
	// reason, which is the same reason the registry sorts.
	want := []string{"dnd5e", "house-rules", "spoilerbox", "wordcount"}
	got := registry.Names()
	if len(got) != len(want) {
		t.Fatalf("the build registered %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestThePluginsAddTheirCommandsToHelp is the other half of the registration: a
// plugin whose command is not in `wiki help` is a plugin a DM cannot discover, which
// is the same as a plugin that is not there.
func TestThePluginsAddTheirCommandsToHelp(t *testing.T) {
	t.Parallel()

	help := wikiHelpText()

	for _, want := range []string{"wordcount", "house-rules", "dnd5e", "character-sheet"} {
		if !strings.Contains(help, want) {
			t.Errorf("`wiki help` does not mention %q:\n%s", want, help)
		}
	}

	// And the core's own commands are still there, which is the failure a
	// registration bug would produce: a map that got *replaced* rather than merged.
	for _, want := range []string{"serve", "sync", "reindex"} {
		if !strings.Contains(help, want) {
			t.Errorf("`wiki help` lost the core command %q:\n%s", want, help)
		}
	}
}

// TestAPluginCommandIsDispatchable: the command is not just *listed*. This is the
// whole of what "a plugin may add a subcommand" means, and the only way to know it
// works is to run it.
func TestAPluginCommandIsDispatchable(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	execute(t.Context(), []string{"wordcount"}, &stdout, &stderr)

	// What matters is that the command was *reached*. A dispatcher that did not know
	// it prints "unknown command" and nothing else, and there is no way to tell that
	// from a command which printed its usage and failed.
	if strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("the dispatcher does not know the plugin's command:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "usage: wiki wordcount") {
		t.Errorf("the command did not print its usage:\n%s", stderr.String())
	}
}

// quietLogger is a logger that throws everything away, so a test's output is the
// assertion and not a plugin registering itself.
func quietLogger(t *testing.T) *slog.Logger {
	t.Helper()

	return slog.New(slog.NewTextHandler(&strings.Builder{},
		&slog.HandlerOptions{Level: slog.LevelError + 8}))
}

// wikiHelpText is what `wiki help` prints, as a string.
//
// It goes through `execute` rather than calling the formatter, because the question
// is what a DM sees and the dispatcher is part of the answer.
func wikiHelpText() string {
	var stdout, stderr bytes.Buffer
	execute(context.Background(), []string{"help"}, &stdout, &stderr)
	return stdout.String()
}

// TestTheCoreCommandListIsTheDispatchersOwn holds `internal/plugin`'s reserved
// command names against the dispatcher's own map, in **both** directions.
//
// The comment on that list names this test, and the test did not exist. The list it
// described reserved `init`, `mint` and `versions` — commands that have never been in
// the dispatcher — and omitted `version` and `migrate`, which are. `wiki migrate` is
// how a database's schema is applied, so a plugin could have registered that name and
// taken it, and the only thing standing in the way was a literal in a file nobody had
// read since.
//
// It is here, in `cmd/wiki`, rather than beside the list, because `cmd/wiki` is a
// `main` package: this is the only side of the comparison that can see both. That is
// the cost of a reserved list living with the registry, and it is paid once.
//
// The two directions are not symmetric and both are checked:
//   - a dispatcher name missing from the list is a **plugin can take it**, which is
//     the bug that was there;
//   - a list name missing from the dispatcher is a **stale reservation**, which
//     refuses a name nothing uses and reads as a command that exists.
func TestTheCoreCommandListIsTheDispatchersOwn(t *testing.T) {
	t.Parallel()

	reserved := plugin.CoreCommands()

	for name := range commands {
		if !plugin.IsCoreCommand(name) {
			t.Errorf("the dispatcher has %q and no plugin may claim it: "+
				"add it to plugin.CoreCommands", name)
		}
	}

	// `help` is registered by `init` rather than in the literal, and the
	// `allCommands` merge is what a plugin's command is compared against, so the
	// dispatcher's own view is the merged one.
	for _, name := range reserved {
		if _, exists := allCommands()[name]; !exists {
			t.Errorf("plugin.CoreCommands reserves %q and the dispatcher has no such "+
				"command; a stale reservation refuses a name nothing uses", name)
		}
	}

	// And the property the whole list exists for, asked through the registry: a
	// plugin cannot register a core command even if it tries.
	reg := plugin.New(slog.Default())
	for _, name := range reserved {
		err := reg.AddCommand(plugin.Command{
			Name:    name,
			Summary: "mine now",
			Run:     func(context.Context, []string, io.Writer, io.Writer) error { return nil },
		})
		if !errors.Is(err, plugin.ErrReservedName) {
			t.Errorf("a plugin registered the core command %q: %v", name, err)
		}
	}
}
