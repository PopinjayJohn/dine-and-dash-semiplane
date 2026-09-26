package vault_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

func TestArchiveRevision(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	first := parsed(t, "---\ntitle: Rivergate\n---\n\nA fortified town.\n")
	at := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

	name, err := v.ArchiveRevision(pagePath, 1, at, first)
	if err != nil {
		t.Fatalf("ArchiveRevision: %v", err)
	}

	// The layout is ADR 0005's, and the timestamp has no colons in it because a
	// colon is a forbidden character in a filename on Windows.
	want := "_history/locations/rivergate/1-2026-02-14T19-03-00Z.md"
	if name != want {
		t.Errorf("archived as %q, want %q", name, want)
	}

	// The bytes are the whole file, frontmatter and all: a revision is a
	// snapshot, not a diff.
	if _, statErr := os.Stat(filepath.Join(v.Root(), filepath.FromSlash(name))); statErr != nil {
		t.Errorf("the revision is not where it says: %v", statErr)
	}

	// And it is not a page: the history directory is not in the page list, so a
	// revision can never be edited as though it were the page it is a copy of.
	pages, err := v.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, page := range pages {
		if strings.HasPrefix(page, "_history") {
			t.Errorf("List returned the revision %q as a page", page)
		}
	}
}

func TestRevisions(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	// A page with no history has none, rather than an error: a vault that has
	// never been edited in Obsidian has no _history at all.
	none, err := v.Revisions(pagePath)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a page with no history has %d revisions", len(none))
	}

	base := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)
	// Written out of order, and two of them in the same second, so the order
	// has to come from the revision number rather than the clock.
	for _, rev := range []int{3, 1, 2, 4} {
		if _, archiveErr := v.ArchiveRevision(pagePath, rev, base, parsed(t, "Revision.\n")); archiveErr != nil {
			t.Fatalf("ArchiveRevision(%d): %v", rev, archiveErr)
		}
	}

	got, err := v.Revisions(pagePath)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}

	want := []string{
		"_history/locations/rivergate/1-2026-02-14T19-03-00Z.md",
		"_history/locations/rivergate/2-2026-02-14T19-03-00Z.md",
		"_history/locations/rivergate/3-2026-02-14T19-03-00Z.md",
		"_history/locations/rivergate/4-2026-02-14T19-03-00Z.md",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Revisions returned %v, want %v", got, want)
	}

	// Revisions of one page are not the revisions of another, even though the
	// directory structure invites the mistake.
	other, err := v.Revisions("npcs/garros-ironbar")
	if err != nil {
		t.Fatalf("Revisions of another page: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("a page with no history has %d revisions", len(other))
	}
}

func TestRestoreRevision(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	const before = "---\ntitle: Rivergate\n---\n\nA fortified town, before the flood.\n"
	at := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)
	name, err := v.ArchiveRevision(pagePath, 1, at, parsed(t, before))
	if err != nil {
		t.Fatalf("ArchiveRevision: %v", err)
	}
	revised := filepath.Base(filepath.FromSlash(name))

	doc, err := v.RestoreRevision(pagePath, revised)
	if err != nil {
		t.Fatalf("RestoreRevision: %v", err)
	}
	if doc.Title() != "Rivergate" {
		t.Errorf("the restored page has title %q", doc.Title())
	}
	if doc.Body() != "A fortified town, before the flood.\n" {
		t.Errorf("the restored body is %q", doc.Body())
	}

	// Restoring it is a write to the page, and the write goes through the same
	// atomic sequence as any other.
	if writeErr := v.Write(pagePath, doc); writeErr != nil {
		t.Fatalf("writing the restored page: %v", writeErr)
	}
	page, readErr := v.Read(pagePath)
	if readErr != nil {
		t.Fatalf("Read: %v", readErr)
	}
	if page.Body() != "A fortified town, before the flood.\n" {
		t.Errorf("the restored page has body %q", page.Body())
	}
}

func TestArchiveRevisionRefuses(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"
	doc := parsed(t, "A fortified town.\n")
	at := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

	tests := map[string]struct {
		rev  int
		doc  *vault.Document
		path string
		want string
	}{
		"revision zero, which would be a thing that never happened": {
			rev:  0,
			doc:  doc,
			path: pagePath,
			want: "numbered from 1",
		},
		"a negative revision": {
			rev:  -1,
			doc:  doc,
			path: pagePath,
			want: "numbered from 1",
		},
		"no document": {
			rev:  1,
			doc:  nil,
			path: pagePath,
			want: "no document to archive",
		},
		"a page path that is not one": {
			rev:  1,
			doc:  doc,
			path: "../outside",
			want: `has a ".." segment`,
		},
		"a page inside the history directory": {
			rev:  1,
			doc:  doc,
			path: "_history/locations/rivergate/1",
			want: "reserved directory",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := v.ArchiveRevision(tt.path, tt.rev, at, tt.doc)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ArchiveRevision(%q, %d) = %v, want one containing %q", tt.path, tt.rev, err, tt.want)
			}
		})
	}
}

func TestRestoreRevisionRefuses(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	tests := map[string]struct {
		page     string
		revision string
		want     string
	}{
		"a revision that is not there": {
			page:     pagePath,
			revision: "1-2026-02-14T19-03-00Z.md",
			want:     "not found",
		},
		"a traversal in the revision name": {
			page:     pagePath,
			revision: "../../../etc/passwd",
			want:     "is a path",
		},
		"a path where a name belongs": {
			page:     pagePath,
			revision: "_history/locations/rivergate/1-2026-02-14T19-03-00Z.md",
			want:     "is a path",
		},
		"a page path that is not one": {
			page:     "../outside",
			revision: "1-2026-02-14T19-03-00Z.md",
			want:     `has a ".." segment`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := v.RestoreRevision(tt.page, tt.revision)
			if err == nil {
				t.Fatalf("RestoreRevision(%q, %q) was accepted", tt.page, tt.revision)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q, want one containing %q", err, tt.want)
			}
		})
	}

	// A missing revision is a not-found, which a caller can match, rather than
	// any other failure.
	_, err := v.RestoreRevision(pagePath, "1-2026-02-14T19-03-00Z.md")
	if !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("RestoreRevision of a missing revision = %v, want an error matching ErrNotFound", err)
	}
}

func TestWriteAndReadAttachment(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())

	// A small PNG header is enough: nothing here looks inside the bytes.
	const image = "\x89PNG\r\n\x1a\n not really a png"

	reference, err := v.WriteAttachment("map-rivergate.png", []byte(image))
	if err != nil {
		t.Fatalf("WriteAttachment: %v", err)
	}
	if want := "_attachments/map-rivergate.png"; reference != want {
		t.Errorf("WriteAttachment returned %q, want %q", reference, want)
	}

	// A page refers to it both ways, and both read the same bytes: the way a
	// page writes it, and the way a DM names a file in the folder.
	for _, in := range []string{"map-rivergate.png", "_attachments/map-rivergate.png"} {
		got, readErr := v.ReadAttachment(in)
		if readErr != nil {
			t.Fatalf("ReadAttachment(%q): %v", in, readErr)
		}
		if string(got) != image {
			t.Errorf("ReadAttachment(%q) = %q, want %q", in, got, image)
		}
	}

	names, err := v.Attachments()
	if err != nil {
		t.Fatalf("Attachments: %v", err)
	}
	if len(names) != 1 || names[0] != "map-rivergate.png" {
		t.Errorf("Attachments returned %v, want the one file", names)
	}

	if deleteErr := v.DeleteAttachment("map-rivergate.png"); deleteErr != nil {
		t.Fatalf("DeleteAttachment: %v", deleteErr)
	}
	if _, err := v.ReadAttachment("map-rivergate.png"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("ReadAttachment after a delete = %v, want an error matching ErrNotFound", err)
	}
	if err := v.DeleteAttachment("map-rivergate.png"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("a second DeleteAttachment = %v, want an error matching ErrNotFound", err)
	}
}

func TestAttachmentRefuses(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	// A file outside the vault, to be sure a refused reference does not reach it.
	outside := filepath.Join(t.TempDir(), "secrets.png")
	if err := os.WriteFile(outside, []byte("not an attachment\n"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	// A reference is what a page wrote and can be a path; a name is what a DM
	// uploaded and cannot. Both are refused, and each is refused for its own
	// reason, so the two columns of expectations differ.
	tests := map[string]struct {
		reference string
		wantRead  string
		wantWrite string
	}{
		"empty": {
			reference: "",
			wantRead:  "reference is empty",
			wantWrite: "attachment name is empty",
		},
		"one level up": {
			reference: "../secrets.png",
			wantRead:  `has a ".." segment`,
			wantWrite: "contains a separator",
		},
		"one level up as a bare name": {
			reference: "..",
			wantRead:  "is not a file name",
			wantWrite: "is not a file name",
		},
		"an absolute path": {
			reference: "/etc/passwd",
			wantRead:  "is absolute",
			wantWrite: "contains a separator",
		},
		"a Windows path": {
			reference: `C:\secrets.png`,
			wantRead:  "contains a separator",
			wantWrite: "contains a separator",
		},
		"a backslash inside a path": {
			reference: `_attachments\map.png`,
			wantRead:  "contains a separator",
			wantWrite: "contains a separator",
		},
		"into the revision directory": {
			reference: "_history/locations/rivergate/1.md",
			wantRead:  "reserved directory",
			wantWrite: "contains a separator",
		},
		"into Obsidian's directory": {
			reference: ".obsidian/app.json",
			wantRead:  "reserved directory",
			wantWrite: "contains a separator",
		},
		"a device name": {
			reference: "nul.png",
			wantRead:  "device name",
			wantWrite: "device name",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := v.ReadAttachment(tt.reference); err == nil || !strings.Contains(err.Error(), tt.wantRead) {
				t.Errorf("ReadAttachment(%q) = %v, want one containing %q", tt.reference, err, tt.wantRead)
			}

			if _, err := v.WriteAttachment(tt.reference, []byte("x")); err == nil || !strings.Contains(err.Error(), tt.wantWrite) {
				t.Errorf("WriteAttachment(%q) = %v, want one containing %q", tt.reference, err, tt.wantWrite)
			}
		})
	}

	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the file outside the vault is gone: %v", err)
	}
}

func TestResolveAttachmentRefusesASymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secrets.png"), []byte("not an attachment\n"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "_attachments")); err != nil {
		t.Skipf("this machine will not let the test make a symlink: %v", err)
	}

	v := newVault(t, root)

	// The attachments directory is a way out of the vault, and a reference to a
	// name inside it is refused rather than read.
	_, err := v.ReadAttachment("secrets.png")
	if err == nil || !strings.Contains(err.Error(), "outside the vault") {
		t.Errorf("ReadAttachment through a symlinked directory = %v, want a refusal", err)
	}

	if _, err := v.WriteAttachment("map.png", []byte("x")); err == nil {
		t.Error("WriteAttachment through a symlinked directory was accepted")
	}

	if _, err := os.Stat(filepath.Join(outside, "secrets.png")); err != nil {
		t.Errorf("the file outside the vault is gone: %v", err)
	}
}

func TestAttachmentsIgnoresTemporaryFiles(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())

	if _, err := v.WriteAttachment("map-rivergate.png", []byte("an image")); err != nil {
		t.Fatalf("WriteAttachment: %v", err)
	}

	// A crash can leave a temporary file beside an attachment, and it is not
	// something a DM ever uploaded.
	if err := os.WriteFile(filepath.Join(v.Root(), "_attachments", ".wiki-tmp-1-1"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing a temporary file: %v", err)
	}

	names, err := v.Attachments()
	if err != nil {
		t.Fatalf("Attachments: %v", err)
	}
	if len(names) != 1 || names[0] != "map-rivergate.png" {
		t.Errorf("Attachments returned %v, want only the uploaded file", names)
	}
}
