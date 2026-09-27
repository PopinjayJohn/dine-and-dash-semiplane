package main

import (
	"fmt"
	"log/slog"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/dnd5e"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/houserules"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/spoilerbox"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/wordcount"
)

// # The compile-time list
//
// This file is the whole of ADR 0002's "compile-time list", and it is one function
// returning three values for a reason that has nothing to do with taste: a list in
// this package is a list somebody can read, and a DM who pastes their log into a bug
// report can see which three plugins their build had.
//
// There is no scanning, no `init()` and no `go:generate`. A plugin is imported, named
// here, and run; the alternative — a directory walk and a `symbol.Lookup` — is the
// dynamic loader ADR 0002 refused, and a `//go:embed plugins/*` is the same idea with
// fewer of the reasons against it.
//
// # What a plugin is given
//
// A constructor argument, and only a constructor argument. `wordcount` needs the
// store to list a campaign; `houserules` needs a logger; `spoilerbox` needs nothing
// and is constructed with nothing. A plugin that reached for a package-level handle
// instead would be a plugin that could not be registered in a test, and
// `internal/plugin/contract` is a test that every plugin in here runs.

// bundled are the plugins this build ships with, in the order they are registered.
//
// The order does not matter and the comment says why: `plugin.Registry.Add` sorts by
// `(Priority, Name)` afterwards, so a plugin's position is decided by the priority it
// asks for rather than by where it was written down. A list whose order mattered
// would be a list where a reordering commit changed what a DM sees.
func bundled(store *store.Store, logger *slog.Logger) []plugin.Plugin {
	return []plugin.Plugin{
		// Render hook, event subscriber and a command.
		houserules.New(logger),
		// An access policy and a render hook.
		spoilerbox.New(),
		// A search field, a route and a command.
		wordcount.New(store),
		// The ruleset: four page types, fifteen fields and a character-sheet
		// scaffold. It is the only one of the four that is a thing a DM would
		// install rather than a demonstration, which is why it is last -- a plugin
		// that stops the build from starting is the first thing anybody notices.
		dnd5e.New(logger),
	}
}

// buildRegistry is the registry every `wiki` command that cares about builds.
//
// It is a function rather than a package variable because a package variable is a
// second, ambient plugin registry — and "no ambient globals" is the guarantee
// `internal/plugin` makes and the reason `TestTheRegistryHasNoAmbientState` exists.
// A test builds its own; `wiki` builds one; nothing finds one by asking.
func buildRegistry(s *store.Store, logger *slog.Logger) (*plugin.Registry, error) {
	if logger == nil {
		logger = slog.Default()
	}

	registry := plugin.New(logger)
	for _, p := range bundled(s, logger) {
		if err := registry.Add(p); err != nil {
			// Loudly, and at startup. A plugin that cannot register is a page
			// rendered without its contribution, and a DM has no way to tell that
			// from a plugin that was never written.
			return nil, fmt.Errorf("registering plugin %s: %w", p.Name(), err)
		}
	}

	return registry, nil
}

// pluginCommands is a plugin's subcommands in the dispatcher's own shape.
//
// It is a conversion rather than an assignment because the two maps have different
// value types, and it is a *function* rather than something done inside
// `buildRegistry` because doing it there makes the dispatcher's own `commands`
// variable part of an initialisation cycle: the map holds `runServe`, which builds
// a registry, which would then write into the map. The cycle is a compile error and
// it is the dependency graph being honest about the fact that a command and a
// server are the same thing here.
func pluginCommands(registry *plugin.Registry) map[string]command {
	added := make(map[string]command)
	for name, pluginCommand := range registry.Commands() {
		added[name] = command{
			summary: pluginCommand.Summary,
			run:     pluginCommand.Run,
			plugin:  pluginCommand.Plugin,
		}
	}
	return added
}
