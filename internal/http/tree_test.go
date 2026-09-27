package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # The page tree
//
// The sidebar is a wiki's navigation and it is built from the same rows the rest
// of the page is, so a mistake in it is a mistake about what exists rather than
// about how it is drawn.

// TestTheTreeHasTheShapeOfTheVault is the structural test, read out of the rendered
// HTML rather than out of the builder, because the builder is unexported and the
// HTML is what a reader sees.
//
// The interesting case is `campaign`, which is a page *and* a folder in a vault
// with `campaign/` alongside it. A tree that treats "already there" as "already
// handled" drops the child.
func TestTheTreeHasTheShapeOfTheVault(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	// A page that is also a folder, and a child inside it.
	folder := domain.Page{
		CampaignID:  f.campaign.ID,
		Path:        "locations/rivergate",
		Title:       "Rivergate",
		Type:        domain.PageTypeLocation,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: Rivergate\nvisibility: players\n",
		Body:        "# Rivergate\n\nA fortified town.\n",
		ContentHash: "hash-rivergate-again",
	}
	if _, err := f.store.UpsertPage(t.Context(), folder, store.AsDM(f.campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	inside := domain.Page{
		CampaignID:  f.campaign.ID,
		Path:        "locations/rivergate/the-drowned-hound",
		Title:       "The Drowned Hound",
		Type:        domain.PageTypeLocation,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: The Drowned Hound\nvisibility: players\n",
		Body:        "# The Drowned Hound\n\nA pub that is not there any more.\n",
		ContentHash: "hash-hound",
	}
	if _, err := f.store.UpsertPage(t.Context(), inside, store.AsDM(f.campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	got := f.get("/c/"+f.campaign.Slug.String()+"/", player)
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200", got.status)
	}

	// Both the folder-page and the child are in the tree.
	for _, want := range []string{
		`href="/c/blackwater/locations/rivergate"`,
		`href="/c/blackwater/locations/rivergate/the-drowned-hound"`,
	} {
		if !strings.Contains(got.body, want) {
			t.Errorf("the tree does not contain %q:\n%s", want, got.body)
		}
	}

	// The child is nested inside the parent's item rather than sitting beside it,
	// which is what makes the tree a tree.
	parent := strings.Index(got.body, `href="/c/blackwater/locations/rivergate"`)
	child := strings.Index(got.body, `href="/c/blackwater/locations/rivergate/the-drowned-hound"`)
	if parent < 0 || child < 0 || child < parent {
		t.Errorf("the child is not after its parent in the markup (parent %d, child %d)", parent, child)
	}
}

// TestTheTreeIsSortedByNameNotByCase: `Sessions` and `announcements` should
// interleave. A tree that sorts by byte order puts every capital letter first, and
// a DM with a dozen folders ends up with all the proper nouns together and all the
// lowercase ones together, which is not how anybody reads a list.
func TestTheTreeIsSortedByNameNotByCase(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	for _, path := range []string{"notes/Zebra", "notes/apple", "notes/Mango", "notes/banana"} {
		page := domain.Page{
			CampaignID:  f.campaign.ID,
			Path:        path,
			Title:       path,
			Type:        domain.PageTypeNote,
			Visibility:  domain.VisibilityDMOnly,
			Frontmatter: "title: " + path + "\nvisibility: dm-only\n",
			Body:        "A note.\n",
			ContentHash: "hash-" + path,
		}
		if _, err := f.store.UpsertPage(t.Context(), page, store.AsDM(f.campaign.ID)); err != nil {
			t.Fatalf("UpsertPage(%q): %v", path, err)
		}
	}

	got := f.get("/c/"+f.campaign.Slug.String()+"/", dm)

	apple := strings.Index(got.body, ">apple<")
	banana := strings.Index(got.body, ">banana<")
	mango := strings.Index(got.body, ">Mango<")
	zebra := strings.Index(got.body, ">Zebra<")

	if apple < 0 || banana < 0 || mango < 0 || zebra < 0 {
		t.Fatalf("the tree is missing one of the four notes:\n%s", got.body)
	}
	if apple >= banana || banana >= mango || mango >= zebra {
		t.Errorf("the tree is not in case-folded order: apple %d, banana %d, Mango %d, Zebra %d",
			apple, banana, mango, zebra)
	}
}

// TestTheTreeHasOnlyPagesTheReaderMaySee: a `dm-only` page's *name* tells a player
// a character exists, so the tree is a disclosure surface and it is filtered by the
// same predicate as the page list -- not by a filter of its own.
func TestTheTreeHasOnlyPagesTheReaderMaySee(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	dm := f.dmSession()

	asPlayer := f.get("/c/"+f.campaign.Slug.String()+"/", player)
	if strings.Contains(asPlayer.body, "vel") {
		t.Errorf("a player's tree lists a DM-only page:\n%s", asPlayer.body)
	}

	asDM := f.get("/c/"+f.campaign.Slug.String()+"/", dm)
	if !strings.Contains(asDM.body, "vel") {
		t.Errorf("the DM's own tree does not list their DM-only page:\n%s", asDM.body)
	}
}

// TestThePageBeingReadIsMarked is the `aria-current` assertion, and it is a
// one-liner because the browser's own notion of the current page is what a keyboard
// user and a screen reader already understand, and a bespoke class is a second
// implementation of it that will drift.
func TestThePageBeingReadIsMarked(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	got := f.get(f.pageURL("locations/rivergate"), player)
	if !strings.Contains(got.body, `href="/c/blackwater/locations/rivergate" aria-current="page"`) {
		t.Errorf("the page being read is not marked as the current page:\n%s", got.body)
	}

	// And a page that is not being read is not.
	if strings.Contains(got.body, `aria-current="page">campaign<`) {
		t.Errorf("another page is marked as the current one:\n%s", got.body)
	}
}

// TestACampaignWithNoPagesSaysSoRatherThanDrawingAnEmptyList: an empty `<ul>` in
// the sidebar looks like a wiki that has failed to load its navigation, and "There
// is nothing to read here yet" is the same information in a form a reader can act
// on.
func TestACampaignWithNoPagesSaysSoRatherThanDrawingAnEmptyList(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	empty, err := f.store.CreateCampaign(t.Context(), domain.Campaign{
		Slug:     "brand-new",
		Name:     "Brand New",
		VaultDir: "vault/brand-new",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	got := f.get("/c/" + empty.Slug.String() + "/")
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200", got.status)
	}
	if !strings.Contains(got.body, "Nothing here yet") {
		t.Errorf("an empty campaign does not say so:\n%s", got.body)
	}
	if !strings.Contains(got.body, "0 pages you can read") {
		t.Errorf("the root does not say how many pages there are:\n%s", got.body)
	}
}
