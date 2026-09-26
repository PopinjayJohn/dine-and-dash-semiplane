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

// Process exit codes. Anything non-zero means the command did not do what it
// was asked to do.
const (
	exitOK      = 0
	exitFailure = 1
)

func main() {
	// SIGINT and SIGTERM cancel the context, which is what lets long-running
	// commands drain SSE streams and close the database cleanly.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	code := execute(ctx, os.Args[1:], os.Stdout, os.Stderr)

	// Released explicitly: os.Exit below skips deferred calls.
	stop()
	os.Exit(code)
}

// execute is main without the two things a test cannot reach: installing
// signal handlers and ending the process. Everything observable, including the
// exit code and what lands on stderr, is decided here.
func execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return exitCode(run(ctx, args, stdout, stderr), stderr)
}

// exitCode turns a command error into a process exit code, and reports the
// error unless it is a cancellation. Kept separate from execute so that the
// mapping can be tested without a subcommand that blocks, which M0 does not
// have yet.
func exitCode(err error, stderr io.Writer) int {
	if err == nil {
		return exitOK
	}

	// A Ctrl-C is an ordinary way to stop, not a failure worth reporting. A
	// script that ran `wiki serve` still sees the non-zero code.
	if !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "wiki: %v\n", err)
	}
	return exitFailure
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
	"migrate": {
		summary: "apply database migrations to a data directory",
		run:     runMigrate,
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
