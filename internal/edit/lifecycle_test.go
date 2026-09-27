package edit_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/edit"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # The rename

// TestARenameMovesThePageAndFollowsEveryLink is the feature: a page moves, its
// file moves, its row moves, and every page that linked to it says so again.
func TestARenameMovesThePageAndFollowsEveryLink(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const (
		from = "locations/rivergate"
		to   = "locations/rivergate-crossing"
	)

	f.mustSave(edit.Save{Path: from, Markdown: dmFrontmatter, Creating: true, As: f.dm})
	linking := "---\ntitle: A Journey\ntype: note\n---\n\nThrough [[locations/rivergate|the bridge]],\n" +
		"and past [the inn](locations/rivergate#the-inn).\n"
	f.mustSave(edit.Save{Path: "notes/journey", Markdown: linking, Creating: true, As: f.dm})

	moved, followed, err := f.editor.Rename(t.Context(), from, to, f.dm)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	if moved.Path != to {
		t.Errorf("the page is now %q, want %q", moved.Path, to)
	}
	if len(followed) != 1 || followed[0] != "notes/journey" {
		t.Errorf("the links followed were %v, want notes/journey", followed)
	}

	// The file moved, and the old one is gone — the vault is the truth, so a row
	// at the old path with a file behind it would be a second page.
	if got := f.readFile(to); got != dmFrontmatter {
		t.Errorf("the new file is %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.vaultDir, "locations", "rivergate.md")); err == nil {
		t.Error("the old file is still there")
	}

	// And the linking page now points at the new name, with its alias and its
	// fragment intact.
	got := f.readFile("notes/journey")
	for _, want := range []string{
		"[[locations/rivergate-crossing|the bridge]]",
		"[the inn](locations/rivergate-crossing#the-inn)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the linking page does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "locations/rivergate]]") || strings.Contains(got, "(locations/rivergate)") {
		t.Errorf("the linking page still points at the old name:\n%s", got)
	}
}

// TestARenameIsDMOnly: a page moving out of a character folder stops being owned,
// and a player must not be able to move their own page somewhere it is not.
func TestARenameIsDMOnly(t *testing.T) {
	t.Parallel()

	f := newEditor(t)

	_, _, err := f.editor.Rename(t.Context(), "characters/aria", "characters/aria-old", f.player)
	if !errors.Is(err, store.ErrNotAllowed) {
		t.Fatalf("a player renamed their own character page: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.vaultDir, "characters", "aria-old.md")); statErr == nil {
		t.Error("the refused rename created a file")
	}
	if got := f.readFile("characters/aria"); !strings.Contains(got, "A character") {
		t.Errorf("the refused rename changed the old file:\n%s", got)
	}
}

// TestARenameRefusesAnOccupiedDestination, because overwriting a page is not what
// a rename means.
func TestARenameRefusesAnOccupiedDestination(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	f.mustSave(edit.Save{Path: "locations/rivergate", Markdown: dmFrontmatter, Creating: true, As: f.dm})
	f.mustSave(edit.Save{
		Path: "locations/thornford", Creating: true, As: f.dm,
		Markdown: "---\ntitle: Thornford\ntype: location\n---\n\nAnother town.\n",
	})

	if _, _, err := f.editor.Rename(t.Context(), "locations/rivergate", "locations/thornford", f.dm); err == nil {
		t.Fatal("a rename overwrote another page")
	}
	if got := f.readFile("locations/thornford"); !strings.Contains(got, "Another town") {
		t.Errorf("the occupied page was changed:\n%s", got)
	}
	if got := f.readFile("locations/rivergate"); got != dmFrontmatter {
		t.Errorf("the source page was changed by the refused rename:\n%s", got)
	}
}

// TestARenameOutOfACharacterFolderTakesTheOwnerWithIt is the ownership half, and
// it is the reason the gate is asked about the *destination*: a page that leaves
// `characters/aria/` is no longer hers, and one that arrives is.
func TestARenameOutOfACharacterFolderTakesTheOwnerWithIt(t *testing.T) {
	t.Parallel()

	f := newEditor(t)

	// A page in Aria's folder, owned by Aria.
	owned := "---\ntitle: Spells\ntype: note\n---\n\nLevitate.\n"
	f.mustSave(edit.Save{Path: "characters/aria/spells", Markdown: owned, Creating: true, As: f.dm})
	if f.indexed("characters/aria/spells").OwnerCharacterPageID != f.aria.ID {
		t.Fatal("the page is not owned by Aria before the rename, so this test proves nothing")
	}

	moved, _, err := f.editor.Rename(t.Context(), "characters/aria/spells", "notes/aria-spells", f.dm)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// The path no longer says Aria and there is no `character:` key, so the page
	// is nobody's: the owner followed the path rather than staying behind.
	if moved.OwnerCharacterPageID != "" {
		t.Errorf("the moved page is owned by %q, want nobody", moved.OwnerCharacterPageID)
	}
	if f.player.Role != domain.RolePlayer {
		t.Fatal("the fixture's player is not a player, so this test proves nothing")
	}
}

// TestARenameKeepsTheHistory is restore fidelity's other half: a page's revisions
// are about its *text* and travel with it, under the same numbers.
func TestARenameKeepsTheHistory(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"

	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})
	_, hash := f.read(path)
	second := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a bridge.\n", 1)
	f.mustSave(edit.Save{Path: path, Markdown: second, Expect: hash, As: f.dm})

	before, err := f.editor.History(t.Context(), path)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("there is %d revision(s) before the rename, want 1", len(before))
	}

	if _, _, renameErr := f.editor.Rename(t.Context(), path, "locations/rivergate-crossing", f.dm); renameErr != nil {
		t.Fatalf("Rename: %v", renameErr)
	}

	after, err := f.editor.History(t.Context(), "locations/rivergate-crossing")
	if err != nil {
		t.Fatalf("History after the rename: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("the moved page has %d revisions, want the %d it had", len(after), len(before))
	}
	for i := range before {
		if after[i].Rev != before[i].Rev || after[i].Markdown != before[i].Markdown {
			t.Errorf("revision %d is %+v, want %+v", before[i].Rev, after[i], before[i])
		}
	}
}

// # Restore

// TestRestoringIsASaveAndIsUndoable: the two properties a restore has, both of
// which come from it being a save rather than a write of its own.
func TestRestoringIsASaveAndIsUndoable(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"

	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})
	original, hash := f.read(path)

	edited := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a bridge.\n", 1)
	f.mustSave(edit.Save{Path: path, Markdown: edited, Expect: hash, As: f.dm, Message: "the bridge"})

	// Restoring revision 1 puts the original text back, byte for byte.
	if _, err := f.editor.Restore(t.Context(), path, 1, f.hashOf(path), f.dm); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := f.readFile(path); got != original {
		t.Errorf("the restored file is:\n%q\nwant:\n%q", got, original)
	}

	// And the text the restore replaced is itself kept, so the restore can be
	// undone. That is what "it is a save" buys.
	history, err := f.editor.History(t.Context(), path)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("there are %d revisions after the restore, want 2", len(history))
	}
	if history[0].Markdown != edited {
		t.Errorf("the newest revision is not the text the restore replaced:\n%q", history[0].Markdown)
	}
	if !strings.Contains(history[0].Message, "restored") {
		t.Errorf("the newest revision says %q, which does not say it is a restore", history[0].Message)
	}
}

// TestARestoreIsRefusedOnAStaleETag: a history panel that did not send the ETag
// would restore over whatever is there now.
func TestARestoreIsRefusedOnAStaleETag(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"

	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})
	_, hash := f.read(path)
	edited := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a bridge.\n", 1)
	f.mustSave(edit.Save{Path: path, Markdown: edited, Expect: hash, As: f.dm})

	before := f.readFile(path)
	if _, err := f.editor.Restore(t.Context(), path, 1, hash, f.dm); !errors.Is(err, edit.ErrConflict) {
		t.Errorf("a stale restore returned %v, want ErrConflict", err)
	}
	if got := f.readFile(path); got != before {
		t.Error("a refused restore wrote the file")
	}
}

// TestAPlayerMayRestoreTheirOwnPageAndNotOthers is restore's access-control half.
func TestAPlayerMayRestoreTheirOwnPageAndNotOthers(t *testing.T) {
	t.Parallel()

	f := newEditor(t)

	// Aria's page, with a history.
	original := strings.Replace(f.readFile("characters/aria"), "A character.", "A thief from Lockwater.", 1)
	_, hash := f.read("characters/aria")
	f.mustSave(edit.Save{Path: "characters/aria", Markdown: original, Expect: hash, As: f.player})

	if _, err := f.editor.Restore(t.Context(), "characters/aria", 1, f.hashOf("characters/aria"), f.player); err != nil {
		t.Errorf("a player could not restore their own page: %v", err)
	}
	if got := f.readFile("characters/aria"); !strings.Contains(got, "A character") {
		t.Errorf("the page was not restored:\n%s", got)
	}

	// And a DM's page, for the same player. It needs a revision of its own, which
	// means a second save -- and a restore of revision 1 of a page the player may
	// not write has to say *that*, rather than complaining that revision 1 does
	// not exist.
	f.mustSave(edit.Save{Path: "locations/rivergate", Markdown: dmFrontmatter, Creating: true, As: f.dm})
	edited := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a bridge.\n", 1)
	f.mustSave(edit.Save{Path: "locations/rivergate", Markdown: edited, Expect: f.hashOf("locations/rivergate"), As: f.dm})

	before := f.readFile("locations/rivergate")
	if _, err := f.editor.Restore(t.Context(), "locations/rivergate", 1, f.hashOf("locations/rivergate"), f.player); !errors.Is(err, store.ErrNotAllowed) {
		t.Errorf("a player restored a DM's page: %v", err)
	}
	if got := f.readFile("locations/rivergate"); got != before {
		t.Error("a refused restore wrote the file")
	}
}

// # Archive and purge

// TestArchivingRemovesTheFileAndKeepsTheRow is the difference between the two
// operations, and it is the reason archive is not delete.
func TestArchivingRemovesTheFileAndKeepsTheRow(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"
	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})

	if err := f.editor.Archive(t.Context(), path, f.dm); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	// The file is gone, which is what the row's `is_deleted` means.
	if _, err := os.Stat(filepath.Join(f.vaultDir, "locations", "rivergate.md")); err == nil {
		t.Error("the file is still there after an archive")
	}

	// The row is not: it is marked deleted, which the *read* predicate filters on,
	// so an archived page is not readable by anybody and the ordinary lookup says
	// not-found. The administrative one is the only way to see it, and that is the
	// point of its name.
	if _, err := f.store.GetPage(t.Context(), f.campaign.ID, path, store.AsDM(f.campaign.ID)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an archived page is still readable: %v", err)
	}
	row, err := f.store.GetPageArchived(t.Context(), f.campaign.ID, path, store.AsDM(f.campaign.ID))
	if err != nil {
		t.Fatalf("the row is gone after an archive: %v", err)
	}
	if !row.IsDeleted {
		t.Error("the row is not marked deleted")
	}
	if _, histErr := f.editor.History(t.Context(), path); histErr != nil {
		t.Errorf("the history is gone after an archive: %v", histErr)
	}

	// And it does not appear to a reader, because the read predicate filters
	// deleted pages.
	if text, _, readErr := f.editor.Read(t.Context(), path); readErr == nil {
		t.Errorf("an archived page is still readable: %q", text)
	}
}

// TestAnArchiveIsIdempotentAndAPurgeIsNot: a second archive has nothing to do and
// says so; a second purge refuses, because a purge that reports success for a page
// that is not there is a purge a DM cannot trust.
func TestAnArchiveIsIdempotentAndAPurgeIsNot(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"
	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})

	if err := f.editor.Archive(t.Context(), path, f.dm); err != nil {
		t.Fatalf("the first archive: %v", err)
	}
	if err := f.editor.Archive(t.Context(), path, f.dm); err != nil {
		t.Errorf("the second archive returned %v, want nothing to do", err)
	}

	if err := f.editor.Purge(t.Context(), path, f.dm); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if err := f.editor.Purge(t.Context(), path, f.dm); !errors.Is(err, edit.ErrNoSuchPage) {
		t.Errorf("the second purge returned %v, want ErrNoSuchPage", err)
	}
}

// TestAPurgeThrowsTheHistoryAway is the other half of the pair, and it is the
// warning a DM's confirm dialog is about.
func TestAPurgeThrowsTheHistoryAway(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"

	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})
	_, hash := f.read(path)
	edited := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a bridge.\n", 1)
	f.mustSave(edit.Save{Path: path, Markdown: edited, Expect: hash, As: f.dm})

	// The page is archived and then purged, which is the flow a DM means by "gone
	// for good" -- and the purge finds the archived row rather than the live one,
	// which is the only reason it can be a two-step operation at all.
	if err := f.editor.Archive(t.Context(), path, f.dm); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if err := f.editor.Purge(t.Context(), path, f.dm); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	// Nothing is left: no row, so no revisions, and no restore.
	if _, err := f.store.GetPage(t.Context(), f.campaign.ID, path, store.AsDM(f.campaign.ID)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the row survived a purge: %v", err)
	}
	if _, err := f.editor.History(t.Context(), path); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the history is reachable after a purge: %v", err)
	}

	// And the DM's own `_history` files are still on disk, because the vault is
	// the DM's and this application does not delete from it what it did not write
	// as a page. That is worth saying out loud: a purge throws away the *index's*
	// memory of a page, and the file history is the DM's own.
	if files, err := f.vault.Revisions(path); err != nil {
		t.Errorf("reading the vault's history after a purge: %v", err)
	} else if len(files) == 0 {
		t.Error("the purge deleted the DM's own _history files")
	}
}

// TestAPlayerMayArchiveTheirOwnPageAndNotADMs is the access-control half of the
// pair, and it is a page the player is *allowed* to remove: it is theirs.
func TestAPlayerMayArchiveTheirOwnPageAndNotADMs(t *testing.T) {
	t.Parallel()

	f := newEditor(t)

	if err := f.editor.Archive(t.Context(), "locations/rivergate", f.player); err == nil {
		// The page does not exist, so this is ErrNoSuchPage rather than a refusal.
		t.Error("archiving a page that does not exist did not report that")
	}

	f.mustSave(edit.Save{Path: "locations/rivergate", Markdown: dmFrontmatter, Creating: true, As: f.dm})
	if err := f.editor.Archive(t.Context(), "locations/rivergate", f.player); !errors.Is(err, store.ErrNotAllowed) {
		t.Errorf("a player archived a DM's page: %v", err)
	}
	if got := f.readFile("locations/rivergate"); got != dmFrontmatter {
		t.Error("a refused archive removed the file")
	}

	if err := f.editor.Archive(t.Context(), "characters/aria", f.player); err != nil {
		t.Errorf("a player could not archive their own character page: %v", err)
	}
}

// helpers
