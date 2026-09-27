package auth_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// TestNoTokenInLogs is the named test from §14, and it is written the way the
// requirement is phrased: a *full auth flow*, and then a grep of everything it
// produced.
//
// Both halves matter and they are not the same check. A test that only looked at
// the redaction unit would pass against a logger that redacts and a flow that never
// logs the token — which is a logger that has not been connected to the thing that
// matters. A test that ran the flow and looked for the token is the only one that
// says "no token in no log line, ever", which is the property.
func TestNoTokenInLogs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var logs strings.Builder
	m, s, campaign := newMinter(t)
	logger := auth.NewLogger(&logs, slog.LevelDebug)

	// A real flow, end to end, with a logger attached to every step. The logger
	// calls here are what a router would write, not what a redaction test would
	// find convenient: the URL and the raw token are both logged, because both are
	// the sort of thing somebody logs without thinking.
	issued, err := m.Issue(ctx, campaign, domain.RolePlayer, "Alice (Ranger)")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	token := issued.Token.Hex()

	logger.Info("minted a share link",
		"url", issued.URL,
		"token", issued.Token,
		"hint", issued.Principal.TokenHint,
		"label", issued.Principal.Label,
	)

	r := &auth.Redeemer{Backend: s, Config: m.Config}

	logger.Info("about to redeem", "k", token, "slug", campaign.Slug)
	redeemed, err := r.Redeem(ctx, campaign.Slug, token)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	logger.Info("redeemed", "principal", redeemed.Principal.ID, "session", redeemed.SessionID)

	if _, authErr := r.Authenticate(ctx, redeemed.SessionID); authErr != nil {
		t.Fatalf("Authenticate: %v", authErr)
	}

	// And the failure paths, because a refused redemption is exactly when somebody
	// helpfully logs the token that did not work.
	other, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if _, foreignErr := r.Redeem(ctx, campaign.Slug, other.Hex()); foreignErr == nil {
		t.Fatal("Redeem accepted a token that is not ours")
	}
	logger.Info("a redemption failed", "token", other.Hex(), "error", err)
	logger.Error("a redemption failed", "token", other, "error", "that share link is not valid for this campaign")

	if err := r.EndSession(ctx, redeemed.SessionID); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	logger.Info("logged out", "session", redeemed.SessionID)

	// The grep. Everything the flow produced, checked for every token it touched.
	output := logs.String()
	if output == "" {
		t.Fatal("the flow wrote no log lines at all, so this test proves nothing: " +
			"a logger nobody logs to is a logger that has not been connected to anything")
	}

	for _, secret := range []string{token, other.Hex()} {
		if strings.Contains(output, secret) {
			t.Errorf("a share-link token is in the log:\n%s", output)
		}
	}

	// The session id is a credential too, and a different shape: it is in a cookie,
	// where a token is in a URL. It should be readable — it is how a DM matches a
	// browser to a row — but it must not be a cookie *header*.
	if strings.Contains(output, "__Host-") {
		t.Errorf("a cookie header reached the log:\n%s", output)
	}

	// And the log is still a log: redaction that leaves nothing to read teaches a
	// DM to stop reading it, which is how the next real problem goes unnoticed.
	for _, want := range []string{
		"minted a share link",
		"about to redeem",
		"redeemed",
		"a redemption failed",
		"logged out",
		issued.Principal.Label,
		issued.Principal.TokenHint,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("the log is missing %q, so redaction has eaten more than it should:\n%s", want, output)
		}
	}
}

// Redaction is keyed on the shape of a token *and* on the name of the attribute,
// because a caller who logs a whole URL is doing something a shape check alone
// only catches by luck, and a caller who logs something under the key "token" is
// telling us it is a token.
func TestRedaction(t *testing.T) {
	t.Parallel()

	token, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	secret := token.Hex()

	tests := map[string]struct {
		log  func(*slog.Logger)
		want string
		hide string
	}{
		"the URL a DM was given": {
			log: func(l *slog.Logger) {
				l.Info("minted", "url", "https://wiki.example/c/blackwater/?k="+secret)
			},
			want: "https://wiki.example/c/blackwater/",
		},
		"the query parameter by its own name": {
			log:  func(l *slog.Logger) { l.Info("redeeming", "k", secret) },
			want: "[redacted",
		},
		"a cookie header": {
			log:  func(l *slog.Logger) { l.Info("response", "set-cookie", "__Host-x="+secret+"; HttpOnly") },
			want: "[redacted",
		},
		"an authorization header": {
			log:  func(l *slog.Logger) { l.Info("proxy", "authorization", "Bearer "+secret) },
			want: "[redacted",
		},
		// Under a sensitive key the value is redacted by name, and the Token's own
		// Stringer never gets a say — which is the right order: a caller who logs
		// under "token" has said what it is.
		"the token value under a sensitive key": {
			log:  func(l *slog.Logger) { l.Info("minted", "token", token) },
			want: "[redacted",
		},
		// Under any other key the Token prints itself, and the hint is the point of
		// that: a DM with two links in a log can tell them apart.
		"the token value under a neutral key": {
			log:  func(l *slog.Logger) { l.Info("minted", "issued", token) },
			want: "hint",
		},
		"a token in the message rather than an attribute": {
			log:  func(l *slog.Logger) { l.Info("could not redeem " + secret) },
			want: "[redacted",
		},
		"inside a group": {
			log: func(l *slog.Logger) {
				l.Info("minted", slog.Group("request", "url", "https://x/?k="+secret))
			},
			want: "[redacted",
		},
		"through WithAttrs, which is not a record": {
			log:  func(l *slog.Logger) { l.With("url", "https://x/?k="+secret).Info("minted") },
			want: "[redacted",
		},
		// Redacted, and this one surprised me. A token's SHA-256 is 64 hex
		// characters, and so is the token, and nothing about the two strings tells
		// them apart — so the shape check catches a hash along with a credential.
		// The plan had been that a hash is a fingerprint and worth logging; the
		// cost of the collision is one correlation, and the log carries the
		// principal id, so nothing is lost by it.
		"a token hash is redacted because it cannot be told apart from a token": {
			log:  func(l *slog.Logger) { l.Info("minted", "token_hash", token.Hash()) },
			want: "[redacted",
		},
		"an ordinary long hex value that is not token-shaped": {
			log:  func(l *slog.Logger) { l.Info("content hash", "sha256", strings.Repeat("ab", 16)) },
			want: strings.Repeat("ab", 16),
		},
		"a value that merely contains a token": {
			log:  func(l *slog.Logger) { l.Info("error", "msg", "bad token "+secret+" in link") },
			want: "bad token",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var logs strings.Builder
			tt.log(auth.NewLogger(&logs, slog.LevelDebug))

			output := logs.String()
			if strings.Contains(output, secret) {
				t.Errorf("the token is in the log:\n%s", output)
			}
			if !strings.Contains(output, tt.want) {
				t.Errorf("the log does not contain %q, so something was over-redacted:\n%s", tt.want, output)
			}
		})
	}
}

// The guard has to be narrow, and narrow in a particular way: it matches a
// *window*, and the window is the whole 64 hex characters, with no requirement
// about what follows.
//
// That is a decision, and the wrong version of it is easy to imagine: requiring a
// non-hex character after the window, so that a 65-character hex string is left
// alone. It would also leave a token followed by a digit in a message — "redeem
// <token>1" — unredacted, which is a leak in every sentence a human writes. So a
// 65-character hex run has its first 64 redacted and the last one left, and that
// is the intended behaviour rather than a gap in it.
//
// Narrow in the other direction, too: a content hash is 64 hex characters, so it
// is redacted along with a token, and the comment in logging.go says what that
// costs.
func TestRedactionIsNarrow(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		want  string
	}{
		"one character short is not a token": {
			input: strings.Repeat("a", 63),
			want:  strings.Repeat("a", 63),
		},
		"a non-hex character anywhere in the window is not a token": {
			input: strings.Repeat("a", 63) + "z",
			want:  strings.Repeat("a", 63) + "z",
		},
		"a non-hex character at the start is not a token": {
			input: "z" + strings.Repeat("a", 63),
			want:  "z" + strings.Repeat("a", 63),
		},
		// The window is the whole test: a 65-character run has 64 token-shaped
		// characters at the front of it.
		"one character long has its first 64 redacted": {
			input: strings.Repeat("a", 65),
			want:  "[redacted share-link token]a",
		},
		"a token at the end of a sentence": {
			input: "the link is " + strings.Repeat("a", 64) + ".",
			want:  "the link is [redacted share-link token].",
		},
		"two tokens in one string": {
			input: strings.Repeat("a", 64) + " and " + strings.Repeat("b", 64),
			want:  "[redacted share-link token] and [redacted share-link token]",
		},
		"no token at all": {
			input: "a perfectly ordinary log line",
			want:  "a perfectly ordinary log line",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, _ := auth.RedactTokenShaped(tt.input)
			if got != tt.want {
				t.Errorf("RedactTokenShaped(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// A JSON logger is a logger with the same redaction, and a caller who wants one
// for a file gets one rather than a way round the redaction.
func TestTheRedactionIsNotBypassedByTheFormat(t *testing.T) {
	t.Parallel()

	token, err := auth.NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}

	var logs strings.Builder
	json := slog.New(auth.NewRedactingHandler(slog.NewJSONHandler(&logs, nil)))
	json.Info("minted", "url", "https://wiki.example/c/blackwater/?k="+token.Hex())

	if strings.Contains(logs.String(), token.Hex()) {
		t.Errorf("the JSON handler bypassed the redaction:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "blackwater") {
		t.Errorf("the JSON handler produced nothing readable:\n%s", logs.String())
	}
}
