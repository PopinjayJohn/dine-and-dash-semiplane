// Command wiki is the single entry point for the TTRPG wiki. It serves the
// application and provides the maintenance commands that operate on a data
// directory.
//
// Subcommands are registered in the commands map; the usage text is generated
// from that map, so a command cannot exist without being documented.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/version"
)

func main() {
	// SIGINT and SIGTERM cancel the context, which is what lets long-running
	// commands drain SSE streams and close the database cleanly. The stop
	// function is called explicitly below because os.Exit skips defers.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()

	if err != nil {
		if !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "wiki: %v\n", err)
		}
		os.Exit(1)
	}
}

// command is a wiki subcommand. It receives the arguments following the
// subcommand name.
type command struct {
	summary string
	run     func(ctx context.Context, args []string, stdout, stderr io.Writer) error
}

// commands is the complete set of subcommands. Adding one here is all the
// registration required.
var commands = map[string]command{
	"version": {
		summary: "print the build version",
		run:     runVersion,
	},
}

func init() {
	// help is registered apart from the literal above because it is the one
	// command that has to read the map it lives in. Naming it in the literal
	// would be an initialisation cycle.
	commands["help"] = command{
		summary: "show usage for a command",
		run:     runHelp,
	}
}

// run dispatches args to a subcommand. It is separate from main so that the
// whole CLI surface is testable without spawning a process.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usage(stdout)
	}

	switch args[0] {
	case "-h", "--help", "help":
		return runHelp(ctx, args[1:], stdout, stderr)
	}

	cmd, ok := commands[args[0]]
	if !ok {
		if err := usage(stderr); err != nil {
			return err
		}
		return fmt.Errorf("unknown command %q", args[0])
	}

	return cmd.run(ctx, args[1:], stdout, stderr)
}

func runVersion(_ context.Context, _ []string, stdout, _ io.Writer) error {
	_, err := fmt.Fprintln(stdout, version.Get().String())
	return err
}

func runHelp(_ context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) == 0 {
		return usage(stdout)
	}

	cmd, ok := commands[args[0]]
	if !ok {
		if err := usage(stdout); err != nil {
			return err
		}
		return fmt.Errorf("unknown command %q", args[0])
	}

	_, err := fmt.Fprintf(stdout, "wiki %s: %s\n", args[0], cmd.summary)
	return err
}

// usage is generated from commands, sorted for determinism.
func usage(w io.Writer) error {
	var b strings.Builder
	b.WriteString("wiki is a TTRPG wiki for DMs and their players.\n\n")
	b.WriteString("Usage:\n  wiki <command> [flags]\n\nCommands:\n")

	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)

	width := 0
	for _, name := range names {
		if len(name) > width {
			width = len(name)
		}
	}
	for _, name := range names {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, name, commands[name].summary)
	}

	b.WriteString("\nRun 'wiki help <command>' for details.\n")
	b.WriteString("See docs/spec.md and docs/adr/ for the full design.\n")

	_, err := io.WriteString(w, b.String())
	return err
}
