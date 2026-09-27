package index_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// TestASyncThatWritesAsAPlayerIsRefusedByTheGate is the property ADR 0017's
// residual was waiting for.
//
// A player's editor writes a file, and the file has to be re-derived into a row —
// so the question is who that re-derivation writes as. If it writes as the DM,
// because that is what a sync does, then the store's write gate never runs for an
// editor save and a player may write any page in the campaign. If it writes as the
// caller, the gate runs, and the gate checks the *derived* page — whose owner came
// from the path and the frontmatter, neither of which the caller supplied as an
// owner.
//
// The second is `SyncPathAs`, and this is the test that says the difference is
// real: the same file, written the same way, indexed as the DM and as a player
// bound to somebody else's character.
func TestASyncThatWritesAsAPlayerIsRefusedByTheGate(t *testing.T) {
	t.Parallel()

	f := newWriterFixture(t)
	ctx := f.ctx

	// A DM's page in the campaign, and a player bound to a character of their own.
	if _, err := f.store.UpsertPage(ctx, domain.Page{
		CampaignID:  f.campaign.ID,
		Path:        "npcs/vel",
		Title:       "Captain Vell",
		Type:        domain.PageTypeNPC,
		Visibility:  domain.VisibilityDMOnly,
		Frontmatter: "title: Captain Vell\nvisibility: dm-only\n",
		Body:        "The toll collector.\n",
		ContentHash: "hash-vel",
	}, store.AsDM(f.campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	aria := f.createCharacter("aria")
	player := f.createPlayer("a player of the fixture")
	f.bind(player, aria)

	// The player's *own* character subtree is writable, which is the other half
	// and the one a gate that simply refused everything would get wrong.
	f.writeFile("characters/aria/spells",
		"---\ntitle: Spells\ntype: note\ncharacter: aria\n---\n\nLevitate.\n")

	if err := f.sync.CheckWritableAs(ctx, "characters/aria/spells", player); err != nil {
		t.Errorf("a player was refused their own character subtree: %v", err)
	}
	if _, err := f.sync.SyncPathAs(ctx, "characters/aria/spells", player); err != nil {
		t.Errorf("a player could not index their own character subtree: %v", err)
	}

	// And somebody else's page is refused, by name, in the store's own words --
	// **before** the file is written, which is the order that matters. A save that
	// wrote first and rolled back would leave a file the watcher indexes as the DM.
	f.writeFile("npcs/vel",
		"---\ntitle: Captain Vell\ntype: npc\nvisibility: dm-only\n---\n\nThe toll collector, rewritten.\n")

	err := f.sync.CheckWritableAs(ctx, "npcs/vel", player)
	if err == nil {
		t.Fatal("a player may write a DM-only page")
	}
	if !errors.Is(err, store.ErrNotAllowed) {
		t.Errorf("the refusal is %v, want store.ErrNotAllowed", err)
	}

	if _, err := f.sync.SyncPathAs(ctx, "npcs/vel", player); err == nil {
		t.Error("a player re-indexed a DM-only page as themselves")
	}

	// The row is untouched, which is what makes the refusal a refusal and not a
	// partial write: the file is the truth, so the file has the new text and the
	// row has the old, and the next DM sync settles it.
	indexed, getErr := f.store.GetPage(ctx, f.campaign.ID, "npcs/vel", store.AsDM(f.campaign.ID))
	if getErr != nil {
		t.Fatalf("GetPage: %v", getErr)
	}
	if indexed.Body != "The toll collector.\n" {
		t.Errorf("the row was written by a refused save:\n%s", indexed.Body)
	}
}

// TestTheOwnerIsDerivedAndNotSupplied is the other half of the residual: a caller
// cannot make a page its own by saying so.
//
// The page is outside the character's subtree, which is the case the `character:`
// key exists for, and it names a character the player *is* bound to. That is
// legitimate — a player's spell sheet, a note about their character — and the
// store's gate is what makes it so: the owner comes from the frontmatter, and the
// gate checks the principal against that owner.
//
// The negative is the interesting one: a page that names a character the player is
// *not* bound to is refused, and the test would not catch it if the owner were
// something the caller passed in.
func TestTheOwnerIsDerivedAndNotSupplied(t *testing.T) {
	t.Parallel()

	f := newWriterFixture(t)
	ctx := f.ctx

	aria := f.createCharacter("aria")
	brian := f.createCharacter("brian")

	player := f.createPlayer("the Aria player")
	f.bind(player, aria)

	stranger := f.createPlayer("the Brian player")
	f.bind(stranger, brian)

	// Aria's spell sheet, outside her folder, owned by Aria. The `character:`
	// key is what owns it, and the page is outside the subtree, which is exactly
	// the case the key exists for.
	f.writeFile("notes/aria-spells",
		"---\ntitle: Aria's Spells\ntype: note\ncharacter: aria\nvisibility: dm-and-owner\n---\n\nLevitate.\n")

	if err := f.sync.CheckWritableAs(ctx, "notes/aria-spells", player); err != nil {
		t.Errorf("Aria's player was refused Aria's spell sheet: %v", err)
	}
	if _, err := f.sync.SyncPathAs(ctx, "notes/aria-spells", player); err != nil {
		t.Errorf("Aria's player could not index Aria's spell sheet: %v", err)
	}

	// And the same file for the player who is bound to somebody else.
	if err := f.sync.CheckWritableAs(ctx, "notes/aria-spells", stranger); err == nil {
		t.Error("a player may write a page belonging to a character they are not bound to")
	}
}

// TestAPlayerMayNotLoosenTheOwnersGateByWritingOutsideTheirSubtree is the path
// rule, which is the half ADR 0017 said it did not check and named as the editor's
// residual to close.
//
// The store's gate checks the owner column. The owner column is derived from the
// path, so a page under `characters/brian/` that declares `character: aria` would
// carry Aria's id and pass a gate keyed on Aria — if the derivation let it through.
// It does not: the path wins, and the conflict is a refusal the sync reports, so
// the page is written *unowned*, which a player cannot write and a DM can.
func TestAPlayerMayNotLoosenTheGateByWritingOutsideTheirSubtree(t *testing.T) {
	t.Parallel()

	f := newWriterFixture(t)
	ctx := f.ctx

	aria := f.createCharacter("aria")
	player := f.createPlayer("the Aria player")
	f.bind(player, aria)

	// A page under Brian's folder claiming to be Aria's. The path is what counts.
	f.writeFile("characters/brian/pretend",
		"---\ntitle: Pretend\ntype: note\ncharacter: aria\n---\n\nAria's page, under Brian's name.\n")

	err := f.sync.CheckWritableAs(ctx, "characters/brian/pretend", player)
	if err == nil {
		t.Fatal("a player may write a page whose path says one character and whose frontmatter says another")
	}
	if !strings.Contains(err.Error(), "the path is what counts") {
		t.Errorf("the refusal does not say which rule won: %v", err)
	}
	if _, syncErr := f.sync.SyncPathAs(ctx, "characters/brian/pretend", player); syncErr == nil {
		t.Error("the sync wrote a conflicting page on a player's behalf")
	}

	// The page is not in the index at all, which is the fail-closed answer: a
	// player never got a row, so nothing joins to Aria.
	if _, getErr := f.store.GetPage(ctx, f.campaign.ID, "characters/brian/pretend", store.AsDM(f.campaign.ID)); !errors.Is(getErr, store.ErrNotFound) {
		t.Errorf("the row is %v, want not found", getErr)
	}

	// And the DM may still write it, because a DM is the one who fixes a typo.
	// The row it produces is owned by nobody, which is the same fail-closed
	// answer one step later: the predicate joins on an empty owner column, so the
	// page is the DM's and nobody else's.
	if _, dmErr := f.sync.SyncPath(ctx, "characters/brian/pretend"); dmErr != nil {
		t.Fatalf("the DM was refused a page with a typo in it: %v", dmErr)
	}
	indexed, getErr := f.store.GetPage(ctx, f.campaign.ID, "characters/brian/pretend", store.AsDM(f.campaign.ID))
	if getErr != nil {
		t.Fatalf("GetPage: %v", getErr)
	}
	if indexed.OwnerCharacterPageID == aria.ID {
		t.Error("a conflicting page ended up owned by the character its frontmatter named")
	}
	if indexed.OwnerCharacterPageID != "" {
		t.Errorf("a conflicting page is owned by %q, want nobody", indexed.OwnerCharacterPageID)
	}
}

// TestAFullSyncIsStillTheDMs is the regression guard for the threading: a sync has
// always written as the DM, and it has to keep doing so or a campaign with a
// `dm-only` page and a player bound to a character would stop indexing the page the
// player may not touch.
func TestAFullSyncIsStillTheDMs(t *testing.T) {
	t.Parallel()

	f := newWriterFixture(t)
	ctx := f.ctx

	f.writeFile("npcs/vel",
		"---\ntitle: Captain Vell\ntype: npc\nvisibility: dm-only\n---\n\nThe toll collector.\n")

	report, err := f.sync.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(report.Indexed) != 1 || report.Indexed[0] != "npcs/vel" {
		t.Errorf("the sync indexed %v, want npcs/vel", report.Indexed)
	}

	// And a per-path sync, which is what a watcher calls, is the same.
	f.writeFile("locations/rivergate",
		"---\ntitle: Rivergate\ntype: location\nvisibility: dm-only\n---\n\nA fortified town.\n")

	one, err := f.sync.SyncPath(ctx, "locations/rivergate")
	if err != nil {
		t.Fatalf("SyncPath: %v", err)
	}
	if len(one.Indexed) != 1 {
		t.Errorf("SyncPath indexed %v, want the one page", one.Indexed)
	}
}

// TestOwnerPageIDIsTheSameAnswerASyncGets is the exposed-lookup test. Two callers
// asking one question must get one answer, and the way to find out is to ask both
// and compare.
func TestOwnerPageIDIsTheSameAnswerASyncGets(t *testing.T) {
	t.Parallel()

	f := newWriterFixture(t)
	ctx := f.ctx

	aria := f.createCharacter("aria")

	tests := map[string]struct {
		path    string
		body    string
		wantOwn bool
	}{
		"a character's own page is its own owner": {
			path:    "characters/aria",
			body:    "---\ntitle: Aria\ntype: character\n---\n\nA thief.\n",
			wantOwn: true,
		},
		"a page in the character's folder": {
			path:    "characters/aria/backstory.md",
			body:    "---\ntitle: Backstory\ntype: note\n---\n\nShe grew up in Lockwater.\n",
			wantOwn: true,
		},
		"a page outside it, with the key": {
			path:    "notes/aria-spells.md",
			body:    "---\ntitle: Spells\ntype: note\ncharacter: aria\n---\n\nLevitate.\n",
			wantOwn: true,
		},
		"a page nobody owns": {
			path:    "locations/rivergate.md",
			body:    "---\ntitle: Rivergate\ntype: location\n---\n\nA fortified town.\n",
			wantOwn: false,
		},
		"a page claiming a character that is not there": {
			path:    "notes/ghost.md",
			body:    "---\ntitle: Ghost\ntype: note\ncharacter: nobody\n---\n\nNobody.\n",
			wantOwn: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f.writeFile(tt.path, tt.body)
			if _, err := f.sync.SyncPath(ctx, tt.path); err != nil {
				t.Fatalf("SyncPath: %v", err)
			}

			doc, err := vault.Parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("parsing the fixture: %v", err)
			}

			got, problem := f.sync.OwnerPageID(ctx, tt.path, doc)

			if !tt.wantOwn {
				if problem == nil && got != "" {
					t.Errorf("the page is owned by %q, want nobody", got)
				}
				return
			}
			if problem != nil {
				t.Fatalf("OwnerPageID reported %q, want an owner", problem.Reason)
			}
			if got != aria.ID {
				t.Errorf("the owner is %q, want Aria's page id %q", got, aria.ID)
			}
		})
	}
}

// TestAWriteIsGatedEvenWhenItWouldChangeNothing is the finding, and it is a hole
// that only exists because of how the sync is built.
//
// `SyncPathAs` plans the page and writes it *if the index is not already what the
// file says*. A player who saves content the index already holds writes nothing —
// and so never reaches the store's write gate, because the gate is on the row
// write. The result was a principal who could "save" any page in the campaign,
// as long as what they wrote was what the index already said: a no-op is not a
// refusal, and it is not a success either, it is an absence.
//
// The two tests above are the same hole from the other side: they ask
// `CheckWritableAs`, which derives the page and asks the gate *without* consulting
// whether anything would change. A dry run that short-circuits on "settled" is a
// dry run that says yes.
func TestAWriteIsGatedEvenWhenItWouldChangeNothing(t *testing.T) {
	t.Parallel()

	f := newWriterFixture(t)
	ctx := f.ctx

	// A DM's page, indexed.
	f.writeFile("npcs/vel", "---\ntitle: Captain Vell\ntype: npc\nvisibility: dm-only\n---\n\nThe toll collector.\n")
	if _, err := f.sync.SyncPath(ctx, "npcs/vel"); err != nil {
		t.Fatalf("SyncPath: %v", err)
	}

	player := f.createPlayer("a player with no character")

	// The file is unchanged, so the sync has nothing to do...
	report, err := f.sync.SyncPathAs(ctx, "npcs/vel", player)
	if err != nil {
		t.Fatalf("SyncPathAs on an unchanged page: %v", err)
	}
	if len(report.Indexed) != 0 {
		t.Errorf("the sync indexed %v an unchanged page", report.Indexed)
	}

	// ...and the question "may this player write this page" is still no, because
	// the question is about the page and not about the work.
	if err := f.sync.CheckWritableAs(ctx, "npcs/vel", player); err == nil {
		t.Error("a player with no character was told it may write a DM-only page")
	}

	// And the same answer for a page the player *does* own, whose content they are
	// re-saving unchanged: yes, because the page is theirs, not because the sync
	// had nothing to do.
	aria := f.createCharacter("aria")
	f.bind(player, aria)
	f.writeFile("characters/aria/notes", "---\ntitle: Notes\ntype: note\ncharacter: aria\n---\n\nMine.\n")
	if _, err := f.sync.SyncPath(ctx, "characters/aria/notes"); err != nil {
		t.Fatalf("SyncPath: %v", err)
	}

	if err := f.sync.CheckWritableAs(ctx, "characters/aria/notes", player); err != nil {
		t.Errorf("a player was refused their own page over a save that changed nothing: %v", err)
	}
}
