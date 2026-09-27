package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
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

	want := []string{"house-rules", "spoilerbox", "wordcount"}
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

	for _, want := range []string{"wordcount", "house-rules"} {
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
