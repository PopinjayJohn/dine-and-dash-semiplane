package main

import (
	"archive/zip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// `wiki export` and `wiki import` are two commands that move a **vault** and
// nothing else, and [ADR 0024](../../docs/adr/0024-an-export-is-a-vault.md) is
// what says so. The short version:
//
//   - An export is a campaign's vault plus the `.obsidian/` directory, and **no
//     database**. A zip a DM emails to a player must not be a file of session ids and
//     share-link hashes; `wiki backup` is the command for a data directory.
//   - An import **copies files into an existing campaign's vault and writes nothing
//     else**, and a collision is a list of paths and a non-zero exit rather than an
//     overwrite.
//   - Neither archive invents a container: both are `tar -xzf` and `unzip` with no
//     help, because ADR 0011's argument for a backup is the same argument for a
//     portable vault, and the person who needs to recover a campaign is the person who
//     cannot get this binary working.

// epoch is the timestamp every archive entry carries.
//
// A zip stores a timestamp per entry, and a real one makes two exports of an
// unchanged vault differ in bytes for no reason a person can see. Fixed at the Unix
// epoch, so `git diff` after an export is a diff of *content* — which is the whole
// of the reason the archive is deterministic, and the reason a DM can commit it.
var epoch = time.Unix(0, 0).UTC()

// errCollision is a file that exists at the destination, whether the command found
// it before it started or between listing and writing.
//
// It is a sentinel because the two are the *same decision* — leave the DM's file
// alone — and the caller needs to skip rather than fail for the one it could not see
// coming. An error type would make the caller ask which of the two it was, and the
// answer does not change what it does.
var errCollision = errors.New("a file is already there")

// archiveMode is the mode every entry in an exported zip carries.
//
// `0644` for files, because a zip a DM hands to a player is read by them, and a
// `0600` entry extracted by somebody else's unzip is a file they cannot read. There
// is no executable and no key in an export, which is the point of it not being a
// data directory.
const archiveMode = 0o644

// runExport is `wiki export --zip`.
func runExport(ctx context.Context, args []string, stdout, _ io.Writer) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	opts := &exportOptions{}
	flags.StringVar(&opts.dataDir, "data-dir", "",
		"the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.StringVar(&opts.campaign, "campaign", "", "the campaign to export, by slug")
	flags.BoolVar(&opts.zip, "zip", false, "write a zip, which is the only format in v1")
	flags.StringVar(&opts.out, "out", "", "where to write it; stdout with - means the zip itself")

	if err := parseFlags(flags, args, "wiki export"); err != nil {
		return err
	}

	if !opts.zip {
		// `--zip` is required rather than defaulted, so that a `wiki export` with no
		// flags is a message about what to type rather than a file on a DM's disk
		// they did not ask for. The flag is a placeholder for another format, and a
		// placeholder that can be omitted is not one.
		return errors.New("wiki export: pass --zip; it is the only format in v1")
	}
	if strings.TrimSpace(opts.campaign) == "" {
		return errors.New("wiki export: which campaign: pass --campaign <slug>")
	}

	dir, err := datadir.Resolve(opts.dataDir)
	if err != nil {
		return fmt.Errorf("finding the data directory: %w", err)
	}

	source := filepath.Join(dir, "vault", opts.campaign)
	if info, statErr := os.Stat(source); statErr != nil || !info.IsDir() {
		return fmt.Errorf("no campaign %q under %s", opts.campaign, filepath.Join(dir, "vault"))
	}

	files, err := vaultFilesUnder(source)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("%s holds no files, so there is nothing to export", source)
	}

	if opts.out == "-" {
		if zipErr := writeZipTo(stdout, files); zipErr != nil {
			return fmt.Errorf("writing the archive: %w", zipErr)
		}
		return nil
	}

	// A path the DM chose, created with `O_EXCL`: an export that silently replaced a
	// file somebody had already put there is the same accident as a backup that
	// truncated an archive, and it is the one that loses work.
	out, err := os.OpenFile(opts.out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is the DM's
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s already exists; move it or name another --out", opts.out)
		}
		return fmt.Errorf("creating %s: %w", opts.out, err)
	}
	defer func() { _ = out.Close() }()

	if zipErr := writeZipTo(out, files); zipErr != nil {
		_ = out.Close()
		_ = os.Remove(opts.out)
		return fmt.Errorf("writing %s: %w", opts.out, zipErr)
	}

	_, err = fmt.Fprintf(stdout, "exported %d file(s) from %s to %s\n", len(files), source, opts.out)
	return err
}

// exportOptions is what `wiki export` takes.
type exportOptions struct {
	dataDir  string
	campaign string
	zip      bool
	out      string
}

// namedFile is one file to put in an archive: where it is now, and what it is called
// inside.
type namedFile struct {
	source string
	name   string
}

// vaultFilesUnder is every file under a vault directory, as archive names, sorted.
//
// The names are **slash-separated and rooted at the campaign's own directory**, so an
// export of `blackwater` unzips into a folder a DM can drop straight into Obsidian
// rather than into one they have to unpack by hand and rename.
func vaultFilesUnder(root string) ([]namedFile, error) {
	var files []namedFile

	err := filepath.Walk(root, func(found string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relative, relErr := filepath.Rel(root, found)
		if relErr != nil {
			return relErr
		}
		files = append(files, namedFile{
			source: found,
			name:   filepath.ToSlash(relative),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sorted by *archive name*, so the archive's order is the same on every machine
	// and every filesystem. `filepath.Walk` already sorts, but this is the property
	// being asserted rather than a side effect relied on.
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	return files, nil
}

// writeZipTo is the archive.
//
// **No directory entries.** A zip with directory entries has a different number of
// them on two machines whose directories were created in a different order, and the
// determinism above is the reason any of this file exists. `unzip` and Obsidian both
// create the directories they need from the file names.
func writeZipTo(w io.Writer, files []namedFile) error {
	archive := zip.NewWriter(w)

	for _, file := range files {
		contents, err := os.ReadFile(file.source) //nolint:gosec // the path is under the campaign's vault
		if err != nil {
			_ = archive.Close()
			return fmt.Errorf("reading %s: %w", file.source, err)
		}

		header := &zip.FileHeader{
			Name:     file.name,
			Method:   zip.Deflate,
			Modified: epoch,
		}
		// SetMode rather than `Mode:`, because a `FileHeader` written with a literal
		// `Mode` and no `CreatorVersion` records no permissions at all, and an entry
		// with no permissions extracts as a file some unzippers cannot read.
		header.SetMode(archiveMode)

		entry, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			return fmt.Errorf("adding %s to the archive: %w", file.name, err)
		}
		if _, err := entry.Write(contents); err != nil {
			_ = archive.Close()
			return fmt.Errorf("writing %s into the archive: %w", file.name, err)
		}
	}

	return archive.Close()
}

// runImport is `wiki import`, and the subcommand word is part of the name.
//
// It is a dispatcher rather than a command because `obsidian` is **not** a
// directory and the alternative — treating it as one — is a command that quietly
// imports whatever is in a folder called `obsidian`. A future `wiki import <other>`
// is a second case in a `switch`, and a second case is the shape a new format gets
// rather than a fourth positional.
func runImport(ctx context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) == 0 {
		return usageError{name: "import", flags: flagsForSubcommandHelp(nil)}
	}

	switch args[0] {
	case "obsidian":
		return runImportObsidian(ctx, args[1:], stdout)
	case "-h", "--help", "help":
		_, err := io.WriteString(stdout, `usage: wiki import obsidian <dir> --campaign <slug>

Copies an Obsidian vault into a campaign that already exists, and writes
nothing until it has shown you what it would do. Run it with --yes to copy.
`)
		return err
	default:
		return fmt.Errorf("wiki import: %q is not a format; it is obsidian", args[0])
	}
}

// The `obsidian` in the name is because the directory is an Obsidian vault. Nothing
// about the *files* is Obsidian-specific — a vault is a directory of markdown with
// optional frontmatter, and this application reads a documented subset of it
// (ADR 0005) — but calling it `wiki import <dir>` would promise a generality this
// milestone does not have, and a DM who tried it on a directory of PDFs deserves the
// message that says what was expected.
func runImportObsidian(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("import obsidian", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	opts := &importOptions{}
	flags.StringVar(&opts.dataDir, "data-dir", "",
		"the data directory ("+datadir.EnvVar+" overrides the default)")
	flags.StringVar(&opts.campaign, "campaign", "",
		"the campaign to import into, by slug; it must already exist")
	flags.BoolVar(&opts.yes, "yes", false, "copy anyway after showing what would happen")

	if err := parseFlagsAllowing(flags, args, "wiki import obsidian", 1); err != nil {
		return err
	}

	source := strings.TrimSpace(flags.Arg(0))
	if source == "" {
		return usageError{name: "wiki import obsidian", flags: flags, takes: "<dir>"}
	}

	dir, err := datadir.Resolve(opts.dataDir)
	if err != nil {
		return fmt.Errorf("finding the data directory: %w", err)
	}

	// **The campaign must exist.** This is the decision ADR 0024 §3 is about and it
	// is the one an importer gets wrong by default: `wiki sync` *creates* a campaign
	// for a folder that is not in the database, because a vault is something a DM can
	// make by creating a folder. An import is a different verb — somebody else's
	// directory arriving in a data directory — and a mistyped `--campaign` must not
	// leave an empty campaign behind.
	if _, _, openErr := openOneCampaign(ctx, dir, opts.campaign); openErr != nil {
		return fmt.Errorf("%w\n--  an import copies files into a campaign that already "+
			"exists; make one with `wiki sync` on an empty vault first", openErr)
	}

	candidates, skipped, err := importableFiles(source)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("%s holds no importable files%s", source, skippedSuffix(skipped))
	}

	destination := filepath.Join(dir, "vault", opts.campaign)
	collisions, collideErr := collisionsWith(destination, candidates)
	if collideErr != nil {
		return collideErr
	}

	// **Everything is shown before anything is written.** The confirmation is not a
	// formality: the destination is the DM's existing vault and a merge with no undo
	// is a merge nobody should agree to by accident.
	if _, said := fmt.Fprintf(stdout, "importing %d file(s) from %s into %s\n\n",
		len(candidates), source, destination); said != nil {
		return said
	}
	for _, name := range candidates {
		if _, said := fmt.Fprintf(stdout, "  %s\n", name); said != nil {
			return said
		}
	}
	if len(collisions) > 0 {
		if _, said := fmt.Fprintf(stdout, "\n%d file(s) are already there and will NOT "+
			"be overwritten:\n", len(collisions)); said != nil {
			return said
		}
		for _, name := range collisions {
			if _, said := fmt.Fprintf(stdout, "  %s\n", name); said != nil {
				return said
			}
		}
	}

	if len(skipped) > 0 {
		if _, said := fmt.Fprintf(stdout, "\n%d file(s) are not pages and will be "+
			"skipped%s:\n", len(skipped), skippedSuffix(skipped)); said != nil {
			return said
		}
		for _, name := range skipped {
			if _, said := fmt.Fprintf(stdout, "  %s\n", name); said != nil {
				return said
			}
		}
	}

	if !opts.yes {
		// The non-zero exit is the point. A DM who reads this and types the command
		// again with `--yes` has read it; a DM who has not has lost nothing, because
		// nothing was written.
		return errors.New("nothing was written; re-run with --yes to copy them")
	}

	copied, copyErr := copyInto(source, destination, candidates, collisions)
	if copyErr != nil {
		return copyErr
	}

	_, said := fmt.Fprintf(stdout, "\ncopied %d file(s). Run `wiki sync --campaign %s` to "+
		"read them into the index, and `wiki users new <name>` to mint a link.\n",
		copied, opts.campaign)
	return said
}

// importOptions is what `wiki import obsidian` takes.
type importOptions struct {
	dataDir  string
	campaign string
	yes      bool
}

// importableFiles is what would be copied, and what would be skipped.
//
// **The two halves are returned together** because the interesting output of this
// command is the second one: a DM whose vault has a canvas file in it deserves to be
// told the canvas file was not copied, rather than to find out next week when a note
// they expected is missing.
func importableFiles(source string) (importable, skipped []string, err error) {
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, nil, fmt.Errorf("reading %s: %w", source, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			// A nested directory is walked, because an Obsidian vault is a tree and
			// `locations/rivergate.md` is the ordinary case.
			nested, nestedSkipped, walkErr := importableFiles(filepath.Join(source, entry.Name()))
			if walkErr != nil {
				return nil, nil, walkErr
			}
			for _, one := range nested {
				importable = append(importable, filepath.ToSlash(entry.Name())+"/"+one)
			}
			skipped = append(skipped, nestedSkipped...)
			continue
		}

		relative := entry.Name()

		// Not a markdown file: an attachment is a page's picture and belongs in
		// `_attachments/`, and anything else is not this application's business. It is
		// *not* an error — a vault is full of PDFs and PNGs — and it is listed as
		// skipped rather than copied.
		if !strings.HasSuffix(strings.ToLower(relative), ".md") {
			skipped = append(skipped, relative)
			continue
		}

		// The path rules, from `internal/vault`, applied to the name with the
		// extension removed. A file whose path the application could not index is a
		// file that would sit in the vault forever, invisible, and a DM would think
		// the import lost it.
		pagePath := strings.TrimSuffix(relative, filepath.Ext(relative))
		if _, checkErr := vault.CheckPagePath(filepath.ToSlash(pagePath)); checkErr != nil {
			skipped = append(skipped, relative+" ("+checkErr.Error()+")")
			continue
		}

		importable = append(importable, relative)
	}

	sort.Strings(importable)
	sort.Strings(skipped)
	return importable, skipped, nil
}

// collisionsWith is which of the candidates already exist at the destination.
//
// A collision is a **refusal**, not a resolution, and ADR 0024 §4 is the reason: the
// safe answer to "a file is already there" is to make the DM look, and a `--force`
// that means "do it anyway" is a flag a DM uses before reading the list.
func collisionsWith(destination string, candidates []string) ([]string, error) {
	var collisions []string

	for _, name := range candidates {
		target := filepath.Join(destination, filepath.FromSlash(name))
		switch _, err := os.Stat(target); {
		case err == nil:
			collisions = append(collisions, name)
		case errors.Is(err, os.ErrNotExist):
		default:
			return nil, fmt.Errorf("checking %s: %w", target, err)
		}
	}

	return collisions, nil
}

// copyInto copies the candidates that are not already there, and returns how many.
//
// `from` is the directory that was imported and `into` is the campaign's vault, and
// both are needed because the copy is from one to the other — which is the thing the
// first draft of this function got wrong, by reading and writing the same path and
// therefore reporting ten copies and moving nothing.
//
// The collision check is **repeated here** rather than trusted from the earlier one.
// Between the list a DM read and this line nothing else is *expected* to be writing,
// but the command said it would not overwrite, and the only way to keep that promise
// against a race is to make the write itself exclusive rather than to check and then
// hope.
func copyInto(from, into string, candidates, collisions []string) (int, error) {
	// The collisions are skipped rather than refused, because **the rest of the
	// import is still what the DM asked for.** A vault where one page has been
	// re-written in Obsidian and forty are new should bring the forty in, and the
	// first version of this function refused the whole command over the one — which is
	// a command a DM runs twice and then cannot get past.
	refused := make(map[string]bool, len(collisions))
	for _, name := range collisions {
		refused[name] = true
	}

	copied := 0

	for _, name := range candidates {
		if refused[name] {
			continue
		}

		target := filepath.Join(into, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return copied, fmt.Errorf("creating %s: %w", filepath.Dir(target), err)
		}

		if err := copyFileExclusive(target, filepath.Join(from, filepath.FromSlash(name))); err != nil {
			if errors.Is(err, errCollision) {
				// A file that appeared between the list and this line, and the list is
				// a moment old. Skipping it is the same decision as the one above,
				// taken later.
				continue
			}
			return copied, err
		}
		copied++
	}

	return copied, nil
}

// copyFileExclusive is the one place an import writes, and it creates the destination
// with `O_EXCL`.
//
// The mode is `0600` rather than the `0644` an export uses, and the difference is the
// direction: an export is a file leaving the DM's machine and a file being read by
// somebody else, while an import is a file arriving *in* the DM's campaign. The vault
// is the DM's and a file nobody else should be able to write is the right default for
// a directory the application writes into.
func copyFileExclusive(target, source string) error {
	in, err := os.Open(source) //nolint:gosec // the path is the directory the DM named
	if err != nil {
		return fmt.Errorf("reading %s: %w", source, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // the path is under the campaign's vault
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// A race, and this is the collision branch rather than an overwrite: the
			// list a DM read is a moment old, and this is what happened in it. The
			// sentinel is what lets the caller *skip* it rather than fail, which is
			// the decision `copyInto` makes for the collisions it already knows about.
			return fmt.Errorf("%s: %w", target, errCollision)
		}
		return fmt.Errorf("creating %s: %w", target, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("writing %s: %w", target, err)
	}
	return nil
}

// skippedSuffix is the tail of the "and N were skipped" sentence, and it is a
// function because the sentence is the whole of the message a DM reads when a file
// they expected is not in the import.
func skippedSuffix(skipped []string) string {
	if len(skipped) == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d of them)", len(skipped))
}
