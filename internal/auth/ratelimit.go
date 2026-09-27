package auth

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// ErrRateLimited says too many redemptions came from one address.
//
// It is a named error so a router can answer 429 rather than 500, and because the
// caller — a player who clicked twice on a slow connection, or an attacker
// enumerating tokens — is not the same and wants different things.
var ErrRateLimited = errors.New("auth: too many share-link redemptions from this address")

// Redemption limits, and the reasoning.
//
// Ten a minute is what the specification asks for and it is about right for a
// campaign at a table: a player redeems once, a DM redeems twice when they test a
// link they just minted, and everything after that is somebody guessing. A
// legitimate player never reaches it, which is the property to protect — a limit
// that a real player hits is a limit that has locked somebody out of their
// campaign, and the failure they see is "your link does not work" with no cause.
const (
	// DefaultRedemptionLimit is how many redemptions one address may attempt.
	DefaultRedemptionLimit = 10

	// DefaultRedemptionWindow is the period the limit counts over.
	//
	// A window rather than a token bucket, because the bucket's burst is exactly
	// the thing that lets a script do ten attempts and then another ten in the
	// same instant. A fixed window is slightly unfair at the boundary — a player
	// who attempts at 59s and again at 61s has used two in two seconds — and the
	// unfairness is one extra attempt once a minute, which costs an attacker
	// nothing and costs a player nothing.
	DefaultRedemptionWindow = time.Minute

	// maxTrackedAddresses bounds the memory the limiter uses.
	//
	// A limiter keyed on a client address that has never been swept is a memory
	// leak with an attack on it: every attempt from a new address is a new entry,
	// and an attacker with a botnet gets a free allocation each. The oldest
	// addresses are dropped past the bound, which means the limit is approximate
	// for a very large number of addresses, and that is the right trade: a
	// campaign is one DM, one machine and a handful of players, and the addresses
	// that matter are all in the first few dozen.
	maxTrackedAddresses = 4096
)

// Limiter refuses too many redemption attempts from one address.
//
// It is a fixed window per address, held in memory, and it is **not** a
// distributed limiter and does not pretend to be one. A DM runs one binary on one
// machine; there is no second process to share a counter with, and a store-backed
// one would be a table that grows with every address somebody tries. If this
// application is ever run as several processes, the limit becomes per-process and
// the fix is here — which is written down so the day somebody runs two, they find
// this sentence rather than a limit that is quietly three times what it says.
type Limiter struct {
	mu sync.Mutex

	// limit and window are the two knobs, read once per check.
	limit  int
	window time.Duration

	// attempts is address to the window's start and count. A window is identified
	// by its start time, so the map holds one entry per address per active window
	// and the sweep below is what keeps that from being per-address for ever.
	attempts map[string]*windowCount

	// sinceSweep counts calls since the last closed-window sweep, so the sweep is
	// amortised rather than run on every check. See sweep.
	sinceSweep int

	// now is the clock. Injected so the tests do not sleep.
	now func() time.Time
}

type windowCount struct {
	// start is when this window opened. Zero means "no window", which is the same
	// answer as "a window with nothing in it".
	start time.Time
	count int
}

// NewLimiter is a limiter with the given limit over the given window.
//
// A limit of zero or less means the default, because a limiter that refuses
// everything is a denial of service on the DM's own players, and a limit that
// refuses nothing is the thing it exists to prevent. Neither is what "unset"
// should mean, so unset is the default.
func NewLimiter(limit int, window time.Duration, now func() time.Time) *Limiter {
	if limit <= 0 {
		limit = DefaultRedemptionLimit
	}
	if window <= 0 {
		window = DefaultRedemptionWindow
	}
	if now == nil {
		now = time.Now
	}
	return &Limiter{
		limit:    limit,
		window:   window,
		attempts: map[string]*windowCount{},
		now:      now,
	}
}

// Allow records an attempt from an address and reports whether it is within the
// limit.
//
// The attempt is recorded whether it is allowed or not: a limiter that does not
// count the requests it refuses is a limiter that refuses the tenth and then
// allows the eleventh, which is a limit of one.
//
// The address is a string rather than a net.Addr because this package does not
// parse headers — M8 does, and it hands over whatever it concluded. That is
// deliberate: "what is a client address" is an HTTP question, and answering it
// here would mean this package trusted a header it has no business knowing the
// shape of.
func (l *Limiter) Allow(address string) error {
	address = normaliseAddress(address)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweep(now)

	entry, tracked := l.attempts[address]
	if !tracked || !now.Before(entry.start.Add(l.window)) {
		// A new window. The count restarts rather than carrying over, which is
		// what "fixed window" means and the reason the boundary is one attempt
		// unfair rather than two.
		l.attempts[address] = &windowCount{start: now, count: 1}
		return nil
	}

	if entry.count >= l.limit {
		return ErrRateLimited
	}
	entry.count++
	return nil
}

// Remaining is how many attempts an address has left in its current window.
//
// It exists for the `X-RateLimit-Remaining` header and for the tests. It is
// approximate for an address that is not tracked, which is the same approximation
// the memory bound makes, and it does not open a window: asking must not be the
// thing that costs you an attempt.
func (l *Limiter) Remaining(address string) int {
	address = normaliseAddress(address)
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, tracked := l.attempts[address]
	if !tracked || !now.Before(entry.start.Add(l.window)) {
		return l.limit
	}

	return max(0, l.limit-entry.count)
}

// Reset forgets one address, which is what a DM's "unlock this player" button
// would call. It is not on any path a request takes.
func (l *Limiter) Reset(address string) {
	address = normaliseAddress(address)

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, address)
}

// sweepEvery is how many checks pass between closed-window sweeps.
//
// The sweep is O(n) over a map bounded at 4096, and running it on *every* check
// makes every check O(n) — which a burst of one-window addresses turns into the
// quadratic case the bound exists to prevent. Amortising it is safe because a
// stale closed window is not a correctness problem: `Allow` treats a window that
// has closed as a new one and overwrites the entry, so an unswept entry costs one
// map slot and nothing else. The bound itself is enforced exactly, on every call,
// because that is a hard limit rather than housekeeping.
const sweepEvery = 64

// sweep drops the windows that have closed, and enforces the memory bound.
//
// The bound is exact and every call. The closed-window sweep is amortised, and
// there is deliberately no background goroutine: one would need a stop channel, a
// WaitGroup and a test for both, in a program whose selling point is that it runs
// on somebody's laptop with no goroutines of its own.
func (l *Limiter) sweep(now time.Time) {
	if l.sinceSweep++; l.sinceSweep >= sweepEvery {
		l.sinceSweep = 0
		for address, entry := range l.attempts {
			if !now.Before(entry.start.Add(l.window)) {
				delete(l.attempts, address)
			}
		}
	}

	// Over the bound, the *oldest* window goes. Oldest is the one most likely to
	// have closed already, so the amortised sweep above usually empties the space
	// first and this is the backstop for a burst that all arrived in one window.
	//
	// Which means an address can lose its count to eviction and start again. That
	// is the documented price of the bound: past a few thousand addresses in one
	// minute the limit is approximate, and a campaign is one DM, one machine and a
	// handful of players, so the addresses that matter are all in the first few
	// dozen and none of them is ever the one evicted.
	for len(l.attempts) > maxTrackedAddresses {
		var (
			oldest    string
			oldestAt  time.Time
			foundSome bool
		)
		for address, entry := range l.attempts {
			if !foundSome || entry.start.Before(oldestAt) {
				oldest, oldestAt, foundSome = address, entry.start, true
			}
		}
		if !foundSome {
			return
		}
		delete(l.attempts, oldest)
	}
}

// Tracked is how many addresses are being counted.
//
// It exists so the memory bound is a testable claim rather than a comment: a
// bound that cannot be observed is a bound nobody notices being removed. It is
// not on any request path.
func (l *Limiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.attempts)
}

// normaliseAddress is the key the limiter counts on.
//
// The empty string is a real key and not "no key": a request that arrives without
// a client address — a unix socket, a misconfigured proxy, a test — is counted
// together with every other such request. That is the right answer. An attacker
// who can make the address unidentifiable has already defeated any
// per-address limit, and the alternative — not counting those requests at all —
// makes "hide your address" a way to be unlimited.
func normaliseAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}

// ClientIPFromRequest is [ClientIP] for the `http.Request` an HTTP router has.
//
// It exists because every framework hands the router a `RemoteAddr` *string* and
// `ClientIP` takes a `net.Addr`, so the conversion from `"10.0.0.1:54321"` to
// something addressable is a step every caller has to invent. Inventing it in three
// places is how one of them ends up believing the port as part of the address — which
// is a different limit key per connection, and therefore no limit at all.
//
// It is a function in `auth` rather than in `http` because the decision it makes is
// the one [ClientIP]'s doc comment argues for, and a caller that has to write the
// parsing to get at the decision is a caller that gets the decision wrong.
func ClientIPFromRequest(r *http.Request, trustedProxy bool) string {
	if r == nil {
		return ""
	}
	return ClientIP(remoteAddrOf(r.RemoteAddr), r.Header.Get("X-Forwarded-For"), trustedProxy)
}

// remoteAddrOf is a `RemoteAddr` string as a `net.Addr`.
//
// A string that is not `host:port` is handed back as an opaque address rather than
// refused, because a router's `RemoteAddr` for a unix socket is a *path* and there is
// nothing to split it into. `ClientIP` already handles a non-TCP address by taking its
// `String()`; this keeps that path in one place.
func remoteAddrOf(remote string) net.Addr {
	if remote == "" {
		return nil
	}
	// `net.LookupPort` is avoided deliberately: it takes a *service name* and can
	// consult the system resolver, which is a network call on the path of every
	// redemption. `netip.ParseAddrPort` does the same job in one parse and cannot
	// block, and this function's whole purpose is to be cheap enough to call on
	// every attempt.
	addressPort, err := netip.ParseAddrPort(remote)
	if err != nil {
		// A `RemoteAddr` that is not `host:port`, which a unix socket's path and a
		// custom listener may both be. There is no host to take out of it.
		return opaqueAddr(remote)
	}
	return &net.TCPAddr{IP: addressPort.Addr().AsSlice(), Port: int(addressPort.Port())}
}

// opaqueAddr is an address with no parts this package can see, which is what a unix
// socket path and a malformed `RemoteAddr` both are.
type opaqueAddr string

func (o opaqueAddr) Network() string { return "unix" }
func (o opaqueAddr) String() string  { return string(o) }

// ClientIP is what a router should hand the limiter, given a request.
//
// It is here, as a function over a *net.TCPAddr and a forwarded header, because
// the decision about which of those to believe is a security decision and it
// should be made once and written down rather than at every call site.
//
// The answer: **a `RemoteAddr` socket address, and a forwarded header only when
// the request came through a proxy this deployment is configured to trust.** A
// forwarded header is attacker-controlled on any direct connection, so believing
// it unconditionally means the rate limit is trivially bypassed by sending
// `X-Forwarded-For` with a fresh value per attempt — which is the exact attack
// the limit exists to slow. M8 reads the trusted-proxy setting from config.
func ClientIP(remote net.Addr, forwardedFor string, trustedProxy bool) string {
	if trustedProxy && forwardedFor != "" {
		// The left-most entry is the original client as recorded by the outermost
		// trusted proxy. Taking a later one would be taking something the client
		// can write.
		if first, _, found := strings.Cut(forwardedFor, ","); found {
			return normaliseAddress(first)
		}
		return normaliseAddress(forwardedFor)
	}
	if remote == nil {
		return ""
	}
	// A unix socket has no IP, and several of them behind one proxy is one player,
	// not several.
	if tcp, ok := remote.(*net.TCPAddr); ok {
		return tcp.IP.String()
	}
	host, _, err := net.SplitHostPort(remote.String())
	if err != nil {
		return normaliseAddress(remote.String())
	}
	return normaliseAddress(host)
}
