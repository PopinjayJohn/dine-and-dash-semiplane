package auth_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// Rotation is the hardening item ADR 0003 lists as "session rotation whenever role
// or character binding changes", and it is worth being clear about why it exists:
// a session is a *row*, and a row is not told that what it may read has changed. So
// a change to a role or a binding has to say so explicitly, or it takes effect when
// the cookie happens to expire.

func TestARoleChangeEndsEverySession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, _, campaign := newMinter(t)
	issued, err := m.Issue(ctx, campaign, domain.RoleDM, "The DM")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	r := &auth.Redeemer{Backend: m.Backend, Config: m.Config}

	first, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	second, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("the second Redeem: %v", err)
	}

	// Two browsers, because "ends every session" is the claim and one browser does
	// not test it.
	for _, session := range []string{first.SessionID, second.SessionID} {
		if _, err := r.Authenticate(ctx, session); err != nil {
			t.Fatalf("a session does not authenticate before the change: %v", err)
		}
	}

	// The DM makes themselves a player, which is the change that matters most:
	// a stale cookie holding DM is a principal with the authority nobody granted
	// them any more. The principal starts as a DM for that reason — rotating a
	// player to a player is the no-op case, which has its own test.
	if issued.Principal.Role != domain.RoleDM {
		t.Fatalf("the fixture principal is a %q, want a DM", issued.Principal.Role)
	}
	if err := r.SetRole(ctx, issued.Principal, domain.RolePlayer); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	for _, session := range []string{first.SessionID, second.SessionID} {
		if _, err := r.Authenticate(ctx, session); err == nil {
			t.Error("a session still authenticates after a role change")
		}
	}

	// The link still works, so the player is not locked out — they click it again.
	// A role change is a demotion, not a revocation, and a demotion that also
	// revoked the link would be a second, unasked-for revocation.
	if _, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex()); err != nil {
		t.Errorf("the link stopped working after a role change: %v", err)
	}
}

// Unbinding somebody has to take effect on the next request, and it only does
// because the change rotates the sessions rather than waiting for them to lapse.
func TestABindingChangeEndsEverySession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	page, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeNote,
		Visibility:  domain.VisibilityDMAndOwner,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker.\n",
		ContentHash: "hash-of-aria",
	}, store.AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	// Bound, with a live session.
	if bindErr := r.SetCharacters(ctx, issued.Principal, []string{page.ID}); bindErr != nil {
		t.Fatalf("binding: %v", bindErr)
	}
	session, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if _, err := r.Authenticate(ctx, session.SessionID); err != nil {
		t.Fatalf("the session does not authenticate after being bound: %v", err)
	}

	// Unbound. The session from before the binding is now a fortnight of access to
	// a character page this principal no longer owns, and this is the change that
	// stops it.
	if err := r.SetCharacters(ctx, issued.Principal, nil); err != nil {
		t.Fatalf("unbinding: %v", err)
	}
	if _, authErr := r.Authenticate(ctx, session.SessionID); authErr == nil {
		t.Error("the session from before the unbinding still authenticates: " +
			"unbinding somebody would not take effect for a fortnight")
	}
}

// Saving a role that was already the role is not a change, and must not log the
// players out. A DM who opens a settings page and presses save should not end every
// session in the campaign.
func TestAnUnchangedRoleRotatesNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _, campaign, issued := newRedeemer(t)

	session, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	if err := r.SetRole(ctx, issued.Principal, domain.RolePlayer); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if _, err := r.Authenticate(ctx, session.SessionID); err != nil {
		t.Errorf("saving the role that was already set ended the session: %v", err)
	}
}

// Every rotation is in the log, with its reason, and the reason is what a DM reads
// six months later when they ask why a player had to click their link again.
func TestRotationIsRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	// A DM, so that the role change is a change: setting a player's role to player
	// is a no-op by design, and a no-op rotates nothing.
	m, s, campaign := newMinter(t)
	issued, err := m.Issue(ctx, campaign, domain.RoleDM, "The DM")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	r := &auth.Redeemer{Backend: m.Backend, Config: m.Config}

	page, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeNote,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker.\n",
		ContentHash: "hash-of-aria",
	}, store.AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}

	if roleErr := r.SetRole(ctx, issued.Principal, domain.RolePlayer); roleErr != nil {
		t.Fatalf("SetRole: %v", roleErr)
	}
	if bindErr := r.SetCharacters(ctx, issued.Principal, []string{page.ID}); bindErr != nil {
		t.Fatalf("SetCharacters: %v", bindErr)
	}

	entries, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	var rotations, roles, bindings int
	for _, entry := range entries {
		switch entry.Action {
		case domain.AuditSessionRotated:
			rotations++
			if entry.Detail == "" {
				t.Error("a rotation was recorded with no reason")
			}
		case domain.AuditRoleChanged:
			roles++
		case domain.AuditBindingChanged:
			bindings++
		default:
			// The issuance, and the redemption if there was one. Not what this test
			// is about.
		}
	}

	if rotations != 2 {
		t.Errorf("%d rotations were recorded, want 2: one per change", rotations)
	}
	if roles != 1 {
		t.Errorf("%d role changes were recorded, want 1", roles)
	}
	if bindings != 1 {
		t.Errorf("%d binding changes were recorded, want 1", bindings)
	}
}

// The binding detail is a count and not the page ids. The log is the thing
// somebody pastes into a bug report, and a log that listed a player's characters
// is a log that says who plays what.
func TestTheBindingDetailIsACountAndNotAPageList(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	page, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeNote,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker.\n",
		ContentHash: "hash-of-aria",
	}, store.AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	if bindErr := r.SetCharacters(ctx, issued.Principal, []string{page.ID}); bindErr != nil {
		t.Fatalf("SetCharacters: %v", err)
	}

	entries, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	for _, entry := range entries {
		if entry.Action != domain.AuditBindingChanged {
			continue
		}
		if strings.Contains(entry.Detail, page.ID) {
			t.Errorf("the binding detail names the page %q: %q", page.ID, entry.Detail)
		}
		if !strings.Contains(entry.Detail, "1") {
			t.Errorf("the binding detail %q does not say how many characters there are", entry.Detail)
		}
		return
	}
	t.Fatal("no binding change was recorded")
}

// A rotation of a principal with no sessions is still recorded, because "nobody was
// logged in" and "we did not check" are different answers and this is the place
// that tells them apart.
func TestARotationWithNoSessionsIsStillRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	ended, err := r.Rotate(ctx, issued.Principal, auth.RotatedForRoleChange)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if ended != 0 {
		t.Errorf("rotating a principal with no sessions ended %d", ended)
	}

	entries, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	var rotations int
	for _, entry := range entries {
		if entry.Action == domain.AuditSessionRotated {
			rotations++
		}
	}
	if rotations != 1 {
		t.Errorf("%d rotations were recorded, want 1 even though there was nothing to end", rotations)
	}
}

// A rotation must not take effect later than the change it belongs to. This is the
// ordering, and the safe direction for it: a failure between the two leaves a
// principal with the new role and stale cookies, which the predicate resolves
// against the player.
func TestAChangeTakesEffectOnTheNextRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	session, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	page, err := s.UpsertPage(ctx, domain.Page{
		CampaignID:  campaign.ID,
		Path:        "characters/aria",
		Title:       "Aria",
		Type:        domain.PageTypeNote,
		Frontmatter: "title: Aria\n",
		Body:        "A lockpicker.\n",
		ContentHash: "hash-of-aria",
	}, store.AsDM(campaign.ID))
	if err != nil {
		t.Fatalf("UpsertPage: %v", err)
	}
	if bindErr := r.SetCharacters(ctx, issued.Principal, []string{page.ID}); bindErr != nil {
		t.Fatalf("SetCharacters: %v", bindErr)
	}

	// Immediately, with no waiting and no clock moving: the next request is the
	// next request.
	if _, authErr := r.Authenticate(ctx, session.SessionID); authErr == nil {
		t.Fatal("a session created before the change still authenticates")
	}

	// And a fresh session reflects the new bindings immediately, which is the
	// other half of "takes effect on the next request".
	fresh, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem after the change: %v", err)
	}
	if _, err := r.Authenticate(ctx, fresh.SessionID); err != nil {
		t.Errorf("a session created after the change does not authenticate: %v", err)
	}

	// A fortnight is a long time to be reading somebody else's character page, so
	// the window is not something the test needs to move a clock for.
	if lifetime := auth.DefaultSessionLifetime; lifetime < time.Hour {
		t.Errorf("the default session lifetime is %v, which is not long enough to make "+
			"rotation necessary", lifetime)
	}
}
