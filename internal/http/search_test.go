package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// # The search dropdown
//
// Three things it has to be: correct about who may see what, cheap enough to fire
// on every keystroke, and honest about having nothing to show.

// searchURL is the dropdown's own route, which is a query on the campaign root
// because a path segment would shadow a page a DM could write.
func (f *fixture) searchURL(query string) string {
	return "/c/" + f.campaign.Slug.String() + "/?search=1&q=" + urlEscape(query)
}

func urlEscape(s string) string {
	return strings.NewReplacer(" ", "+", "&", "%26", "?", "%3F", "#", "%23", `"`, "%22").Replace(s)
}

// TestTheDropdownIsTheACLIsTheAnswer is the property, and the negative is the
// half that matters: a `dm-only` page's *title* in a dropdown is the disclosure
// that ADR 0020's fix removed from `[[link]]`, arriving by another route.
func TestTheDropdownIsTheACLIsTheAnswer(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	asDM := f.get(f.searchURL("Captain Vell"), f.dmSession())
	if asDM.status != http.StatusOK {
		t.Fatalf("the DM's search is %d, want 200\nbody: %s", asDM.status, asDM.body)
	}
	if !strings.Contains(asDM.body, "Captain Vell") {
		t.Errorf("the DM's own search did not find their own page:\n%s", asDM.body)
	}

	asPlayer := f.get(f.searchURL("Captain Vell"), f.playerSession())
	if asPlayer.status != http.StatusOK {
		t.Fatalf("a player's search is %d, want 200", asPlayer.status)
	}
	for _, leak := range []string{"Captain Vell", "npcs/vel", "toll"} {
		if strings.Contains(asPlayer.body, leak) {
			t.Errorf("a player's dropdown carries %q, which is a DM's page:\n%s", leak, asPlayer.body)
		}
	}
	if !strings.Contains(asPlayer.body, "Nothing matches") {
		t.Errorf("a player's dropdown did not say it had nothing:\n%s", asPlayer.body)
	}
}

// TestTheDropdownFindsAPageAPlayerMayRead is the other half, because a dropdown
// that finds nothing for everybody is not an access control.
func TestTheDropdownFindsAPageAPlayerMayRead(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get(f.searchURL("fortified"), f.playerSession())
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200", got.status)
	}
	if !strings.Contains(got.body, "rivergate") {
		t.Errorf("a player could not search for a page they may read:\n%s", got.body)
	}
	// And the row is a link a keyboard can reach, which is the half a screenshot
	// would not show.
	if !strings.Contains(got.body, `href="/c/blackwater/locations/rivergate"`) {
		t.Errorf("the candidate is not a link to the page:\n%s", got.body)
	}
	if !strings.Contains(got.body, `role="option"`) {
		t.Errorf("the candidate is not an option in a listbox:\n%s", got.body)
	}
}

// TestAnUnidentifiedRequestSearchesNothing: the dropdown is reachable without a
// session, so it has to be as empty as the campaign root is.
func TestAnUnidentifiedRequestSearchesNothing(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get(f.searchURL("fortified"))
	if got.status != http.StatusOK {
		t.Fatalf("status %d, want 200", got.status)
	}
	if !strings.Contains(got.body, "Nothing matches") {
		t.Errorf("an unidentified request searched a campaign:\n%s", got.body)
	}
}

// TestTheShortAndTheEmptyQueryAreNotSearches is the cost control.
//
// A search box fires a request per keystroke, so the two answers that save the
// database are "too short to be worth a query" and "not a query at all" — and both
// are 200 with an empty dropdown, because a player mashing the spacebar is not
// making a malformed request and a red box in a dropdown is worse than no box.
func TestTheShortAndTheEmptyQueryAreNotSearches(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		query     string
		wantEmpty bool
	}{
		"no query at all": {query: "", wantEmpty: true},
		"one character":   {query: "f", wantEmpty: true},
		"one space":       {query: " ", wantEmpty: true},
		// A word the campaign has, and a prefix of one: both are searches, and
		// the first is a search the *engine* refuses to match, which is a
		// different thing from a query the handler declines to run.
		"a word the campaign has": {query: "fortified", wantEmpty: false},
		"a prefix of one":         {query: "fort", wantEmpty: false},
		// A quote is not a syntax error in this query language -- an unterminated
		// one is a literal quote -- so a DM typing `"the toll` on the way to
		// `"the toll collector"` gets results rather than an empty box. This row is
		// here because the *obvious* implementation treats it as an error, and the
		// second version of the handler guessed at that with string matching.
		"a half-typed quote": {query: `"fortified`, wantEmpty: false},
		// `is:` with no level is the one thing `Parse` refuses, and it is a 200
		// with an empty dropdown rather than a 500.
		"a filter with no level": {query: "is:", wantEmpty: true},
		"a filter with a typo":   {query: "is:secret", wantEmpty: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			got := f.get(f.searchURL(tt.query), f.dmSession())

			if got.status != http.StatusOK {
				t.Errorf("status %d, want 200\nbody: %s", got.status, got.body)
			}
			empty := strings.Contains(got.body, "Nothing matches")
			if tt.wantEmpty && !empty {
				t.Errorf("the dropdown found something for %q:\n%s", tt.query, got.body)
			}
			if !tt.wantEmpty && empty {
				t.Errorf("the dropdown found nothing for %q, which the campaign does have:\n%s", tt.query, got.body)
			}
		})
	}
}

// TestTheDropdownIsNeverCached: its contents are a function of what somebody is
// typing and of who they are, and a cached one is a list of page titles delivered to
// the next person to look at it.
func TestTheDropdownIsNeverCached(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get(f.searchURL("fortified"), f.dmSession())
	if cacheControl := got.header.Get("Cache-Control"); cacheControl != "no-store" {
		t.Errorf("the dropdown is cached: %q", cacheControl)
	}
}

// TestTheDropdownIsAResponseAndNotAPage: it is a fragment, so the page's chrome is
// not re-rendered underneath a reader's cursor, and there is exactly one place the
// candidates are drawn.
func TestTheDropdownIsAResponseAndNotAPage(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	got := f.get(f.searchURL("fortified"), f.dmSession())
	for _, notAFragment := range []string{"<!DOCTYPE", "<nav", "class=\"sidebar\"", "Log out"} {
		if strings.Contains(got.body, notAFragment) {
			t.Errorf("the dropdown contains %q, so it is a whole page rather than a fragment", notAFragment)
		}
	}
	if !strings.Contains(got.body, `role="listbox"`) {
		t.Errorf("the dropdown is not a listbox:\n%s", got.body)
	}
}

// TestASecretHitIsMarkedRatherThanHidden: a player searching their own character
// page and finding it in the private index is the point of the private index, and a
// dropdown that silently hid it would be a dropdown lying about what it searched.
func TestASecretHitIsMarkedRatherThanHidden(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()

	// A page the player owns, with a secret block in it, so the public index does
	// not have the word and the private one does.
	//
	// The word searched for is one segment of the canary, not the whole of it: the
	// query language ANDs on hyphens, so a search for `ULTRAMARINE-FOXTRAP-7742`
	// is a search for three terms and two of them are in no index. A test that
	// searched for the whole canary would find nothing and blame the search.
	const secretWord = "ULTRAMARINE"
	//
	// The title has no apostrophe in it, and that is not an accident of naming: the
	// first version of this was `Aria's Plan`, and the assertion looked for
	// `Aria's Plan` in a body that says `Aria&#39;s Plan` because the template
	// escapes it. Both halves were right -- the escaping is the feature and the
	// test was reading markup as text -- and a title without a quote is one fewer
	// thing between the test and the thing it is testing.
	markdown := "---\ntitle: The Plan\ntype: " + domain.PageTypeNote.String() +
		"\nvisibility: dm-and-owner\ncharacter: aria\n---\n\n> [!SECRET] The plan\n> " + canary + "\n"
	f.saveNew("characters/aria/plan", markdown)

	// The DM has the same page and searches the same word.
	asDM := f.get(f.searchURL(secretWord), f.dmSession())
	if !strings.Contains(asDM.body, "The Plan") {
		t.Errorf("the DM's search did not find a page with a secret in it:\n%s", asDM.body)
	}
	if !strings.Contains(asDM.body, "from a secret") {
		t.Errorf("a hit from the private index is not marked:\n%s", asDM.body)
	}

	// And the owner, for the same page, is marked the same way — which is the
	// assertion. Hiding it would be a dropdown that says it searched the wiki and
	// did not.
	asOwner := f.get(f.searchURL(secretWord), player)
	if !strings.Contains(asOwner.body, "The Plan") {
		t.Errorf("the owner could not search their own secret block:\n%s", asOwner.body)
	}
	if !strings.Contains(asOwner.body, "from a secret") {
		t.Errorf("the owner's hit from the private index is not marked:\n%s", asOwner.body)
	}
}
