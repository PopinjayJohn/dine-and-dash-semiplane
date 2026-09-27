package http

import (
	"net"
	"net/netip"
	"strings"
)

// trustlist answers "is this request's remote address one of ours", for the
// `X-Forwarded-For` question.
//
// # Why it is a type and not a loop
//
// A list of addresses is parsed on every request if it is parsed in the handler, and
// a CIDR entry is a `netip.Prefix` that is expensive to build. `trustlist` parses
// once — at construction, from `Config` — and every request after that is a map
// lookup and a prefix test.
//
// It is a value rather than a pointer so that a zero one is a valid trustlist that
// trusts nothing, which is the answer for every deployment that has not configured
// one. That matters because the alternative — a nil map that a method has to test —
// is a nil dereference waiting for the one caller that forgets.
//
// # What counts as a match
//
// A bare address matches that address only. A CIDR matches anything inside it, which
// is what a reverse proxy on a home network needs: its source address moves, and a
// list of single addresses is a list that breaks silently when the router hands out a
// different one.
//
// **A hostname is not a match**, and that is a refusal rather than a limitation. A
// name in this list would have to be resolved on every request, and a resolved name
// can resolve somewhere else tomorrow, so the answer would be a different one than
// the DM wrote down. An entry that is neither an address nor a prefix is kept as
// `unusable` and reported by [trustlist.Unusable], so a deployment with a typo in
// its config learns about it rather than quietly trusting nothing.
type trustlist struct {
	exact    map[string]bool
	prefixes []netip.Prefix

	// unusable is the entries that were neither an address nor a prefix, kept so a
	// caller can say what it ignored.
	unusable []string
}

// parseTrustlist is a list of entries as a trustlist.
func parseTrustlist(entries []string) trustlist {
	list := trustlist{exact: make(map[string]bool, len(entries))}

	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}

		if prefix, err := netip.ParsePrefix(trimmed); err == nil {
			list.prefixes = append(list.prefixes, prefix.Masked())
			continue
		}
		if address, err := netip.ParseAddr(trimmed); err == nil {
			list.exact[address.Unmap().String()] = true
			continue
		}

		list.unusable = append(list.unusable, trimmed)
	}

	return list
}

// Unusable is the entries that were neither an address nor a CIDR prefix.
//
// It exists for the same reason a config loader refuses an unknown key: a deployment
// that wrote `trusted_proxies: [proxy.example.com]` has asked for something and this
// is the answer, and an empty list that trusts nothing is indistinguishable from one
// nobody configured.
func (l trustlist) Unusable() []string {
	return l.unusable
}

// Contains is whether a request's `RemoteAddr` is in the list.
//
// The address is the part before the colon, and an `RemoteAddr` that is not
// `host:port` — a unix socket's path — is not in the list, because a path is not an
// address and a list of paths is not a list of proxies.
func (l trustlist) Contains(remoteAddr string) bool {
	if len(l.exact) == 0 && len(l.prefixes) == 0 {
		return false
	}

	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}

	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	address = address.Unmap()

	if l.exact[address.String()] {
		return true
	}
	for _, prefix := range l.prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
