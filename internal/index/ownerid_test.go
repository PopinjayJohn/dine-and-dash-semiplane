package index_test

import (
	"path/filepath"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// A page's owner is a page id, and the whole of M4's resolution becomes that
// column. These are the cases where the two can disagree, because a disagreement
// is a page whose audience is not who the file says it is.

// The rule the column changed: every page under a character has the *character's
// own page* as its owner, not itself.
//
// This is the difference between "Alice may read her character's backstory" and
// "Alice may read exactly the one file she happens to be bound to", and the second
// is what a predicate correlating on the page itself would have produced.
func TestSyncStoresTheCharacterPageAsTheOwner(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	writeVaultFile(t, vaultDir, "characters/aria.md",
		"---\ntitle: Aria\ntype: character\n---\n\nA lockpicker.\n")
	writeVaultFile(t, vaultDir, "characters/aria/backstory.md",
		"---\ntitle: Backstory\n---\n\nBefore the heist.\n")
	writeVaultFile(t, vaultDir, "locations/rivergate.md",
		"---\ntitle: Rivergate\n---\n\nA fortified town.\n")

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	character, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "characters/aria", store.AsDM(syncer.Campaign().ID))
	if err != nil {
		t.Fatalf("GetPage for the character: %v", err)
	}

	// The character page is its own owner. Without this a character page could not
	// be `dm-and-owner` at all, and a DM marking a character's notes private
	// would be marking a page nobody but themselves could ever open.
	if character.OwnerCharacterPageID != character.ID {
		t.Errorf("the character page's owner is %q, want its own id %q",
			character.OwnerCharacterPageID, character.ID)
	}

	// Everything under it has the character as its owner, and the three of them
	// agree.
	for _, path := range []string{"characters/aria/backstory", "characters/aria"} {
		page, getErr := syncer.Store().GetPage(ctx, syncer.Campaign().ID, path, store.AsDM(syncer.Campaign().ID))
		if getErr != nil {
			t.Fatalf("GetPage(%q): %v", path, getErr)
		}
		if page.OwnerCharacterPageID != character.ID {
			t.Errorf("%q has the owner %q, want the character page %q",
				path, page.OwnerCharacterPageID, character.ID)
		}
	}

	// And a page outside any character has no owner at all, which is the
	// fail-closed reading rather than an accident of the walk.
	town, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "locations/rivergate", store.AsDM(syncer.Campaign().ID))
	if err != nil {
		t.Fatalf("GetPage for the town: %v", err)
	}
	if town.OwnerCharacterPageID != "" {
		t.Errorf("a page outside any character has the owner %q, want none", town.OwnerCharacterPageID)
	}
}

// A `character:` key on a page outside the subtree is how a player's spell sheet
// says whose it is, and it resolves to the same id the path rule would.
func TestSyncResolvesACharacterKeyToTheSameOwner(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	writeVaultFile(t, vaultDir, "characters/aria.md",
		"---\ntitle: Aria\ntype: character\n---\n\nA lockpicker.\n")
	writeVaultFile(t, vaultDir, "notes/arias-spells.md",
		"---\ntitle: Aria's spells\ncharacter: aria\n---\n\nMisty step.\n")

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	character, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "characters/aria", store.AsDM(syncer.Campaign().ID))
	if err != nil {
		t.Fatalf("GetPage for the character: %v", err)
	}
	spells, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "notes/arias-spells", store.AsDM(syncer.Campaign().ID))
	if err != nil {
		t.Fatalf("GetPage for the spell notes: %v", err)
	}

	if spells.OwnerCharacterPageID != character.ID {
		t.Errorf("the spell notes have the owner %q, want the character page %q",
			spells.OwnerCharacterPageID, character.ID)
	}
}

// A character page that does not exist leaves the page with no owner, and the
// report says why.
//
// No owner is the fail-closed answer: a `dm-and-owner` page nobody owns is
// readable by the DM and by nobody else, which is a missing feature rather than a
// disclosure. The alternative -- carrying the slug in a column documented as a
// page id, and letting the predicate fail to join -- reaches the same answer by a
// longer route, with a lie in a column in it.
func TestACharacterThatDoesNotExistLeavesNoOwner(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	writeVaultFile(t, vaultDir, "notes/arias-spells.md",
		"---\ntitle: Aria's spells\ncharacter: aria\n---\n\nMisty step.\n")

	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if len(report.Ownership) == 0 {
		t.Fatal("a character: key naming a page that does not exist was not reported")
	}
	page, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "notes/arias-spells", store.AsDM(syncer.Campaign().ID))
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.OwnerCharacterPageID != "" {
		t.Errorf("the page has the owner %q, want none: an owner that does not exist is not an owner",
			page.OwnerCharacterPageID)
	}
}

// An owner is part of what "settled" means, so a page whose character moved is
// rewritten rather than left with the old owner forever.
func TestAChangeOfOwnerIsNotSettled(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	writeVaultFile(t, vaultDir, "characters/aria.md",
		"---\ntitle: Aria\ntype: character\n---\n\nA lockpicker.\n")
	writeVaultFile(t, vaultDir, "notes/arias-spells.md",
		"---\ntitle: Aria's spells\ncharacter: aria\n---\n\nMisty step.\n")

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("the first Sync: %v", err)
	}
	unchanged, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("the second Sync: %v", err)
	}
	if unchanged.Changed() != 0 {
		t.Fatalf("an unchanged vault was written to: %+v", unchanged)
	}

	// The note changes hands.
	writeVaultFile(t, vaultDir, "notes/arias-spells.md",
		"---\ntitle: Aria's spells\ncharacter: brian\n---\n\nMisty step.\n")
	reported, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("the third Sync: %v", err)
	}
	if reported.Changed() == 0 {
		t.Error("a page whose character changed was settled: the old owner is still on the row")
	}

	// And the report says why, which is the other half: a player refused their own
	// notes because a `character:` key was a typo is a support ticket somebody
	// has to read.
	if len(reported.Ownership) == 0 {
		t.Error("a character: key naming a page that does not exist was not reported")
	}

	page, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "notes/arias-spells", store.AsDM(syncer.Campaign().ID))
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.OwnerCharacterPageID != "" {
		t.Errorf("the page still has the owner %q, want none: the character it named does not exist", page.OwnerCharacterPageID)
	}
}
