package vault_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

func TestWriteThenRead(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	original := parsed(t, "---\ntitle: Rivergate\ntype: location\n---\n\nA fortified town.\n")
	if err := v.Write(pagePath, original); err != nil {
		t.Fatalf("Write: %v", err)
	}

	read, err := v.Read(pagePath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// A document that was only read is the file, byte for byte.
	readBytes, err := read.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	originalBytes, err := original.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if string(readBytes) != string(originalBytes) {
		t.Errorf("the page came back as %q, want %q", readBytes, originalBytes)
	}

	if read.Title() != "Rivergate" || read.PageType() != domain.PageTypeLocation {
		t.Errorf("the page came back with title %q and type %q", read.Title(), read.PageType())
	}

	// The directories the write needed were made, and the file is where the
	// path says it is.
	if _, err := os.Stat(filepath.Join(v.Root(), "locations", "rivergate.md")); err != nil {
		t.Errorf("the file is not where the page path says: %v", err)
	}
}

func TestWriteThenChangeThenRead(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "characters/aria"

	if err := v.Write(pagePath, parsed(t, "---\ntitle: Aria\n---\n\nA halfling ranger.\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	doc, err := v.Read(pagePath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if setErr := doc.Set(vault.KeyTitle, "Aria, after the flood"); setErr != nil {
		t.Fatalf("Set: %v", setErr)
	}
	if writeErr := v.Write(pagePath, doc); writeErr != nil {
		t.Fatalf("Write after a change: %v", writeErr)
	}

	reread, err := v.Read(pagePath)
	if err != nil {
		t.Fatalf("Read after a change: %v", err)
	}
	if reread.Title() != "Aria, after the flood" {
		t.Errorf("the page came back with title %q", reread.Title())
	}
	if reread.Body() != "A halfling ranger.\n" {
		t.Errorf("the body came back as %q", reread.Body())
	}
}

func TestReadRefusesAMissingPage(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())

	_, err := v.Read("locations/rivergate")
	if !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("Read of a missing page = %v, want an error matching ErrNotFound", err)
	}

	exists, err := v.Exists("locations/rivergate")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("Exists reported a page that is not there")
	}
}

func TestExistsReportsAFileThatIsNotAPage(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	// Written with os rather than the vault, because the point is a file the
	// application would refuse to parse.
	if err := os.MkdirAll(filepath.Join(v.Root(), "locations"), 0o700); err != nil {
		t.Fatalf("creating the directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(v.Root(), "locations", "rivergate.md"),
		[]byte("---\n- not a set of keys\n---\n"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	// The file exists and is not a page. "Does this vault have this page" has
	// to be able to say so, rather than the answer being yes and the truth
	// turning up three layers away.
	if _, err := v.Exists(pagePath); err == nil {
		t.Error("Exists reported true for a file whose frontmatter is not usable")
	}

	if _, err := v.Read(pagePath); err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Errorf("Read of an unusable page = %v, want it to say the frontmatter is not usable", err)
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	if err := v.Write(pagePath, parsed(t, "A fortified town.\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if err := v.Delete(pagePath); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, err := v.Read(pagePath); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("Read after a delete = %v, want an error matching ErrNotFound", err)
	}

	// Deleting twice says so, rather than claiming it worked: the page is
	// already gone from every read.
	if err := v.Delete(pagePath); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("a second Delete = %v, want an error matching ErrNotFound", err)
	}

	// And the directory the page was the only one in is gone too, because a DM
	// who deletes the last page of a section should not be left with an empty
	// folder in Obsidian's tree.
	if _, err := os.Stat(filepath.Join(v.Root(), "locations")); !os.IsNotExist(err) {
		t.Errorf("the directory the deleted page was alone in is still there: %v", err)
	}
}

func TestDeleteRefusesAFileOutsideTheVault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "campaigns.md")
	if err := os.WriteFile(outside, []byte("not the DM's campaign\n"), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	v := newVault(t, root)

	// A traversal is refused, and so is a page path that happens to be
	// perfectly valid and points at a file the vault does not own.
	for _, pagePath := range []string{"../outside", "/etc/passwd", "../../etc/passwd"} {
		if err := v.Delete(pagePath); !errors.Is(err, vault.ErrPath) {
			t.Errorf("Delete(%q) = %v, want an error matching ErrPath", pagePath, err)
		}
	}

	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the file outside the vault is gone: %v", err)
	}
}

func TestList(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	v := newVault(t, root)

	pages := map[string]string{
		"sessions/2026-02-14-dragon-heist": "A heist.\n",
		"locations/rivergate":              "A fortified town.\n",
		"characters/aria":                  "A halfling ranger.\n",
		"campaign":                         "The Blackwater.\n",
	}
	for pagePath, body := range pages {
		if err := v.Write(pagePath, parsed(t, body)); err != nil {
			t.Fatalf("Write(%q): %v", pagePath, err)
		}
	}

	// Things that are not pages and must not be listed as pages.
	directories := []string{"_attachments", "_history/locations/rivergate", ".obsidian"}
	for _, dir := range directories {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "ignored.md"), []byte("not a page\n"), 0o600); err != nil {
			t.Fatalf("writing into %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("a file that is not markdown\n"), 0o600); err != nil {
		t.Fatalf("writing a text file: %v", err)
	}

	got, err := v.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := []string{"campaign", "characters/aria", "locations/rivergate", "sessions/2026-02-14-dragon-heist"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("List returned %v, want %v", got, want)
	}

	// The list is the same every time, which is what lets a caller diff two of
	// them.
	again, err := v.List()
	if err != nil {
		t.Fatalf("List again: %v", err)
	}
	if strings.Join(again, "|") != strings.Join(got, "|") {
		t.Errorf("the second List returned %v, the first %v", again, got)
	}
}

func TestReadAll(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())

	for pagePath, body := range map[string]string{
		"locations/rivergate": "---\ntitle: Rivergate\n---\n\nA fortified town.\n",
		"npcs/garros-ironbar": "---\ntitle: Garros Ironbar\n---\n\nA toll-collector.\n",
	} {
		if err := v.Write(pagePath, parsed(t, body)); err != nil {
			t.Fatalf("Write(%q): %v", pagePath, err)
		}
	}

	pages, err := v.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if len(pages) != 2 {
		t.Fatalf("ReadAll returned %d pages, want 2", len(pages))
	}
	if pages["locations/rivergate"].Title() != "Rivergate" {
		t.Errorf("the first page came back with title %q", pages["locations/rivergate"].Title())
	}
	if pages["npcs/garros-ironbar"].Title() != "Garros Ironbar" {
		t.Errorf("the second page came back with title %q", pages["npcs/garros-ironbar"].Title())
	}
}

func TestWriteRefusesADocumentThatDoesNotExist(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())

	if err := v.Write("locations/rivergate", nil); err == nil {
		t.Error("Write accepted a nil document")
	}
}

func TestContentHashIsOfTheBytesOnDisk(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())
	const pagePath = "locations/rivergate"

	body := "---\ntitle: Rivergate\n---\n\nA fortified town.\n"
	if err := v.Write(pagePath, parsed(t, body)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// The hash the indexer stores and the hash a reindex computes have to be
	// the same number, or every page looks changed on every run.
	fromFile, err := v.ReadBytes(pagePath)
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	doc, err := v.Read(pagePath)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	docHash, err := doc.ContentHash()
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}

	if want := vault.Hash(fromFile); docHash != want {
		t.Errorf("the document hashes to %q and the file to %q", docHash, want)
	}
	if docHash != vault.Hash([]byte(body)) {
		t.Errorf("the hash is not of the file: %q", docHash)
	}
}

func parsed(t *testing.T, in string) *vault.Document {
	t.Helper()

	doc, err := vault.Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse(%q): %v", in, err)
	}
	return doc
}
