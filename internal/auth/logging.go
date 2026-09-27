package auth

import (
	"context"
	"io"
	"log/slog"
	"strings"
)

// Redacted is the text a redacted value is replaced with.
//
// The shape matters in a way a bare "REDACTED" would not: it says *what* was
// hidden, so a line in a DM's log that reads
//
//	share link redeemed principal=id-7 hint=c267
//
// is a line they can read, and a line that reads
//
//	share link redeemed principal=id-7 token=REDACTED
//
// is not a line anybody can learn anything from. A log that cannot be read is a
// log nobody reads, and the point of writing one down is that somebody will.
const redactedText = "[redacted share-link token]"

// Logger is this package's logging, and the reason it is not `slog.Default()`.
//
// The one thing this package must never write is a token, and the way to
// guarantee that is not to ask every call site to be careful. So the logger here
// has a redacting handler underneath it, and the redaction happens in the handler
// rather than in the callers — which means a caller that logs a whole request
// struct, or an error that happens to contain a URL, is covered by the same rule
// as one that logs the token deliberately.
//
// The one thing a handler cannot do is un-print a value a caller already formatted
// into a string. By the time `slog.Any("url", issued.URL)` reaches a handler it is
// a string with the token in it, and the handler can only find it because it is a
// token-shaped run of hex — which is why the test greps the *output* for the token
// rather than asserting on the records.

// NewLogger is a logger that redacts, and a handler that redacts.
//
// The handler wraps whatever it is given, so a caller who wants a JSON log for a
// file gets one with the redaction still in place. There is no unwrapped
// constructor, deliberately: a way to get the unredacting handler is a way for
// somebody to reach for it once.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(NewRedactingHandler(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: replaceSensitive,
	})))
}

// redactingHandler is a handler that redacts on the way through, whatever is under
// it.
//
// It exists for the values a handler alone cannot help with: a `time.Time` is fine,
// a `Token` prints redacted by itself, and a *string* holding a whole request URL
// is neither. A logger that only replaced known keys would be a logger whose
// safety depends on the caller having used the right key, and a caller who has
// written `slog.String("url", ...)` has used the right key.
type RedactingHandler struct {
	inner slog.Handler
}

func NewRedactingHandler(inner slog.Handler) *RedactingHandler {
	return &RedactingHandler{inner: inner}
}

func (h *RedactingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *RedactingHandler) Handle(ctx context.Context, record slog.Record) error {
	safe := slog.NewRecord(record.Time, record.Level, redactMessage(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		safe.AddAttrs(redactAttr(attr))
		return true
	})
	return h.inner.Handle(ctx, safe)
}

func (h *RedactingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	safe := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		safe = append(safe, redactAttr(attr))
	}
	return &RedactingHandler{inner: h.inner.WithAttrs(safe)}
}

func (h *RedactingHandler) WithGroup(name string) slog.Handler {
	return &RedactingHandler{inner: h.inner.WithGroup(name)}
}

// sensitiveKeys are attribute names whose *value* is a credential whatever it
// looks like.
//
// Matching the key as well as the shape is deliberate. A key named `token` whose
// value is a `Token` already prints redacted, and a key named `token` whose value
// is somebody's *idea* of a token — a hint, a fixture, a value from a future
// version of this program — is redacted too. A rule with no exceptions is worth
// more than a precise one that somebody has to reason about at each call site.
//
// **`token_hash` is not in the list because it cannot be on the other side of it
// either.** A token's SHA-256 is 64 hex characters, and so is the token, and
// nothing about the two strings tells them apart — so the shape check redacts a
// hash along with a credential. That was not the plan: the plan was that a hash is
// a fingerprint rather than a credential, and logging it is how a DM correlates
// "this link was used twice" with two rows. The cost of the collision is one
// correlation, and the log carries the principal id, so nothing is actually lost
// by it. The alternative — special-casing the hash out of the shape check — would
// be a rule with an exception, and the exception is "this 64-character string,
// which is exactly a token, is fine".
var sensitiveKeys = map[string]bool{
	"token":         true,
	"k":             true, // the query parameter's own name, from a parsed URL
	"set-cookie":    true,
	"cookie":        true,
	"authorization": true,
	"session":       true,
}

// redactAttr redacts one attribute, and recurses into groups.
func redactAttr(attr slog.Attr) slog.Attr {
	if sensitiveKeys[strings.ToLower(attr.Key)] {
		return slog.String(attr.Key, redactedText)
	}

	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		// The case this handler exists for: a whole URL in an attribute.
		if redacted, found := RedactTokenShaped(value.String()); found {
			return slog.String(attr.Key, redacted)
		}
		return attr
	case slog.KindGroup:
		safe := value.Group()
		for i, member := range safe {
			safe[i] = redactAttr(member)
		}
		return slog.Attr{Key: attr.Key, Value: slog.GroupValue(safe...)}
	default:
		// A number, a duration, a time, a bool, or an `any` holding something with
		// a Format method. The last is a `Token`, and it is already safe: reaching
		// for its String here would be the wrong layer to do it at, because a
		// Token that printed itself would still print itself through a handler
		// with no redaction at all, which is the leak the method exists to stop.
		return attr
	}
}

// redactMessage is the message string, which may contain a URL somebody pasted in.
func redactMessage(message string) string {
	redacted, _ := RedactTokenShaped(message)
	return redacted
}

// RedactTokenShaped replaces every token-shaped run of hex in a string, and says
// whether it found one.
//
// It is deliberately narrow: exactly `TokenBytes` bytes of hex, nothing else. A
// guard that matched everything would redact the whole log and teach a DM to
// ignore the log, and a guard that matched a *substring* would redact the parts of
// a session id that happen to be hex and leave the rest.
func RedactTokenShaped(s string) (string, bool) {
	var (
		out   strings.Builder
		found bool
	)
	out.Grow(len(s))

	for i := 0; i < len(s); {
		if looksLikeToken(s[i:]) {
			out.WriteString(redactedText)
			i += 2 * TokenBytes
			found = true
			continue
		}
		out.WriteByte(s[i])
		i++
	}

	return out.String(), found
}

// replaceSensitive is the text handler's own hook, for the attributes it formats
// itself: `slog.Time` and the built-in source and level keys.
//
// It is the second of the two redaction points, and it exists because the text
// handler formats some values itself rather than going through Attr. Without it, a
// time is safe and a level is safe, which is true — and the hook is here so that
// the next value the text handler grows a formatter for inherits the redaction
// rather than becoming a hole.
func replaceSensitive(groups []string, attr slog.Attr) slog.Attr {
	if len(groups) == 0 && sensitiveKeys[strings.ToLower(attr.Key)] {
		return slog.String(attr.Key, redactedText)
	}
	return attr
}
