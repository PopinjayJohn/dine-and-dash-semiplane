package http_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// # CSRF
//
// The double-submit token, tested through the one mutation there is. A CSRF test
// that posts to a form nobody has is a test of a form nobody has.

// csrfTokenPattern finds the token in a rendered logout form, so that the test
// starts from what a player would actually submit.
var csrfTokenPattern = regexp.MustCompile(`name="csrf" value="([^"]*)"`)

func logoutToken(t *testing.T, f *fixture, cookie *http.Cookie) string {
	t.Helper()

	return logoutTokenIn(t, f, f.campaign.Slug, cookie)
}

// logoutTokenIn reads the token out of a named campaign's form, which is how the
// tenancy case gets a token that belongs somewhere else. Reading it out of *this*
// campaign's form would be impossible on purpose: the campaign middleware refuses to
// treat a foreign session as a principal of this campaign, so there is no form to
// read.
func logoutTokenIn(t *testing.T, f *fixture, slug domain.Slug, cookie *http.Cookie) string {
	t.Helper()

	got := f.get("/c/"+slug.String()+"/", cookie)
	raw := csrfTokenPattern.FindStringSubmatch(got.body)
	if raw == nil {
		t.Fatalf("there is no logout form, so there is no token to submit:\n%s", got.body)
	}
	if raw[1] == "" {
		t.Fatal("the logout form's CSRF token is empty, so the form can never be submitted")
	}
	return raw[1]
}

// TestAMutationWithoutTheTokenIsRefused is the property: a POST that did not come
// from a page of this wiki does not happen.
//
// The token is read out of the rendered form, so the happy path and the refused
// path differ by one thing: the token. A test that invented its own token would be
// testing that an invented token is refused, which is not the property.
func TestAMutationWithoutTheTokenIsRefused(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	root := "/c/" + f.campaign.Slug.String() + "/"

	tests := map[string]struct {
		form       map[string]string
		wantStatus int
	}{
		"no token at all": {
			form:       map[string]string{},
			wantStatus: http.StatusForbidden,
		},
		"an empty token": {
			form:       map[string]string{"csrf": ""},
			wantStatus: http.StatusForbidden,
		},
		"a made-up token": {
			form:       map[string]string{"csrf": strings.Repeat("a", 43)},
			wantStatus: http.StatusForbidden,
		},
		"the real token": {
			wantStatus: http.StatusSeeOther,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t)
			cookie := f.playerSession()

			form := tt.form
			if form == nil {
				form = map[string]string{"csrf": logoutToken(t, f, cookie)}
			}

			got := f.postForm(root, form, cookie)
			if got.status != tt.wantStatus {
				t.Errorf("status %d, want %d\nbody: %s", got.status, tt.wantStatus, got.body)
			}
		})
	}
}

// TestTheTokenIsTiedToThePrincipal is the second half of the double submit, and it
// is the half that makes the token a token: a player cannot compute one, and one
// player's is not another's.
//
// The tenancy case is in here too. Two campaigns' principals have different ids, so
// a token minted for one campaign is useless in the other -- the check happening in
// a form field rather than in a URL.
func TestTheTokenIsTiedToThePrincipal(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	dm := f.dmSession()
	root := "/c/" + f.campaign.Slug.String() + "/"

	playerToken := logoutToken(t, f, player)
	dmToken := logoutToken(t, f, dm)

	if playerToken == dmToken {
		t.Error("two principals have the same CSRF token, so the token is not tied to either")
	}

	// A player posting the DM's token is refused, and the DM is still logged in
	// afterwards -- which is the assertion that matters: the refusal did not log
	// anybody out.
	if got := f.postForm(root, map[string]string{"csrf": dmToken}, player); got.status != http.StatusForbidden {
		t.Errorf("a player posting the DM's token is %d, want 403", got.status)
	}
	if got := f.get(f.pageURL("npcs/vel"), dm); !strings.Contains(got.body, "Captain Vell") {
		t.Errorf("a refused POST logged the DM out:\n%s", got.body)
	}

	// And the token from another campaign does not work here. It is read out of
	// *that* campaign's form, which is the only place it exists.
	other := f.redeemIn(t, f.otherName, f.otherLink)
	otherToken := logoutTokenIn(t, f, f.otherName.Slug, other)
	if otherToken == playerToken {
		t.Error("two campaigns' principals have the same CSRF token, so it is not tied to a campaign")
	}
	if got := f.postForm(root, map[string]string{"csrf": otherToken}, player); got.status == http.StatusSeeOther {
		t.Error("a token from another campaign was accepted")
	}
}

// TestTheTokenSurvivesASessionRotation: the token is tied to the principal and not
// to the session, and the reason is that a session rotation is a security event --
// a role change, a binding change -- at exactly the moment a player is most likely
// to have the page open. A token that died with the session would make them reload
// at the worst possible moment.
func TestTheTokenSurvivesASessionRotation(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	player := f.playerSession()
	root := "/c/" + f.campaign.Slug.String() + "/"

	token := logoutToken(t, f, player)

	// A binding change is one of the two things that rotates a session.
	aria, err := f.store.GetPage(t.Context(), f.campaign.ID, "characters/aria", storeAsDM(f.campaign.ID))
	if err != nil {
		t.Fatalf("GetPage for Aria: %v", err)
	}
	if err := f.store.ReplacePrincipalCharacters(t.Context(), f.playerLink.Principal.ID, []string{aria.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}

	// The old cookie may or may not still authenticate depending on whether the
	// store rotated it, so the test asks the question that does not depend on
	// that: is the token still the right one for this principal?
	if got := logoutToken(t, f, player); got != token {
		t.Errorf("the token changed when the binding did: %q became %q", token, got)
	}
	if got := f.postForm(root, map[string]string{"csrf": token}, player); got.status != http.StatusSeeOther {
		t.Errorf("the pre-rotation token was refused: %d", got.status)
	}
}

// TestTheOriginIsCheckedOnTop: `SameSite=Lax` already blocks the cross-site POST,
// and the token is what is left when a browser sends no `Origin` at all. The origin
// check is on top because a request from another site is unambiguous, and there is
// no reason to look further once it has been seen.
func TestTheOriginIsCheckedOnTop(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		allowed    string
		origin     string
		wantStatus int
	}{
		"no configured origin and no header": {
			wantStatus: http.StatusSeeOther,
		},
		"a configured origin and no header": {
			allowed: "https://wiki.example", wantStatus: http.StatusSeeOther,
		},
		"a header from the configured origin": {
			allowed: "https://wiki.example", origin: "https://wiki.example", wantStatus: http.StatusSeeOther,
		},
		"a header from somewhere else": {
			allowed: "https://wiki.example", origin: "https://evil.invalid", wantStatus: http.StatusForbidden,
		},
		"no configured origin and a header from somewhere else": {
			// The check is skipped, because comparing against nothing would refuse
			// every request, and the token is the check that does not depend on the
			// header being there at all.
			origin: "https://evil.invalid", wantStatus: http.StatusSeeOther,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			f := newFixtureIn(t, false)
			f.cfg.AllowedOrigin = tt.allowed
			f.rebuild()

			cookie := f.playerSession()
			token := logoutToken(t, f, cookie)

			req := f.newRequest(http.MethodPost, "/c/"+f.campaign.Slug.String()+"/",
				"csrf="+token, cookie)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			got := f.serve(req)
			if got.status != tt.wantStatus {
				t.Errorf("status %d, want %d\nbody: %s", got.status, tt.wantStatus, got.body)
			}
		})
	}
}

// TestThereIsNoLogoutFormForNobody: a form with an empty token is a form that
// cannot be submitted, and it is worse than no form, because a player clicks it and
// nothing happens and the wiki looks broken.
func TestThereIsNoLogoutFormForNobody(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	anonymous := f.get("/c/" + f.campaign.Slug.String() + "/")
	if strings.Contains(anonymous.body, "Log out") {
		t.Error("a request with no session has a logout form")
	}
	if strings.Contains(anonymous.body, `name="csrf"`) {
		t.Error("a request with no session has a CSRF field")
	}
}
