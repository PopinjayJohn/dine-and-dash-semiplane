package testsuite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// Store runs the contract against whatever a factory returns.
//
// Every subtest builds its own store, because a contract test that inherits
// state from the one before it can only be run as a suite, and a suite that
// can only be run as a suite is a suite that stops being run.
func Store(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("a value written comes back unchanged", func(t *testing.T) { roundTrip(t, factory) })
	t.Run("a blank id and blank timestamps are filled in", func(t *testing.T) { fillsBlanks(t, factory) })
	t.Run("writing the same value twice is idempotent", func(t *testing.T) { idempotent(t, factory) })
	t.Run("lists are complete and ordered", func(t *testing.T) { ordered(t, factory) })
	t.Run("an archived row is invisible and still there", func(t *testing.T) { archiving(t, factory) })
	t.Run("a path in two campaigns is two pages", func(t *testing.T) { pathsArePerCampaign(t, factory) })
	t.Run("a taken slug is a conflict", func(t *testing.T) { conflicts(t, factory) })
	t.Run("a missing row is not found", func(t *testing.T) { notFound(t, factory) })
	t.Run("a row that references nothing is refused", func(t *testing.T) { referencesAreEnforced(t, factory) })
	t.Run("revision numbers start at one, per page", func(t *testing.T) { revisionNumbers(t, factory) })
	t.Run("replacing links is all or nothing", func(t *testing.T) { linkReplacement(t, factory) })
	t.Run("the link graph holds links to pages that do not exist", func(t *testing.T) { unresolvedLinks(t, factory) })
	t.Run("a page answers to its aliases and to its own name", func(t *testing.T) { lookupTargets(t, factory) })
	t.Run("a page whose aliases were replaced answers only to the new ones", func(t *testing.T) { replacingAliases(t, factory) })
	t.Run("an archived page answers to nothing", func(t *testing.T) { archivedAnswersToNothing(t, factory) })
	t.Run("a target in one campaign is not found in another", func(t *testing.T) { targetsArePerCampaign(t, factory) })
	t.Run("a page's two index rows are written, and replaced", func(t *testing.T) { indexRows(t, factory) })
	t.Run("the index can be compared, so it can settle", func(t *testing.T) { indexSettles(t, factory) })
	t.Run("a link is found by its hash, and never by its token", func(t *testing.T) {
		principalIsFoundByItsTokenHash(t, factory)
	})
	t.Run("revoking a principal ends its sessions", func(t *testing.T) {
		revokingAPrincipalEndsItsSessions(t, factory)
	})
	t.Run("revoking every principal is scoped to one campaign", func(t *testing.T) {
		revokingEveryPrincipalIsScopedToOneCampaign(t, factory)
	})
	t.Run("a stored session always expires", func(t *testing.T) { aStoredSessionAlwaysExpires(t, factory) })
	t.Run("a slid session's expiry never moves backwards", func(t *testing.T) {
		touchSessionOnlyExtends(t, factory)
	})
	t.Run("a character binding is a replace", func(t *testing.T) { aCharacterBindingIsAReplace(t, factory) })
	t.Run("the audit log is appended and read newest first", func(t *testing.T) {
		theAuditLogIsAppendedAndReadNewestFirst(t, factory)
	})
}

// roundTrip is the property every column has to satisfy: what goes in comes
// out, including a timestamp with an awkward number of nanoseconds. A layout
// that trims trailing zeros would pass a whole-second test and quietly lose
// precision on the rows that matter.
func roundTrip(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	created := createCampaign(t, s)
	stored := createPage(t, s, created.ID)

	read, err := s.GetPage(ctx, created.ID, stored.Path)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if read != stored {
		t.Errorf("GetPage returned %+v, want %+v", read, stored)
	}

	byID, err := s.GetPageByID(ctx, stored.ID)
	if err != nil {
		t.Fatalf("GetPageByID: %v", err)
	}
	if byID != stored {
		t.Errorf("GetPageByID returned %+v, want %+v", byID, stored)
	}

	if stored.ContentHash != pageHash {
		t.Errorf("content hash = %q, want %q", stored.ContentHash, pageHash)
	}
	if stored.Body != pageBody {
		t.Errorf("body = %q, want %q", stored.Body, pageBody)
	}
	if !stored.CreatedAt.Equal(oddNanos) {
		t.Errorf("created_at = %v, want %v: a layout that trims trailing zeros loses this", stored.CreatedAt, oddNanos)
	}
	if stored.Frontmatter != frontmatter {
		t.Errorf("frontmatter = %q, want %q: a key the application does not understand must survive", stored.Frontmatter, frontmatter)
	}
}

// fillsBlanks is the contract behind the store's clock and id generator: a
// caller that hands over a value with nothing filled in gets back a value with
// something in those fields, and the two timestamps are the same instant.
func fillsBlanks(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)

	created := createCampaign(t, s)
	if created.ID == "" {
		t.Error("a campaign stored with a blank id has no id")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Errorf("a campaign stored with blank timestamps has created_at %v and updated_at %v",
			created.CreatedAt, created.UpdatedAt)
	}

	// Two pages, two ids, and the ids are not the same one: an id generator
	// that repeats is a primary key violation waiting for the second campaign.
	first := createPage(t, s, created.ID)
	second := createPage(t, s, created.ID, func(p *domain.Page) { p.Path = "npcs/garros-ironbar" })

	if first.ID == second.ID {
		t.Errorf("two pages were given the same id %q", first.ID)
	}
	if first.CreatedAt.IsZero() || first.UpdatedAt.IsZero() {
		t.Error("a page stored with blank timestamps came back with a zero timestamp")
	}
}

// idempotent is what makes a reindex safe to run twice, which it will be, by a
// DM, on a vault, without meaning to.
func idempotent(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	created := createCampaign(t, s)
	first := createPage(t, s, created.ID)
	second := createPage(t, s, created.ID)

	if second.ID != first.ID {
		t.Errorf("storing the same path twice gave ids %q and %q, want one row per path", first.ID, second.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("storing the same path twice gave creation times %v and %v, want the first one kept",
			first.CreatedAt, second.CreatedAt)
	}
	if second.ContentHash != first.ContentHash {
		t.Errorf("content hash changed from %q to %q on an unchanged write", first.ContentHash, second.ContentHash)
	}

	pages, err := s.ListPages(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 1 {
		t.Errorf("the campaign has %d pages after two identical writes, want 1", len(pages))
	}
}

// ordered is the contract that makes any of this testable: two calls in a row
// return the same rows in the same order, and every row written is in it.
func ordered(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	for _, name := range []string{"thornford", "blackwater", "rivergate"} {
		slug, err := domain.NewSlug(name)
		if err != nil {
			t.Fatalf("the fixture has an invalid slug: %v", err)
		}
		if _, err := s.CreateCampaign(ctx, domain.Campaign{
			Slug:     slug,
			Name:     name,
			VaultDir: "vault/" + name,
		}); err != nil {
			t.Fatalf("CreateCampaign(%q): %v", name, err)
		}
	}

	created, err := s.CampaignBySlug(ctx, "blackwater")
	if err != nil {
		t.Fatalf("CampaignBySlug: %v", err)
	}
	for _, path := range []string{"sessions/2026-02-14-dragon-heist", "characters/aria", "locations/rivergate"} {
		createPage(t, s, created.ID, func(p *domain.Page) { p.Path = path })
	}

	wantCampaigns := []domain.Slug{"blackwater", "rivergate", "thornford"}
	wantPaths := []string{"characters/aria", "locations/rivergate", "sessions/2026-02-14-dragon-heist"}

	for range 2 {
		campaigns, err := s.ListCampaigns(ctx)
		if err != nil {
			t.Fatalf("ListCampaigns: %v", err)
		}
		if len(campaigns) != len(wantCampaigns) {
			t.Fatalf("ListCampaigns returned %d campaigns, want %d", len(campaigns), len(wantCampaigns))
		}
		for i, slug := range wantCampaigns {
			if campaigns[i].Slug != slug {
				t.Errorf("campaign %d is %q, want %q", i, campaigns[i].Slug, slug)
			}
		}

		pages, err := s.ListPages(ctx, created.ID)
		if err != nil {
			t.Fatalf("ListPages: %v", err)
		}
		if len(pages) != len(wantPaths) {
			t.Fatalf("ListPages returned %d pages, want %d", len(pages), len(wantPaths))
		}
		for i, path := range wantPaths {
			if pages[i].Path != path {
				t.Errorf("page %d is %q, want %q", i, pages[i].Path, path)
			}
		}
	}
}

// archiving: the row stays, because revisions and inbound links point at it and
// an archive is recoverable; every read stops returning it, because an archived
// page is not a page anyone may read.
func archiving(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	created := createCampaign(t, s)
	stored := createPage(t, s, created.ID)

	if _, err := s.AppendRevision(ctx, revisionFixture(stored.ID)); err != nil {
		t.Fatalf("AppendRevision: %v", err)
	}

	if err := s.DeletePage(ctx, stored.ID); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	if _, err := s.GetPage(ctx, created.ID, stored.Path); !errors.Is(err, NotFound) {
		t.Errorf("GetPage after archiving returned %v, want an error matching NotFound", err)
	}
	if _, err := s.GetPageByID(ctx, stored.ID); !errors.Is(err, NotFound) {
		t.Errorf("GetPageByID after archiving returned %v, want an error matching NotFound", err)
	}
	pages, err := s.ListPages(ctx, created.ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 0 {
		t.Errorf("ListPages returned %d pages after archiving the only one", len(pages))
	}

	// The row is still there: writing the path again keeps the same identity,
	// which is the difference between an archive and a delete.
	restored := createPage(t, s, created.ID)
	if restored.ID != stored.ID {
		t.Errorf("writing an archived path again gave id %q, want the row's id %q: archiving deleted it", restored.ID, stored.ID)
	}
	if !restored.CreatedAt.Equal(stored.CreatedAt) {
		t.Errorf("writing an archived path again gave created_at %v, want the row's %v", restored.CreatedAt, stored.CreatedAt)
	}
}

// pathsArePerCampaign: a page's path is its identity *within* a campaign, so
// two campaigns can hold a page at the same path. Two DM's towns may both be
// called Rivergate.
func pathsArePerCampaign(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	first := createCampaign(t, s, func(c *domain.Campaign) { c.Slug = "rivergate-county" })
	second := createCampaign(t, s, func(c *domain.Campaign) { c.Slug = "thornford-county" })

	one := createPage(t, s, first.ID)
	two := createPage(t, s, second.ID)

	if one.ID == two.ID {
		t.Fatalf("the same path in two campaigns produced one row, id %q", one.ID)
	}

	// Each campaign sees only its own page, even at the same path.
	pages, err := s.ListPages(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 1 || pages[0].CampaignID != first.ID {
		t.Errorf("the first campaign lists %+v, want only its own page", pages)
	}
}

func conflicts(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	createCampaign(t, s)

	// A second campaign with the same slug: the slug is the campaign's
	// permanent identity, so there can only be one.
	duplicate := campaignFixture("blackwater")
	if _, err := s.CreateCampaign(ctx, duplicate); !errors.Is(err, Conflict) {
		t.Errorf("creating a second campaign with a taken slug returned %v, want an error matching Conflict", err)
	}

	// A page path is unique within a campaign, and the same path twice is an
	// update rather than a conflict.
	created := createCampaign(t, s, func(c *domain.Campaign) { c.Slug = "rivergate-county" })
	stored := createPage(t, s, created.ID)
	again := createPage(t, s, created.ID)
	if again.ID != stored.ID {
		t.Errorf("the same path twice gave two rows, ids %q and %q", stored.ID, again.ID)
	}
}

func notFound(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	if _, err := s.GetCampaign(ctx, "no-such-campaign"); !errors.Is(err, NotFound) {
		t.Errorf("GetCampaign returned %v, want an error matching NotFound", err)
	}
	if _, err := s.CampaignBySlug(ctx, "no-such-campaign"); !errors.Is(err, NotFound) {
		t.Errorf("CampaignBySlug returned %v, want an error matching NotFound", err)
	}
	if _, err := s.GetPage(ctx, "no-such-campaign", "locations/rivergate"); !errors.Is(err, NotFound) {
		t.Errorf("GetPage returned %v, want an error matching NotFound", err)
	}
	if _, err := s.GetPageByID(ctx, "no-such-page"); !errors.Is(err, NotFound) {
		t.Errorf("GetPageByID returned %v, want an error matching NotFound", err)
	}
	if _, err := s.GetRevision(ctx, "no-such-page", 1); !errors.Is(err, NotFound) {
		t.Errorf("GetRevision returned %v, want an error matching NotFound", err)
	}
	if err := s.DeletePage(ctx, "no-such-page"); !errors.Is(err, NotFound) {
		t.Errorf("DeletePage returned %v, want an error matching NotFound", err)
	}
}

// referencesAreEnforced: a page that belongs to no campaign, a revision that
// belongs to no page and a link to no page are all rows nothing can reach. A
// store that accepts them has a vault-shaped hole in it.
func referencesAreEnforced(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	if _, err := s.UpsertPage(ctx, pageFixture("no-such-campaign")); err == nil {
		t.Error("a page belonging to a campaign that does not exist was accepted")
	}
	if _, err := s.AppendRevision(ctx, revisionFixture("no-such-page")); err == nil {
		t.Error("a revision belonging to a page that does not exist was accepted")
	}

	created := createCampaign(t, s)
	stored := createPage(t, s, created.ID)

	err := s.ReplaceLinks(ctx, stored.ID, []domain.PageLink{
		{DstPath: "locations/rivergate", Kind: domain.LinkKindLink},
		{DstPath: "locations/does-not-exist", DstPageID: "no-such-page", Kind: domain.LinkKindLink},
	})
	if err == nil {
		t.Error("a link to a page that does not exist was accepted")
	}

	// And the refused replacement left nothing behind.
	links, err := s.LinksFrom(ctx, stored.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("a refused link replacement left %d links: %+v", len(links), links)
	}
}

func revisionNumbers(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	created := createCampaign(t, s)
	first := createPage(t, s, created.ID)
	second := createPage(t, s, created.ID, func(p *domain.Page) { p.Path = "npcs/garros-ironbar" })

	for want := 1; want <= 2; want++ {
		stored, err := s.AppendRevision(ctx, revisionFixture(first.ID))
		if err != nil {
			t.Fatalf("AppendRevision: %v", err)
		}
		if stored.Rev != want {
			t.Errorf("revision number = %d, want %d: numbering starts at one and increases by one", stored.Rev, want)
		}
	}

	// Numbering is per page: a second page's first revision is number one.
	other, err := s.AppendRevision(ctx, revisionFixture(second.ID))
	if err != nil {
		t.Fatalf("AppendRevision: %v", err)
	}
	if other.Rev != 1 {
		t.Errorf("the second page's first revision is numbered %d, want 1", other.Rev)
	}

	revisions, err := s.ListRevisions(ctx, first.ID)
	if err != nil {
		t.Fatalf("ListRevisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Fatalf("the page has %d revisions, want 2", len(revisions))
	}
	if revisions[0].Rev > revisions[1].Rev {
		t.Errorf("revisions came back as %d then %d, want oldest first", revisions[0].Rev, revisions[1].Rev)
	}
}

func linkReplacement(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	created := createCampaign(t, s)
	source := createPage(t, s, created.ID)
	target := createPage(t, s, created.ID, func(p *domain.Page) { p.Path = "npcs/garros-ironbar" })

	if err := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: target.Path, DstPageID: target.ID, Kind: domain.LinkKindEmbed},
		{DstPath: "locations/not-written-yet", Kind: domain.LinkKindLink},
	}); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	links, err := s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("the page has %d links, want 2", len(links))
	}

	// A shorter replacement removes the links that are not in it, and the
	// empty replacement clears them: the caller has just parsed a file and
	// knows every link in it.
	if replaceErr := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: target.Path, DstPageID: target.ID, Kind: domain.LinkKindLink},
	}); replaceErr != nil {
		t.Fatalf("ReplaceLinks: %v", replaceErr)
	}

	links, err = s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 1 || links[0].DstPath != target.Path {
		t.Errorf("after a shorter replacement the page links to %+v, want only %q", links, target.Path)
	}

	// Replacing with the same links twice is not two sets of links.
	for range 2 {
		if replaceErr := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
			{DstPath: target.Path, DstPageID: target.ID, Kind: domain.LinkKindLink},
		}); replaceErr != nil {
			t.Fatalf("ReplaceLinks: %v", replaceErr)
		}
	}
	links, err = s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("running the same replacement three times left %d links, want 1", len(links))
	}
}

func unresolvedLinks(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	created := createCampaign(t, s)
	source := createPage(t, s, created.ID)

	// A DM writes links to pages that do not exist yet, on purpose. The graph
	// has to hold them: an unresolved link is the interesting case, and a
	// graph that drops it has to be rebuilt from scratch every time a file
	// is saved.
	if err := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: "npcs/not-written-yet", Kind: domain.LinkKindEmbed},
	}); err != nil {
		t.Fatalf("ReplaceLinks: %v", err)
	}

	links, err := s.LinksFrom(ctx, source.ID)
	if err != nil {
		t.Fatalf("LinksFrom: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("the page has %d links, want 1", len(links))
	}
	if links[0].Resolved() {
		t.Error("a link to a page that does not exist is marked resolved")
	}
	if links[0].Kind != domain.LinkKindEmbed {
		t.Errorf("the link kind is %q, want %q", links[0].Kind, domain.LinkKindEmbed)
	}

	byPath, err := s.LinksToPath(ctx, "npcs/not-written-yet")
	if err != nil {
		t.Fatalf("LinksToPath: %v", err)
	}
	if len(byPath) != 1 || byPath[0].SrcPageID != source.ID {
		t.Errorf("LinksToPath returned %+v, want the one link from %q", byPath, source.ID)
	}

	// Once the target exists, the backlink appears.
	target := createPage(t, s, created.ID, func(p *domain.Page) { p.Path = "npcs/not-written-yet" })
	if replaceErr := s.ReplaceLinks(ctx, source.ID, []domain.PageLink{
		{DstPath: target.Path, DstPageID: target.ID, Kind: domain.LinkKindEmbed},
	}); replaceErr != nil {
		t.Fatalf("ReplaceLinks: %v", replaceErr)
	}

	backlinks, err := s.Backlinks(ctx, target.ID)
	if err != nil {
		t.Fatalf("Backlinks: %v", err)
	}
	if len(backlinks) != 1 || backlinks[0].SrcPageID != source.ID {
		t.Errorf("Backlinks returned %+v, want the one link from %q", backlinks, source.ID)
	}
}

// Fixtures. They use the spec's example vault, and a timestamp with an awkward
// number of nanoseconds so a lossy layout is caught here rather than in a
// golden file three milestones from now.
const (
	frontmatter = "title: Rivergate\ntype: location\ndm_notes: kept verbatim\n"

	// pageBody and pageHash are the body and the SHA-256 of the whole file it
	// came from, frontmatter included. The store never checks that they agree;
	// the sync engine does, and the point of keeping the pair here is that a
	// test can assert the hash survived rather than that it is right.
	pageBody = "A fortified town at the confluence of the [[Blackwater]].\n"
	pageHash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
)

var oddNanos = time.Date(2026, 2, 14, 19, 3, 0, 123456789, time.UTC)

// campaignFixture is a complete campaign with everything but its id filled in:
// leaving the id blank means every fixture in this suite exercises the contract
// that the store mints one.
func campaignFixture(slug string) domain.Campaign {
	parsed, err := domain.NewSlug(slug)
	if err != nil {
		panic("the fixture has an invalid slug: " + err.Error())
	}

	return domain.Campaign{
		Slug:      parsed,
		Name:      "The Blackwater",
		VaultDir:  "vault/" + parsed.String(),
		CreatedAt: oddNanos,
		UpdatedAt: oddNanos,
	}
}

// pageFixture is a complete page, with a blank id for the same reason.
func pageFixture(campaignID string) domain.Page {
	return domain.Page{
		CampaignID:      campaignID,
		Path:            "locations/rivergate",
		Title:           "Rivergate",
		Type:            domain.PageTypeLocation,
		Frontmatter:     frontmatter,
		Body:            pageBody,
		ContentHash:     pageHash,
		RendererVersion: 1,
		CreatedAt:       oddNanos,
		UpdatedAt:       oddNanos,
	}
}

func revisionFixture(pageID string) domain.PageRevision {
	return domain.PageRevision{
		PageID:      pageID,
		Markdown:    frontmatter + "\nA fortified town.\n",
		ContentHash: pageHash,
		Message:     "tolls are too high",
		CreatedAt:   oddNanos,
	}
}

func createCampaign(t *testing.T, s API, mutate ...func(*domain.Campaign)) domain.Campaign {
	t.Helper()

	c := campaignFixture("blackwater")
	for _, m := range mutate {
		m(&c)
	}

	stored, err := s.CreateCampaign(context.Background(), c)
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	return stored
}

// createCampaignWithSlug creates a campaign whose fixture is named by its slug,
// for the cases that need two of them and the second one is the point.
func createCampaignWithSlug(t *testing.T, s API, slug string) domain.Campaign {
	t.Helper()

	return createCampaign(t, s, func(c *domain.Campaign) {
		parsed, err := domain.NewSlug(slug)
		if err != nil {
			t.Fatalf("the fixture has an invalid slug: %v", err)
		}
		c.Slug = parsed
		c.VaultDir = "vault/" + parsed.String()
	})
}

// createFixturePage creates the fixture page, which is the one every case here
// wants.
func createFixturePage(t *testing.T, s API, campaignID string) domain.Page {
	t.Helper()

	return createPage(t, s, campaignID)
}

func createPage(t *testing.T, s API, campaignID string, mutate ...func(*domain.Page)) domain.Page {
	t.Helper()

	p := pageFixture(campaignID)
	for _, m := range mutate {
		m(&p)
	}

	stored, err := s.UpsertPage(context.Background(), p)
	if err != nil {
		t.Fatalf("UpsertPage(%q): %v", p.Path, err)
	}
	return stored
}

// lookupTargets is the contract for the two fallbacks a wiki link tries after
// the exact path, and it exists here rather than only in the store's own tests
// because a link that resolves in Obsidian and not in the wiki is a broken link
// in a wiki that is otherwise Obsidian-openable.
//
// The four cases are the ones the rules turn on: an alias as written, a name in
// another case, a name that is a whole path, and a name nobody has.
func lookupTargets(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")

	town := createFixturePage(t, s, campaign.ID)
	if err := s.ReplacePageAliases(ctx, town.ID, []string{"the toll town", "Flussport"}); err != nil {
		t.Fatalf("ReplacePageAliases: %v", err)
	}

	if _, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "npcs/garros-ironbar",
		Title:       "Garros Ironbar",
		Type:        domain.PageTypeNPC,
		ContentHash: "hash-of-garros",
	}); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	tests := map[string]struct {
		alias string
		name  string
		want  string
		found bool
	}{
		"an alias as written": {
			alias: "the toll town", want: "locations/rivergate", found: true,
		},
		"an alias that is not a path": {
			alias: "Flussport", want: "locations/rivergate", found: true,
		},
		"a file name as written": {
			name: "rivergate", want: "locations/rivergate", found: true,
		},
		"a file name in another case": {
			name: "RIVERGATE", want: "locations/rivergate", found: true,
		},
		"a whole path finds the same page as its name": {
			name: "locations/rivergate", want: "locations/rivergate", found: true,
		},
		"a name with the extension finds the same page": {
			name: "rivergate.md", want: "locations/rivergate", found: true,
		},
		"a partial name is not a name": {
			name: "rive", want: "", found: false,
		},
		"a name nobody has": {
			name: "thornford", want: "", found: false,
		},
		"an alias nobody has": {
			alias: "the drowned hound", want: "", found: false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.alias != "" {
				got, found, err := s.FindPageByAlias(ctx, campaign.ID, tt.alias)
				if err != nil {
					t.Fatalf("FindPageByAlias(%q): %v", tt.alias, err)
				}
				if found != tt.found {
					t.Fatalf("FindPageByAlias(%q) found = %t, want %t", tt.alias, found, tt.found)
				}
				if found && got.Path != tt.want {
					t.Errorf("FindPageByAlias(%q) = %q, want %q", tt.alias, got.Path, tt.want)
				}
				return
			}

			got, found, err := s.FindPageByName(ctx, campaign.ID, tt.name)
			if err != nil {
				t.Fatalf("FindPageByName(%q): %v", tt.name, err)
			}
			if found != tt.found {
				t.Fatalf("FindPageByName(%q) found = %t, want %t", tt.name, found, tt.found)
			}
			if found && got.Path != tt.want {
				t.Errorf("FindPageByName(%q) = %q, want %q", tt.name, got.Path, tt.want)
			}
		})
	}

	// A page answers to its own name from the moment the row exists, without a
	// caller having to say so.
	targets, err := s.PageTargets(ctx, town.ID)
	if err != nil {
		t.Fatalf("PageTargets: %v", err)
	}
	if got := targets["name"]; len(got) != 1 || got[0] != "rivergate" {
		t.Errorf("the page's own name targets are %v, want [rivergate]", got)
	}
}

// replacingAliases: an alias the DM removed from their frontmatter must stop
// resolving, or a link outlives the thing it pointed at.
func replacingAliases(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	town := createFixturePage(t, s, campaign.ID)

	if err := s.ReplacePageAliases(ctx, town.ID, []string{"the toll town", "Flussport"}); err != nil {
		t.Fatalf("ReplacePageAliases: %v", err)
	}
	if err := s.ReplacePageAliases(ctx, town.ID, []string{"the bridge town"}); err != nil {
		t.Fatalf("ReplacePageAliases: %v", err)
	}

	if _, found, err := s.FindPageByAlias(ctx, campaign.ID, "Flussport"); err != nil || found {
		t.Errorf("an alias the page no longer declares still resolves (found = %t, %v)", found, err)
	}

	got, found, err := s.FindPageByAlias(ctx, campaign.ID, "the bridge town")
	if err != nil || !found || got.Path != "locations/rivergate" {
		t.Errorf("the new alias resolves to %+v (found = %t, %v), want the page", got, found, err)
	}

	// Replacing the aliases left the name target alone, because a page's name is
	// a property of its path.
	targets, err := s.PageTargets(ctx, town.ID)
	if err != nil {
		t.Fatalf("PageTargets: %v", err)
	}
	if got := targets["name"]; len(got) != 1 || got[0] != "rivergate" {
		t.Errorf("replacing the aliases took the name target with them: %v", got)
	}
}

// archivedAnswersToNothing: an archived page is gone from every read, and a link
// that resolved to it would be a link to a page nobody can open.
func archivedAnswersToNothing(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	town := createFixturePage(t, s, campaign.ID)

	if err := s.ReplacePageAliases(ctx, town.ID, []string{"the toll town"}); err != nil {
		t.Fatalf("ReplacePageAliases: %v", err)
	}
	if err := s.DeletePage(ctx, town.ID); err != nil {
		t.Fatalf("DeletePage: %v", err)
	}

	if _, found, err := s.FindPageByName(ctx, campaign.ID, "rivergate"); err != nil || found {
		t.Errorf("an archived page answers to its own name (found = %t, %v)", found, err)
	}
	if _, found, err := s.FindPageByAlias(ctx, campaign.ID, "the toll town"); err != nil || found {
		t.Errorf("an archived page answers to its alias (found = %t, %v)", found, err)
	}
}

// targetsArePerCampaign: a wiki link is resolved inside a campaign, because a DM
// has two of them open in two tabs and a page called `rivergate` in each.
func targetsArePerCampaign(t *testing.T, factory Factory) {
	t.Helper()

	ctx := context.Background()
	s := factory(t)

	first := createCampaignWithSlug(t, s, "blackwater")
	second := createCampaignWithSlug(t, s, "rivergate-county")

	for _, campaign := range []domain.Campaign{first, second} {
		page := createFixturePage(t, s, campaign.ID)
		if err := s.ReplacePageAliases(ctx, page.ID, []string{"the toll town"}); err != nil {
			t.Fatalf("ReplacePageAliases: %v", err)
		}
	}

	for _, campaign := range []domain.Campaign{first, second} {
		got, found, err := s.FindPageByAlias(ctx, campaign.ID, "the toll town")
		if err != nil || !found {
			t.Fatalf("FindPageByAlias in campaign %s: found = %t, %v", campaign.Slug, found, err)
		}
		if got.CampaignID != campaign.ID {
			t.Errorf("campaign %s's alias resolved to a page of campaign %s", campaign.Slug, got.CampaignID)
		}
	}

	// And a campaign with no such page says so, rather than reaching into
	// another one.
	empty := createCampaignWithSlug(t, s, "thornford-county")
	if _, found, err := s.FindPageByName(ctx, empty.ID, "rivergate"); err != nil || found {
		t.Errorf("a page from another campaign was found (found = %t, %v)", found, err)
	}
}
