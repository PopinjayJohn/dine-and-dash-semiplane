package domain_test

import (
	"context"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The type assertion in `PrincipalFrom` is a comma-ok, and there is no test here
// for the wrong-type half of it: a context key this package owns can only be
// reached through `WithPrincipal`, and a test that reached past it to store a
// string would be testing the standard library's panic message.

// TestAPrincipalTravelsInTheContext is the mechanism, and it is three cases: one
// that reads back what was put in, one that reads nobody out of a context that has
// never had one, and one that reads nobody out of a context that *did* and then had
// it taken away.
//
// The third is the one that matters. The session middleware clears the principal
// before it tries to resolve one — a request whose cookie is refused must not keep
// whatever identity the context already carried — and a `WithPrincipal` that could
// not be taken back would be a `WithPrincipal` that does not clear anything.
func TestAPrincipalTravelsInTheContext(t *testing.T) {
	t.Parallel()

	player := domain.Principal{ID: "p-1", CampaignID: "c-1", Role: domain.RolePlayer}

	t.Run("a principal comes back", func(t *testing.T) {
		t.Parallel()

		got := domain.PrincipalFrom(domain.WithPrincipal(context.Background(), player))
		if got.ID != player.ID || got.Role != player.Role {
			t.Errorf("PrincipalFrom returned %+v, want %+v", got, player)
		}
	})

	t.Run("a context with none is nobody", func(t *testing.T) {
		t.Parallel()

		got := domain.PrincipalFrom(context.Background())
		if !got.IsNobody() {
			t.Errorf("an empty context produced %+v, want nobody", got)
		}
	})

	t.Run("a principal can be taken away", func(t *testing.T) {
		t.Parallel()

		ctx := domain.WithPrincipal(context.Background(), player)
		cleared := domain.WithPrincipal(ctx, domain.Nobody())

		if got := domain.PrincipalFrom(ctx); got.IsNobody() {
			t.Error("the first context lost its principal when a second one was derived from it")
		}
		if got := domain.PrincipalFrom(cleared); !got.IsNobody() {
			t.Errorf("the cleared context still carries %+v", got)
		}
	})
}

// TestNobodyHasNoRole is the fail-closed direction, and it is worth a test because
// a zero `Principal` with a *role* in it is the shape a bug would take.
//
// `store.Nobody()` is the zero value today, so this cannot fail — and that is the
// point. The day somebody gives `Nobody` a role, or builds a principal by hand with
// a role and no id, this fails, and the store's predicate stops being the only
// thing standing between a player and every `dm-only` page in a campaign.
func TestNobodyHasNoRole(t *testing.T) {
	t.Parallel()

	if got := domain.Nobody(); got.Role != "" {
		t.Errorf("Nobody has the role %q", got.Role)
	}
	if got := domain.Nobody(); got.CampaignID != "" {
		t.Errorf("Nobody is of the campaign %q", got.CampaignID)
	}
	if !domain.Nobody().IsNobody() {
		t.Error("Nobody is not nobody")
	}
	// And a principal with a role and no id is *also* nobody, which is the other
	// shape a bug takes.
	if !(domain.Principal{Role: domain.RoleDM}).IsNobody() {
		t.Error("a role with no id is not nobody, and the predicate admits nothing for it by accident")
	}
}
