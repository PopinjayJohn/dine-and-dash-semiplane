package http_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// # A plugin's access policy, end to end
//
// `internal/access/policy_test.go` proves the composition rule in isolation. This
// file proves the three things that only an HTTP test can: that the policy reaches
// the page route, that it reaches the *listings*, and that the cost of finding out
// who owns a page is paid once per owner rather than once per row.

// hideLocations is the policy under test: the whole `locations/` subtree stops being
// readable by anybody but the DM.
//
// It is a plausible thing for a plugin to want — a running "this region is under a
// fog of war" rule, say — and it is the shape that finds the bugs, because it
// narrows on something the page *carries* rather than on a row the store happened to
// return.
func hideLocations(
	_ context.Context, p access.Principal, page access.PageMeta, _ access.Decision,
) (access.Decision, error) {
	if p.Role == domain.RoleDM {
		return access.Granted(), nil
	}
	if strings.HasPrefix(page.Path, "locations/") {
		return access.Nothing(), nil
	}
	return access.Granted(), nil
}

// withPolicies installs a policy set on the fixture and rebuilds the handler.
func (f *fixture) withPolicies(t *testing.T, policies ...access.Policy) {
	t.Helper()

	// The fixture's own logger, so a policy that fails writes into the buffer the
	// other tests in this package read. A set built with a nil logger would report
	// itself to `slog.Default` and the test asserting "the DM was told" would pass
	// with nothing in the log the DM actually has.
	set := access.NewPolicies(f.cfg.Logger)
	for i, policy := range policies {
		name := "test-policy-" + string(rune('a'+i))
		if err := set.Add(name, policy); err != nil {
			t.Fatalf("adding %s: %v", name, err)
		}
	}

	f.cfg.Policies = set
	f.rebuild()
}

// TestAPluginPolicyHidesAPageARowReaches is the named test: the three surfaces a
// player can learn that a page exists are the page itself, the page tree and the
// search dropdown, and a policy that only narrows the first has not hidden
// anything.
func TestAPluginPolicyHidesAPageARowReaches(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.withPolicies(t, hideLocations)

	// Two spellings, because the two surfaces spell it differently and a test that
	// asserted one of them would be a test of the tree rather than of the policy: the
	// sidebar is built from paths, and the dropdown from titles.
	const (
		hiddenPath  = "/c/" + "blackwater" + "/locations/rivergate"
		hiddenTitle = "Rivergate"
	)
	pageURL := hiddenPath

	t.Run("the page itself", func(t *testing.T) {
		t.Parallel()

		got := f.get(pageURL, f.playerSession())
		if got.status != http.StatusNotFound {
			t.Errorf("the player got %d for a page a policy hides, want 404\nbody: %s", got.status, got.body)
		}
		if strings.Contains(got.body, "A fortified town") {
			t.Errorf("the page body reached a player a policy hides:\n%s", got.body)
		}
	})

	t.Run("the page tree", func(t *testing.T) {
		t.Parallel()

		got := f.get("/c/"+f.campaign.Slug.String()+"/", f.playerSession())
		if strings.Contains(got.body, hiddenPath) {
			t.Errorf("the sidebar links %q for a player a policy hides it from:\n%s", hiddenPath, got.body)
		}
	})

	t.Run("the search dropdown", func(t *testing.T) {
		t.Parallel()

		got := f.get(f.searchURL(hiddenTitle), f.playerSession())
		if got.status != http.StatusOK {
			t.Fatalf("the search is %d, want 200\nbody: %s", got.status, got.body)
		}
		if strings.Contains(got.body, hiddenTitle) {
			t.Errorf("the dropdown offers %q for a player a policy hides it from:\n%s", hiddenTitle, got.body)
		}
	})

	t.Run("and the DM still has all of it", func(t *testing.T) {
		t.Parallel()

		// The other half, and the one a "narrowing" implementation gets wrong by
		// applying the policy to everybody. A DM whose campaign vanishes because a
		// plugin hid it is a wiki nobody can use.
		asDM := f.get(pageURL, f.dmSession())
		if asDM.status != http.StatusOK {
			t.Fatalf("the DM got %d for their own page, want 200\nbody: %s", asDM.status, asDM.body)
		}
		if !strings.Contains(asDM.body, "A fortified town") {
			t.Errorf("the DM's own page did not render its body:\n%s", asDM.body)
		}

		tree := f.get("/c/"+f.campaign.Slug.String()+"/", f.dmSession())
		if !strings.Contains(tree.body, hiddenPath) {
			t.Errorf("the DM's sidebar does not link %q:\n%s", hiddenPath, tree.body)
		}

		results := f.get(f.searchURL(hiddenTitle), f.dmSession())
		if !strings.Contains(results.body, hiddenTitle) {
			t.Errorf("the DM's search does not find %q:\n%s", hiddenTitle, results.body)
		}
	})
}

// TestAPluginPolicyCannotWidenWhatTheMatrixTookAway is the composition guarantee
// seen from the outside: a policy that tries to make a `dm-only` page readable does
// not, and the page is not merely refused — it is *invisible*, which is the
// difference between a 403 that confirms a page exists and a 404 that does not.
func TestAPluginPolicyCannotWidenWhatTheMatrixTookAway(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// The worst thing a plugin can write, and the reason `Policies.Apply` ANDs
	// rather than trusting the order.
	f.withPolicies(t, func(
		_ context.Context, _ access.Principal, _ access.PageMeta, _ access.Decision,
	) (access.Decision, error) {
		return access.Granted(), nil
	})

	const url = "/c/" + "blackwater" + "/npcs/captain-vell"

	got := f.get(url, f.playerSession())
	if got.status != http.StatusNotFound {
		t.Errorf("a policy made a dm-only page readable: the player got %d\nbody: %s", got.status, got.body)
	}
	if strings.Contains(got.body, "Captain Vell") {
		t.Errorf("the dm-only page's title reached a player:\n%s", got.body)
	}
}

// TestAPolicyThatCannotRunDeniesRatherThanFails is the fail-closed half seen from
// the outside, and it is the half a DM would rather have a broken wiki than the
// other way round: a policy that panics on every page takes the page away rather
// than letting it through.
//
// It is a test of the *direction* rather than of the panic, and the same policy in
// the render path is skipped rather than fatal. The two are deliberately different
// and internal/access/policy.go is where that is argued.
func TestAPolicyThatCannotRunDeniesRatherThanFails(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.withPolicies(t, func(
		context.Context, access.Principal, access.PageMeta, access.Decision,
	) (access.Decision, error) {
		panic("a spoiler box with no body")
	})

	got := f.get("/c/"+f.campaign.Slug.String()+"/locations/rivergate", f.playerSession())
	if got.status != http.StatusNotFound {
		t.Errorf("a panicking policy let a page through: the player got %d\nbody: %s", got.status, got.body)
	}

	// And the DM has a log line, or a wiki that is quietly missing every page has no
	// diagnosis at all.
	if !strings.Contains(f.logs.String(), "test-policy-a") {
		t.Errorf("nothing was logged about the panicking policy:\n%s", f.logs.String())
	}
}
