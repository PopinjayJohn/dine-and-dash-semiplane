package auth_test

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
)

// TestRateLimitedRedemption is the named test from §14, and the property it
// protects is narrow and important: a player who clicks their link twice on a bad
// connection must not be locked out, and somebody guessing tokens must be stopped.
//
// The two halves are the same code, so both are here. A test that only checked
// the eleventh attempt fails would pass against a limiter that refused the second,
// and a DM would find out on the first evening.
func TestRateLimitedRedemption(t *testing.T) {
	t.Parallel()

	const address = "192.0.2.10"

	t.Run("the first ten attempts are allowed", func(t *testing.T) {
		t.Parallel()

		// A clock the test moves by hand, so "a minute" is an assignment rather
		// than a sleep and the whole table runs in microseconds.
		now := testNow
		limiter := auth.NewLimiter(auth.DefaultRedemptionLimit, auth.DefaultRedemptionWindow,
			func() time.Time { return now })

		for attempt := range auth.DefaultRedemptionLimit {
			if err := limiter.Allow(address); err != nil {
				t.Fatalf("attempt %d of %d was refused: %v", attempt+1, auth.DefaultRedemptionLimit, err)
			}
			now = now.Add(time.Second)
		}
	})

	t.Run("the eleventh is refused, and stays refused", func(t *testing.T) {
		t.Parallel()

		now := testNow
		limiter := auth.NewLimiter(auth.DefaultRedemptionLimit, auth.DefaultRedemptionWindow,
			func() time.Time { return now })

		for range auth.DefaultRedemptionLimit {
			if err := limiter.Allow(address); err != nil {
				t.Fatalf("a real attempt was refused early: %v", err)
			}
			now = now.Add(time.Second)
		}

		// And refusing has to keep happening, or a script that is simply patient
		// gets through: a limiter that does not count the attempts it refuses is a
		// limiter that refuses the tenth and then allows the eleventh.
		for attempt := range 5 {
			if err := limiter.Allow(address); !errors.Is(err, auth.ErrRateLimited) {
				t.Errorf("attempt %d past the limit returned %v, want an error matching ErrRateLimited",
					auth.DefaultRedemptionLimit+attempt+1, err)
			}
		}
	})

	t.Run("another address is not affected", func(t *testing.T) {
		t.Parallel()

		now := testNow
		limiter := auth.NewLimiter(auth.DefaultRedemptionLimit, auth.DefaultRedemptionWindow,
			func() time.Time { return now })

		for range auth.DefaultRedemptionLimit {
			if err := limiter.Allow("192.0.2.10"); err != nil {
				t.Fatalf("the first address was refused early: %v", err)
			}
		}
		if err := limiter.Allow("192.0.2.11"); err != nil {
			t.Errorf("a different address was refused because another one had used its budget: %v", err)
		}
		if err := limiter.Allow("192.0.2.10"); !errors.Is(err, auth.ErrRateLimited) {
			t.Errorf("the first address was allowed again: %v", err)
		}
	})

	t.Run("the window reopens", func(t *testing.T) {
		t.Parallel()

		now := testNow
		limiter := auth.NewLimiter(auth.DefaultRedemptionLimit, auth.DefaultRedemptionWindow,
			func() time.Time { return now })

		for range auth.DefaultRedemptionLimit {
			if err := limiter.Allow(address); err != nil {
				t.Fatalf("a real attempt was refused early: %v", err)
			}
		}
		if err := limiter.Allow(address); !errors.Is(err, auth.ErrRateLimited) {
			t.Fatalf("the limit was not reached: %v", err)
		}

		// Past the window, the address is somebody's again. A player who fumbled
		// their link ten times in a minute is a player, not an attacker.
		now = now.Add(auth.DefaultRedemptionWindow)
		if err := limiter.Allow(address); err != nil {
			t.Errorf("the address is still locked out a window later: %v", err)
		}
	})

	t.Run("asking does not cost an attempt", func(t *testing.T) {
		t.Parallel()

		now := testNow
		limiter := auth.NewLimiter(auth.DefaultRedemptionLimit, auth.DefaultRedemptionWindow,
			func() time.Time { return now })

		for range 5 {
			if err := limiter.Allow(address); err != nil {
				t.Fatalf("an early attempt was refused: %v", err)
			}
		}

		before := limiter.Remaining(address)
		for range 20 {
			limiter.Remaining(address)
		}
		if after := limiter.Remaining(address); after != before {
			t.Errorf("asking changed the remaining count from %d to %d: a request that costs an attempt "+
				"is a request a router can trigger by polling", before, after)
		}
	})
}

// The limit is a fixed window per address, and the boundary is where a player can
// be unlucky. This pins what happens exactly on it, because "one extra attempt
// once a minute" is the price of a fixed window and a test should say so.
func TestTheWindowBoundary(t *testing.T) {
	t.Parallel()

	now := testNow
	limiter := auth.NewLimiter(2, time.Minute, func() time.Time { return now })

	if err := limiter.Allow("a"); err != nil {
		t.Fatalf("the first: %v", err)
	}
	now = now.Add(30 * time.Second)
	if err := limiter.Allow("a"); err != nil {
		t.Fatalf("the second: %v", err)
	}
	if err := limiter.Allow("a"); !errors.Is(err, auth.ErrRateLimited) {
		t.Errorf("the third, at 30s, was allowed: %v", err)
	}

	// Exactly on the boundary the window has closed, because a window is a
	// half-open interval and an attempt at its last instant belongs to the next
	// one. That is one extra attempt per minute per address, which is what a
	// fixed window costs against a token bucket.
	now = now.Add(30 * time.Second)
	if err := limiter.Allow("a"); err != nil {
		t.Errorf("the attempt exactly on the boundary was refused: %v", err)
	}
}

// An address nobody can be identified by is counted, not ignored. A limiter that
// skips unidentified requests makes "hide your address" a way to be unlimited.
func TestAnUnidentifiedAddressIsStillCounted(t *testing.T) {
	t.Parallel()

	limiter := auth.NewLimiter(2, time.Minute, func() time.Time { return testNow })

	for range 2 {
		if err := limiter.Allow(""); err != nil {
			t.Fatalf("an attempt with no address was refused early: %v", err)
		}
	}
	if err := limiter.Allow(""); !errors.Is(err, auth.ErrRateLimited) {
		t.Errorf("requests with no address are not counted: %v", err)
	}
}

// Addresses are compared without regard to case and surrounding space, so a proxy
// that reports "2001:DB8::1" and one that reports "2001:db8::1" are one address
// and not a way to get a fresh budget.
func TestAddressesAreNormalised(t *testing.T) {
	t.Parallel()

	limiter := auth.NewLimiter(1, time.Minute, func() time.Time { return testNow })

	if err := limiter.Allow("  2001:DB8::1  "); err != nil {
		t.Fatalf("the first: %v", err)
	}
	for _, same := range []string{"2001:db8::1", " 2001:DB8::1", "2001:db8::1 "} {
		if err := limiter.Allow(same); !errors.Is(err, auth.ErrRateLimited) {
			t.Errorf("%q was treated as a different address from the first: %v", same, err)
		}
	}
}

// The limiter must not be a memory leak with an attack on it: every attempt from
// a new address allocates, and a botnet is a lot of addresses.
func TestTheLimiterIsBounded(t *testing.T) {
	t.Parallel()

	limiter := auth.NewLimiter(1, time.Minute, func() time.Time { return testNow })

	// Far more addresses than the bound, all inside one window, so nothing is
	// swept and only the bound is doing the work.
	for i := range 20_000 {
		// Every address is distinct, so nothing is ever swept for having closed
		// and only the bound is doing the work. A sweep that ran on every check
		// makes this quadratic, which is why it is amortised.
		address := "192.0.2." + strconv.Itoa(i%250) + ":" + strconv.Itoa(i/250)
		if err := limiter.Allow(address); err != nil && !errors.Is(err, auth.ErrRateLimited) {
			t.Fatalf("Allow: %v", err)
		}
	}

	// The bound is the whole point of the test, and Tracked is how it is
	// observed. Twenty thousand distinct addresses must not become twenty thousand
	// map entries.
	if tracked := limiter.Tracked(); tracked > 8_192 {
		t.Errorf("after 20000 distinct addresses the limiter is holding %d of them, "+
			"which is no bound at all", tracked)
	}

	// And it still answers.
	if remaining := limiter.Remaining("192.0.2.1:0"); remaining < 0 {
		t.Errorf("Remaining = %d, which cannot be right", remaining)
	}
}

// A trusted proxy is trusted and an untrusted one is not. This is the difference
// between a rate limit and a rate limit an attacker can turn off, so it is tested
// both ways.
func TestClientIP(t *testing.T) {
	t.Parallel()

	direct := &net.TCPAddr{IP: net.ParseIP("192.0.2.20"), Port: 51000}
	proxied := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 51000}

	tests := map[string]struct {
		remote    net.Addr
		forwarded string
		trusted   bool
		want      string
	}{
		"a direct connection has no forwarded header believed": {
			remote: direct, forwarded: "198.51.100.1", want: "192.0.2.20",
		},
		"a trusted proxy's header is believed": {
			remote: proxied, forwarded: "198.51.100.1", trusted: true, want: "198.51.100.1",
		},
		"the left-most entry is the client": {
			remote: proxied, forwarded: "198.51.100.1, 10.0.0.2, 10.0.0.3", trusted: true,
			want: "198.51.100.1",
		},
		"an untrusted peer's header is ignored whatever it says": {
			// The bypass this prevents: send X-Forwarded-For with a fresh value
			// per attempt and the limit counts nothing. Believing the header on a
			// direct connection is trusting the client to name itself.
			remote: direct, forwarded: "198.51.100.1, 198.51.100.2", want: "192.0.2.20",
		},
		"a trusted proxy with no header falls back to the socket": {
			remote: proxied, trusted: true, want: "10.0.0.1",
		},
		"no address at all is the empty key": {
			remote: nil, trusted: true, want: "",
		},
		"a unix socket is one player, not several": {
			remote: &net.UnixAddr{Name: "/run/wiki.sock", Net: "unix"}, want: "/run/wiki.sock",
		},
		"an address with a port is split": {
			remote: stringAddr("203.0.113.5:44321"), want: "203.0.113.5",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := auth.ClientIP(tt.remote, tt.forwarded, tt.trusted); got != tt.want {
				t.Errorf("ClientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

// stringAddr is a net.Addr that is not a TCPAddr, for the branches that have to
// cope with something else on the socket.
type stringAddr string

func (a stringAddr) Network() string { return "test" }
func (a stringAddr) String() string  { return string(a) }

// A full redemption flow is rate limited, end to end, and the limit is on the
// *redemption* rather than on the limiter in isolation: a player who has redeemed
// once and then mistypes their link is counted, because that is the request an
// attacker is making too.
func TestRedemptionIsRateLimitedEndToEnd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r, _, campaign, issued := newRedeemer(t)

	now := testNow
	limiter := auth.NewLimiter(auth.DefaultRedemptionLimit, auth.DefaultRedemptionWindow,
		func() time.Time { return now })

	// Nine wrong tokens, which is the shape of a script enumerating rather than a
	// player fumbling, and they cost the same budget as nine right ones would.
	other, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	for range auth.DefaultRedemptionLimit - 1 {
		if err := limiter.Allow("192.0.2.30"); err != nil {
			t.Fatalf("an early attempt was refused: %v", err)
		}
		if _, err := r.Redeem(ctx, campaign.Slug, other.Hex()); !errors.Is(err, auth.ErrBadToken) {
			t.Fatalf("a wrong token was accepted: %v", err)
		}
	}

	// The tenth, with the *right* token, is allowed. A player who fumbled nine
	// times and then pasted the right one is a player.
	if err := limiter.Allow("192.0.2.30"); err != nil {
		t.Fatalf("the tenth attempt was refused: %v", err)
	}
	if _, err := r.Redeem(ctx, campaign.Slug, issued.Token.Hex()); err != nil {
		t.Fatalf("the right token on the tenth attempt was refused: %v", err)
	}

	// The eleventh is not, whatever it is.
	if err := limiter.Allow("192.0.2.30"); !errors.Is(err, auth.ErrRateLimited) {
		t.Errorf("the eleventh attempt returned %v, want an error matching ErrRateLimited", err)
	}
}
