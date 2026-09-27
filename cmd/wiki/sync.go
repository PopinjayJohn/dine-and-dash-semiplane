package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/lockfile"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// staleLock is how long a lock may go without its holder refreshing it before
// another process may take it over.
//
// It is minutes rather than seconds because a full reindex of a large campaign
// is a second or two and the holder refreshes on a timer, so the window only has
// to be comfortably longer than the gap between refreshes.
const staleLock = 2 * time.Minute

// syncOptions is what `wiki sync` and `wiki reindex` share. They differ in one
// bool and nothing else, because a command that rebuilds the index and a command
// that syncs it are the same code with a different verb, and two copies of that
// code drift.
type syncOptions struct {
	dataDir  string
	campaign string
	check    bool
	full     bool
}

// runSync is `wiki sync`: read a campaign's vault into its index.
func runSync(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags, opts := syncFlags("sync")
	flags.BoolVar(&opts.check, "check", false,
		"report what a sync would do and change nothing; exits non-zero when the index is not in step")
	flags.BoolVar(&opts.full, "full", false, "rebuild the index from the files, discarding what is there")

	if err := parseFlags(flags, args, "wiki sync"); err != nil {
		return err
	}

	return syncCampaigns(ctx, *opts, stdout, stderr)
}

// runReindex is `wiki reindex --full`.
//
// It is a separate command rather than a flag on sync because the full rebuild
// is the one operation here that throws work away, and a command a DM can type
// on its own is a command they will type on purpose. The --full is required, and
// saying so is the whole of the confirmation: `wiki reindex --full` is a thing
// you type, not a thing that happens to you.
func runReindex(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags, opts := syncFlags("reindex")
	flags.BoolVar(&opts.full, "full", false, "required, and the point of the command")

	if err := parseFlags(flags, args, "wiki reindex"); err != nil {
		return err
	}

	if !opts.full {
		return errors.New("wiki reindex: --full is required; it discards the index and rebuilds it from the files, so it is worth typing on purpose")
	}

	opts.full = true
	return syncCampaigns(ctx, *opts, stdout, stderr)
}

func syncFlags(name string) (*flag.FlagSet, *syncOptions) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	// The flag package's own output would say the same thing as the error this
	// returns, and exitCode prints the error with "wiki: " in front.
	flags.SetOutput(io.Discard)

	opts := &syncOptions{}
	flags.StringVar(&opts.dataDir, "data-dir", "", "the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.StringVar(&opts.campaign, "campaign", "", "the campaign to sync, by slug; every campaign if empty")

	return flags, opts
}

// syncCampaigns is the body of both commands: open the data directory, work out
// which campaigns are in it, and sync each one.
func syncCampaigns(ctx context.Context, opts syncOptions, stdout, stderr io.Writer) error {
	dir, err := datadir.Resolve(opts.dataDir)
	if err != nil {
		return fmt.Errorf("finding the data directory: %w", err)
	}

	s, err := openCampaignStore(ctx, dir)
	if err != nil {
		return err
	}
	defer func() { _ = s.Close() }()

	dirs, err := campaignDirs(filepath.Join(dir, "vault"))
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		fmt.Fprintf(stderr, "wiki: no campaigns under %s\n", filepath.Join(dir, "vault"))
		return nil
	}

	locksDir := filepath.Join(dir, "locks")
	readOnly := opts.check

	problems := 0
	for _, path := range dirs {
		slug, slugErr := domain.NewSlug(filepath.Base(path))
		if slugErr != nil {
			// A directory that is not a campaign name cannot be addressed by
			// one, and a sync that guessed would sync the wrong campaign.
			fmt.Fprintf(stderr, "wiki: %s is not a campaign name (a directory under vault/ must be a slug); skipping it\n", path)
			problems++
			continue
		}

		if opts.campaign != "" && opts.campaign != slug.String() {
			continue
		}

		syncer, release, err := syncerFor(ctx, s, path, slug, locksDir, readOnly)
		if err != nil {
			// A campaign somebody else is syncing is not a reason to skip the
			// others: a DM with two campaigns can still sync the one nobody is
			// working on.
			fmt.Fprintf(stderr, "wiki: %s: %v\n", slug, err)
			problems++
			continue
		}

		report, syncErr := runOneSync(ctx, syncer, opts)
		if release != nil {
			release()
		}
		if syncErr != nil {
			fmt.Fprintf(stderr, "wiki: %s: %v\n", slug, syncErr)
			problems++
			continue
		}

		printReport(stdout, slug, report, opts)

		// A check answers a question, and the answer belongs in the exit code as
		// well as on stdout: a script asking "is my index in step" should not
		// have to read the output to find out. Every other mode exits non-zero
		// for files that need a human, which is a different question.
		if opts.check && !report.InStep() {
			problems++
			continue
		}
		problems += report.Problems()
	}

	if problems > 0 {
		return fmt.Errorf("%d file(s) need attention; the lines above say which", problems)
	}
	return nil
}

// runOneSync is sync, check or reindex, differing in one line each, so that
// "would change" and "changed" cannot be answers to two different questions.
func runOneSync(ctx context.Context, syncer *index.Syncer, opts syncOptions) (index.Report, error) {
	switch {
	case opts.check:
		return syncer.Check(ctx)
	case opts.full:
		return syncer.ReindexFull(ctx)
	default:
		return syncer.Sync(ctx)
	}
}

// syncerFor opens a campaign and takes its lock, unless the caller only reads.
func syncerFor(ctx context.Context, s *store.Store, dir string, slug domain.Slug, locksDir string, readOnly bool) (*index.Syncer, func(), error) {
	v, err := vault.Open(dir)
	if err != nil {
		return nil, nil, err
	}

	campaign, err := campaignFor(ctx, s, slug)
	if err != nil {
		_ = v.Close()
		return nil, nil, err
	}

	syncer := index.New(v, s, campaign)
	if readOnly {
		return syncer, func() { _ = v.Close() }, nil
	}

	lock, err := lockfile.Acquire(locksDir, slug.String(), time.Now(), staleLock)
	if err != nil {
		_ = v.Close()
		return nil, nil, err
	}

	// The lock is refreshed while the work happens, so a full rebuild of a
	// large campaign does not look abandoned half way through.
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(staleLock / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = lock.Refresh(time.Now())
			}
		}
	}()

	return syncer, func() {
		close(stop)
		_ = lock.Release()
		_ = v.Close()
	}, nil
}

// campaignFor is the campaign's row, created if the vault has none.
//
// A vault on disk with no row in the index is drift the sync repairs rather
// than a folder to import, because the files are the truth (ADR 0001) and
// refusing to index a campaign a DM can see in Obsidian would be the wrong
// answer. Importing somebody else's vault is M12's job, and it is a job with a
// confirmation step.
func campaignFor(ctx context.Context, s *store.Store, slug domain.Slug) (domain.Campaign, error) {
	campaign, err := s.CampaignBySlug(ctx, slug)
	switch {
	case err == nil:
		return campaign, nil
	case !errors.Is(err, store.ErrNotFound):
		return domain.Campaign{}, err
	}

	created, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     slug,
		Name:     slug.String(),
		VaultDir: "vault/" + slug.String(),
	})
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("recording the campaign %s: %w", slug, err)
	}

	return created, nil
}

// printReport is what a DM actually reads, so it says what happened in words and
// names the pages rather than counting them.
func printReport(out io.Writer, slug domain.Slug, report index.Report, opts syncOptions) {
	switch {
	case opts.check && report.InStep():
		fmt.Fprintf(out, "%s: the index is in step with the vault (%d pages, %d passes)\n",
			slug, report.Unchanged, report.Passes)
		return
	case opts.check:
		fmt.Fprintf(out, "%s: the index is NOT in step with the vault\n", slug)
	case opts.full:
		fmt.Fprintf(out, "%s: rebuilt the index from the files (%d pages, %d replaced)\n",
			slug, len(report.Indexed), report.Rebuilt)
	case report.Changed() == 0:
		fmt.Fprintf(out, "%s: nothing to do (%d pages already in step)\n", slug, report.Unchanged)
	default:
		fmt.Fprintf(out, "%s: indexed %d, archived %d, %d already in step (%d passes)\n",
			slug, len(report.Indexed), len(report.Archived), report.Unchanged, report.Passes)
	}

	for _, path := range report.Indexed {
		fmt.Fprintf(out, "  + %s\n", path)
	}
	for _, path := range report.Archived {
		fmt.Fprintf(out, "  - %s (the file is gone)\n", path)
	}
	for _, skip := range report.Skipped {
		fmt.Fprintf(out, "  ? %s: %s\n", skip.Path, skip.Reason)
	}
	for _, refusal := range report.Refused {
		fmt.Fprintf(out, "  ! %s: %s\n", refusal.Path, refusal.Reason)
	}
	for _, problem := range report.Ownership {
		fmt.Fprintf(out, "  ! %s: %s\n", problem.Path, problem.Reason)
	}
}

// campaignDirs lists the campaigns under a vault root, which are its
// directories.
//
// A directory with no markdown in it is skipped and said nothing about:
// somebody's `Downloads` folder is not a campaign, and a campaign with one empty
// page is a folder a DM has not written in yet. Anything with a name that is not
// a slug is *said* about, because a sync that guessed would sync the wrong
// campaign.
func campaignDirs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		path := filepath.Join(root, entry.Name())
		pages, err := markdownCount(path)
		if err != nil {
			return nil, err
		}
		if pages == 0 {
			continue
		}

		dirs = append(dirs, path)
	}

	slices.Sort(dirs)
	return dirs, nil
}

// markdownCount is how many markdown files are under a directory, at any depth.
//
// It counts rather than listing because this runs before anything is opened, and
// opening a vault to ask whether it is a campaign is a lot of work for a
// question about a directory name.
func markdownCount(root string) (int, error) {
	count := 0

	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			count++
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("reading %s: %w", root, err)
	}

	return count, nil
}

// openCampaignStore opens the campaign database and migrates it, which is what
// every command that touches a campaign needs and the one place that knows how.
func openCampaignStore(ctx context.Context, dir string) (*store.Store, error) {
	s, err := store.Open(ctx, datadir.DatabaseFile(dir), store.Options{
		Clock: clock.System{},
		IDGen: idgen.UUID{},
	})
	if err != nil {
		return nil, fmt.Errorf("opening the campaign database: %w", err)
	}

	if _, err := s.Migrate(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("migrating the campaign database: %w", err)
	}

	return s, nil
}

// parseFlags is the flag handling every subcommand shares: a bad flag is the
// command's own error rather than the flag package's, so it is printed once.
func parseFlags(flags *flag.FlagSet, args []string, name string) error {
	return parseFlagsAllowing(flags, args, name, 0)
}

// parseFlagsAllowing is [parseFlags] for a command that takes `wanted` positional
// arguments.
//
// A command whose whole input is one word — `wiki users new Alice` — cannot be
// written with [parseFlags], and the version that parses and then checks `NArg` is
// two rules in two functions, which is how a command ends up accepting two arguments
// when it takes one. So the count is the parameter and the default is zero.
//
// A count of one is a separate case from zero, not a special value: a bad flag, a
// missing argument and a surplus argument are three different mistakes and the
// messages say which.
func parseFlagsAllowing(flags *flag.FlagSet, args []string, name string, wanted int) error {
	if err := flags.Parse(args); err != nil {
		return usageError{name: name, flags: flags}
	}

	switch {
	case flags.NArg() > wanted:
		return fmt.Errorf("%s takes %s, got %d arguments", name, positionalName(wanted), flags.NArg())
	case flags.NArg() < wanted:
		return usageError{
			name:  name,
			flags: flags,
			takes: positionalName(wanted),
		}
	}

	return nil
}

// positionalName is how a command's positional arguments are described in a message:
// "no arguments", one argument, two arguments.
//
// It is a function because the two mistakes a caller can make are about there being
// too many and there being too few, and they read differently — "took 3 arguments"
// for one and "takes no arguments, got \"x\"" for the other.
func positionalName(count int) string {
	switch count {
	case 0:
		return "no arguments"
	case 1:
		return "one argument"
	default:
		return fmt.Sprintf("%d arguments", count)
	}
}

// usageError is "this command's flags, and a non-zero exit", carried as an error
// so the top-level exit path decides where it is printed.
type usageError struct {
	name  string
	flags *flag.FlagSet

	// takes is the positional argument the command wanted, for the commands whose
	// flags all have defaults and whose mistake is to leave it out. Empty means the
	// command takes none, which is what `parseFlagsAllowing`'s zero already says.
	takes string
}

func (e usageError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", e.name)

	if e.takes != "" {
		fmt.Fprintf(&b, "Takes: %s\n\n", e.takes)
	}

	// The flag set's output is borrowed rather than given a writer, because
	// PrintDefaults is the only way to get a flag set's own listing out of the
	// standard library. A nil flag set is the one caller that has no flags to show --
	// `wiki users` with no subcommand -- and the listing is then just the sentence
	// above, which is the whole of the useful message.
	if e.flags != nil {
		b.WriteString("Flags:\n")
		e.flags.SetOutput(&b)
		e.flags.PrintDefaults()
	}

	return b.String()
}

// flagsForSubcommandHelp is an empty flag set, for the one usage error a
// subcommand dispatcher produces before it has parsed anything.
//
// It exists because a `usageError` with a nil flag set was the alternative and it
// made `Error` conditional, and "is the flag set nil" is a question every reader of
// `Error` would then have to answer. An empty set prints nothing, which is what
// there is to print.
func flagsForSubcommandHelp(_ io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet("", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}
