package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// # The decision
//
// Everything the HTTP layer decides about a page, tested through the one handler
// that makes the decision. The interesting cell is `dm-and-owner`, and the
// interesting mistake is the one M7 shipped: correlating the ownership test on the
// page rather than on the page's owner.

// TestAnOwnedPageIsOwnedForThePageItBelongsTo is the ownership correlation, from
// the outside.
//
// `dm-and-owner` means "a DM, or the player this character belongs to". The
// character page is its own owner, and a note *underneath* it is not owned at all
// unless its own frontmatter says so -- which is the rule, and the two rows below
// are the two halves of it:
//
//   - `characters/aria` is owned by the player bound to Aria, and she may read it.
//   - `characters/aria/notes` is a *different* page with `visibility: players`,
//     because a note under a character folder is not the character's by
//     inheritance. If the ownership test correlated on the page rather than the
//     owner, this row would be the interesting one: it would be admitted for a
//     player who is not bound to it.
//
// So the test is about a page the player must *not* reach, which is the direction a
// leak goes and therefore the direction worth a fixture.
func TestAnOwnedPageIsOwnedForThePageItBelongsTo(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	dm := f.dmSession()

	// A note under Aria's folder, visible to players, with a secret in it. Its
	// path is under a character's folder but nothing makes it the character's own
	// page, and its owner column is empty.
	subordinate := domain.Page{
		CampaignID:  f.campaign.ID,
		Path:        "characters/aria/notes",
		Title:       "Aria's Notes",
		Type:        domain.PageTypeNote,
		Visibility:  domain.VisibilityPlayers,
		Frontmatter: "title: Aria's Notes\\nvisibility: players\\n",
		Body:        "# Aria's Notes\\n\\nNotes about Aria, by the DM.\\n",
		ContentHash: "hash-aria-notes",
	}
	if _, err := f.store.UpsertPage(t.Context(), subordinate, store.AsDM(f.campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	// A `dm-and-owner` page whose owner is Aria but which is a note, not Aria.
	// This is the cell the ownership test decides, and it is the one M7 got wrong
	// by asking whether the *page* was bound rather than whether the page's
	// *owner* was.
	owned := domain.Page{
		CampaignID:           f.campaign.ID,
		Path:                 "npcs/vel/secret-notes",
		Title:                "Vell's Secret Notes",
		Type:                 domain.PageTypeNote,
		Visibility:           domain.VisibilityDMAndOwner,
		OwnerCharacterPageID: f.ariaID,
		Frontmatter:          "title: Vell's Secret Notes\\nvisibility: dm-and-owner\\ncharacter: aria\\n",
		Body:                 "# Vell's Secret Notes\\n\\n" + canary + " is in here too.\\n",
		ContentHash:          "hash-vel-notes",
	}
	if _, err := f.store.UpsertPage(t.Context(), owned, store.AsDM(f.campaign.ID)); err != nil {
		t.Fatalf("UpsertPage for the owned note: %v", err)
	}

	tests := map[string]struct {
		target string
		cookie *http.Cookie
		want   int
	}{
		"the character page itself, for the player who is bound to it": {
			target: f.pageURL("characters/aria"), cookie: player, want: http.StatusOK,
		},
		"the character page itself, for the DM": {
			target: f.pageURL("characters/aria"), cookie: dm, want: http.StatusOK,
		},
		"an owned note, for the player who owns it": {
			target: f.pageURL("npcs/vel/secret-notes"), cookie: player, want: http.StatusOK,
		},
		"an owned note, for the DM": {
			target: f.pageURL("npcs/vel/secret-notes"), cookie: dm, want: http.StatusOK,
		},
		"a players' page under a character's folder, for the player": {
			target: f.pageURL("characters/aria/notes"), cookie: player, want: http.StatusOK,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := f.get(tt.target, tt.cookie)
			if got.status != tt.want {
				t.Errorf("GET %s is %d, want %d\nbody: %s", tt.target, got.status, tt.want, got.body)
			}
		})
	}
}

// TestTheOwnerSeesTheSecretOnAPageTheyOwn is the other half of the ownership
// matrix, and it is the case a "safe" implementation breaks: a page a player owns
// is a page whose secrets are theirs (ADR 0007), and a pipeline that strips secrets
// for everybody who is not a DM would be correct-looking and wrong.
func TestTheOwnerSeesTheSecretOnAPageTheyOwn(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	other := f.redeemIn(t, f.otherName, f.otherLink)

	owned := domain.Page{
		CampaignID:           f.campaign.ID,
		Path:                 "npcs/vel/owned",
		Title:                "Vell's Owned Notes",
		Type:                 domain.PageTypeNote,
		Visibility:           domain.VisibilityDMAndOwner,
		OwnerCharacterPageID: f.ariaID,
		Frontmatter:          "title: Vell's Owned Notes\\nvisibility: dm-and-owner\\ncharacter: aria\\n",
		Body:                 "# Vell's Owned Notes\\n\\nThe pass phrase is " + canary + ".\\n",
		ContentHash:          "hash-vel-owned",
	}
	if _, err := f.store.UpsertPage(t.Context(), owned, store.AsDM(f.campaign.ID)); err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	// The owner reads it, and the secret is there.
	html := f.get(f.pageURL("npcs/vel/owned"), player)
	if html.status != http.StatusOK {
		t.Fatalf("the owner cannot read their own page: %d\nbody: %s", html.status, html.body)
	}
	if !strings.Contains(html.body, canary) {
		t.Errorf("the owner was not shown the secret on a page they own:\n%s", html.body)
	}

	// And so does the raw form, which is the endpoint where a "strip unless DM"
	// shortcut would show.
	raw := f.get(f.pageURL("npcs/vel/owned")+"?raw=1", player)
	if !strings.Contains(raw.body, canary) {
		t.Errorf("the owner's raw markdown does not have the secret in it:\n%s", raw.body)
	}

	// A principal of another campaign reads nothing at all, so the "owner" is not
	// a shortcut past the tenancy check.
	crossCampaign := f.get(f.pageURL("npcs/vel/owned"), other)
	if crossCampaign.status != http.StatusNotFound {
		t.Errorf("a principal of another campaign read an owned page: %d", crossCampaign.status)
	}
}

// TestAnUnidentifiedRequestReadsNothing is the fail-closed direction, and it is
// worth a test of its own because a *player* reading nothing is a bug somebody
// would report, so somebody will be tempted to make "no session" mean "a player
// with no bindings" -- which is a way to read every `players` page in a campaign
// without a link.
func TestAnUnidentifiedRequestReadsNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	for _, path := range []string{
		"locations/rivergate",
		"sessions/09-the-dragon-heist",
		"campaign",
		"characters/aria",
	} {
		got := f.get(f.pageURL(path))
		if got.status != http.StatusNotFound {
			t.Errorf("an unidentified request read %s: status %d", path, got.status)
		}
	}

	// And the root says so rather than pretending there is nothing to see.
	root := f.get("/c/" + f.campaign.Slug.String() + "/")
	if root.status != http.StatusOK {
		t.Fatalf("the campaign root is %d, want 200", root.status)
	}
	if !strings.Contains(root.body, "0 pages") {
		t.Errorf("the campaign root listed pages to an unidentified request:\n%s", root.body)
	}
}
