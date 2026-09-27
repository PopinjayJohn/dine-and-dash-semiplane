package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// The renderer takes `access.Decision`, and this is the test for the seam: a
// decision that came out of the resolver, put straight into a render, produces
// bytes that match what the resolver said.
//
// It exists because M3 shipped a placeholder with one field and a comment
// promising this, and a promise in a comment is not a thing. If the two types ever
// drift apart — a field added to `access.Decision` that the renderer's cache key
// does not include, say — the failure is a render made for a DM served to a
// player, and it is silent.
//
// The canary assertions are over the *raw* HTML rather than a DOM, which is the
// rule ADR 0007 states and the one this project has kept since M3: a secret is
// removed from the response bytes, and a test that inspects a DOM is a test that
// passes when the secret is present but hidden.
func TestADecisionFromTheResolverReachesTheRender(t *testing.T) {
	t.Parallel()

	const canary = "IlithyaMarrowCanary"

	page := render.Page{
		Path:        "locations/rivergate",
		ContentHash: "hash-of-rivergate",
		Body: "A fortified town at the confluence of the [[Blackwater]].\n" +
			"\n" +
			"> [!SECRET] The toll-collector's real name\n" +
			"> Captain Vell is actually **" + canary + "**, sworn to the Umbral Court.\n",
	}

	// The three principals the rights matrix has, and the three pages they might
	// be looking at. The decision comes from the resolver; nothing here computes
	// one.
	tests := map[string]struct {
		principal domain.Principal
		page      access.PageMeta
		want      bool
	}{
		"a DM sees the secrets of a dm-only page": {
			principal: domain.Principal{ID: "dm-1", Role: domain.RoleDM},
			page:      access.PageMeta{Visibility: domain.VisibilityDMOnly},
			want:      true,
		},
		"a player sees none of a dm-only page's secrets, even owning it": {
			// §8: `dm-only` is absolute. The resolver decides this and the
			// renderer is only told the answer, so this case is a test of the
			// resolver reaching the render path -- which is the thing that was
			// promised in M3 and never checked.
			principal: domain.Principal{ID: "p-1", Role: domain.RolePlayer},
			page:      access.PageMeta{Visibility: domain.VisibilityDMOnly, Owned: true},
			want:      false,
		},
		"a player sees the secrets of a character page they own": {
			principal: domain.Principal{ID: "p-1", Role: domain.RolePlayer},
			page:      access.PageMeta{Visibility: domain.VisibilityDMAndOwner, Owned: true},
			want:      true,
		},
		"a player sees none of a character page they do not own": {
			principal: domain.Principal{ID: "p-2", Role: domain.RolePlayer},
			page:      access.PageMeta{Visibility: domain.VisibilityDMAndOwner},
			want:      false,
		},
		"a player sees none of a players' page's secrets": {
			// The case the whole of ADR 0009's split exists for: the page is
			// readable and the secret on it is not theirs.
			principal: domain.Principal{ID: "p-1", Role: domain.RolePlayer},
			page:      access.PageMeta{Visibility: domain.VisibilityPlayers},
			want:      false,
		},
		"an archived page renders with no secrets, for a DM": {
			// An archived page is nothing for everybody, and a render of one is a
			// render a viewer should not get at all. The decision is what the
			// resolver says, and the renderer's job is to obey it.
			principal: domain.Principal{ID: "dm-1", Role: domain.RoleDM},
			page:      access.PageMeta{Visibility: domain.VisibilityPlayers, Archived: true},
			want:      false,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			decision := access.For(access.PrincipalOf(tt.principal), tt.page)

			result, err := render.New().Render(context.Background(), page, decision)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			leaked := strings.Contains(result.HTML, canary)
			if leaked != tt.want {
				t.Errorf("the secret is in the HTML = %t, want %t; the decision was %s",
					leaked, tt.want, decision)
			}

			// And the page's own prose is in both, because a decision about
			// secrets is a decision about secrets and not about the page.
			if !strings.Contains(result.HTML, "fortified town") {
				t.Errorf("the page's prose is missing from the render: %s", result.HTML)
			}
		})
	}
}

// The renderer's decision and `access.Decision` are the same type, and that is
// what stops a fourth implementation of the secret policy appearing.
func TestTheRenderersDecisionIsTheAccessOne(t *testing.T) {
	t.Parallel()

	// A value of one, a value of the other, and a comparison. If the renderer had
	// a type of its own the two values below would be of different types and the
	// comparison would not compile, which is the point: the alias is what makes "a
	// second implementation of the secret policy" a compile error rather than a
	// review question.
	decision := access.Granted()
	fromAccess := render.Grant()

	if decision != fromAccess {
		t.Errorf("render.Grant is %s and access.Granted is %s, want the same", decision, fromAccess)
	}
	if render.NewDecision() != access.Nothing() {
		t.Errorf("render.NewDecision is %s, want the safe zero", render.NewDecision())
	}
}
