// Package auth is a share link: how one is minted, how it is redeemed, and who
// is holding what afterwards.
//
// # What this is, and what it is not
//
// The decision is ADR 0003 and it is a capability URL exchanged exactly once for
// an opaque session cookie:
//
//	https://host/c/<slug>?k=<token>   -- minted, shown once, never stored
//	https://host/c/<slug>/            -- after redemption, and the token is gone
//
// What this package deliberately does *not* have is `net/http`. A redemption is a
// decision and a set of bytes: "this token is good, here is a session id, put this
// in a cookie, send the browser here, and the token leaves the URL." Producing a
// `*http.Request` to say that would mean the interesting part of the logic could
// only be tested by standing up a server, and the bytes a browser receives would
// be decided inside a handler where nobody looks at them twice. So the exchange
// returns values, M8's router turns them into a response, and the whole of it is
// testable with a fixture. See ADR 0016.
//
// # The token
//
// 32 bytes from crypto/rand, hex-encoded, never stored, never recoverable. What is
// stored is its SHA-256 and four characters of it. Two consequences worth stating
// because they are what makes this safe rather than merely obscure:
//
//   - The database cannot leak a token, because it has never held one. A dump of
//     the campaign database gives an attacker nothing that can be presented to a
//     browser, and no amount of admin access to the host turns into a player's
//     access.
//   - The token is not recoverable, so a DM who loses it mints a new one. That is
//     the correct behaviour anyway (ADR 0003), and it is why there is no reset
//     link and no email channel.
//
// The token travels in a query parameter exactly once, which is the one request
// line that contains it, and the 303 on redemption is what takes it back out. That
// is the whole reason the exchange exists: the alternative is a bearer credential
// in the address bar for the life of the link, leaking through Referer, through
// history, through screenshots and into every access log that records the line.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// TokenBytes is how much entropy a share-link token carries.
//
// Thirty-two bytes is the size the specification asks for and it is what a
// bearer token is normally 256 bits of, which is the point at which guessing is no
// longer a thing anybody can do. The URL ends up a little over forty characters
// longer for it, and the DM shares links rather than typing them, so the length
// buys nothing to give back.
const TokenBytes = 32

// Token is a plaintext share-link token.
//
// It is a distinct type and not a string for one reason: a bare string is the
// shape of every other value in this program, and the one value that must never be
// logged, stored, or put in an error. A type that does not implement
// fmt.Stringer, and which this package only ever converts to bytes at the two
// places that need it, is a type that a careless `slog.Any("token", tok)` cannot
// print by accident.
type Token struct {
	// bytes is unexported, so the only ways out are Hash, Hint and String, and
	// String is the redacting one.
	bytes []byte
}

// NewToken mints a token from crypto/rand.
//
// crypto/rand and nothing else, and there is no interface here to inject one: a
// token minted from a seeded generator is a token anybody with the seed can mint,
// and "the generator is injected for testability" is how that happens. The cost
// is that a test cannot predict a token, and the tests below are written to check
// properties rather than values, which is what they should be anyway.
func NewToken() (Token, error) {
	raw := make([]byte, TokenBytes)
	if _, err := rand.Read(raw); err != nil {
		// The error is wrapped and not swallowed: a system whose entropy source
		// has failed cannot issue credentials, and the only safe response is to
		// refuse rather than to fall back to something weaker.
		return Token{}, fmt.Errorf("reading %d bytes of randomness for a share-link token: %w", TokenBytes, err)
	}
	return Token{bytes: raw}, nil
}

// Hash is the SHA-256 of the token, hex-encoded, which is what is stored and what
// redemption looks the token up by.
//
// SHA-256 and not a password hash, deliberately: a token is 32 bytes of entropy,
// so there is nothing to brute-force and nothing a slow KDF would help with. A
// password hash exists to make guessing *cheap* inputs expensive; a 256-bit random
// string has no cheap inputs. A KDF here would be 100ms of latency on every
// redemption to defend against an attack that already cannot succeed.
func (t Token) Hash() string {
	sum := sha256.Sum256(t.bytes)
	return hex.EncodeToString(sum[:])
}

// Hint is the last four characters, for the label beside a link in the DM's list.
//
// Four characters of a 64-character hex encoding is sixteen bits: enough to tell
// a dozen links apart, and useless to anyone who finds one, because finding it
// still leaves 2^240 to search.
func (t Token) Hint() string { return Hint(t.Hex()) }

// Hex is the token as it appears in a URL.
//
// Exported because a share link has to be built, and a link that could not be
// built without a private field accessor would have a `String()` that prints the
// real thing and a `String()` that does not, which is a trap. This is the
// dangerous one, named as such.
func (t Token) Hex() string { return hex.EncodeToString(t.bytes) }

// String is the redacting form, and it is what every accidental formatting of a
// Token produces.
//
// It prints the hint and nothing else, so a `%v`, a `slog.Any`, a test failure
// message or a stack trace prints four characters of a credential rather than all
// of it. A type that panics when printed would be caught by the first test; a type
// that prints itself is caught by nobody.
func (t Token) String() string { return "share-link token (hint " + t.Hint() + ")" }

// GoString is what %#v prints, and %#v does not consult String.
//
// That is a leak with a hole in it that is easy to miss: `String` is the
// interface everybody knows to implement, `t.Errorf("%#v", err)` and the
// struct-dumping log handlers reach straight past it for the Go-syntax
// representation, and a Token with only String prints all thirty-two bytes as
// `auth.Token{bytes:[]uint8{0xe1, 0x0, ...}}`. This is the second method and it
// exists because the test for the first one passed while the leak was still there.
func (t Token) GoString() string { return t.String() }

// Format is the last of the three, and it exists because two verbs reach past
// String and GoString: `%x` and `%X` on a struct print its fields hex-encoded, so
// a Token with only the first two methods hands out the whole token in hex to
// anybody who wrote `%x`. With a Format method, fmt routes every verb through this
// one function and there is no verb left that can reach the unexported bytes.
//
// The width and precision are honoured rather than dropped so a padded log column
// still lines up, and the flags are passed through for the same reason. The text
// itself never changes: it is a fixed string, and a token is never actually
// padded, because nothing that prints one knows how wide the redacted form is,
// which is rather the point of it.
func (t Token) Format(state fmt.State, verb rune) {
	redacted := t.String()

	if verb == 'q' {
		fmt.Fprintf(state, "%q", redacted)
		return
	}

	var spec strings.Builder
	spec.WriteByte('%')
	for _, flag := range []byte("+-# 0") {
		if state.Flag(int(flag)) {
			spec.WriteByte(flag)
		}
	}
	if width, given := state.Width(); given {
		fmt.Fprintf(&spec, "%d", width)
	}
	if precision, given := state.Precision(); given {
		fmt.Fprintf(&spec, ".%d", precision)
	}
	spec.WriteRune(verb)

	// A verb fmt will not accept for a string produces the standard `%!v(...)`
	// complaint, which names the verb and is not the token. That is the right
	// outcome: a caller who asked for a number got a complaint rather than a
	// credential.
	_, _ = fmt.Fprintf(state, spec.String(), redacted)
}

// ParseToken is the inverse of Hex: the token as it came out of a URL, and the
// Token it stands for.
//
// It exists because the two halves of this package have to agree about what a
// token *is*, and the version of that agreement where each side hashes whatever
// it happens to be holding is a bug that no unit test finds and every player
// does: the minting side hashes the 32 raw bytes and the redemption side would
// hash the 64 characters of hex, and every link in every campaign fails to work
// with "that share link is not valid".
//
// A string that is not 64 hex characters is refused here, before the store is
// touched, which also means a redemption with a two-character token does no
// database work at all.
func ParseToken(hexToken string) (Token, error) {
	if len(hexToken) != 2*TokenBytes {
		return Token{}, fmt.Errorf("auth: a share-link token is %d hex characters, this one is %d",
			2*TokenBytes, len(hexToken))
	}
	raw, err := hex.DecodeString(hexToken)
	if err != nil {
		return Token{}, fmt.Errorf("auth: a share-link token is hex, this one is not")
	}
	return Token{bytes: raw}, nil
}

// Redacted is whether a string might be a token, for the logger.
//
// It is a *guard*, not a filter: the logger redacts anything that looks like a
// token in a message it did not construct, and this is the test it uses. A caller
// that knows it holds a token does not pass it here at all — it passes
// Token.String(), which is already safe.
func Redacted(s string) bool { return len(s) == 2*TokenBytes && isHex(s) }

// isHex reports whether s is a hex string and nothing else. Written out rather
// than using a regexp, because this runs on every log line and because the
// question is one character class.
func isHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// Hint is the last HintLength characters of a token, for the label beside a link
// in the DM's list.
//
// It is a function on a string rather than a method on Principal because the only
// legal input is the plaintext, which is not a field of anything: the principal
// stores the hint, and this is what put it there.
func Hint(hexToken string) string {
	if len(hexToken) <= HintLength {
		return hexToken
	}
	return hexToken[len(hexToken)-HintLength:]
}

// HintLength is how many characters of a token are kept.
//
// Four is what the specification says, and four is about right: it separates a
// dozen links and is useless to anyone who finds one, because a 32-byte token
// hex-encodes to 64 characters and the last four of those are sixteen bits.
const HintLength = 4
