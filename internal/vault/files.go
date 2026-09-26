package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
)

// The filesystem half of a Vault: read a page, write it, delete it, list what
// is there.
//
// Every one of them goes through the directory handle, so none of them can be
// talked into touching a file outside the campaign directory, and every write
// goes through the atomic sequence in atomic.go, so a reader never sees half a
// page.

// Read returns a page's file, parsed.
//
// A missing file is ErrNotFound rather than a failure, because "this page is
// not in the vault" is the answer a sync pass expects for most of the pages it
// asks about.
func (v *Vault) Read(pagePath string) (*Document, error) {
	data, err := v.ReadBytes(pagePath)
	if err != nil {
		return nil, err
	}

	doc, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", pagePath, err)
	}
	return doc, nil
}

// ReadBytes returns a page's bytes without parsing them, which is what the
// indexer wants when all it is going to do is compare hashes.
func (v *Vault) ReadBytes(pagePath string) ([]byte, error) {
	name, err := v.pageFile(pagePath)
	if err != nil {
		return nil, err
	}

	data, err := v.root.ReadFile(name)
	if isNotExist(err) {
		return nil, notFound(pagePath)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", pagePath, err)
	}
	return data, nil
}

// Write replaces a page's file with the document's bytes.
//
// An unmodified document is still written, because a caller asked for it to be
// and refusing would make the caller's decision harder to express. A caller
// that wants to know whether anything changed asks the document: that is what
// Modified is for.
func (v *Vault) Write(pagePath string, doc *Document) error {
	if doc == nil {
		return fmt.Errorf("%w: there is no document to write to %s", ErrEncoding, pagePath)
	}

	name, err := v.pageFile(pagePath)
	if err != nil {
		return err
	}

	data, err := doc.Bytes()
	if err != nil {
		return fmt.Errorf("writing %s: %w", pagePath, err)
	}

	return v.writeAtomic(name, data)
}

// Delete removes a page's file.
//
// It is a real delete, not an archive: the archive is a row in the database
// (M9), and the file is what the DM asked to have gone. A missing file is
// ErrNotFound, because "delete this" on a page that is not there is a mistake
// worth reporting.
func (v *Vault) Delete(pagePath string) error {
	name, err := v.pageFile(pagePath)
	if err != nil {
		return err
	}

	if err := v.root.Remove(name); err != nil {
		if isNotExist(err) {
			return notFound(pagePath)
		}
		return fmt.Errorf("deleting %s: %w", pagePath, err)
	}

	// Removing the file is the important half; removing the directories it left
	// empty is tidiness, and a failure to tidy is not a failure to delete.
	v.pruneEmptyParents(name)

	return nil
}

// Exists reports whether a page's file is there, and whether it parses.
//
// The parse is part of the answer on purpose: a file that exists and is not a
// page is not a page, and a caller that asked "does this vault have this page"
// needs to hear that rather than find out later.
func (v *Vault) Exists(pagePath string) (bool, error) {
	if _, err := v.Read(pagePath); err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// List returns every page path in the vault, sorted, without extensions.
//
// The reserved directories are skipped rather than filtered afterwards, and a
// file that does not end in .md is not a page because a vault is markdown. The
// order is the filesystem's, sorted, because a caller diffing two lists of
// pages needs them in the same order every time.
func (v *Vault) List() ([]string, error) {
	paths := []string{}

	err := fs.WalkDir(v.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walking %s: %w", name, err)
		}

		// The reserved directories are skipped rather than filtered afterwards.
		// A page cannot be written inside them, so nothing in them is a page,
		// and a revision that appeared in the page list would be a page the
		// application would then let a DM edit.
		if first, _, _ := strings.Cut(name, "/"); reservedDirs[first] {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if entry.IsDir() || !strings.HasSuffix(name, pageExtension) {
			return nil
		}
		paths = append(paths, strings.TrimSuffix(name, pageExtension))
		return nil
	})
	if err != nil {
		return nil, err
	}

	slices.Sort(paths)
	return paths, nil
}

// ReadAll returns every page in the vault, parsed, keyed by page path.
//
// It is here because "index the whole vault" and "reindex from scratch" are both
// things this application has to do, and neither of them should have to
// re-implement the walk.
func (v *Vault) ReadAll() (map[string]*Document, error) {
	paths, err := v.List()
	if err != nil {
		return nil, err
	}

	pages := make(map[string]*Document, len(paths))
	for _, pagePath := range paths {
		doc, err := v.Read(pagePath)
		if err != nil {
			return nil, err
		}
		pages[pagePath] = doc
	}
	return pages, nil
}

// notFound is the package's own not-found error, which a caller can match and
// which carries the page path with it.
func notFound(pagePath string) error {
	return fmt.Errorf("page %q: %w", pagePath, ErrNotFound)
}

// pruneEmptyParents removes the directories a delete left empty, up to but not
// including the vault root. A DM who deletes the last page of a section does
// not expect an empty directory to be left behind in Obsidian's tree.
func (v *Vault) pruneEmptyParents(name string) {
	// segments[:i] is the i-th directory above the file, and i stops at one so
	// that the vault root itself is never a candidate.
	segments := strings.Split(name, "/")
	for i := len(segments) - 1; i >= 1; i-- {
		dir := strings.Join(segments[:i], "/")

		entries, err := fs.ReadDir(v.root.FS(), dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := v.root.Remove(dir); err != nil {
			return
		}
	}
}

// sweepTemporaries removes the temporary files a previous crash left anywhere
// in the vault.
//
// It runs when a Vault is opened, and that is the only place it can run without
// a race: a temporary file found then belongs to a process that is no longer
// running, because one data directory has one server (ADR 0011). Sweeping at
// the start of every write instead would delete a live write's temporary file
// out from under it, and the rename would fail -- a loud error rather than
// corruption, but still an error the caller did nothing to deserve.
//
// Without this a crashed write would leave a file in the DM's vault for ever. It
// is invisible to Obsidian and to the page list, and nothing would ever notice
// it, which is the same as saying it is not cleaned up.
func (v *Vault) sweepTemporaries() {
	_ = fs.WalkDir(v.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasPrefix(entry.Name(), tempPrefix) {
			return nil //nolint:nilerr // a tree we cannot read has nothing to sweep
		}
		// Through the handle, so the removal is resolved inside the vault
		// rather than against a name a walk handed us.
		_ = v.root.Remove(name)
		return nil
	})
}
