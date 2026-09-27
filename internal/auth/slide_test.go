package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// A session that is used stays alive, and the point of sliding is that a player
// who plays every week never sees their link again.
//
// The clock here is a variable rather than a sleep, so "three weeks later" is an
// assignment and the whole table runs in microseconds.
func TestASessionSlides(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, s, campaign := newMinter(t)
	r := &auth.Redeemer{Backend: s, Config: m.Config}

	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	now := testNow
	r.Config.Now = func() time.Time { return now }

	redeemed, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// The redemption set it, and the default is thirty days.
	if want := testNow.Add(30 * 24 * time.Hour); !redeemed.ExpiresAt.Equal(want) {
		t.Errorf("the session expires at %v, want %v: the default is thirty days",
			redeemed.ExpiresAt, want)
	}
	if auth.DefaultSessionLifetime != 30*24*time.Hour {
		t.Errorf("DefaultSessionLifetime is %v, want 30 days", auth.DefaultSessionLifetime)
	}

	// Three weeks in, the session is still live and its expiry has moved.
	now = testNow.Add(21 * 24 * time.Hour)
	if _, err := r.Authenticate(ctx, redeemed.SessionID); err != nil {
		t.Fatalf("Authenticate three weeks in: %v", err)
	}
	assertSessionExpiry(t, s, redeemed.SessionID, now.Add(30*24*time.Hour))

	// Twenty days later, so 41 days after the redemption: past what a fixed
	// thirty days would have allowed, and still live. This is the case a fixed
	// window gets wrong and a sliding one does not.
	//
	// Twenty rather than twenty-one because the boundary is on purpose: a session
	// whose expiry is exactly now has expired, since `Session.Expired` is
	// `!now.Before(expires)`. Landing on the boundary would test the clock rather
	// than the slide, and there is a case below for the clock.
	now = testNow.Add(41 * 24 * time.Hour)
	if _, err := r.Authenticate(ctx, redeemed.SessionID); err != nil {
		t.Fatalf("Authenticate 41 days in: %v", err)
	}
	assertSessionExpiry(t, s, redeemed.SessionID, now.Add(30*24*time.Hour))

	// And the session is genuinely still live rather than the row having been
	// left behind: a session that reads as live because nothing checked it is not
	// a session.
	if _, err := r.Authenticate(ctx, redeemed.SessionID); err != nil {
		t.Errorf("the slid session does not authenticate on an immediate second request: %v", err)
	}
}

// The two ways to get sliding wrong, and both of them end a session that was
// working.

// A refused authentication must not slide anything, or a script guessing cookie
// ids keeps sessions alive by trying them.
func TestARefusedAuthenticationDoesNotSlide(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, s, campaign, issued := newRedeemer(t)

	redeemed, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	before := sessionExpiry(t, s, redeemed.SessionID)

	// A wrong cookie, an empty one, and one that names a session that never
	// existed. None of them is a session, so none of them slides one.
	for _, wrong := range []string{"", "no-such-session", issued.Token.Hex()} {
		if _, err := r.Authenticate(ctx, wrong); err == nil {
			t.Fatalf("Authenticate(%q) succeeded", wrong)
		}
	}

	if after := sessionExpiry(t, s, redeemed.SessionID); !after.Equal(before) {
		t.Errorf("a refused authentication moved the session's expiry from %v to %v",
			before, after)
	}

	// A revoked principal's session does not slide either, and it is the one that
	// matters: a revocation that is silently undone by the next request would not
	// be a revocation.
	if err := s.RevokePrincipal(ctx, issued.Principal.ID); err != nil {
		t.Fatalf("RevokePrincipal: %v", err)
	}
	if _, err := r.Authenticate(ctx, redeemed.SessionID); err == nil {
		t.Fatal("a session authenticated after a revoke")
	}
}

// A clock that goes backwards must not shorten a session that was working. A
// laptop whose battery died, a machine that had the wrong time, a player who
// changed their timezone and had a bad afternoon — all of them produce a `now`
// earlier than the expiry, and the failure is a player logged out mid-session with
// no cause.
func TestASessionIsNotShortenedByAClockThatWentBackwards(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, s, campaign := newMinter(t)
	r := &auth.Redeemer{Backend: s, Config: m.Config}

	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	now := testNow.Add(10 * 24 * time.Hour)
	r.Config.Now = func() time.Time { return now }

	redeemed, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	live := sessionExpiry(t, s, redeemed.SessionID)

	// The clock jumps back a year.
	now = testNow.Add(-365 * 24 * time.Hour)
	if _, err := r.Authenticate(ctx, redeemed.SessionID); err != nil {
		t.Fatalf("a session stopped working because the clock went backwards: %v", err)
	}

	if after := sessionExpiry(t, s, redeemed.SessionID); !after.Equal(live) {
		t.Errorf("the session's expiry moved from %v to %v because the clock went backwards",
			live, after)
	}
}

// The lifetime is a configuration setting, because the right answer depends on
// the campaign. A DM who plays once a year wants a week; one who plays weekly wants
// a month. Both should be able to say so.
func TestTheSessionLifetimeIsConfigurable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, s, campaign := newMinter(t)

	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// A week, a year, and no setting at all.
	for _, lifetime := range []time.Duration{7 * 24 * time.Hour, 365 * 24 * time.Hour, 0} {
		r := &auth.Redeemer{Backend: s, Config: auth.Config{
			Now:             func() time.Time { return testNow },
			SessionLifetime: lifetime,
		}}

		redeemed, redeemErr := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
		if redeemErr != nil {
			t.Fatalf("Redeem with a lifetime of %v: %v", lifetime, redeemErr)
		}

		want := lifetime
		if want == 0 {
			want = auth.DefaultSessionLifetime
		}
		if expected := testNow.Add(want); !redeemed.ExpiresAt.Equal(expected) {
			t.Errorf("with a lifetime of %v the session expires at %v, want %v",
				lifetime, redeemed.ExpiresAt, expected)
		}
	}
}

// A DM who wants their players logged out periodically still can: the lifetime is
// a number, and a number that is shorter than the gap between sessions does
// exactly that. Nothing here is a special mode.
func TestAShortLifetimeLogsAPlayerOut(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m, s, campaign := newMinter(t)
	r := &auth.Redeemer{
		Backend: s,
		Config:  auth.Config{Now: func() time.Time { return testNow }, SessionLifetime: 7 * 24 * time.Hour},
	}

	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	redeemed, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	// Six days: still in, and slid.
	soon := testNow.Add(6 * 24 * time.Hour)
	r.Config.Now = func() time.Time { return soon }
	if _, err := r.Authenticate(ctx, redeemed.SessionID); err != nil {
		t.Fatalf("a session died before its lifetime: %v", err)
	}

	// Six weeks: gone, and ended rather than merely refused.
	muchLater := testNow.Add(42 * 24 * time.Hour)
	r.Config.Now = func() time.Time { return muchLater }
	if _, err := r.Authenticate(ctx, redeemed.SessionID); err == nil {
		t.Error("a session outlived a week of sliding by six weeks")
	}
	if _, found, err := s.SessionByID(ctx, redeemed.SessionID); err != nil {
		t.Fatalf("SessionByID: %v", err)
	} else if found {
		t.Error("the lapsed session is still in the table")
	}
}

// sessionExpiry reads a session's expiry, for the assertions above.
func sessionExpiry(t *testing.T, s *store.Store, id string) time.Time {
	t.Helper()

	sess, found, err := s.SessionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("SessionByID(%q): %v", id, err)
	}
	if !found {
		t.Fatalf("session %q is not in the table", id)
	}
	return sess.ExpiresAt
}

// assertSessionExpiry is sessionExpiry with a message, for the cases that read as
// steps in a story rather than as one state.
func assertSessionExpiry(t *testing.T, s *store.Store, id string, want time.Time) {
	t.Helper()

	if got := sessionExpiry(t, s, id); !got.Equal(want) {
		t.Errorf("the session's expiry is %v, want %v", got, want)
	}
}
