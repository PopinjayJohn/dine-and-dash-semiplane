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
	"log/slog"
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

	// plugin is the name of the plugin that added the command, or empty for a
	// core one.
	//
	// It is printed by `wiki help` because "which of the four things I installed
	// added `wordcount`" is the first question a DM asks about a command they did
	// not write, and the answer is otherwise only in a source file. The registry
	// fills it in rather than the plugin, for the reason ADR 0022 gives: a
	// constructor that takes a name is a constructor that can be called with the
	// wrong one.
	plugin string
}

// commands is the core set of subcommands. Adding one here is all the
// registration required; a *plugin*'s commands are merged in at dispatch time by
// [allCommands], because the registry cannot write into this map at init time and a
// second map is a second answer to "what commands are there".
var commands = map[string]command{
	"version": {
		summary: "print the build version",
		run:     runVersion,
	},
	"migrate": {
		summary: "apply database migrations to a data directory",
		run:     runMigrate,
	},
	"sync": {
		summary: "read a campaign's vault into its index",
		run:     runSync,
	},
	"reindex": {
		summary: "rebuild the index from the files, discarding what is there",
		run:     runReindex,
	},
	"serve": {
		summary: "serve the wiki over HTTP, and watch the vaults for changes",
		run:     runServe,
	},
	"backup": {
		summary: "write a timestamped archive of the database and the vault",
		run:     runBackup,
	},
	"export": {
		summary: "write a campaign's vault as a zip, for Obsidian or for a player",
		run:     runExport,
	},
	"import": {
		summary: "copy an Obsidian vault into a campaign, after showing what it would do",
		run:     runImport,
	},
	"users": {
		summary: "mint, revoke and list the share links for a campaign",
		run:     runUsers,
	},
}

// allCommands is the core commands plus a plugin\'s, in one map.
//
// It is built per dispatch rather than once at init because the registry cannot be a
// package variable — "no ambient globals" is a guarantee `internal/plugin` makes and
// a test enforces — and because a plugin is constructed with a store that only exists
// once a data directory has been opened. Three commands ask for the map twice and
// three map builds of three entries is not worth a cache.
func allCommands() map[string]command {
	merged := make(map[string]command, len(commands))
	for name, cmd := range commands {
		merged[name] = cmd
	}

	// A nil store, because a plugin's command is asked *whether it exists* long
	// before it is run, and a command that is never run must not have needed a
	// database. The one plugin in this build that needs one answers "this build has
	// no index" rather than dereferencing it.
	//
	// **A logger above Info, deliberately.** Every plugin logs a line as it
	// registers, which is right for `wiki serve` and wrong for everything else:
	// `wiki version` is the command `docs/security.md` tells people to paste into a
	// bug report, and four `INFO plugin registered` lines above the version make that
	// paste useless. A DM asking what is in this build did not ask for a plugin audit
	// trail, and `wiki help` is a page a person reads.
	//
	// The warning below still goes to stderr at Warn, because a plugin that *fails*
	// to register is a different thing from one that registers quietly: commands are
	// missing, and the DM is the only person who can do anything about it.
	registry, err := buildRegistry(nil, slog.New(slog.NewTextHandler(
		os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	if err != nil {
		// A plugin that cannot register is a startup failure, and this is a
		// *dispatch* rather than a startup. The core commands are still there, so a
		// DM can still run `wiki help` and see what is wrong.
		slog.Warn("the bundled plugins did not register, so their commands are missing", "error", err)
		return merged
	}
	for name, cmd := range pluginCommands(registry) {
		merged[name] = cmd
	}

	return merged
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

	cmd, ok := allCommands()[args[0]]
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

	cmd, ok := allCommands()[args[0]]
	if !ok {
		if err := usage(stdout); err != nil {
			return err
		}
		return fmt.Errorf("unknown command %q", args[0])
	}

	_, err := fmt.Fprintf(stdout, "wiki %s: %s\n", args[0], cmd.summary)
	return err
}

// usage is generated from every command there is, sorted for determinism.
//
// It reads [allCommands] rather than `commands`, because a plugin's subcommand that
// is not in the help is a subcommand a DM cannot find — and the whole of the
// capability is that a plugin can *add* one.
func usage(w io.Writer) error {
	var b strings.Builder
	b.WriteString("wiki is a TTRPG wiki for DMs and their players.\n\n")
	b.WriteString("Usage:\n  wiki <command> [flags]\n\nCommands:\n")

	available := allCommands()
	names := make([]string, 0, len(available))
	for name := range available {
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
		fmt.Fprintf(&b, "  %-*s  %s", width, name, available[name].summary)
		if from := available[name].plugin; from != "" {
			fmt.Fprintf(&b, "  (%s)", from)
		}
		fmt.Fprintln(&b)
	}

	b.WriteString("\nRun 'wiki help <command>' for details.\n")
	b.WriteString("See docs/spec.md and docs/adr/ for the full design.\n")

	_, err := io.WriteString(w, b.String())
	return err
}
