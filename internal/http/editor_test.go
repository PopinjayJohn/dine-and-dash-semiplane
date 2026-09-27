package http_test

import (
	"html"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// # The editor over HTTP
//
// The four things the editor route has to get right, and each has a test that says
// what "right" is: a save that works, a save that is refused, a conflict that is a
// 409 with three texts, and a preview that renders without saving.

// actionAttr is the `action="..."` a rendered form carries, so a test can name the
// attribute without fighting a raw string's backtick.
func actionAttr(url string) string { return `action="` + url + `"` }

// between is the text between a prefix and a suffix, for a test reading a rendered
// value out of a URL or a form. A test that scraped markup and posted the escape
// would get a conflict on every save, which is the right answer to the wrong
// request.
func between(t *testing.T, body, prefix, suffix string) string {
	t.Helper()

	_, after, found := strings.Cut(body, prefix)
	if !found {
		t.Fatalf("there is no %s in:\n%s", prefix, body)
	}
	if suffix == "" {
		// To the end of the line, which is what a query parameter at the end of a
		// redirect is. `strings.Cut` with an empty separator is *not* that -- it
		// reports "not found" -- and a test that read the whole remainder either
		// way would be testing a helper rather than a route.
		return html.UnescapeString(after)
	}
	value, _, found := strings.Cut(after, suffix)
	if !found {
		t.Fatalf("the %s is not closed:\n%s", prefix, body)
	}
	return html.UnescapeString(value)
}

// etagIn is the ETag a rendered form carries, so a test sends back the one the
// browser would rather than one it invented.
var etagIn = regexp.MustCompile(`name="etag" value="([^"]*)"`)

func editorETag(t *testing.T, body string) string {
	t.Helper()

	match := etagIn.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("the editor's form carries no ETag, so a save from it would be refused:\n%s", body)
	}
	// The rendered value is HTML-escaped -- a quoted ETag's quotes are `&#34;` in
	// the markup -- and a browser posts the *decoded* value. A test that scraped the
	// markup and posted the escape would get a conflict on every save, which is the
	// right answer to the wrong request.
	return html.UnescapeString(match[1])
}

// csrfIn is the token a rendered form carries, for the same reason: a test that
// made up its own would be testing that an invented token is refused.
var csrfIn = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

func editorCSRF(t *testing.T, body string) string {
	t.Helper()

	match := csrfIn.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("the editor's form carries no CSRF token:\n%s", body)
	}
	return match[1]
}

// TestTheEditorWorksWithNoScript is the one that decides the shape of everything
// else: the form posts to itself with a real action, the ETag and the CSRF token
// are hidden fields, and the save button is a submit.
//
// A JavaScript-only save path is untestable from Go and unshippable on a machine
// where the script did not load, so this test posts the form the way a browser
// with scripting disabled would and says the page came back.
func TestTheEditorWorksWithNoScript(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", dm)
	if editor.status != http.StatusOK {
		t.Fatalf("the editor is %d, want 200\nbody: %s", editor.status, editor.body)
	}

	// A real action, so the form is a form.
	if want := actionAttr(f.pageURL("locations/rivergate") + "?edit=1"); !strings.Contains(editor.body, want) {
		t.Errorf("the editor's form has no action, so it posts nowhere:\n%s", editor.body)
	}
	// A submit button, not a `fetch`.
	if !strings.Contains(editor.body, `type="submit"`) {
		t.Errorf("the editor has no submit button:\n%s", editor.body)
	}
	// And the text is in a textarea, byte for byte as the file has it.
	if !strings.Contains(editor.body, "A fortified town") {
		t.Errorf("the textarea does not hold the page:\n%s", editor.body)
	}
	if !strings.Contains(editor.body, "A fortified town on the confluence.") {
		t.Errorf("the editor did not load the page's body:\n%s", editor.body)
	}

	saved := f.postForm(f.pageURL("locations/rivergate")+"?edit=1", map[string]string{
		"csrf":     editorCSRF(t, editor.body),
		"etag":     editorETag(t, editor.body),
		"markdown": "A fortified town, and a bridge.\n",
	}, dm)

	if saved.status != http.StatusSeeOther {
		t.Fatalf("the save is %d, want 303\nbody: %s", saved.status, saved.body)
	}
	if !strings.Contains(f.readFile("locations/rivergate"), "and a bridge") {
		t.Errorf("the save did not write the file:\n%s", f.readFile("locations/rivergate"))
	}
	if got := f.get(f.pageURL("locations/rivergate"), dm); !strings.Contains(got.body, "and a bridge") {
		t.Errorf("the save did not reach the page:\n%s", got.body)
	}
}

// TestTheEditorIsOfferedToWhomMayWriteAndToNobodyElse: read and write are two
// questions, and every player reads every `players` page in the campaign.
func TestTheEditorIsOfferedToWhomMayWriteAndToNobodyElse(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		target   string
		cookie   *http.Cookie
		wantCode int
	}{
		"a DM's page, for the DM":          {target: "npcs/vel", wantCode: http.StatusOK},
		"a character's own page, for them": {target: "characters/aria", wantCode: http.StatusOK},
		"a players' page, for a player":    {target: "locations/rivergate", wantCode: http.StatusForbidden},
		"a page that is not there":         {target: "locations/nowhere", wantCode: http.StatusNotFound},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			// A DM's page in one case and a players' page in another, so "the
			// player is refused" is not the same answer as "the page is not
			// theirs" in every row.
			// The DM's own pages for the DM; a player's session for everything
			// else, because a player reading a `players` page and asking to edit it
			// is the case that has to be refused, and a player who cannot even read
			// a page never reaches the question.
			principal := f.dmSession()
			if tt.wantCode != http.StatusOK {
				principal = f.playerSession()
			}

			got := f.get(f.pageURL(tt.target)+"?edit=1", principal)
			if got.status != tt.wantCode {
				t.Errorf("GET %s?edit=1 is %d, want %d\nbody: %s", tt.target, got.status, tt.wantCode, got.body)
			}
		})
	}
}

// TestASaveWithNoCSRFTokenIsRefused: a forged POST is not a conversation, and it
// is the first thing the save route checks.
func TestASaveWithNoCSRFTokenIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	before := f.readFile("locations/rivergate")

	got := f.postForm(f.pageURL("locations/rivergate")+"?edit=1", map[string]string{
		"markdown": "somebody else's text",
		"etag":     f.hashOf("locations/rivergate"),
	}, dm)

	if got.status != http.StatusForbidden {
		t.Errorf("a save with no token is %d, want 403", got.status)
	}
	if after := f.readFile("locations/rivergate"); after != before {
		t.Error("a save with no token wrote the file")
	}
}

// TestAStaleSaveIsA409WithThreeTexts is the conflict, and the three texts are the
// point: a DM who is told "somebody else saved this" and shown nothing has nothing
// to decide.
func TestAStaleSaveIsA409WithThreeTexts(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	// What the DM was looking at.
	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", dm)
	staleETag := editorETag(t, editor.body)

	// Somebody else saves it.
	first := f.readFile("locations/rivergate")
	f.savePage("locations/rivergate", strings.Replace(first, "A fortified town", "A fortified town, and a bridge", 1))

	// And now the DM saves what they were looking at.
	conflict := f.postForm(f.pageURL("locations/rivergate")+"?edit=1", map[string]string{
		"csrf":     editorCSRF(t, editor.body),
		"etag":     staleETag,
		"markdown": "A fortified town, and a mill.\n",
	}, dm)

	if conflict.status != http.StatusConflict {
		t.Fatalf("a stale save is %d, want 409\nbody: %s", conflict.status, conflict.body)
	}

	// The three texts: what is on disk, what the DM wanted, and what it was when
	// they started. The base is the *first* revision this application kept, which
	// is the text the DM's editor loaded.
	for _, want := range []string{
		"Somebody else saved this page", // the heading
		"and a bridge",                  // theirs
		"and a mill",                    // yours
		"on disk now",                   // the columns
	} {
		if !strings.Contains(conflict.body, want) {
			t.Errorf("the conflict page does not contain %q:\n%s", want, conflict.body)
		}
	}

	// And nothing was written.
	if after := f.readFile("locations/rivergate"); !strings.Contains(after, "and a bridge") {
		t.Errorf("the refused save changed the file:\n%s", after)
	}
}

// TestAPreviewRendersThePageAndSavesNothing is the preview, and the two halves are
// one test because the second is the reason the first is allowed: a preview that
// wrote would be a route a browser's prefetch could write to.
func TestAPreviewRendersThePageAndSavesNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	before := f.readFile("locations/rivergate")

	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", dm)

	preview := f.postForm(f.pageURL("locations/rivergate")+"?preview=1", map[string]string{
		"csrf":     editorCSRF(t, editor.body),
		"markdown": "---\ntitle: Rivergate\ntype: " + domain.PageTypeLocation.String() + "\nvisibility: dm-only\n---\n\nA fortified town, with a **new** sentence.\n",
	}, dm)

	if preview.status != http.StatusOK {
		t.Fatalf("the preview is %d, want 200\nbody: %s", preview.status, preview.body)
	}
	if !strings.Contains(preview.body, "<strong>new</strong>") {
		t.Errorf("the preview is not the rendered page:\n%s", preview.body)
	}
	// The same article component, so it is the page and not a summary of it.
	if !strings.Contains(preview.body, `<article class="page" id="page">`) {
		t.Errorf("the preview is not the article:\n%s", preview.body)
	}

	if after := f.readFile("locations/rivergate"); after != before {
		t.Errorf("the preview wrote the file:\n%s", after)
	}
}

// TestThePreviewIsUnderTheReadersDecision is the canary on the preview route, and
// the two answers it asserts are both surprising enough to be worth a test.
//
// **A player previewing their own page sees their own secrets**, because §8 says
// a page's owner sees its secrets and a character page is the player's. That looks
// like a leak and is not one, so it is asserted: a future reader who "fixes" it
// would be taking a player away from their own notes' secrets, and the fix would
// look like a security improvement.
//
// **Nobody else sees them.** There is no route to a preview for a page the reader
// may not write, because the editor is refused first, so the second half is
// asserted as a refusal rather than as a stripped page — a stripped page would mean
// the preview route renders for somebody the editor would not open for.
func TestThePreviewIsUnderTheReadersDecision(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	withSecret := "---\ntitle: Aria\ntype: " + domain.PageTypeCharacter.String() +
		"\nvisibility: dm-and-owner\ncharacter: aria\n---\n\n> [!SECRET] The plan\n> " + canary + "\n"

	editor := f.get(f.pageURL("characters/aria")+"?edit=1", player)
	if editor.status != http.StatusOK {
		t.Fatalf("the editor is %d, want 200\nbody: %s", editor.status, editor.body)
	}

	// The owner sees it, and that is correct.
	owners := f.postForm(f.pageURL("characters/aria")+"?preview=1", map[string]string{
		"csrf":     editorCSRF(t, editor.body),
		"markdown": withSecret,
	}, player)
	if !strings.Contains(owners.body, canary) {
		t.Errorf("the owner was not shown the secret on their own page:\n%s", owners.body)
	}

	// And the frontmatter is not rendered as prose, which is the bug this test
	// started as: the preview takes the whole file, and the renderer wants the
	// body, and a caller that passed the file rendered an `<hr>` where the fences
	// were.
	if strings.Contains(owners.body, "visibility: dm-and-owner") {
		t.Errorf("the preview rendered the frontmatter as prose:\n%s", owners.body)
	}
	if !strings.Contains(owners.body, "<article class=\"page\" id=\"page\">") {
		t.Errorf("the preview is not the article:\n%s", owners.body)
	}

	// A principal who may not write the page is refused, not served.
	other := f.redeemIn(t, f.otherName, f.otherLink)
	refused := f.postForm(f.pageURL("characters/aria")+"?preview=1", map[string]string{
		"csrf":     editorCSRF(t, editor.body),
		"markdown": withSecret,
	}, other)
	if refused.status != http.StatusForbidden {
		t.Errorf("a principal of another campaign previewed this one: %d\nbody: %s", refused.status, refused.body)
	}
	if strings.Contains(refused.body, canary) {
		t.Error("the refused preview carried the secret anyway")
	}
}

// TestAPreviewOfSomethingThatIsNotAPageYetSaysSoRatherThanFailing: a DM typing a
// frontmatter fence is in that state for a second, and a 500 while typing is a
// preview they turn off.
func TestAPreviewOfSomethingThatIsNotAPageYetSaysSoRatherThanFailing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", dm)

	// Two things a DM types constantly that are not yet a page: an audience the
	// vault does not know, and a fence they have not closed. Both are *refused*
	// rather than skipped, and both are ordinary.
	for _, halfTyped := range []string{
		"---\ntitle: Rivergate\nvisibility: secret\n---\n\nA fortified town.\n",
		"---\ntitle: Rivergate\ntype: [a, b\n---\n\nBroken.\n",
	} {
		preview := f.postForm(f.pageURL("locations/rivergate")+"?preview=1", map[string]string{
			"csrf":     editorCSRF(t, editor.body),
			"markdown": halfTyped,
		}, dm)

		if preview.status != http.StatusOK {
			t.Errorf("a preview of %q is %d, want 200", halfTyped, preview.status)
		}
		if !strings.Contains(preview.body, "editor-hint") {
			t.Errorf("a preview of %q does not say why it cannot show anything:\n%s", halfTyped, preview.body)
		}
	}
}

// TestANewPageIsCreatedAndListed: the create flow end to end, which is the one a
// DM uses at the start of a session.
func TestANewPageIsCreatedAndListed(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	root := "/c/" + f.campaign.Slug.String() + "/"

	editor := f.get(root+"?new=1", dm)
	if editor.status != http.StatusOK {
		t.Fatalf("the new-page editor is %d, want 200\nbody: %s", editor.status, editor.body)
	}
	if want := actionAttr(root + "?new=1"); !strings.Contains(editor.body, want) {
		t.Errorf("the new-page form does not post to the root:\n%s", editor.body)
	}

	created := f.postForm(root+"?new=1", map[string]string{
		"csrf":     editorCSRF(t, editor.body),
		"path":     "sessions/10-the-lost-coat",
		"markdown": "---\ntitle: The Lost Coat\ntype: " + domain.PageTypeSessionLog.String() + "\nvisibility: dm-only\n---\n\nThey found the coat.\n",
	}, dm)
	if created.status != http.StatusSeeOther {
		t.Fatalf("the create is %d, want 303\nbody: %s", created.status, created.body)
	}
	if want := f.pageURL("sessions/10-the-lost-coat"); created.header.Get("Location") != want {
		t.Errorf("the create redirected to %q, want %q", created.header.Get("Location"), want)
	}

	// It is a file, a row and a page.
	if !strings.Contains(f.readFile("sessions/10-the-lost-coat"), "They found the coat") {
		t.Error("the create did not write the file")
	}
	if got := f.get(f.pageURL("sessions/10-the-lost-coat"), dm); !strings.Contains(got.body, "They found the coat") {
		t.Errorf("the created page is not readable:\n%s", got.body)
	}
}

// TestANewPageForAPlayerIsRefusedOutsideTheirOwnFolder: a player may create a
// page, and the pages they may create are the ones inside their own character's
// folder. The rule is the store's, so this is a test of the route's answer rather
// than of the rule.
func TestANewPageForAPlayerIsRefusedOutsideTheirOwnFolder(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	root := "/c/" + f.campaign.Slug.String() + "/"

	editor := f.get(root+"?new=1", player)
	csrf := editorCSRF(t, editor.body)

	// Their own character's folder, which the editor suggests and which is theirs.
	mine := f.postForm(root+"?new=1", map[string]string{
		"csrf":     csrf,
		"path":     "characters/aria/spells",
		"markdown": "---\ntitle: Spells\ntype: " + domain.PageTypeNote.String() + "\ncharacter: aria\n---\n\nLevitate, once a day.\n",
	}, player)
	if mine.status != http.StatusSeeOther {
		t.Errorf("a player could not create a page in their own folder: %d\nbody: %s", mine.status, mine.body)
	}

	// And not outside it. The path does not exist, so the create is attempted and
	// the *gate* is what refuses it — which is the case worth testing, because a
	// path that already existed would be refused earlier for a different reason.
	theirs := f.postForm(root+"?new=1", map[string]string{
		"csrf":     csrf,
		"path":     "locations/rivergate-under-the-bridge",
		"markdown": "---\ntitle: Under the Bridge\ntype: " + domain.PageTypeLocation.String() + "\n---\n\nTheir town.\n",
	}, player)
	if theirs.status != http.StatusForbidden {
		t.Errorf("a player created a page outside their folder: %d\nbody: %s", theirs.status, theirs.body)
	}
}

// TestTheETagIsTheFilesHash: the ETag on the editor is a fact about the *file*, and
// the one the save compares is the same string. A caller that can compute one
// cannot compute the other.
func TestTheETagIsTheFilesHash(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()

	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", dm)
	got := editorETag(t, editor.body)

	if strings.HasPrefix(got, "W/") {
		t.Errorf("the ETag is weak: %q", got)
	}
	// The form's value is the *quoted* form, because that is what an ETag is; the
	// server unquotes it before comparing, which is what lets a browser's
	// `If-Match` and a form field be the same value.
	if want := `"` + vault.Hash([]byte(f.readFile("locations/rivergate"))) + `"`; got != want {
		t.Errorf("the ETag is %q and the file's hash, quoted, is %q", got, want)
	}
}

// TestAPostToAPageThatIsNotAnEditIsA405: a form that posts to the wrong address
// has a bug in it, and working around the bug silently is how it survives to be a
// security problem later.
func TestAPostToAPageThatIsNotAnEditIsA405(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	dm := f.dmSession()
	before := f.readFile("locations/rivergate")

	got := f.postForm(f.pageURL("locations/rivergate"), map[string]string{
		"markdown": "a form that posted to the wrong address",
	}, dm)

	if got.status != http.StatusMethodNotAllowed {
		t.Errorf("a POST to a page is %d, want 405", got.status)
	}
	if allow := got.header.Get("Allow"); !strings.Contains(allow, "GET") {
		t.Errorf("the 405 does not say what is allowed: %q", allow)
	}
	if after := f.readFile("locations/rivergate"); after != before {
		t.Error("a 405 wrote the file")
	}
}

// TestTheEditorSaysWhenASaveIsRefusedByTheGateRatherThanAsNotFound: a player was
// looking at a page a moment ago, so telling them it does not exist is a lie about
// a page they have just read.
func TestTheEditorSaysWhenASaveIsRefusedByTheGateRatherThanAsNotFound(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	before := f.readFile("npcs/vel")

	// A player POSTs a save for a page they may read and may not write, with a
	// correct token and a correct ETag -- because they are crafty, or because a
	// stale tab of theirs has the button.
	got := f.postForm(f.pageURL("npcs/vel")+"?edit=1", map[string]string{
		"csrf":     editorCSRF(t, f.get(f.pageURL("locations/rivergate")+"?edit=1", player).body),
		"etag":     vault.Hash([]byte(before)),
		"markdown": "the player has had a go at this",
	}, player)

	if got.status != http.StatusForbidden {
		t.Errorf("a player's save of a DM's page is %d, want 403\nbody: %s", got.status, got.body)
	}
	if got.status == http.StatusNotFound {
		t.Error("the refusal is a 404, which claims a page they can read is not there")
	}
	if after := f.readFile("npcs/vel"); after != before {
		t.Error("the refused save wrote the file")
	}
}

// TestTheEditorIsLinkedFromThePage is discoverability: a page with no Edit link is
// an editor nobody finds, and a DM who cannot find it concludes the wiki has no
// editor.
func TestTheEditorIsLinkedFromThePage(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	asDM := f.get(f.pageURL("locations/rivergate"), f.dmSession())
	if !strings.Contains(asDM.body, "?edit=1") {
		t.Errorf("a DM's page has no Edit link:\n%s", asDM.body)
	}

	// A player gets an Edit link for the pages they may write and not for the ones
	// they may not: a link the gate refuses is a promise the wiki does not keep.
	player := f.playerSession()
	own := f.get(f.pageURL("characters/aria"), player)
	if !strings.Contains(own.body, "?edit=1") {
		t.Errorf("a player's own page has no Edit link:\n%s", own.body)
	}
	other := f.get(f.pageURL("locations/rivergate"), player)
	if strings.Contains(other.body, "?edit=1") {
		t.Errorf("a player is offered an editor for a page they may not write:\n%s", other.body)
	}
}

// TestNoConflictPanelWithoutAConflict: a template that always drew the diff would
// draw an empty one on every save, and a permanent red box is a box nobody reads.
func TestNoConflictPanelWithoutAConflict(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	editor := f.get(f.pageURL("locations/rivergate")+"?edit=1", f.dmSession())

	if strings.Contains(editor.body, "conflict-heading") {
		t.Errorf("the editor has a conflict panel with no conflict:\n%s", editor.body)
	}
}

// TestTheSaveKeepsAHistory is restore fidelity through the route, and the
// assertion is on the row rather than on the response: the save answers 303 and
// says nothing about revisions, and that is right.
func TestTheSaveKeepsAHistory(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	first := f.readFile("locations/rivergate")
	f.savePage("locations/rivergate", strings.Replace(first, "A fortified town", "A fortified town, and a bridge", 1))

	history, err := f.editor.History(t.Context(), "locations/rivergate")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("there is/are %d revision(s), want 1", len(history))
	}
	if history[0].Markdown != first {
		t.Errorf("the revision is not the text that was there before:\n%q", history[0].Markdown)
	}
}
