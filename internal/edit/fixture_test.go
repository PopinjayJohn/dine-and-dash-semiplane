package edit_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/edit"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// # The editor fixture
//
// A real vault, a real store, a real campaign, and a DM and a player who are both
// people. The properties this package has are about what ends up *on disk* and
// *in the database* after a sequence of saves, and a fixture with fakes for either
// would be a fixture that agrees with whatever the test implemented.

const dmFrontmatter = "---\ntitle: Rivergate\ntype: location\nvisibility: dm-only\n---\n\nA fortified town.\n"

func newEditor(t *testing.T) *fixture {
	t.Helper()

	ctx := t.Context()
	root := t.TempDir()
	vaultDir := filepath.Join(root, "vault", "blackwater")
	if err := os.MkdirAll(vaultDir, 0o700); err != nil {
		t.Fatalf("creating the vault directory: %v", err)
	}

	v, err := vault.Open(vaultDir)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	t.Cleanup(func() { _ = v.Close() })

	s, err := store.Open(ctx, datadir.DatabaseFile(root), store.Options{
		Clock: clock.NewFixed(fixedNow, time.Minute),
		IDGen: idgen.NewSequence("id"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, migrateErr := s.Migrate(ctx); migrateErr != nil {
		t.Fatalf("Migrate: %v", migrateErr)
	}

	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	f := &fixture{t: t, root: root, vaultDir: vaultDir, vault: v, store: s, campaign: campaign}
	f.editor = edit.New(v, s, campaign)
	f.dm = f.principal("the DM", domain.RoleDM)
	f.aria = f.character("aria")
	// A second character, because "a page inside somebody else's folder" needs
	// somebody to be somebody else, and a refusal that is really a missing character
	// is a different refusal.
	f.brian = f.character("brian")
	f.player = f.principal("Aria (Ranger)", domain.RolePlayer)
	f.bind(f.player, f.aria)

	return f
}

// fixedNow is the moment every test in this package runs at, so that a cookie's
// Max-Age and a revision's timestamp are the same numbers every run.
var fixedNow = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

type fixture struct {
	t *testing.T

	root     string
	vaultDir string
	vault    *vault.Vault
	store    *store.Store
	campaign domain.Campaign
	editor   *edit.Editor

	dm     domain.Principal
	aria   domain.Page
	brian  domain.Page
	player domain.Principal
}

func (f *fixture) principal(label string, role domain.Role) domain.Principal {
	f.t.Helper()

	created, err := f.store.CreatePrincipal(f.t.Context(), domain.Principal{
		CampaignID: f.campaign.ID,
		Label:      label,
		Role:       role,
		TokenHash:  "hash-" + label,
		TokenHint:  "test",
		CreatedAt:  fixedNow,
	})
	if err != nil {
		f.t.Fatalf("CreatePrincipal(%q): %v", label, err)
	}
	return created
}

func (f *fixture) character(slug string) domain.Page {
	f.t.Helper()

	path := "characters/" + slug
	body := "---\ntitle: " + slug + "\ntype: character\nvisibility: dm-and-owner\n---\n\nA character.\n"
	f.writeFile(path, body)

	// Indexed as the DM, which is what the watcher does with a file in a vault: a
	// save with the ETag a browser would have been served, against text that is
	// already there.
	if _, _, err := f.editor.Save(f.t.Context(), edit.Save{
		Path:     path,
		Markdown: body,
		Expect:   f.hashOf(path),
		As:       f.dm,
	}); err != nil {
		f.t.Fatalf("indexing the character %s: %v", slug, err)
	}

	page, err := f.store.GetPage(f.t.Context(), f.campaign.ID, path, store.AsDM(f.campaign.ID))
	if err != nil {
		f.t.Fatalf("reading back the character %s: %v", slug, err)
	}
	return page
}

func (f *fixture) bind(principal domain.Principal, character domain.Page) {
	f.t.Helper()

	if err := f.store.ReplacePrincipalCharacters(f.t.Context(), principal.ID, []string{character.ID}); err != nil {
		f.t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}
}

// writeFile puts a file in the vault, which is what a DM's editor does.
func (f *fixture) writeFile(path, body string) {
	f.t.Helper()

	full := filepath.Join(f.vaultDir, filepath.FromSlash(path)+".md")
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatalf("creating the directory for %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		f.t.Fatalf("writing %s: %v", path, err)
	}
}

// readFile is what is on disk, which is the truth (ADR 0001) and the thing every
// assertion about a save should be about.
func (f *fixture) readFile(path string) string {
	f.t.Helper()

	data, err := os.ReadFile(filepath.Join(f.vaultDir, filepath.FromSlash(path)+".md"))
	if err != nil {
		f.t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// hashOf is a file's content hash without reading it through the editor, so that a
// test can name an ETag the way a browser would have received it.
func (f *fixture) hashOf(path string) string {
	f.t.Helper()

	return vault.Hash([]byte(f.readFile(path)))
}

// read is a page's text and its content hash — what a browser was served, and the
// ETag it would send back. It fails the test rather than returning a zero value,
// because a page that is not there is a thing a test has to arrange deliberately.
func (f *fixture) read(path string) (text, hash string) {
	f.t.Helper()

	text, hash, err := f.editor.Read(f.t.Context(), path)
	if err != nil {
		f.t.Fatalf("reading %s: %v", path, err)
	}
	return text, hash
}

// indexed is the row, for a test that wants to know what the index says.
func (f *fixture) indexed(path string) domain.Page {
	f.t.Helper()

	page, err := f.store.GetPage(f.t.Context(), f.campaign.ID, path, store.AsDM(f.campaign.ID))
	if err != nil {
		f.t.Fatalf("GetPage(%s): %v", path, err)
	}
	return page
}

// mustSave fails the test rather than returning a zero page, for the tests that are
// about something else and just need a page to exist.
func (f *fixture) mustSave(in edit.Save) domain.Page {
	f.t.Helper()

	page, _, err := f.editor.Save(f.t.Context(), in)
	if err != nil {
		f.t.Fatalf("Save(%s): %v", in.Path, err)
	}
	return page
}
