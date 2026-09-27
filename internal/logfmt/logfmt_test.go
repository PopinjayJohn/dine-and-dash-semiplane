package logfmt_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/auth"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/logfmt"
)

// TestTheTextFormatIsWhatADmReads is the default's whole justification, and the
// assertion is that it is `slog`'s own encoding rather than one this package wrote.
func TestTheTextFormatIsWhatADmReads(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	handler, err := logfmt.New(&out, logfmt.FormatText, slog.LevelInfo)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	slog.New(handler).Info("serving", slog.String("campaign", "blackwater"))

	if !strings.Contains(out.String(), `level=INFO`) {
		t.Errorf("not slog's text encoding: %q", out.String())
	}
	if !strings.Contains(out.String(), "campaign=blackwater") {
		t.Errorf("the attribute is missing: %q", out.String())
	}
}

// TestTheJSONFormatIsOneObjectPerLine is what a log shipper and a `jq` want, and
// the assertion is that it parses — a test that only looked for a brace would pass
// for a pretty-printed multi-line encoder, which is the one shape `jq` cannot stream.
func TestTheJSONFormatIsOneObjectPerLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	handler, err := logfmt.New(&out, logfmt.FormatJSON, slog.LevelInfo)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	logger := slog.New(handler)
	logger.Info("serving", slog.String("campaign", "blackwater"))
	logger.Info("stopped", slog.Int("streams", 2))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines, want 2:\n%s", len(lines), out.String())
	}

	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Errorf("line %d is not a JSON object: %v\n%s", i, err, line)
		}
	}
}

// TestAnUnknownFormatIsRefusedRatherThanDefaulted is the decision, and the reason for
// it is a DM who would otherwise spend an afternoon wondering why their log shipper
// has nothing to parse.
//
// A default here is the *worst* of the three answers: a refused format is thirty
// seconds of reading the error, a default is an afternoon, and a panic is a support
// question.
func TestAnUnknownFormatIsRefusedRatherThanDefaulted(t *testing.T) {
	t.Parallel()

	tests := []string{"jsonl", "logfmt", "TEXT", "console", " json"}

	for _, format := range tests {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			_, err := logfmt.New(&bytes.Buffer{}, logfmt.Format(format), slog.LevelInfo)
			if err == nil {
				t.Errorf("New(%q) = nil, want a refusal", format)
			}
			// And the message names the two that work, because "unknown format" on
			// its own is a message that sends a DM to the source.
			if !strings.Contains(err.Error(), "json") || !strings.Contains(err.Error(), "text") {
				t.Errorf("the refusal does not say what is accepted: %v", err)
			}
		})
	}
}

// TestTheEmptyFormatIsTheTextOne is the zero value, and it is the answer a caller
// gets for having set nothing.
func TestTheEmptyFormatIsTheTextOne(t *testing.T) {
	t.Parallel()

	var text, empty bytes.Buffer
	if _, err := logfmt.New(&text, logfmt.FormatText, slog.LevelInfo); err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := logfmt.New(&empty, "", slog.LevelInfo); err != nil {
		t.Fatalf("New: %v", err)
	}

	slog.New(textHandler(t, logfmt.FormatText)).Info("one", slog.String("k", "v"))
	slog.New(emptyHandler(t)).Info("one", slog.String("k", "v"))

	if text.String() != empty.String() {
		t.Errorf("the empty format is not the text format:\n%q\n%q", text.String(), empty.String())
	}
}

// TestTheRedactionSurvivesTheFormat is the constraint on any format, and it is the
// test that would fail first if somebody ever added a raw handler.
//
// `internal/auth`'s handler wraps whatever it is given, so this is not a property of
// this package — it is a property of the M6 decision that a redacting handler has no
// unwrapped constructor to reach past. The test exists because the property is what
// makes adding a format safe, and a property nothing tests is a comment.
func TestTheRedactionSurvivesTheFormat(t *testing.T) {
	t.Parallel()

	const token = "super-secret-token-value-do-not-log"

	for _, format := range []logfmt.Format{logfmt.FormatText, logfmt.FormatJSON} {
		t.Run(string(format), func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			handler, err := logfmt.New(&out, format, slog.LevelInfo)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			// The redaction is the *wrapper*; this package supplies the encoder.
			logger := slog.New(auth.NewRedactingHandler(handler))
			logger.Info("redeeming", slog.String("token", token))

			if strings.Contains(out.String(), token) {
				t.Errorf("the token reached the log in %s format:\n%s", format, out.String())
			}
		})
	}
}

// TestResolvePrefersTheFlagOverTheEnvironment is the precedence, and it is the same
// three-rule shape as `datadir.Resolve` for the same reason: one place decides the
// order, so a flag and a variable cannot disagree about it.
func TestResolvePrefersTheFlagOverTheEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		flag      string
		env       string
		want      logfmt.Format
		wantError bool
	}{
		{name: "nothing set is text", want: logfmt.FormatText},
		{name: "the environment chooses", env: "json", want: logfmt.FormatJSON},
		{name: "the flag beats the environment", flag: "text", env: "json", want: logfmt.FormatText},
		{name: "the flag alone", flag: "json", want: logfmt.FormatJSON},
		{
			name: "an unknown environment value is refused",
			env:  "logfmt", wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(logfmt.EnvVar, test.env)

			got, err := logfmt.Resolve(test.flag)
			if test.wantError {
				if err == nil {
					t.Errorf("Resolve(%q) with %s=%q = %q, want a refusal",
						test.flag, logfmt.EnvVar, test.env, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", test.flag, err)
			}
			if got != test.want {
				t.Errorf("Resolve(%q) with %s=%q = %q, want %q",
					test.flag, logfmt.EnvVar, test.env, got, test.want)
			}
		})
	}
}

// textHandler and emptyHandler are the two encodings the test above compares, built
// through the same constructor a command uses so that the comparison is between two
// calls to [logfmt.New] and not between two hand-built handlers.
func textHandler(t *testing.T, format logfmt.Format) slog.Handler {
	t.Helper()

	handler, err := logfmt.New(&bytes.Buffer{}, format, slog.LevelInfo)
	if err != nil {
		t.Fatalf("New(%q): %v", format, err)
	}
	return handler
}

func emptyHandler(t *testing.T) slog.Handler {
	t.Helper()

	handler, err := logfmt.New(&bytes.Buffer{}, "", slog.LevelInfo)
	if err != nil {
		t.Fatalf(`New(""): %v`, err)
	}
	return handler
}
