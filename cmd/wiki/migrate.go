package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/migrations"
)

// migrate is the manual half of the migration story. Everything else in the
// application migrates on the way up, and a command that only a human runs is
// for the two questions a human has: what schema is this database at, and get
// it to the one this build knows about.
//
// It is deliberately explicit about which database it touched, and it prints
// the path every time. A migration command that says "migrated" without saying
// where is a command that gets run twice in the wrong directory.
func runMigrate(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	// The flag package writes its own message to its own output, and
	// exitCode prints the returned error with the command name in front of
	// it. Leaving the default would say the same thing twice.
	flags.SetOutput(io.Discard)

	var opts migrateOptions
	flags.StringVar(&opts.dataDir, "data-dir", "",
		"the data directory holding campaigns.db ("+datadir.EnvVar+" overrides the default)")
	flags.BoolVar(&opts.status, "status", false,
		"report the schema version and change nothing")

	if parseErr := flags.Parse(args); parseErr != nil {
		if errors.Is(parseErr, flag.ErrHelp) {
			return printMigrateUsage(stdout, flags)
		}
		_, writeErr := fmt.Fprintf(stderr, "wiki migrate: %v\n\n%s", parseErr, flagUsages(flags))
		return errors.Join(writeErr, parseErr)
	}

	if flags.NArg() > 0 {
		return fmt.Errorf("migrate takes no arguments, got %q: pass the data directory with -data-dir", flags.Arg(0))
	}

	dir, err := datadir.Resolve(opts.dataDir)
	if err != nil {
		return fmt.Errorf("finding the data directory: %w", err)
	}
	path := datadir.DatabaseFile(dir)

	if opts.status {
		return reportStatus(ctx, stdout, path)
	}

	s, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		return err
	}
	// A Close that fails can mean the write-ahead log was not checkpointed,
	// so it is reported rather than dropped: for a migration command, "I
	// think that worked" is not a thing to say.
	defer func() { err = errors.Join(err, s.Close()) }()

	state, err := s.Migrate(ctx)
	if err != nil {
		return err
	}

	return report(stdout, path, state)
}

// reportStatus answers the question without writing anything, which means not
// creating the database either. A DM who asks what version a database is at
// should not get a new empty one as a side effect, and a mistyped -data-dir
// should say so rather than answer.
func reportStatus(ctx context.Context, stdout io.Writer, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no database at %s: pass -data-dir if that is not the directory you meant (%w)", path, err)
	}

	s, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	state, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}

	return report(stdout, path, state)
}

func report(stdout io.Writer, path string, state migrations.State) error {
	_, err := fmt.Fprintf(stdout, "database: %s\nschema:   %s\n", path, state)
	return err
}

func printMigrateUsage(stdout io.Writer, flags *flag.FlagSet) error {
	_, err := fmt.Fprintf(stdout,
		"wiki migrate: apply database migrations to a data directory\n\nUsage:\n  wiki migrate [-data-dir DIR] [-status]\n\nFlags:\n%s",
		flagUsages(flags))
	return err
}

// flagUsages renders a flag set's flags the way the flag package would print
// them. It borrows the set's output for the duration, because the package has
// no way to hand the text back, and the set's output is io.Discard for the
// rest of the command.
func flagUsages(flags *flag.FlagSet) string {
	var buf bytes.Buffer
	flags.SetOutput(&buf)
	flags.PrintDefaults()
	flags.SetOutput(io.Discard)
	return buf.String()
}

// migrateOptions is what the flags hold. It exists so the flag definitions and
// the code that reads them are not two halves of one expression across a
// hundred lines.
type migrateOptions struct {
	dataDir string
	status  bool
}
