package index_test

import (
	"path/filepath"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// A page's audience reaches the row. M4 read the `visibility` key, refused a
// value it did not recognise, and then discarded the ones it did — so a page
// written `visibility: dm-only` was indexed as a page anyone could read, and the
// read predicate had nothing to filter on. These are the cases that would catch
// that coming back, and the last one is the one that matters: a page that changes
// its audience has to be re-indexed, or the row still says what it said
// yesterday.
func TestSyncRecordsThePageAudience(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		file string
		body string
		want domain.Visibility
	}{
		"a page that says players": {
			file: "locations/rivergate.md",
			body: "---\ntitle: Rivergate\nvisibility: players\n---\n\nA fortified town.\n",
			want: domain.VisibilityPlayers,
		},
		"a page that says dm-only": {
			file: "npcs/garros-ironbar.md",
			body: "---\ntitle: Garros Ironbar\nvisibility: dm-only\n---\n\nCollects the toll.\n",
			want: domain.VisibilityDMOnly,
		},
		"a page that says dm-and-owner": {
			file: "characters/aria/backstory.md",
			body: "---\ntitle: Backstory\nvisibility: dm-and-owner\n---\n\nBefore the heist.\n",
			want: domain.VisibilityDMAndOwner,
		},
		"a page that says nothing gets the default": {
			file: "notes.md",
			body: "A page with no frontmatter at all.\n",
			want: domain.VisibilityPlayers,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, syncer, root := newSyncer(t)
			writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), tt.file, tt.body)

			if _, err := syncer.Sync(ctx); err != nil {
				t.Fatalf("Sync: %v", err)
			}

			stored, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, pagePathOf(tt.file))
			if err != nil {
				t.Fatalf("GetPage: %v", err)
			}
			if stored.Visibility != tt.want {
				t.Errorf("the page is indexed with visibility %q, want %q",
					stored.Visibility, tt.want)
			}
			if stored.Audience() != tt.want {
				t.Errorf("the page's audience is %q, want %q", stored.Audience(), tt.want)
			}
		})
	}
}

// A page whose audience changes is not settled, and the sync writes it. Settling
// on the content hash alone would miss this: the file changed, so the hash
// changed, so a hash-based check would catch it — but a row that rotted, or a
// change to how the audience is derived, would not be, and the field is
// security-relevant enough to be compared by name like the rest.
func TestSyncRewritesAPageWhoseAudienceChanged(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")
	const file = "npcs/garros-ironbar.md"
	const path = "npcs/garros-ironbar"

	writeVaultFile(t, vaultDir, file,
		"---\ntitle: Garros Ironbar\nvisibility: players\n---\n\nCollects the toll.\n")
	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("the first Sync: %v", err)
	}

	// A second sync with nothing changed writes nothing, which is the baseline
	// the audience change has to be visible against.
	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("the second Sync: %v", err)
	}
	if report.Changed() != 0 {
		t.Fatalf("an unchanged vault was written to: %+v", report)
	}

	writeVaultFile(t, vaultDir, file,
		"---\ntitle: Garros Ironbar\nvisibility: dm-only\n---\n\nCollects the toll.\n")
	if _, syncErr := syncer.Sync(ctx); syncErr != nil {
		t.Fatalf("the third Sync: %v", syncErr)
	}

	stored, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, path)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if stored.Visibility != domain.VisibilityDMOnly {
		t.Errorf("after a visibility change the row says %q, want %q: the audience is compared by name, not by hash",
			stored.Visibility, domain.VisibilityDMOnly)
	}
}
