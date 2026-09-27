package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// Redeeming a link is the one request in this program that carries a long-lived
// credential, and the tests below are about the five things that can go wrong with
// it: accepting a link that is not ours, accepting one for the wrong campaign,
// accepting a revoked or expired one, leaving the token in the URL afterwards, and
// handing out a session that outlives the link it came from.

func newRedeemer(t *testing.T) (*auth.Redeemer, *store.Store, domain.Campaign, auth.Issued) {
	t.Helper()

	m, s, campaign := newMinter(t)
	issued, err := m.Issue(context.Background(), campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	return &auth.Redeemer{Backend: s, Config: m.Config}, s, campaign, issued
}

// A good link exchanges for a session, and the session is the identity.
func TestRedeem(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _, campaign, issued := newRedeemer(t)

	got, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	if got.SessionID == "" {
		t.Error("the redemption returned no session id")
	}
	if got.Principal.ID != issued.Principal.ID {
		t.Errorf("the redemption identified %q, want %q", got.Principal.ID, issued.Principal.ID)
	}
	if got.Principal.Role != domain.RolePlayer {
		t.Errorf("the role came back as %q, want %q", got.Principal.Role, domain.RolePlayer)
	}

	// The session expires, and by the default fortnight. A player who has not
	// played in a month clicks their link again and it works; a session that
	// outlived the evening it was issued would be a credential, not a cookie.
	if want := testNow.Add(auth.DefaultSessionLifetime); !got.ExpiresAt.Equal(want) {
		t.Errorf("the session expires at %v, want %v", got.ExpiresAt, want)
	}

	// And the cookie is a live session for the right principal.
	principal, err := r.Authenticate(ctx, got.SessionID)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.ID != issued.Principal.ID {
		t.Errorf("the session identifies %q, want %q", principal.ID, issued.Principal.ID)
	}
}

// The token leaves the URL. This is the reason the whole package exists, and it is
// asserted on every field of the answer rather than on the redirect alone.
func TestTheTokenLeavesTheURL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _, campaign, issued := newRedeemer(t)
	token := issued.Token.Hex()

	got, err := r.Redeem(ctx, campaign.Slug, token)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	if strings.Contains(got.RedirectTo, token) {
		t.Errorf("the redirect %q contains the token", got.RedirectTo)
	}
	if got.RedirectTo != "/c/blackwater/" {
		t.Errorf("the redirect is %q, want /c/blackwater/", got.RedirectTo)
	}

	// Nothing else in the answer carries it either, because "we did not put it in
	// the redirect" is a much weaker claim than "it is not in the answer".
	if strings.Contains(got.SessionID, token) {
		t.Error("the session id contains the token")
	}
	if strings.Contains(got.Principal.TokenHash, token) {
		t.Error("the principal's hash contains the token")
	}
	if strings.Contains(got.Principal.Label, token) {
		t.Error("the principal's label contains the token")
	}

	// And the link the DM was given does, because that is its job, so a test that
	// says "the token is nowhere" would be wrong.
	if !strings.Contains(issued.URL, token) {
		t.Error("the issued link does not contain the token, so it could not be used")
	}

	// The token appears in exactly one request line: the redemption. The session
	// that replaced it authenticates on its own.
	if _, err := r.Authenticate(ctx, got.SessionID); err != nil {
		t.Errorf("the session does not authenticate without the token: %v", err)
	}
}

// Every way a token can be wrong, and what each one is called. The error is part of
// the interface: a player whose link was revoked needs to be told that, and a DM
// whose link landed in the wrong campaign needs to be told that, and a message
// that says "not valid" for both tells neither.
func TestRedeemRefuses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _, campaign, issued := newRedeemer(t)

	t.Run("no token at all", func(t *testing.T) {
		if _, err := r.Redeem(ctx, campaign.Slug, ""); !errors.Is(err, auth.ErrNoToken) {
			t.Errorf("Redeem with no token returned %v, want an error matching ErrNoToken", err)
		}
	})

	t.Run("a token of the wrong length", func(t *testing.T) {
		// A two-character token does no database work at all: it is refused by
		// shape, before the store is touched.
		for _, token := range []string{"a1b2", strings.Repeat("z", 63), strings.Repeat("z", 65)} {
			if _, err := r.Redeem(ctx, campaign.Slug, token); !errors.Is(err, auth.ErrBadToken) {
				t.Errorf("Redeem with a %d-character token returned %v, want an error matching ErrBadToken",
					len(token), err)
			}
		}
	})

	t.Run("a token that is not hex", func(t *testing.T) {
		if _, err := r.Redeem(ctx, campaign.Slug, strings.Repeat("z", 64)); !errors.Is(err, auth.ErrBadToken) {
			t.Errorf("Redeem with a non-hex token returned %v, want an error matching ErrBadToken", err)
		}
	})

	t.Run("a real token that is not ours", func(t *testing.T) {
		other, err := auth.NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if _, err := r.Redeem(ctx, campaign.Slug, other.Hex()); !errors.Is(err, auth.ErrBadToken) {
			t.Errorf("Redeem with somebody else's token returned %v, want an error matching ErrBadToken", err)
		}
	})

	t.Run("one character different", func(t *testing.T) {
		// Not a prefix, not a truncation: a token either matches its hash or it is
		// not the link, because nothing compares it to a stored value.
		token := issued.Token.Hex()
		for _, mutated := range []string{
			"z" + token[1:],
			token[:len(token)-1] + "z",
			token[:32] + token[:32],
		} {
			if mutated == token {
				t.Fatal("the mutation did not change the token")
			}
			if _, err := r.Redeem(ctx, campaign.Slug, mutated); !errors.Is(err, auth.ErrBadToken) {
				t.Errorf("Redeem with a mutated token returned %v, want an error matching ErrBadToken", err)
			}
		}
	})

	t.Run("a real link for another campaign", func(t *testing.T) {
		// A DM who pastes a link into the wrong campaign gets told so, because that
		// is a mistake somebody can fix.
		if _, err := r.Redeem(ctx, "thornford", issued.Token.Hex()); !errors.Is(err, auth.ErrWrongCampaign) {
			t.Errorf("Redeem into the wrong campaign returned %v, want an error matching ErrWrongCampaign", err)
		}
	})

	t.Run("a revoked link", func(t *testing.T) {
		freshR, freshStore, campaign2, fresh := freshRedeemer(t)
		if err := freshStore.RevokePrincipal(context.Background(), fresh.Principal.ID); err != nil {
			t.Fatalf("RevokePrincipal: %v", err)
		}
		if _, err := freshR.Redeem(context.Background(), campaign2.Slug, fresh.Token.Hex()); !errors.Is(err, auth.ErrRevoked) {
			t.Errorf("Redeem with a revoked link returned %v, want an error matching ErrRevoked", err)
		}
	})
}

// A link with an expiry stops working on the day it says it does, and not the day
// before.
func TestRedeemRefusesAnExpiredLink(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, s, campaign := newMinter(t)
	m.Config.LinkExpiry = 7 * 24 * time.Hour

	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	later := &auth.Redeemer{
		Backend: s,
		Config: auth.Config{
			Now:     func() time.Time { return testNow.Add(8 * 24 * time.Hour) },
			BaseURL: "https://wiki.example",
		},
	}

	if _, err := later.Redeem(ctx, campaign.Slug, issued.Token.Hex()); !errors.Is(err, auth.ErrLinkExpired) {
		t.Errorf("Redeem after the expiry returned %v, want an error matching ErrLinkExpired", err)
	}
}

// Revocation is immediate: the browser holding the cookie stops working on its next
// request, which is the property that makes a row better than a signed blob.
func TestRevokingALinkStopsTheBrowserOnItsNextRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	got, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if _, err := r.Authenticate(ctx, got.SessionID); err != nil {
		t.Fatalf("the session does not authenticate before the revoke: %v", err)
	}

	if err := s.RevokePrincipal(ctx, issued.Principal.ID); err != nil {
		t.Fatalf("RevokePrincipal: %v", err)
	}

	// The cookie is gone, not merely flagged: a session that outlived the
	// revocation is the bug this milestone exists to not have.
	if _, err := r.Authenticate(ctx, got.SessionID); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("the session still authenticates after a revoke: %v", err)
	}

	// And the link itself.
	if _, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex()); !errors.Is(err, auth.ErrRevoked) {
		t.Errorf("the link still redeems after a revoke: %v", err)
	}
}

// A lapsed session is ended, not merely refused, so it stops being found.
func TestALapsedSessionIsEnded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	got, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// A redeemer whose clock is past the session's expiry, with a short lifetime
	// already stamped, so the second Authenticate sees a session that has lapsed.
	later := &auth.Redeemer{
		Backend: s,
		Config: auth.Config{
			Now:     func() time.Time { return testNow.Add(auth.DefaultSessionLifetime) },
			BaseURL: "https://wiki.example",
		},
	}

	if _, err := later.Authenticate(ctx, got.SessionID); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("a lapsed session authenticated: %v", err)
	}

	// And the row is gone, so a third attempt says the same thing rather than
	// failing on a row that is still there.
	if _, found, err := s.SessionByID(ctx, got.SessionID); err != nil {
		t.Fatalf("SessionByID: %v", err)
	} else if found {
		t.Error("the lapsed session is still in the table: it will be found and refused on every request for ever")
	}
}

// A cookie that is not a session is not a session, and asking with nothing is not
// an error worth logging.
func TestAuthenticateRefuses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _, _, _ := newRedeemer(t)

	tests := map[string]struct {
		sessionID string
	}{
		"nothing":                      {sessionID: ""},
		"a session that never existed": {sessionID: "no-such-session"},
		"something else entirely":      {sessionID: strings.Repeat("z", 36)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := r.Authenticate(ctx, tt.sessionID); !errors.Is(err, auth.ErrNoSession) {
				t.Errorf("Authenticate(%q) returned %v, want an error matching ErrNoSession", tt.sessionID, err)
			}
		})
	}
}

// A logout is idempotent, because a browser may send it twice and a back button
// is not a problem to report.
func TestEndSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _, campaign, issued := newRedeemer(t)

	got, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	for range 2 {
		if err := r.EndSession(ctx, got.SessionID); err != nil {
			t.Errorf("EndSession: %v", err)
		}
	}
	if err := r.EndSession(ctx, ""); err != nil {
		t.Errorf("EndSession with nothing: %v", err)
	}
	if _, err := r.Authenticate(ctx, got.SessionID); !errors.Is(err, auth.ErrNoSession) {
		t.Errorf("the session still authenticates after a logout: %v", err)
	}
}

// Every redemption is recorded, with the principal and the time, because that row
// is the answer to "was my link pasted somewhere it should not have been".
func TestRedeemIsRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	if _, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex()); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	entries, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	var used int
	for _, entry := range entries {
		if entry.Action != domain.AuditShareLinkUsed {
			continue
		}
		used++
		if entry.PrincipalID != issued.Principal.ID {
			t.Errorf("the redemption names the principal %q, want %q", entry.PrincipalID, issued.Principal.ID)
		}
		if entry.CampaignID != campaign.ID {
			t.Errorf("the redemption names the campaign %q, want %q", entry.CampaignID, campaign.ID)
		}
		if entry.At.IsZero() {
			t.Error("the redemption has no timestamp, so it cannot be correlated with anything")
		}
	}
	if used != 1 {
		t.Errorf("%d redemptions were recorded, want 1", used)
	}

	// And the issuance is still there, so the log reads as a story: a link was
	// issued, then it was used.
	var issued1 bool
	for _, entry := range entries {
		if entry.Action == domain.AuditPrincipalIssued {
			issued1 = true
		}
	}
	if !issued1 {
		t.Error("the issuance is not in the log, so a leaked link cannot be traced to one")
	}
}

// A refused redemption records nothing. A log that said "this link was used" for a
// token that was not is worse than no log, because it sends a DM looking at the
// wrong player.
func TestARefusedRedemptionIsNotRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	before, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}

	other, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if _, foreignErr := r.Redeem(ctx, campaign.Slug, other.Hex()); foreignErr == nil {
		t.Fatal("Redeem accepted a token that is not ours")
	}
	if _, wrongCampaignErr := r.Redeem(ctx, "thornford", issued.Token.Hex()); wrongCampaignErr == nil {
		t.Fatal("Redeem accepted a link for the wrong campaign")
	}

	after, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the log grew from %d to %d rows on a refused redemption", len(before), len(after))
	}
}

// freshRedeemer is a redeemer with a link of its own, for the subtests that revoke
// or expire one and must not disturb the fixture the others share.
func freshRedeemer(t *testing.T) (*auth.Redeemer, *store.Store, domain.Campaign, auth.Issued) {
	t.Helper()

	m, s, campaign := newMinter(t)
	issued, err := m.Issue(context.Background(), campaign, domain.RolePlayer, "Bob")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return &auth.Redeemer{Backend: s, Config: m.Config}, s, campaign, issued
}
