package index_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// The vault fixtures. A DM's vault has pages that link to each other, a page
// with no frontmatter, a callout, a secret, and a page whose name is not a legal
// page path -- because one always does.
var vaultFiles = map[string]string{
	"locations/rivergate.md": `---
title: Rivergate
aliases: [the toll town, Flussport]
tags: [location, hub]
type: location
visibility: players
---

A fortified town at the confluence of the [[Blackwater]] and the [[Thorn]].

> [!warning] The winter
> The winter is not discussed further here.

The toll-collector is [[npcs/garros-ironbar]] and the map is
![[_attachments/map-rivergate.png]].
`,
	"npcs/garros-ironbar.md": `---
title: Garros Ironbar
type: npc
---

Collects the toll, and has since the winter.

> [!SECRET] What he knows
> He saw the coin go east and said nothing.
`,
	"locations/the-drowned-hound.md": `---
type: location
---

A tavern that has not been a tavern for a year. It is still on the map,
and the [[rivergate]] sign is still outside it.
`,
	"notes.md": "A page with no frontmatter at all, because a DM writing prose\nshould not have to add YAML to be indexed.\n",
}

func newSyncer(t *testing.T) (context.Context, *index.Syncer, string) { //nolint:revive // the root is the vault's parent
	t.Helper()

	ctx := context.Background()
	root := t.TempDir()

	// The data directory layout of ADR 0011: a vault per campaign under
	// vault/, and the database beside it.
	vaultDir := filepath.Join(root, "vault", "blackwater")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatalf("creating the vault directory: %v", err)
	}
	for pagePath, body := range vaultFiles {
		writeVaultFile(t, vaultDir, pagePath, body)
	}

	v, err := vault.Open(vaultDir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })

	s := newStore(t, root)

	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	return ctx, index.New(v, s, campaign), root
}

func TestSyncWritesEveryPageIntoTheIndex(t *testing.T) {
	t.Parallel()

	ctx, syncer, _ := newSyncer(t)

	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if got, want := len(report.Indexed), len(vaultFiles); got != want {
		t.Errorf("the sync indexed %d pages, want %d: %v", got, want, report.Indexed)
	}
	if report.Changed() != len(vaultFiles) {
		t.Errorf("the sync changed %d rows, want %d", report.Changed(), len(vaultFiles))
	}
	if len(report.Skipped) != 0 || len(report.Refused) != 0 {
		t.Errorf("the sync had %d skips and %d refusals for a clean vault: %v %v",
			len(report.Skipped), len(report.Refused), report.Skipped, report.Refused)
	}

	// Every field the index stores is the file's, which is what "dual write"
	// means here: one file, one row, and they agree.
	for pagePath, body := range vaultFiles {
		t.Run(pagePath, func(t *testing.T) {
			// The *file* is `path.md`; the page's identity is `path`, and the
			// index knows nothing about the extension. Passing a file name where
			// a page path belongs is a not-found from the store, which reads
			// like a sync bug and is not one.
			assertIndexedFromFile(t, ctx, syncer, pagePathOf(pagePath), body)
		})
	}
}

func TestSyncIsIdempotentOverManyRuns(t *testing.T) {
	t.Parallel()

	ctx, syncer, _ := newSyncer(t)

	first, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if first.Changed() == 0 {
		t.Fatal("the first sync changed nothing, so there is nothing to be idempotent about")
	}
	before := indexSnapshot(t, ctx, syncer)

	// Five more runs. The second should find nothing to do, and the four after
	// that must not drift away from the second.
	for run := range 5 {
		report, err := syncer.Sync(ctx)
		if err != nil {
			t.Fatalf("Sync, run %d: %v", run+2, err)
		}
		if report.Changed() != 0 {
			t.Errorf("run %d changed %d rows (%v, %v), want none", run+2, report.Changed(),
				report.Indexed, report.Archived)
		}
		if report.Unchanged != len(vaultFiles) {
			t.Errorf("run %d reported %d unchanged pages, want %d", run+2, report.Unchanged, len(vaultFiles))
		}

		after := indexSnapshot(t, ctx, syncer)
		if !slices.Equal(after, before) {
			t.Errorf("run %d changed the index\n before: %v\n  after: %v", run+2, before, after)
		}
	}
}

// TestSyncDoesNotTouchTheVault is the property this milestone can break most
// easily, and the one AGENTS.md names: a sync that reads a file and writes it
// back -- even to normalise it -- puts a whole-file diff in a DM's git.
func TestSyncDoesNotTouchTheVault(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)

	before := vaultFingerprint(t, root)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("second Sync: %v", err)
	}

	after := vaultFingerprint(t, root)
	if !slices.Equal(after, before) {
		t.Errorf("the sync changed the vault\n before: %v\n  after: %v", before, after)
	}
}

func TestSyncPicksUpAnExternalEdit(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// A DM opens the vault in Obsidian and edits a page. That is the whole
	// reason this engine exists: the files are the truth and the app is a
	// reader of them.
	const edited = `---
title: Rivergate
aliases: [the toll town, Flussport]
type: location
---

A fortified town, half under water since the winter.
`
	writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), "locations/rivergate.md", edited)

	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if !slices.Contains(report.Indexed, "locations/rivergate") {
		t.Errorf("the sync indexed %v, want it to include the edited page", report.Indexed)
	}
	if len(report.Indexed) != 1 {
		t.Errorf("the sync indexed %d pages, want only the edited one: %v", len(report.Indexed), report.Indexed)
	}

	page, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "locations/rivergate")
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if !strings.Contains(page.Body, "half under water") {
		t.Errorf("the index has the old body:\n%s", page.Body)
	}
	if page.ContentHash != vault.Hash([]byte(edited)) {
		t.Error("the index has a content hash that is not the file's")
	}
}

func TestSyncArchivesAPageWhoseFileIsGone(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// The DM deletes a file in Obsidian.
	if err := os.Remove(filepath.Join(root, "vault", "blackwater", "npcs", "garros-ironbar.md")); err != nil {
		t.Fatalf("removing the file: %v", err)
	}

	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if !slices.Contains(report.Archived, "npcs/garros-ironbar") {
		t.Errorf("the sync archived %v, want it to include the deleted page", report.Archived)
	}
	if len(report.Archived) != 1 {
		t.Errorf("the sync archived %d pages, want 1: %v", len(report.Archived), report.Archived)
	}

	// Archived, not gone: the row is still there with its revisions, and it is
	// out of every read.
	if _, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, "npcs/garros-ironbar"); err == nil {
		t.Error("an archived page is still returned by a page read")
	}

	// And the link that pointed at it stops resolving, which is the whole
	// point: a link to a page that is not there is unresolved, and a link to an
	// archived one would be a link to nothing.
	if _, found, err := index.NewResolver(syncer.Store(), syncer.Campaign().ID).
		Resolve(ctx, "npcs/garros-ironbar", ""); err != nil {
		t.Fatalf("Resolve: %v", err)
	} else if found {
		t.Error("an archived page still resolves")
	}
}

func TestSyncSkipsWhatItCannotReadAndRefusesWhatItMustNot(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		file string
		body string

		wantSkip    bool
		wantRefusal bool
	}{
		"frontmatter that is not a set of keys": {
			file: "broken/a-list.md", body: "---\n- Rivergate\n- Thornford\n---\n\nBody.\n",
			wantSkip: true,
		},
		"frontmatter that is not valid YAML": {
			file: "broken/tabs.md", body: "---\ntitle: Rivergate\n\ttype: location\n---\n\nBody.\n",
			wantSkip: true,
		},
		"a file whose name is not a page path": {
			file: "broken/.hidden.md", body: "A file whose name starts with a dot.\n",
			wantSkip: true,
		},
		"a visibility key with a typo": {
			file: "broken/visibility.md", body: "---\ntitle: Somewhere\nvisibility: plyers\n---\n\nBody.\n",
			wantRefusal: true,
		},
		"an invented visibility level": {
			file: "broken/level.md", body: "---\ntitle: Somewhere\nvisibility: everyone\n---\n\nBody.\n",
			wantRefusal: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx, syncer, root := newSyncer(t)
			writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), tt.file, tt.body)

			report, err := syncer.Sync(ctx)
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}

			page := pagePathOf(tt.file)
			skippedIt := slices.ContainsFunc(report.Skipped, func(s index.Skip) bool { return s.Path == page })
			refusedIt := slices.ContainsFunc(report.Refused, func(r index.Refusal) bool { return r.Path == page })
			if skippedIt != tt.wantSkip {
				t.Errorf("skipped = %t, want %t: %v", skippedIt, tt.wantSkip, report.Skipped)
			}
			if refusedIt != tt.wantRefusal {
				t.Errorf("refused = %t, want %t: %v", refusedIt, tt.wantRefusal, report.Refused)
			}

			// Either way the page is not in the index, and the rest of the
			// campaign still is: one unreadable file does not stop the other 240.
			if _, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, page); err == nil {
				t.Error("the page is in the index despite not being readable")
			}
			if report.Unchanged+len(report.Indexed) < len(vaultFiles) {
				t.Errorf("the sync stopped early: %+v", report)
			}

			// And the report says why, in a form a DM can act on.
			if report.Err() == nil {
				t.Error("Report.Err() is nil for a sync that did not index a file")
			}
		})
	}
}

// TestSyncResolvesLinksAndRefreshesThemWhenTheTargetAppears is the two-way
// direction that a hash comparison alone would miss: a page that has not changed
// has links that may have.
func TestSyncResolvesLinksAndRefreshesThemWhenTheTargetAppears(t *testing.T) {
	t.Parallel()

	ctx, syncer, root := newSyncer(t)

	// A page that links to a page nobody has written yet.
	const file = "locations/pending.md"
	writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), file,
		"---\ntitle: Pending\n---\n\nSee [[locations/thornford]] for the other town.\n")

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	links := linksFrom(t, ctx, syncer, pagePathOf(file))
	if len(links) != 1 {
		t.Fatalf("the page has %d links, want 1", len(links))
	}
	if links[0].Resolved() {
		t.Error("a link to a page that does not exist is resolved")
	}
	if links[0].DstPath != "locations/thornford" {
		t.Errorf("the link's destination is %q, want the target as written", links[0].DstPath)
	}

	// The DM writes the page. The pending page's *own* file has not changed, and
	// its link has to resolve anyway.
	writeVaultFile(t, filepath.Join(root, "vault", "blackwater"), "locations/thornford.md",
		"---\ntitle: Thornford\n---\n\nThe other town.\n")

	report, err := syncer.Sync(ctx)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if !slices.Contains(report.Indexed, pagePathOf(file)) {
		t.Errorf("the sync indexed %v, want it to revisit the page whose link now resolves", report.Indexed)
	}

	links = linksFrom(t, ctx, syncer, pagePathOf(file))
	if !links[0].Resolved() {
		t.Error("the link still does not resolve after its target was indexed")
	}
	if links[0].DstPageID == "" {
		t.Error("a resolved link has no destination page")
	}
}

func TestSyncWritesTheLinkGraph(t *testing.T) {
	t.Parallel()

	ctx, syncer, _ := newSyncer(t)

	if _, err := syncer.Sync(ctx); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	town := linksFrom(t, ctx, syncer, "locations/rivergate")
	if len(town) != 4 {
		t.Errorf("Rivergate has %d links, want 4: %+v", len(town), town)
	}

	// A link to a name nobody answers to is recorded anyway, with the name the DM
	// wrote: the graph has to be able to answer "what mentions this", including
	// for a page that does not exist yet.
	byTarget := map[string]domain.PageLink{}
	for _, link := range town {
		byTarget[link.DstPath] = link
	}

	// A link to a name nobody answers to is recorded anyway, and unresolved: the
	// graph has to be able to answer "what mentions this", including for a page
	// that does not exist yet.
	for _, want := range []string{"Blackwater", "Thorn"} {
		link, ok := byTarget[want]
		if !ok {
			t.Errorf("no link to %q: %+v", want, town)
			continue
		}
		if link.Resolved() {
			t.Errorf("the link to %q resolved, and there is no such page", want)
		}
	}

	// And a link to a page further down the alphabet is resolved by a later pass
	// of the same sync, so `wiki sync` is one command rather than several.
	forward, ok := byTarget["npcs/garros-ironbar"]
	if !ok {
		t.Error("the link to npcs/garros-ironbar is missing")
	} else if !forward.Resolved() || forward.DstPageID == "" {
		t.Errorf("the link to npcs/garros-ironbar is unresolved after a full sync: %+v", forward)
	}

	// The embed is an embed.
	if embed, ok := byTarget["_attachments/map-rivergate.png"]; !ok {
		t.Error("the embed is not in the link graph")
	} else if embed.Kind != domain.LinkKindEmbed {
		t.Errorf("the embed is recorded as a %q", embed.Kind)
	}

	// A markdown link to a page path is in the graph too, because the DM wrote
	// one and a wiki that resolved one and not the other has a link that works
	// in one editor and not in the other.
	hound := linksFrom(t, ctx, syncer, "locations/the-drowned-hound")
	if len(hound) != 1 || !strings.Contains(hound[0].DstPath, "rivergate") {
		t.Errorf("The Drowned Hound's links are %+v, want one to rivergate", hound)
	}
}

// helpers

func assertIndexedFromFile(t *testing.T, ctx context.Context, syncer *index.Syncer, pagePath, body string) {
	t.Helper()

	indexed, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, pagePath)
	if err != nil {
		t.Fatalf("GetPage(%q): %v", pagePath, err)
	}

	// The expectation is the file as the *vault* reads it, rather than a split
	// of the source the test does itself. The vault owns where the body starts;
	// a test that re-derived it would be testing its own arithmetic.
	doc, err := vault.Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse(%q): %v", pagePath, err)
	}
	frontmatter, err := doc.FrontmatterText()
	if err != nil {
		t.Fatalf("FrontmatterText: %v", err)
	}

	if indexed.ContentHash != vault.Hash([]byte(body)) {
		t.Error("the content hash is not the file's")
	}
	if indexed.Body != doc.Body() {
		t.Errorf("the body in the index is\n%q\nwant\n%q", indexed.Body, doc.Body())
	}
	if indexed.Frontmatter != frontmatter {
		t.Errorf("the frontmatter in the index is\n%q\nwant\n%q", indexed.Frontmatter, frontmatter)
	}
	if indexed.RendererVersion == 0 {
		t.Error("the row was stored with renderer version 0, so a renderer upgrade would not re-render it")
	}
	if indexed.CampaignID != syncer.Campaign().ID {
		t.Errorf("the row belongs to campaign %s, want %s", indexed.CampaignID, syncer.Campaign().ID)
	}
	if indexed.IsDeleted {
		t.Error("a page that is in the vault is stored as archived")
	}

	// The title and the type are what a file that says nothing fall back to,
	// and both fallbacks are decisions rather than defaults.
	wantTitle := doc.Title()
	if wantTitle == "" {
		wantTitle = pagePath[strings.LastIndex(pagePath, "/")+1:]
	}
	if indexed.Title != wantTitle {
		t.Errorf("the title is %q, want %q", indexed.Title, wantTitle)
	}
	wantType := doc.PageType()
	if wantType == "" {
		wantType = domain.PageTypeNote
	}
	if indexed.Type != wantType {
		t.Errorf("the type is %q, want %q", indexed.Type, wantType)
	}
}

func linksFrom(t *testing.T, ctx context.Context, syncer *index.Syncer, pagePath string) []domain.PageLink {
	t.Helper()

	page, err := syncer.Store().GetPage(ctx, syncer.Campaign().ID, pagePath)
	if err != nil {
		t.Fatalf("GetPage(%q): %v", pagePath, err)
	}

	links, err := syncer.Store().LinksFrom(ctx, page.ID)
	if err != nil {
		t.Fatalf("LinksFrom(%q): %v", pagePath, err)
	}
	return links
}

// indexSnapshot is every row of the campaign, in a comparable form. Two runs
// have to produce the same one, and a comparison of structs with a clock in
// them would be a comparison of instants rather than of content.
func indexSnapshot(t *testing.T, ctx context.Context, syncer *index.Syncer) []string {
	t.Helper()

	pages, err := syncer.Store().ListPages(ctx, syncer.Campaign().ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}

	snapshot := make([]string, 0, len(pages))
	for _, page := range pages {
		links, err := syncer.Store().LinksFrom(ctx, page.ID)
		if err != nil {
			t.Fatalf("LinksFrom: %v", err)
		}
		described := make([]string, 0, len(links))
		for _, link := range links {
			described = append(described, link.DstPath+"/"+link.Kind.String())
		}
		sort.Strings(described)

		targets, err := syncer.Store().PageTargets(ctx, page.ID)
		if err != nil {
			t.Fatalf("PageTargets: %v", err)
		}

		snapshot = append(snapshot, strings.Join([]string{
			page.Path,
			page.Title,
			page.ContentHash,
			strconv.Itoa(page.RendererVersion),
			strings.Join(described, ","),
			strings.Join(targets["alias"], ","),
		}, "|"))
	}

	sort.Strings(snapshot)
	return snapshot
}

// vaultFingerprint is every markdown file's hash and modification time, which is
// what "the sync did not touch the vault" has to mean byte for byte.
func vaultFingerprint(t *testing.T, root string) []string {
	t.Helper()

	var fingerprint []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil //nolint:nilerr // an unreadable file cannot be in the vault
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}

		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		fingerprint = append(fingerprint,
			relative+"="+vault.Hash(data)+"@"+info.ModTime().UTC().Format(time.RFC3339Nano))
		return nil
	})
	if err != nil {
		t.Fatalf("walking the vault: %v", err)
	}

	sort.Strings(fingerprint)
	return fingerprint
}

// pagePathOf turns a file name into the page's identity, which has no
// extension. The index knows nothing about the extension, and a test that mixes
// the two up gets a not-found that reads like a sync bug.
func pagePathOf(fileName string) string {
	return strings.TrimSuffix(fileName, ".md")
}

func writeVaultFile(t *testing.T, vaultDir, pagePath, body string) {
	t.Helper()

	full := filepath.Join(vaultDir, filepath.FromSlash(pagePath))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("creating the directory for %s: %v", pagePath, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", pagePath, err)
	}
}
