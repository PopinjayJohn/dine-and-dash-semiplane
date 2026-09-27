package index_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/index"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// # The writer fixture
//
// The tests that are about *who* a write is made as need more than the sync
// fixture: principals, bindings, and the ability to put a file in the vault at a
// moment of their choosing. A DM's real campaign is that, and a fixture that
// cannot produce a principal is a fixture that can only test the DM.

// writerFixture is an empty campaign with a vault, a store and a syncer, and
// nothing in either.
type writerFixture struct {
	t *testing.T

	ctx      context.Context
	root     string
	vaultDir string
	vault    *vault.Vault
	store    *store.Store
	campaign domain.Campaign
	sync     *index.Syncer
}

func newWriterFixture(t *testing.T) *writerFixture {
	t.Helper()

	ctx := context.Background()
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

	s := newStore(t, root)
	campaign, err := s.CreateCampaign(ctx, domain.Campaign{
		Slug:     "blackwater",
		Name:     "The Blackwater",
		VaultDir: "vault/blackwater",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	return &writerFixture{
		t:        t,
		ctx:      ctx,
		root:     root,
		vaultDir: vaultDir,
		vault:    v,
		store:    s,
		campaign: campaign,
		sync:     index.New(v, s, campaign),
	}
}

// writeFile puts a page in the vault, which is what an editor's save does and
// what a DM's editor does. It goes through the vault rather than `os.WriteFile`
// so that the fixture's writes are the same shape of write the application makes.
func (f *writerFixture) writeFile(pagePath, body string) {
	f.t.Helper()

	full := filepath.Join(f.vaultDir, filepath.FromSlash(pagePath)+".md")
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		f.t.Fatalf("creating the directory for %s: %v", pagePath, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		f.t.Fatalf("writing %s: %v", pagePath, err)
	}
}

// createPlayer makes a player principal, which is a row and a token hash: the
// token is not in the store and never is.
func (f *writerFixture) createPlayer(label string) domain.Principal {
	f.t.Helper()

	created, err := f.store.CreatePrincipal(f.ctx, domain.Principal{
		CampaignID: f.campaign.ID,
		Label:      label,
		Role:       domain.RolePlayer,
		TokenHash:  "hash-" + label,
		TokenHint:  "test",
		CreatedAt:  fixedNow,
	})
	if err != nil {
		f.t.Fatalf("CreatePrincipal(%q): %v", label, err)
	}
	return created
}

// createCharacter writes a character page, indexes it, and returns the row.
//
// A character is a page at `characters/<slug>`, not an entity, which is the rule
// the whole ownership story rests on and the one M7 got wrong once.
func (f *writerFixture) createCharacter(slug string) domain.Page {
	f.t.Helper()

	body := "---\ntitle: " + slug + "\ntype: character\nvisibility: dm-and-owner\n---\n\nA character.\n"
	path := index.CharacterDir + "/" + slug
	f.writeFile(path, body)

	if _, err := f.sync.SyncPath(f.ctx, path); err != nil {
		f.t.Fatalf("indexing the character %s: %v", slug, err)
	}

	page, err := f.store.GetPage(f.ctx, f.campaign.ID, path, store.AsDM(f.campaign.ID))
	if err != nil {
		f.t.Fatalf("reading back the character %s: %v", slug, err)
	}
	return page
}

// bind makes a principal the owner of a character page, which is what makes a
// `dm-and-owner` page readable and writable by that player.
func (f *writerFixture) bind(principal domain.Principal, character domain.Page) {
	f.t.Helper()

	if err := f.store.ReplacePrincipalCharacters(f.ctx, principal.ID, []string{character.ID}); err != nil {
		f.t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}
}

// fixedNow is the clock the index tests' store runs at, and it is the same
// moment `newStore` gives it: a test that writes a timestamp of its own and a
// store that mints one from a different clock is a test comparing two dates.
var fixedNow = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)
