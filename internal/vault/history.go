package vault

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Revisions live in `_history/<path>/<n>-<rfc3339>.md`, which is close enough to
// the Obsidian File Recovery plugin's layout to be recognised (ADR 0005).
//
// They are the *DM's* history, for edits made in Obsidian or by hand. A page's
// history as this application sees it is a row in page_revisions, and M1's
// store comment says the same thing: two histories, and the files win.

// historyLayout is the timestamp in a revision's file name.
//
// RFC 3339 with the colons replaced, because a colon is a forbidden character
// in a filename on Windows and a file a DM cannot open on the platform they
// wrote it on is not a revision. Everything else about the format is the
// timestamp the clock gave, so a revision directory reads as a list of moments.
const historyLayout = "2006-01-02T15-04-05Z"

// ArchiveRevision writes a page's file into its history directory and returns
// the vault-relative name it was written under.
//
// The revision number is the caller's, because the store knows it: the numbers
// come from page_revisions, and a vault that renumbered them would disagree with
// the database about the order of a page's history. Two revisions in the same
// second are told apart by the number, which is the point of having it first.
func (v *Vault) ArchiveRevision(pagePath string, rev int, at time.Time, doc *Document) (string, error) {
	if doc == nil {
		return "", fmt.Errorf("%w: there is no document to archive for %s", ErrEncoding, pagePath)
	}
	if rev < 1 {
		return "", fmt.Errorf("%w: a revision is numbered from 1, not %d", ErrPath, rev)
	}

	name, err := v.pageFile(pagePath)
	if err != nil {
		return "", err
	}

	data, err := doc.Bytes()
	if err != nil {
		return "", fmt.Errorf("archiving %s: %w", pagePath, err)
	}

	// The directory is the page's path inside the history directory, so every
	// revision of a page is together and the layout is obvious at a glance.
	stem := strings.TrimSuffix(name, pageExtension)
	revision := fmt.Sprintf("%s/%s/%d-%s%s", historyDir, stem, rev, at.UTC().Format(historyLayout), pageExtension)

	if err := v.checkInside(revision); err != nil {
		return "", err
	}

	if err := v.writeAtomic(revision, data); err != nil {
		return "", err
	}

	return revision, nil
}

// Revisions returns the names of a page's archived revisions, oldest first.
//
// The order is the revision number's, not the filename's, because the number is
// the store's and the timestamp is only as good as the clock that wrote it. A
// revision whose name does not parse is left where it is and reported as an
// error rather than sorted into a place it might not belong.
func (v *Vault) Revisions(pagePath string) ([]string, error) {
	stem, err := v.pageFile(pagePath)
	if err != nil {
		return nil, err
	}

	dir := historyDir + "/" + strings.TrimSuffix(stem, pageExtension)

	names, err := v.readDir(dir)
	if err != nil {
		return nil, err
	}

	revisions := make([]string, 0, len(names))
	for _, name := range names {
		if !strings.HasSuffix(name, pageExtension) {
			continue
		}
		revisions = append(revisions, dir+"/"+name)
	}

	slices.Sort(revisions)
	return revisions, nil
}

// RestoreRevision returns the bytes of an archived revision, which is what a
// caller needs to put one back: a Document parsed from the revision file, and
// then written to the page.
func (v *Vault) RestoreRevision(pagePath, revision string) (*Document, error) {
	if _, err := v.pageFile(pagePath); err != nil {
		// The page path is checked even though the revision is named: a
		// restore is a write to a page, and the page is what has to be
		// checkable.
		return nil, err
	}

	name, err := v.revisionFile(pagePath, revision)
	if err != nil {
		return nil, err
	}

	data, err := v.readFile(name)
	if isNotExist(err) {
		return nil, fmt.Errorf("revision %q of page %q: %w", revision, pagePath, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("reading revision %q of page %q: %w", revision, pagePath, err)
	}

	doc, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("reading revision %q of page %q: %w", revision, pagePath, err)
	}
	return doc, nil
}

// revisionFile checks a revision name the way a reference is checked: it is a
// path the caller supplies, and it has to be one inside this page's history
// directory and nowhere else.
func (v *Vault) revisionFile(pagePath, revision string) (string, error) {
	stem, err := v.pageFile(pagePath)
	if err != nil {
		return "", err
	}
	dir := historyDir + "/" + strings.TrimSuffix(stem, pageExtension)

	// A revision is named by its file name. Accepting a path here would let a
	// caller ask for a revision of a page while naming a different page's.
	if strings.ContainsAny(revision, `/\`) {
		return "", fmt.Errorf("%w: %q is a path; a revision is named by its file name", ErrPath, revision)
	}

	name, err := CheckFileName(revision)
	if err != nil {
		return "", err
	}

	checked := dir + "/" + name
	if err := v.checkInside(checked); err != nil {
		return "", err
	}

	return checked, nil
}
