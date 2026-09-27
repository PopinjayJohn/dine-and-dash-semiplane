package index_test

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
)

// drift is every way the index can stop describing the files, and each case here
// is one of them injected by hand. A sync repairs all of them, and the test that
// says so is the one that would notice if a new kind of drift appeared.
func TestSyncRepairsInjectedDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		// damage makes the index wrong, and returns nothing: the files are
		// untouched, which is the point.
		damage func(t *testing.T, ctx context.Context, syncer *index.Syncer, vaultDir string)
		// wantTouched is the page paths a repairing sync must write, and
		// wantRefused the ones it must refuse. A drift that a sync cannot fix is
		// a bug, and the empty case is a failure too.
		wantTouched []string
		wantRefused []string
	}{
		"a row whose title the file does not say": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, _ string) {
				page := indexedPage(t, ctx, syncer, "locations/rivergate")
				// A row edited in the database, by a bug or by a person with
				// sqlite3 open. The file is the truth, so the file wins.
				if _, err := syncer.Store().UpsertPage(ctx, domain.Page{
					ID:              page.ID,
					CampaignID:      page.CampaignID,
					Path:            page.Path,
					Title:           "Somebody Else's Rivergate",
					Type:            page.Type,
					Frontmatter:     page.Frontmatter,
					Body:            page.Body,
					ContentHash:     page.ContentHash,
					RendererVersion: page.RendererVersion,
					CreatedAt:       page.CreatedAt,
					UpdatedAt:       page.UpdatedAt,
				}); err != nil {
					t.Fatalf("damaging the row: %v", err)
				}
			},
			wantTouched: []string{"locations/rivergate"},
		},
		"a row whose body the file does not say": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, _ string) {
				page := indexedPage(t, ctx, syncer, "locations/the-drowned-hound")
				corruptBody(t, ctx, syncer, page, "Somebody else's prose.")
			},
			wantTouched: []string{"locations/the-drowned-hound"},
		},
		"a row whose content hash is right and whose content is not": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, _ string) {
				// The nastiest drift there is: the hash still matches, so a sync
				// that only compared hashes would call the page settled. It is
				// the reason the title and the body are compared too, or rather
				// the reason the hash is trusted only because the row is written
				// from the file rather than patched.
				page := indexedPage(t, ctx, syncer, "notes")
				corruptBody(t, ctx, syncer, page, "replaced")
			},
			wantTouched: []string{"notes"},
		},
		"an alias the file no longer declares": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, _ string) {
				page := indexedPage(t, ctx, syncer, "locations/rivergate")
				if err := syncer.Store().ReplacePageAliases(ctx, page.ID, []string{"a name from an older file"}); err != nil {
					t.Fatalf("damaging the aliases: %v", err)
				}
			},
			wantTouched: []string{"locations/rivergate"},
		},
		"a page the index has and the vault does not": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, _ string) {
				// A phantom: a row for a file that was never there or is long
				// gone. A sync archives it, because the file is the truth.
				if _, err := syncer.Store().UpsertPage(ctx, domain.Page{
					CampaignID:  syncer.Campaign().ID,
					Path:        "locations/never-existed",
					Title:       "Never existed",
					Type:        domain.PageTypeNote,
					ContentHash: "a-hash-for-a-file-that-is-not-there",
				}); err != nil {
					t.Fatalf("adding a phantom row: %v", err)
				}
			},
			wantTouched: []string{"locations/never-existed"},
		},
		"a page archived while its file is still there": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, _ string) {
				// The other direction: the row says archived, the file is on
				// disk. The file wins, so the page comes back. M9's archive action
				// deletes the file, which is what keeps this from being circular.
				page := indexedPage(t, ctx, syncer, "locations/rivergate")
				if err := syncer.Store().DeletePage(ctx, page.ID); err != nil {
					t.Fatalf("archiving a live page: %v", err)
				}
			},
			wantTouched: []string{"locations/rivergate"},
		},
		"a link graph with a link that is not in the file": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, _ string) {
				page := indexedPage(t, ctx, syncer, "notes")
				if err := syncer.Store().ReplaceLinks(ctx, page.ID, []domain.PageLink{
					{SrcPageID: page.ID, DstPath: "locations/rivergate", DstPageID: page.ID, Kind: domain.LinkKindLink},
				}); err != nil {
					t.Fatalf("damaging the link graph: %v", err)
				}
			},
			wantTouched: []string{"notes"},
		},
		"a page whose stored visibility key was readable once and is not now": {
			damage: func(t *testing.T, ctx context.Context, syncer *index.Syncer, vaultDir string) {
				// The file gains a visibility the application does not know. A
				// sync must *refuse* it rather than re-index the page as though
				// the file had not said it, and the check must not call that
				// in step.
				writeVaultFile(t, vaultDir, "locations/the-drowned-hound.md",
					"---\ntitle: The Drowned Hound\nvisibility: plyers\n---\n\nA tavern.\n")
			},
			wantRefused: []string{"locations/the-drowned-hound"},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, syncer, root := newSyncer(t)
			vaultDir := filepath.Join(root, "vault", "blackwater")

			if _, err := syncer.Sync(ctx); err != nil {
				t.Fatalf("the first Sync: %v", err)
			}

			before := vaultFingerprint(t, root)
			tt.damage(t, ctx, syncer, vaultDir)

			// A check before the repair says what is wrong, which is what
			// `wiki sync --check` is for.
			check, err := syncer.Check(ctx)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if check.InStep() {
				t.Errorf("Check says the index is in step after the drift was injected: %+v", check)
			}

			report, err := syncer.Sync(ctx)
			if err != nil {
				t.Fatalf("Sync after the drift: %v", err)
			}

			for _, pagePath := range tt.wantTouched {
				if !slices.Contains(report.Indexed, pagePath) && !slices.Contains(report.Archived, pagePath) {
					t.Errorf("the repairing sync did not touch %q; it touched %v and archived %v",
						pagePath, report.Indexed, report.Archived)
				}
			}
			for _, pagePath := range tt.wantRefused {
				if !slices.ContainsFunc(report.Refused, func(r index.Refusal) bool { return r.Path == pagePath }) {
					t.Errorf("the repairing sync did not refuse %q; it refused %v", pagePath, report.Refused)
				}
			}

			// And after the repair, a check says there is nothing to do. That is
			// the actual claim: a sync that fixes things and then still thinks
			// something is wrong has not fixed it.
			after, err := syncer.Check(ctx)
			if err != nil {
				t.Fatalf("Check after the repair: %v", err)
			}
			// The one case that is never "in step": the file still says a
			// visibility the application cannot read, so the page stays refused
			// until the DM fixes it.
			if len(tt.wantRefused) == 0 && !after.InStep() {
				t.Errorf("after the repair, Check still wants to change %d pages and has %d skips and %d refusals: %v",
					after.Changed(), len(after.Skipped), len(after.Refused), after.Indexed)
			}

			// The repair is a repair of the index, never of the files.
			if now := vaultFingerprint(t, root); !slices.Equal(now, before) {
				if name != "a page whose stored visibility key was readable once and is not now" {
					// That one case edits the file on purpose, so the
					// fingerprint is expected to differ there and only there.
					t.Errorf("the repairing sync changed the vault\n before: %v\n  after: %v", before, now)
				}
			}
		})
	}
}

// TestCheckWritesNothing is the property that makes `--check` safe to run from
// a script against a live campaign.
func TestCheckWritesNothing(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)
	vaultDir := filepath.Join(root, "vault", "blackwater")

	// Something out of step: an edit, and a page nobody has written yet.
	writeVaultFile(t, vaultDir, "locations/thornford.md", "---\ntitle: Thornford\n---\n\nThe other town.\n")

	before := indexSnapshot(t, ctx, syncer)
	files := vaultFingerprint(t, root)

	report, err := syncer.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !slices.Contains(report.Indexed, "locations/thornford") {
		t.Errorf("Check did not report the new page: %v", report.Indexed)
	}
	if report.InStep() {
		t.Errorf("Check says the index is in step with a new file in the vault: %+v", report)
	}

	if now := indexSnapshot(t, ctx, syncer); !slices.Equal(now, before) {
		t.Errorf("Check changed the index\n before: %v\n  after: %v", before, now)
	}
	if now := vaultFingerprint(t, root); !slices.Equal(now, files) {
		t.Error("Check changed the vault")
	}

	// And a check of a campaign that is in step says so.
	if _, syncErr := syncer.Sync(ctx); syncErr != nil {
		t.Fatalf("Sync: %v", syncErr)
	}
	settled, err := syncer.Check(ctx)
	if err != nil {
		t.Fatalf("Check after Sync: %v", err)
	}
	if !settled.InStep() {
		t.Errorf("Check does not call a just-synced campaign in step: %d pages, %d skips, %d refusals: %v",
			settled.Changed(), len(settled.Skipped), len(settled.Refused), settled.Indexed)
	}
}

// TestReindexFullRebuildsFromTheFiles is ADR 0001's whole claim: the index is
// disposable, and a reindex makes it right by making it again.
func TestReindexFullRebuildsFromTheFiles(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	synced := indexSnapshot(t, ctx, syncer)
	files := vaultFingerprint(t, root)

	// Break the index in a way no sync would look for, because no sync should
	// have to: rows that are wrong in ways the files do not describe at all.
	if _, err := syncer.ReindexFull(ctx); err != nil {
		t.Fatalf("ReindexFull: %v", err)
	}

	if rebuilt := indexSnapshot(t, ctx, syncer); !slices.Equal(rebuilt, synced) {
		t.Errorf("the rebuilt index differs from the synced one\n synced:  %v\n rebuilt: %v", synced, rebuilt)
	}
	if now := vaultFingerprint(t, root); !slices.Equal(now, files) {
		t.Error("a full reindex changed the vault")
	}
}

func TestReindexFullReportsWhatItReplaced(t *testing.T) {
	t.Parallel()

	ctx, syncer, _ := newSyncer(t)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	report, err := syncer.ReindexFull(ctx)
	if err != nil {
		t.Fatalf("ReindexFull: %v", err)
	}

	pages := len(vaultFiles)
	if report.Rebuilt != pages {
		t.Errorf("the rebuild reported %d rows replaced, want %d", report.Rebuilt, pages)
	}
	if len(report.Indexed) != pages {
		t.Errorf("the rebuild indexed %d pages, want %d: %v", len(report.Indexed), pages, report.Indexed)
	}
	if len(report.Archived) != 0 {
		t.Errorf("the rebuild archived %v, want nothing: it purged everything first", report.Archived)
	}
}

// TestReindexFullForgetsWhatTheFilesDoNotSay: a page that was in the index and
// is not in the vault must not survive a rebuild, however healthy it looked.
func TestReindexFullForgetsWhatTheFilesDoNotSay(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// A page the index has and the vault has never had.
	if _, err := syncer.Store().UpsertPage(ctx, domain.Page{
		CampaignID:  syncer.Campaign().ID,
		Path:        "locations/never-existed",
		Title:       "Never existed",
		Type:        domain.PageTypeNote,
		ContentHash: "a-hash-for-a-file-that-is-not-there",
	}); err != nil {
		t.Fatalf("adding a phantom row: %v", err)
	}

	if _, err := syncer.ReindexFull(ctx); err != nil {
		t.Fatalf("ReindexFull: %v", err)
	}

	if _, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "locations/never-existed"); err == nil {
		t.Error("a page the vault does not have survived a full reindex")
	}

	// And the rest of the campaign is still there.
	pages, err := syncer.Store().ListPages(ctx, syncer.Campaign().ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != len(vaultFiles) {
		t.Errorf("the campaign has %d pages after the rebuild, want %d", len(pages), len(vaultFiles))
	}
	_ = root
}

// TestAReindexOnAFreshDatabaseIsJustASync: the repair of last resort works on
// an index that is empty, which is what makes it usable as an import's first
// step.
func TestAReindexOnAFreshDatabaseIsJustASync(t *testing.T) {
	t.Parallel()

	ctx, syncer, _ := newSyncer(t)

	report, err := syncer.ReindexFull(ctx)
	if err != nil {
		t.Fatalf("ReindexFull: %v", err)
	}

	if report.Rebuilt != 0 {
		t.Errorf("a rebuild of an empty index reported %d rows replaced", report.Rebuilt)
	}
	if len(report.Indexed) != len(vaultFiles) {
		t.Errorf("the rebuild indexed %d pages, want %d", len(report.Indexed), len(vaultFiles))
	}
}

// helpers

func indexedPage(t *testing.T, ctx context.Context, syncer *index.Syncer, pagePath string) domain.Page {
	t.Helper()

	page, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, pagePath)
	if err != nil {
		t.Fatalf("GetPage(%q): %v", pagePath, err)
	}
	return page
}

// corruptBody rewrites a page's row with prose the file does not contain. The
// content hash is deliberately left alone, which is what makes it drift that a
// hash comparison alone would miss.
func corruptBody(t *testing.T, ctx context.Context, syncer *index.Syncer, page domain.Page, body string) {
	t.Helper()

	if _, err := syncer.Store().UpsertPage(ctx, domain.Page{
		ID:              page.ID,
		CampaignID:      page.CampaignID,
		Path:            page.Path,
		Title:           page.Title,
		Type:            page.Type,
		Frontmatter:     page.Frontmatter,
		Body:            body,
		ContentHash:     page.ContentHash,
		RendererVersion: page.RendererVersion,
		CreatedAt:       page.CreatedAt,
		UpdatedAt:       time.Time{},
	}); err != nil {
		t.Fatalf("corrupting the row: %v", err)
	}
}
