package access_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The policies in this file are the shape a plugin's policy has: a function handed
// the decision the core reached, returning a decision of its own. Three of the four
// things they do are things no plugin should be able to do, and the tests are mostly
// about that.

// allowEverything is the answer that must make no difference. If a policy returning
// this changed anything, "composition, not override" would be a comment rather than a
// property.
func allowEverything(context.Context, access.Principal, access.PageMeta, access.Decision) (access.Decision, error) {
	return access.Granted(), nil
}

// denyEverything is the answer that must be the whole of the result.
func denyEverything(context.Context, access.Principal, access.PageMeta, access.Decision) (access.Decision, error) {
	return access.Nothing(), nil
}

// TestAPluginPolicyCanOnlyTakeRightsAway is the named test for the capability, and
// it is a table over all five fields because a composition that narrows four of them
// is a composition that widens the fifth.
func TestAPluginPolicyCanOnlyTakeRightsAway(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// decided is what the core's own matrix reached. It is a field of the
		// table because the interesting cases start from different answers, and
		// the re-grant case only means anything when the core said no first.
		decided access.Decision
		policy  access.Policy
		want    access.Decision
	}{
		{
			name:    "a policy that grants everything does not grant anything new",
			decided: access.Granted(),
			policy:  allowEverything,
			want:    access.Granted(),
		},
		{
			name:    "a policy that grants everything does not restore a denied right",
			decided: access.Nothing(),
			policy:  allowEverything,
			want:    access.Nothing(),
		},
		{
			name:    "a policy that denies everything denies everything",
			decided: access.Granted(),
			policy:  denyEverything,
			want:    access.Nothing(),
		},
		{
			name: "a policy that tries to re-grant a right the core took away",
			// The shape of a confused plugin: the page is not readable, the field
			// is right there, let me turn it on. Narrowing is a one-way street and
			// this is the attempt to walk back up it.
			decided: access.Nothing(),
			policy:  allowEverything,
			want:    access.Nothing(),
		},
		{
			name:    "a policy that narrows one field leaves the others alone",
			decided: access.Granted(),
			policy: func(
				context.Context, access.Principal, access.PageMeta, access.Decision,
			) (access.Decision, error) {
				return access.Decision{
					CanRead: true, CanEdit: true, CanReveal: true, ReadsAll: true,
				}, nil
			},
			want: access.Decision{
				CanRead: true, CanEdit: true, CanReveal: true, ReadsAll: true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			policies := access.NewPolicies(nil)
			if err := policies.Add("spoilerbox", test.policy); err != nil {
				t.Fatalf("Add: %v", err)
			}

			got := policies.Apply(t.Context(), access.RoleOf(domain.RoleDM), access.PageMeta{}, test.decided)
			if got != test.want {
				t.Errorf("Apply(%s) = %s, want %s", test.decided, got, test.want)
			}
		})
	}
}

// TestTwoPoliciesCannotDisagreeAndBothWin: the results are ANDed, so a policy that
// takes a right away and another that gives it back leaves it away. It is a separate
// test from the one above because it is about the *order* two plugins run in, and an
// AND is only order-independent if nothing in between remembers.
func TestTwoPoliciesCannotDisagreeAndBothWin(t *testing.T) {
	t.Parallel()

	takesSecrets := func(
		_ context.Context, _ access.Principal, _ access.PageMeta, _ access.Decision,
	) (access.Decision, error) {
		return access.Decision{CanRead: true, CanEdit: true}, nil
	}
	givesSecrets := allowEverything

	for _, order := range [][]string{{"a-takes", "b-gives"}, {"b-gives", "a-takes"}} {
		policies := access.NewPolicies(nil)
		byName := map[string]access.Policy{"a-takes": takesSecrets, "b-gives": givesSecrets}
		for _, name := range order {
			if err := policies.Add(name, byName[name]); err != nil {
				t.Fatalf("Add(%s): %v", name, err)
			}
		}

		got := policies.Apply(t.Context(), access.RoleOf(domain.RoleDM), access.PageMeta{}, access.Granted())
		if got.CanSeeSecrets {
			t.Errorf("order %v: a right one policy took away was given back by the other", order)
		}
		if !got.CanRead {
			t.Errorf("order %v: the rights neither policy touched were lost: %s", order, got)
		}
	}
}

// TestAFailedPolicyDeniesRatherThanDefaultingToTheCore is the fail-closed half, and
// the error case is the one that is easy to get wrong: a policy that could not decide
// is not a policy that said yes.
func TestAFailedPolicyDeniesRatherThanDefaultingToTheCore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy access.Policy
	}{
		{
			name: "it returns an error",
			policy: func(
				context.Context, access.Principal, access.PageMeta, access.Decision,
			) (access.Decision, error) {
				return access.Granted(), errors.New("the character table is unreachable")
			},
		},
		{
			name: "it panics",
			policy: func(
				context.Context, access.Principal, access.PageMeta, access.Decision,
			) (access.Decision, error) {
				panic("a spoiler box with no body")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lines := &strings.Builder{}
			policies := access.NewPolicies(slog.New(slog.NewTextHandler(lines, nil)))
			if err := policies.Add("spoilerbox", test.policy); err != nil {
				t.Fatalf("Add: %v", err)
			}

			got := policies.Apply(t.Context(), access.RoleOf(domain.RoleDM), access.PageMeta{}, access.Granted())
			if got != access.Nothing() {
				t.Errorf("Apply = %s, want %s: a policy that could not run is one that did not happen",
					got, access.Nothing())
			}
			if !strings.Contains(lines.String(), "spoilerbox") {
				t.Errorf("nothing was logged about the failing policy:\n%s", lines.String())
			}
		})
	}
}

// TestAnEmptyPolicySetIsTheMatrix: with no plugins the answer is `For`'s, byte for
// byte, for every cell. It is the test that lets the HTTP layer apply policies
// unconditionally rather than behind a "are there any" check.
func TestAnEmptyPolicySetIsTheMatrix(t *testing.T) {
	t.Parallel()

	var empty *access.Policies

	if !empty.IsEmpty() {
		t.Error("a nil policy set does not report itself empty")
	}
	if got := empty.Len(); got != 0 {
		t.Errorf("Len() on a nil policy set = %d, want 0", got)
	}

	// The matrix's own principal and page, and the answer with and without a policy
	// set that exists but holds nothing.
	principal := access.RoleOf(domain.RolePlayer)
	page := access.PageMeta{Visibility: domain.VisibilityPlayers, Owned: true}

	decided := access.For(principal, page)

	withOne := access.NewPolicies(nil)
	if err := withOne.Add("house-rules", allowEverything); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := empty.Apply(t.Context(), principal, page, decided); got != decided {
		t.Errorf("a nil policy set changed the decision: %s, want %s", got, decided)
	}
	if got := (access.NewPolicies(nil)).Apply(t.Context(), principal, page, decided); got != decided {
		t.Errorf("an empty policy set changed the decision: %s, want %s", got, decided)
	}
	if got := withOne.Apply(t.Context(), principal, page, decided); got != decided {
		t.Errorf("a policy that grants everything changed the decision: %s, want %s", got, decided)
	}
}

// TestAPolicyWithNothingInItIsRefused: a policy that is not in the set is a page the
// DM believes is hidden and is not, so this is a startup failure rather than a log
// line.
func TestAPolicyWithNothingInItIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		plugin string
		policy access.Policy
	}{
		{name: "no plugin name", plugin: "", policy: allowEverything},
		{name: "no function", plugin: "house-rules", policy: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if err := access.NewPolicies(nil).Add(test.plugin, test.policy); err == nil {
				t.Error("Add accepted a policy with a hole in it")
			}
		})
	}

	var nilSet *access.Policies
	if err := nilSet.Add("house-rules", allowEverything); err == nil {
		t.Error("Add to a nil policy set = nil, want an error")
	}
}
