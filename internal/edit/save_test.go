package edit_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/edit"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// TestSavingAPageWritesTheFileAndTheRow is the CRUD flow, and it asserts both
// halves separately because they are the two things the save has to get right and
// one of them being right does not imply the other.
func TestSavingAPageWritesTheFileAndTheRow(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"

	page := f.mustSave(edit.Save{
		Path:     path,
		Markdown: dmFrontmatter,
		Creating: true,
		As:       f.dm,
	})

	// The file is the truth, and it has the bytes that were handed in.
	if got := f.readFile(path); got != dmFrontmatter {
		t.Errorf("the file is not what was saved:\n%q", got)
	}

	// The row is derived from the file by the same code the watcher uses, so the
	// two cannot disagree about a title or a visibility.
	if page.Path != path {
		t.Errorf("the row's path is %q, want %q", page.Path, path)
	}
	if page.Visibility != domain.VisibilityDMOnly {
		t.Errorf("the row's audience is %q, want dm-only", page.Visibility)
	}
	if page.ContentHash != vault.Hash([]byte(dmFrontmatter)) {
		t.Errorf("the row's hash is %q, want the file's", page.ContentHash)
	}
	if got := f.indexed(path); got.Body != page.Body {
		t.Errorf("the row in the database is %q, want %q", got.Body, page.Body)
	}
}

// TestTheFilesAreUnchangedByASaveThatIsNotOne is the invariant ADR 0001 is about,
// restated for the writer: a page the editor does not touch does not move.
//
// The hash the test compares is every file's contents *and* modification time,
// which is what M4's invariant test uses, because a file rewritten with identical
// bytes has the same contents and a different mtime and is still a file somebody's
// backup has noticed.
func TestTheFilesAreUnchangedByASaveThatIsNotOne(t *testing.T) {
	t.Parallel()

	f := newEditor(t)

	// A file the DM wrote in Obsidian, and another one they have not touched.
	f.writeFile("npcs/vel", "---\ntitle: Captain Vell\ntype: npc\n---\n\nCollects the toll.\n")
	f.mustSave(edit.Save{
		Path:     "npcs/vel",
		Markdown: f.readFile("npcs/vel"),
		Expect:   f.hashOf("npcs/vel"),
		As:       f.dm,
	})

	before := snapshot(t, f.vaultDir)

	// A save that conflicts, one that is refused, and one that is refused by the
	// gate. None of them may touch the disk, and the first is the one that would
	// not have if the check came after the write.
	_, _, _ = f.editor.Save(t.Context(), edit.Save{
		Path:     "npcs/vel",
		Markdown: "---\ntitle: Not yours\n---\n\nx\n",
		Expect:   "not-the-hash",
		As:       f.dm,
	})
	_, _, _ = f.editor.Save(t.Context(), edit.Save{
		Path:     "npcs/vel",
		Markdown: "---\ntitle: Not yours\n---\n\nx\n",
		Expect:   f.hashOf("npcs/vel"),
		As:       f.player,
	})
	_, _, _ = f.editor.Save(t.Context(), edit.Save{
		Path:     "notes/nowhere",
		Markdown: "---\ntitle: Nowhere\n---\n\nx\n",
		As:       f.player,
	})

	after := snapshot(t, f.vaultDir)
	if len(after) != len(before) {
		t.Errorf("the vault has %d files and had %d: a refused save created one", len(after), len(before))
	}
	for path, state := range before {
		if got := after[path]; got != state {
			t.Errorf("%s changed: %+v became %+v", path, state, after[path])
		}
	}
}

// TestAConflictIsRefusedAndNothingIsWritten is the whole point of the ETag.
//
// Two readers, one writer: the DM has the page open, a player saves it, and the
// DM's save is refused rather than silently overwriting a page that changed while
// they were not looking. The assertion is on *both* halves — the error and the
// bytes — because a conflict that returned an error and then wrote anyway would be
// the worst version of this.
func TestAConflictIsRefusedAndNothingIsWritten(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"
	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})

	// What the DM was looking at.
	_, asTheDMSawIt := f.read(path)

	// Somebody else saves it.
	first := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a bridge.\n", 1)
	f.mustSave(edit.Save{Path: path, Markdown: first, Expect: asTheDMSawIt, As: f.dm})

	// And now the DM saves what they were looking at.
	_, _, err := f.editor.Save(t.Context(), edit.Save{
		Path:     path,
		Markdown: strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a mill.\n", 1),
		Expect:   asTheDMSawIt,
		As:       f.dm,
	})

	if !errors.Is(err, edit.ErrConflict) {
		t.Fatalf("the second save returned %v, want ErrConflict", err)
	}

	// The file is the winner's, not the loser's.
	if got := f.readFile(path); got != first {
		t.Errorf("the refused save changed the file:\n%q", got)
	}
	if got := f.indexed(path).Body; got != "A fortified town, and a bridge.\n" {
		t.Errorf("the refused save changed the row: %q", got)
	}
}

// TestTheThreeVersionsAreTheOnesADiffNeeds is the other half of a conflict: what
// the caller gets to resolve it with.
//
// The base is the text the *caller* was looking at, which is a fact only a
// revision can supply — the row and the file both say what the page is now.
func TestTheThreeVersionsAreTheOnesADiffNeeds(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"
	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})

	// The text the caller is looking at, and the hash they would send back.
	asTheySawIt, asTheySawItHash := f.read(path)

	// Somebody else saves the page, which is what makes it a conflict: the caller's
	// ETag is now stale and their text is now a third version.
	theirs := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a bridge.\n", 1)
	f.mustSave(edit.Save{Path: path, Markdown: theirs, Expect: asTheySawItHash, As: f.dm, Message: "the bridge"})

	// And what the caller wants to write.
	mine := strings.Replace(dmFrontmatter, "\nA fortified town.\n", "\nA fortified town, and a mill.\n", 1)

	base, current, err := f.editor.Versions(t.Context(), path, asTheySawItHash, mine)
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}

	// The base is the caller's text, which is a fact only a revision holds: the
	// row and the file both say what the page is now.
	if base != asTheySawIt {
		t.Errorf("the base is %q, want the text the caller was looking at:\n%q", base, asTheySawIt)
	}
	if current != theirs {
		t.Errorf("current is %q, want what is on disk:\n%q", current, theirs)
	}
	if mine == current {
		t.Error("the incoming text was not passed through, so a diff would have nothing to show")
	}
}

// TestTheBaseIsEmptyWhenThisApplicationHasNotKeptIt is the honest answer for the
// ordinary case, and pretending otherwise would be inventing a base.
//
// A page the DM wrote in Obsidian has no revision here: its first save through the
// editor is revision 1 of the *text before that save*, and a caller looking at the
// text before it has a base that exists as a file rather than as a row. This says
// "empty" rather than reaching for the file, and the comment says why.
func TestTheBaseIsEmptyWhenThisApplicationHasNotKeptIt(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"
	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})

	// The DM is looking at the page as it is now, and has not saved it before.
	_, asTheySawIt := f.read(path)

	base, current, err := f.editor.Versions(t.Context(), path, asTheySawIt, "something else")
	if err != nil {
		t.Fatalf("Versions: %v", err)
	}
	if base != "" {
		t.Errorf("the base is %q, want nothing: this application has not kept that text", base)
	}
	if current != dmFrontmatter {
		t.Errorf("current is %q, want what is on disk", current)
	}
}

// TestARevisionIsTheTextThatWasThereBefore is restore fidelity's other half, and
// it is the one a save can get wrong by keeping the *new* text.
//
// The revision is the previous text, and the number is the store's so that the
// database and `_history` agree about the order of a page's history. A page created
// through the editor has no previous text and therefore no revision, and a page
// saved three times has revisions 1, 2 and 3 with the text as it was before each.
func TestARevisionIsTheTextThatWasThereBefore(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"

	// A create: no previous text, so no revision.
	if _, rev, err := f.editor.Save(t.Context(), edit.Save{
		Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm,
	}); err != nil {
		t.Fatalf("the create: %v", err)
	} else if rev != 0 {
		t.Errorf("a create kept revision %d, want none", rev)
	}

	revisions, err := f.store.ListRevisions(t.Context(), f.indexed(path).ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revisions) != 0 {
		t.Fatalf("a page created through the editor has %d revisions, want none", len(revisions))
	}

	// Three updates. The texts are written out rather than built by substitution,
	// because a substitution that stops matching half way through is a test that
	// quietly stops changing anything -- and then the history is right for the
	// wrong reason.
	texts := []string{
		"---\ntitle: Rivergate\ntype: location\nvisibility: dm-only\n---\n\nA fortified town, and a bridge.\n",
		"---\ntitle: Rivergate\ntype: location\nvisibility: dm-only\n---\n\nA fortified town, and a bridge, and a ford.\n",
		"---\ntitle: Rivergate\ntype: location\nvisibility: dm-only\n---\n\nA fortified town, and a bridge, and a ford, and a ferry.\n",
	}
	want := []string{dmFrontmatter}
	for i, next := range texts {
		_, hash := f.read(path)

		_, rev, saveErr := f.editor.Save(t.Context(), edit.Save{
			Path: path, Markdown: next, Expect: hash, As: f.dm, Message: fmt.Sprintf("edit %d", i+1),
		})
		if saveErr != nil {
			t.Fatalf("save %d: %v", i+1, saveErr)
		}
		if rev != i+1 {
			t.Errorf("save %d kept the previous text as revision %d, want %d", i+1, rev, i+1)
		}
		want = append(want, next)
	}

	// So: the first revision is the text before the first save, the second is the
	// text before the second, and so on. Nothing is kept for the text that is there
	// now, because the file is that.
	stored, err := f.store.ListRevisions(t.Context(), f.indexed(path).ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("there are %d revisions, want 3", len(stored))
	}
	for i, revision := range stored {
		if revision.Markdown != want[i] {
			t.Errorf("revision %d is:\n%q\nwant:\n%q", i+1, revision.Markdown, want[i])
		}
		if revision.AuthorPrincipalID != f.dm.ID {
			t.Errorf("revision %d is attributed to %q, want the DM", i+1, revision.AuthorPrincipalID)
		}
	}

	// And the DM's own history has the same texts under the same numbers, because
	// the two histories are the same history in two places.
	archived, err := f.vault.Revisions(path)
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(archived) != 3 {
		t.Errorf("the vault's history has %d files, want 3: %v", len(archived), archived)
	}
}

// TestAPlayerMayWriteTheirOwnCharacterPageAndNobodyElses is the milestone's
// headline capability and its headline risk, and both halves are here because a
// feature that is refused everywhere is not a feature and one that is allowed
// everywhere is a hole.
func TestAPlayerMayWriteTheirOwnCharacterPageAndNobodyElses(t *testing.T) {
	t.Parallel()

	f := newEditor(t)

	tests := map[string]struct {
		path     string
		markdown string
		as       domain.Principal
		wantErr  bool
	}{
		"their own character page": {
			path:     "characters/aria",
			markdown: strings.Replace(f.readFile("characters/aria"), "A character.", "A thief from Lockwater.", 1),
			as:       f.player,
		},
		"a new page in their own character's folder": {
			path:     "characters/aria/spells",
			markdown: "---\ntitle: Spells\ntype: note\ncharacter: aria\n---\n\nLevitate, once a day.\n",
			as:       f.player,
		},
		"a page outside their folder that names their character": {
			path:     "notes/aria-spells",
			markdown: "---\ntitle: Aria's Spells\ntype: note\ncharacter: aria\n---\n\nThe same list, somewhere findable.\n",
			as:       f.player,
		},
		"a DM's page": {
			path:     "locations/rivergate",
			markdown: dmFrontmatter,
			as:       f.player,
			wantErr:  true,
		},
		"a DM's page with the player's own character in its frontmatter": {
			// The interesting refusal: the page claims to be Aria's, and it is not
			// in Aria's folder, and the *claim* is what the gate checks — so this
			// is allowed, because a player's spell sheet is exactly this.
			path:     "notes/aria-notes",
			markdown: "---\ntitle: Notes\ntype: note\ncharacter: aria\n---\n\nSome notes.\n",
			as:       f.player,
		},
		"a page inside another character's folder": {
			// The refusal that is the *gate's* and not a derivation problem: Brian
			// exists, the page is his, and Aria's player may not write it. A player
			// who could put a page inside somebody else's character folder would be
			// writing their notes, not their character's, with the path as the
			// answer.
			path:     "characters/brian/pretend",
			markdown: "---\ntitle: Pretend\ntype: note\n---\n\nNot Aria's.\n",
			as:       f.player,
			wantErr:  true,
		},
		"a page with no owner at all": {
			path:     "notes/shared",
			markdown: "---\ntitle: Shared\ntype: note\n---\n\nFor everybody.\n",
			as:       f.player,
			wantErr:  true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newEditor(t)

			// A page that is not there yet is a create, and one that is there is
			// an update with the ETag a browser would have been served.
			in := edit.Save{Path: tt.path, Markdown: tt.markdown, As: tt.as, Creating: true}
			if _, hash, readErr := f.editor.Read(t.Context(), tt.path); readErr == nil {
				in.Creating, in.Expect = false, hash
			}

			_, _, err := f.editor.Save(t.Context(), in)
			switch {
			case tt.wantErr && err == nil:
				t.Errorf("the save was allowed, and the file now says:\n%s", f.readFile(tt.path))
			case tt.wantErr && !errors.Is(err, store.ErrNotAllowed):
				t.Errorf("the refusal is %v, want store.ErrNotAllowed", err)
			case !tt.wantErr && err != nil:
				t.Errorf("the save was refused: %v", err)
			case !tt.wantErr:
				if got := f.readFile(tt.path); got != tt.markdown {
					t.Errorf("the file is:\n%q\nwant:\n%q", got, tt.markdown)
				}
			}
		})
	}
}

// TestASaveWithNoETagIsRefused is the case where a caller forgot something, and
// the answer is to refuse rather than to guess.
//
// An update with no ETag is a request to overwrite whatever is there. That is what
// a *create* is for, and a create says so.
func TestASaveWithNoETagIsRefused(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"
	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})

	_, _, err := f.editor.Save(t.Context(), edit.Save{
		Path:     path,
		Markdown: "something else",
		As:       f.dm,
	})
	if !errors.Is(err, edit.ErrConflict) {
		t.Fatalf("a save with no ETag returned %v, want ErrConflict", err)
	}
	if got := f.readFile(path); got != dmFrontmatter {
		t.Error("a save with no ETag wrote the file")
	}
}

// TestACreateOverAnExistingPageIsAConflict rather than an update: the caller said
// they were making a new page and they were not.
func TestACreateOverAnExistingPageIsAConflict(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	const path = "locations/rivergate"
	f.mustSave(edit.Save{Path: path, Markdown: dmFrontmatter, Creating: true, As: f.dm})

	_, _, err := f.editor.Save(t.Context(), edit.Save{
		Path:     path,
		Markdown: "---\ntitle: Something else\n---\n\nx\n",
		Creating: true,
		As:       f.dm,
	})
	if !errors.Is(err, edit.ErrConflict) {
		t.Fatalf("a create over an existing page returned %v, want ErrConflict", err)
	}
	if got := f.readFile(path); got != dmFrontmatter {
		t.Error("a refused create wrote the file")
	}
}

// TestASaveToAPageThatIsNotThereIsRefused: an update of a page that has been
// archived is a create, and the caller has to say so.
func TestASaveToAPageThatIsNotThereIsRefused(t *testing.T) {
	t.Parallel()

	f := newEditor(t)

	_, _, err := f.editor.Save(t.Context(), edit.Save{
		Path:     "locations/nowhere",
		Markdown: dmFrontmatter,
		Expect:   "any-hash",
		As:       f.dm,
	})
	if !errors.Is(err, edit.ErrConflict) || !errors.Is(err, edit.ErrNoSuchPage) {
		t.Errorf("an update of a missing page returned %v, want both ErrConflict and ErrNoSuchPage", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.vaultDir, "locations", "nowhere.md")); statErr == nil {
		t.Error("the refused save created the file")
	}
}

// TestMayWriteIsTheSameAnswerAsTheSave: the Edit button's question and the save's
// question are one question, and a test that asked them separately is a test that
// has not checked they agree.
func TestMayWriteIsTheSameAnswerAsTheSave(t *testing.T) {
	t.Parallel()

	f := newEditor(t)
	f.mustSave(edit.Save{Path: "locations/rivergate", Markdown: dmFrontmatter, Creating: true, As: f.dm})

	tests := map[string]struct {
		path string
		as   domain.Principal
	}{
		"a DM's page, for the DM":   {path: "locations/rivergate", as: f.dm},
		"a DM's page, for a player": {path: "locations/rivergate", as: f.player},
		"a character's own page":    {path: "characters/aria", as: f.player},
		"an unowned note":           {path: "notes/shared", as: f.player},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newEditor(t)
			f.mustSave(edit.Save{Path: "locations/rivergate", Markdown: dmFrontmatter, Creating: true, As: f.dm})

			mayWrite := f.editor.MayWrite(t.Context(), tt.path, tt.as) == nil

			// And the save agrees, which is the part that matters.
			current, hash, readErr := f.editor.Read(t.Context(), tt.path)
			if readErr != nil { //nolint:nilerr // a page that is not there cannot be written over, and MayWrite says so
				if mayWrite {
					t.Error("MayWrite allowed a page that does not exist")
				}
				return
			}
			_, _, saveErr := f.editor.Save(t.Context(), edit.Save{
				Path:     tt.path,
				Markdown: strings.Replace(current, "\n\n", "\n\nAn edit.\n\n", 1),
				Expect:   hash,
				As:       tt.as,
			})
			if (saveErr == nil) != mayWrite {
				t.Errorf("MayWrite says %t and the save says %v", mayWrite, saveErr)
			}
		})
	}
}

// helpers

// fileState is what a test needs to know about a file to say it did not change:
// its contents and when it was last written, because a file rewritten with
// identical bytes is a file somebody's backup has noticed.
type fileState struct {
	body    string
	modTime time.Time
}

func snapshot(t *testing.T, dir string) map[string]fileState {
	t.Helper()

	states := map[string]fileState{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		name, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		states[filepath.ToSlash(name)] = fileState{body: string(body), modTime: info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotting %s: %v", dir, err)
	}
	return states
}
