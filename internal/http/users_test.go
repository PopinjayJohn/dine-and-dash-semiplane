package http_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// # The users page
//
// The missing half of handing somebody a link: §10 says the DM clicks a button
// and the plaintext is shown once, and until this there was no button.

// tokenIn is a 64-character hex string, which is what a share link's token looks
// like. The tests use it to ask "is a token in this response", which is the only
// question that matters about every surface here.
var tokenIn = regexp.MustCompile(`\b[0-9a-f]{64}\b`)

// usersURL is the users page, and it is a query on the campaign root because
// `users` is exactly the kind of page a DM writes.
func (f *fixture) usersURL() string { return "/c/" + f.campaign.Slug.String() + "/?users=1" }

// TestAMintedLinkIsShownOnceAndOnlyOnce is the property §10's "shows the URL once"
// turns on.
//
// The response that mints it carries the link, every other response does not, and
// the store has only its hash either way. So the test mints, reads the link off
// the response, and then asks the page again.
func TestAMintedLinkIsShownOnceAndOnlyOnce(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	page := f.get(f.usersURL(), dm)
	if page.status != http.StatusOK {
		t.Fatalf("the users page is %d, want 200\nbody: %s", page.status, page.body)
	}
	csrf := editorCSRF(t, page.body)

	minted := f.postForm(f.usersURL(), map[string]string{
		"csrf":   csrf,
		"action": "new",
		"label":  "Sam (Ranger)",
		"role":   "player",
	}, dm)

	// A 303 to the page with the link on it, rather than a page *instead of* the
	// list: the DM has just clicked a button and the answer should be where the
	// button was.
	if minted.status != http.StatusSeeOther {
		t.Fatalf("minting a link is %d, want 303\nbody: %s", minted.status, minted.body)
	}
	location := minted.header.Get("Location")
	link := between(t, location, "issued=", "")

	if !strings.Contains(link, "?k=") {
		t.Errorf("the minted link has no token in it: %q", link)
	}
	if !tokenIn.MatchString(link) {
		t.Errorf("the minted link has no 64-character token in it: %q", link)
	}

	// And the page with it on it says it will not be shown again, because a DM who
	// does not know that will come back for it.
	again := f.get(location, dm)
	if again.status != http.StatusOK {
		t.Fatalf("the page with the fresh link is %d, want 200", again.status)
	}
	if !strings.Contains(again.body, "will not be shown again") {
		t.Errorf("the page does not say the link is a one-off:\n%s", again.body)
	}

	// Which is exactly what the *store* says: only a hash.
	principals, err := f.store.ListPrincipals(t.Context(), f.campaign.ID)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	found := false
	for _, principal := range principals {
		if principal.Label != "Sam (Ranger)" {
			continue
		}
		found = true
		if token := link[strings.Index(link, "k=")+2:]; strings.Contains(principal.TokenHash, token) {
			t.Error("the store has the plaintext token, and it must not")
		}
		if principal.TokenHint == "" {
			t.Error("the store has no hint, so a DM cannot tell two of Sam's links apart")
		}
	}
	if !found {
		t.Error("the minted link is not in the list of principals")
	}
}

// TestTheLinkIsNotOnThePageAfterwards is the second half, and it is a separate test
// because "shown once" is a claim about *time* and not about one response.
func TestTheLinkIsNotOnThePageAfterwards(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	page := f.get(f.usersURL(), dm)

	first := f.postForm(f.usersURL(), map[string]string{
		"csrf":   editorCSRF(t, page.body),
		"action": "new",
		"label":  "Sam",
	}, dm)
	link := between(t, first.header.Get("Location"), "issued=", "")

	// The page on its own, with no `issued` parameter.
	plain := f.get(f.usersURL(), dm)
	if tokenIn.MatchString(plain.body) {
		t.Errorf("the users page carries a token with nothing minted in this request:\n%s", plain.body)
	}

	// And the *list* has no token and no hash, because a list of six people at a
	// table is the worst place for a credential fingerprint.
	for _, leak := range []string{"k=", "token"} {
		if strings.Contains(plain.body, leak) {
			t.Errorf("the list mentions %q:\n%s", leak, plain.body)
		}
	}
	if strings.Contains(plain.body, link) {
		t.Error("the list carries the link that was just minted")
	}
}

// TestTheUsersPageIsForTheDMOnly: a player who finds the URL knows the campaign
// has players, which is not worth a not-found, and a 403 says what is wrong.
func TestTheUsersPageIsForTheDMOnly(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	got := f.get(f.usersURL(), player)
	if got.status != http.StatusForbidden {
		t.Errorf("the users page is %d for a player, want 403", got.status)
	}
	if strings.Contains(got.body, "Aria") {
		t.Errorf("a player can see who has access:\n%s", got.body)
	}

	// And the POSTs are refused the same way, because a read-only page with a
	// working form is a form that does not need to exist.
	posted := f.postForm(f.usersURL(), map[string]string{
		"action": "new",
		"label":  "Not me",
	}, player)
	if posted.status != http.StatusForbidden {
		t.Errorf("minting a link as a player is %d, want 403", posted.status)
	}
}

// TestRevokingEndsAccessAtOnce is §10's "sessions are rows, so deleting the
// principal ends every existing browser's access on the next request" — the whole
// point of revocation, and the reason it is a button rather than a setting.
func TestRevokingEndsAccessAtOnce(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	page := f.get(f.usersURL(), dm)

	minted := f.postForm(f.usersURL(), map[string]string{
		"csrf":   editorCSRF(t, page.body),
		"action": "new",
		"label":  "Sam (Ranger)",
	}, dm)
	link := between(t, minted.header.Get("Location"), "issued=", "")

	// Sam is in.
	samCookie := f.redeemLink(t, link)
	if got := f.get(f.pageURL("locations/rivergate"), samCookie); got.status != http.StatusOK {
		t.Fatalf("the new player cannot read a page: %d", got.status)
	}

	// And out, the moment the button is pressed.
	revoked := f.postForm(f.usersURL(), map[string]string{
		"csrf":   editorCSRF(t, page.body),
		"action": "revoke",
		"id":     f.principalIDOf(t, "Sam (Ranger)"),
	}, dm)
	if revoked.status != http.StatusSeeOther {
		t.Fatalf("revoking is %d, want 303\nbody: %s", revoked.status, revoked.body)
	}

	// The session cookie is still in the browser and is now worth nothing: the
	// page is a 404, which is what an unidentified request gets for everything.
	after := f.get(f.pageURL("locations/rivergate"), samCookie)
	if after.status != http.StatusNotFound {
		t.Errorf("a revoked player's page is %d, want 404 (they read nothing)", after.status)
	}
	if strings.Contains(after.body, "Log out") {
		t.Error("a revoked player is still treated as logged in")
	}

	// And the row says so, so the DM can see the state rather than infer it.
	list := f.get(f.usersURL(), dm)
	if !strings.Contains(list.body, "revoked") {
		t.Errorf("the list does not show the revocation:\n%s", list.body)
	}
}

// TestADMCanNotRevokeThemselves is the one that is a *click* away from locking
// everybody out of a campaign with no way back in but the data directory.
func TestADMCanNotRevokeThemselves(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	page := f.get(f.usersURL(), dm)

	// The DM's own row, found by the label the fixture issued the link under.
	got := f.postForm(f.usersURL(), map[string]string{
		"csrf":   editorCSRF(t, page.body),
		"action": "revoke",
		"id":     f.principalIDOf(t, "the DM"),
	}, dm)

	if got.status == http.StatusSeeOther {
		t.Fatal("the DM revoked their own link, which ends their own access")
	}
	if !strings.Contains(got.body, "your own link") {
		t.Errorf("the refusal does not say why:\n%s", got.body)
	}

	// And they are still in.
	if after := f.get(f.pageURL("locations/rivergate"), dm); after.status != http.StatusOK {
		t.Errorf("the DM is locked out: %d", after.status)
	}
}

// TestARevocationOfSomebodyInAnotherCampaignIsA404: a revocation is a write on a
// row, and a row in another campaign is not this DM's to end — including if this
// DM's session says the id.
func TestARevocationOfSomebodyInAnotherCampaignIsA404(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	page := f.get(f.usersURL(), dm)

	// A principal of the other campaign, minted through its own store, and its id
	// read out of *that* campaign's list -- a test that read it out of this one
	// would find nothing and prove nothing.
	other := f.redeemIn(t, f.otherName, f.otherLink)
	otherID := f.principalIDIn(t, f.otherName.ID, "a player of Thornford")

	got := f.postForm(f.usersURL(), map[string]string{
		"csrf":   editorCSRF(t, page.body),
		"action": "revoke",
		"id":     otherID,
	}, dm)
	if got.status != http.StatusNotFound {
		t.Errorf("revoking somebody in another campaign is %d, want 404", got.status)
	}

	// And they still have theirs.
	if after := f.get("/c/"+f.otherName.Slug.String()+"/", other); after.status != http.StatusOK {
		t.Errorf("the other campaign's player lost their access: %d", after.status)
	}
}

// TestTheUsersPageNeedsTheToken is the same CSRF rule as every other mutation.
func TestTheUsersPageNeedsTheToken(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	// A forged POST from another site, with no token at all.
	forged := f.postForm(f.usersURL(), map[string]string{
		"action": "new",
		"label":  "Somebody who is not here",
	}, dm)
	if forged.status != http.StatusForbidden {
		t.Errorf("a forged mint is %d, want 403", forged.status)
	}

	// A forged revocation, which is the one that matters: a page on another site
	// that ends somebody's access.
	stolen := f.postForm(f.usersURL(), map[string]string{
		"csrf":   "not-the-token",
		"action": "revoke",
		"id":     f.principalIDOf(t, playerName),
	}, dm)
	if stolen.status != http.StatusForbidden {
		t.Errorf("a forged revocation is %d, want 403", stolen.status)
	}
}

// TestThePageToolsAreAllBehindTheGate: rename, archive, purge and restore are four
// POSTs on the editor's URL, and a route that a handler forgot to gate is a
// handler that a player uses.
func TestThePageToolsAreAllBehindTheGate(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	before := f.readFile("characters/aria")

	// A player may archive and restore their own page, so the tools that must be
	// refused for them are the two that touch somebody else's.
	for _, op := range []struct {
		name string
		form map[string]string
	}{
		{name: "rename a DM's page", form: map[string]string{"op": "rename", "path": "locations/rivergate-moved"}},
		{name: "purge a DM's page", form: map[string]string{"op": "purge", "confirm": "locations/rivergate"}},
	} {
		t.Run(op.name, func(t *testing.T) {
			t.Parallel()

			// A fixture of its own, because the two rows want two separate
			// campaigns' worth of state and sharing one is how a test starts
			// passing because of another test's row.
			own := newFixture(t)
			player := own.playerSession()
			csrf := editorCSRF(t, own.get(own.pageURL("characters/aria")+"?edit=1", player).body)

			form := map[string]string{"csrf": csrf}
			for k, v := range op.form {
				form[k] = v
			}

			got := own.postForm(own.pageURL("locations/rivergate")+"?edit=1", form, player)
			if got.status != http.StatusForbidden {
				t.Errorf("%s by a player is %d, want 403\nbody: %s", op.name, got.status, got.body)
			}
		})
	}

	// And the CSRF token is required for all four, on a page the player owns -- so
	// this is a genuine check rather than a refusal for want of permission.
	player := f.playerSession()
	for _, op := range []string{"rename", "archive", "purge", "restore"} {
		got := f.postForm(f.pageURL("characters/aria")+"?edit=1", map[string]string{
			"op":      op,
			"path":    "characters/aria-moved",
			"confirm": "characters/aria",
			"rev":     "1",
		}, player)
		if got.status != http.StatusForbidden {
			t.Errorf("%s with no token is %d, want 403", op, got.status)
		}
	}

	if after := f.readFile("characters/aria"); after != before {
		t.Error("a refused page tool changed the file")
	}
}

// TestAPurgeIsNotOfferedWithoutSayingWhatItLoses: the one irreversible thing in
// the editor. The page says what goes, and the handler asks for a typed
// confirmation — a browser's `confirm()` dialog is suppressed by a prefetch, is
// not announced by a screen reader, and a DM who has pressed Enter twice has a page
// they cannot get back.
func TestAPurgeIsNotOfferedWithoutSayingWhatItLoses(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", dm)
	body := editor.body

	if !strings.Contains(body, "There is no bringing it back") {
		t.Errorf("the editor does not say what a purge loses:\n%s", body)
	}
	if !strings.Contains(body, `name="confirm"`) {
		t.Error("the purge form has no confirmation field")
	}

	// A purge with a wrong confirmation is refused, and the file is untouched.
	wrong := f.postForm(f.pageURL("locations/rivergate")+"?edit=1", map[string]string{
		"csrf":    editorCSRF(t, body),
		"op":      "purge",
		"confirm": "some other page",
	}, dm)
	if wrong.status == http.StatusSeeOther {
		t.Error("a purge with the wrong confirmation went through")
	}
	if !strings.Contains(f.readFile("locations/rivergate"), "A fortified town") {
		t.Error("the refused purge removed the file")
	}

	// And with the right one it does, which is the other half: a confirmation
	// nobody can satisfy is a feature that does not work.
	right := f.postForm(f.pageURL("locations/rivergate")+"?edit=1", map[string]string{
		"csrf":    editorCSRF(t, body),
		"op":      "purge",
		"confirm": "locations/rivergate",
	}, dm)
	if right.status != http.StatusSeeOther {
		t.Errorf("a confirmed purge is %d, want 303\nbody: %s", right.status, right.body)
	}
}

// TestTheHistoryIsListedWithItsNumbers: the restore form posts a number, so the
// number the page shows has to be the number the store keeps.
func TestTheHistoryIsListedWithItsNumbers(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	first := f.readFile("locations/rivergate")
	f.savePage("locations/rivergate", strings.Replace(first, "A fortified town", "A fortified town, and a bridge", 1))
	second := f.readFile("locations/rivergate")
	f.savePage("locations/rivergate", strings.Replace(second, "and a bridge", "and a bridge, and a ford", 1))

	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", dm)
	for _, want := range []string{"Revision 1", "Revision 2", `name="rev" value="1"`, `name="rev" value="2"`} {
		if !strings.Contains(editor.body, want) {
			t.Errorf("the history does not contain %q:\n%s", want, editor.body)
		}
	}

	// And a restore from the page puts the older text back, which is the
	// definition of restore and the reason the numbers matter. The ETag is the one
	// the *form* carries, which is the hash of what the editor loaded.
	restored := f.postForm(f.pageURL("locations/rivergate")+"?edit=1", map[string]string{
		"csrf": editorCSRF(t, editor.body),
		"op":   "restore",
		"rev":  "1",
		"etag": editorETag(t, editor.body),
	}, dm)
	if restored.status != http.StatusSeeOther {
		t.Fatalf("a restore is %d, want 303\nbody: %s", restored.status, restored.body)
	}
	// Revision 1 is the text before the *first* save, so restoring it puts the
	// original prose back -- not the text after the first save.
	if got := f.readFile("locations/rivergate"); got != first {
		t.Errorf("the restored file is:\n%q\nwant:\n%q", got, first)
	}
}

// TestTheEditorOffersTheToolsOnlyToTheDM: a player editing their own page gets an
// editor and not the controls for moving it somewhere else.
func TestTheEditorOffersTheToolsOnlyToTheDM(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	asPlayer := f.get(f.pageURL("characters/aria")+"?edit=1", f.playerSession())
	if !strings.Contains(asPlayer.body, "Archive") {
		t.Errorf("a player cannot archive their own page:\n%s", asPlayer.body)
	}
	for _, notForThem := range []string{"Rename to", "Purge for good"} {
		if strings.Contains(asPlayer.body, notForThem) {
			t.Errorf("a player is offered %q:\n%s", notForThem, asPlayer.body)
		}
	}

	asDM := f.get(f.pageURL("locations/rivergate")+"?edit=1", f.dmSession())
	for _, theirs := range []string{"Rename to", "Archive", "Purge for good"} {
		if !strings.Contains(asDM.body, theirs) {
			t.Errorf("the DM is not offered %q:\n%s", theirs, asDM.body)
		}
	}
}

// TestTheUsersPageIsLinkedFromTheEditor: a page nobody can find is a button
// nobody presses, and the missing half of M9 is a button.
func TestTheUsersPageIsLinkedFromTheEditor(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	asDM := f.get(f.pageURL("locations/rivergate")+"?edit=1", f.dmSession())
	if !strings.Contains(asDM.body, "?users=1") {
		t.Errorf("the editor has no link to the users page:\n%s", asDM.body)
	}

	// And the campaign root, which is where a DM goes looking.
	root := f.get("/c/"+f.campaign.Slug.String()+"/", f.dmSession())
	if !strings.Contains(root.body, "?users=1") {
		t.Errorf("the campaign root does not link to the users page:\n%s", root.body)
	}
}

// helpers

// principalIDOf is the id of a principal of *this* campaign, by its label, for a
// test that posts a revocation.
func (f *fixture) principalIDOf(t *testing.T, label string) string {
	t.Helper()

	return f.principalIDIn(t, f.campaign.ID, label)
}

// principalIDIn is the same, for a named campaign -- which is the whole of the
// cross-campaign test: the row being revoked belongs to somebody else, so it has
// to be found in their list and not in this DM's.
func (f *fixture) principalIDIn(t *testing.T, campaignID, label string) string {
	t.Helper()

	principals, err := f.store.ListPrincipals(t.Context(), campaignID)
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	for _, principal := range principals {
		if principal.Label == label {
			return principal.ID
		}
	}
	t.Fatalf("no principal labelled %q in campaign %s; there are %d", label, campaignID, len(principals))
	return ""
}

// redeemLink exchanges a link for a session, for a test that is following one
// player from mint to revocation.
func (f *fixture) redeemLink(t *testing.T, link string) *http.Cookie {
	t.Helper()

	got := f.get(link)
	if got.status != http.StatusSeeOther {
		t.Fatalf("redeeming the minted link is %d, want 303\nbody: %s", got.status, got.body)
	}
	for _, cookie := range got.cookies {
		if strings.Contains(cookie.Name, "wiki_session") {
			return cookie
		}
	}
	t.Fatalf("the redemption set no session cookie")
	return nil
}
