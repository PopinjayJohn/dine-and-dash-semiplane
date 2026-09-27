// Package logfmt chooses a `log/slog` handler, and it is a package because
// "which format" is a question three commands ask and one place should answer it.
//
// # What "structured logs" means here, since nothing defined it
//
// The M13 row is the only mention of structured logs in the entire repository, and
// there is no ADR, no spec section and no sentence in `docs/security.md` about it.
// So the requirement had to be read off the code, and the code says something
// specific: **the application already logs through `log/slog` everywhere**, with
// `slog.Attr` calls carrying named fields — `method`, `path`, `status`, `request_id`,
// `principal` — and the access log is already machine-readable in the sense that
// matters.
//
// What was missing was a *choice of encoding*. `wiki serve` hardcoded
// `slog.NewTextHandler` to stderr, so a DM whose system log wants JSON had one of two
// options: post-process the text, or run the wiki in a container with a log shipper
// that scrapes it. Both are worse than a flag.
//
// So this package is a *format switch and nothing else*. It does not change what is
// logged, how much, or at what level. It cannot: the set of fields is the
// application, and a log format that could add or remove a field would be a
// compatibility promise about somebody's grep.
//
// # The redaction survives
//
// `internal/auth`'s redacting handler wraps whatever it is given, so a JSON handler
// gets the same redaction as a text one. That was a M6 decision and it is the reason
// this package can be this small: there is no "raw" constructor to reach past, so
// there is no way to ask for a format that skips the redaction.
//
// The consequence is worth stating because it is the constraint on any future format:
// a new format is a new *encoder*, and it is wrapped in the same handler or it is not
// used.

package logfmt

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// Format is a log encoding.
type Format string

const (
	// FormatText is `slog`'s own text encoding: `time=... level=INFO msg="..." key=value`.
	// It is the default because it is what a DM reads when they run the binary in a
	// terminal, and a human reading a log at the table is the common case.
	FormatText Format = "text"

	// FormatJSON is one JSON object per line, which is what `journalctl -o json`,
	// a log shipper and a `jq` all want.
	FormatJSON Format = "json"
)

// EnvVar is the environment variable that selects a format, named like ADR 0011's
// `DDSP_DATA_DIR` so that the one that already exists and the one that do not look
// like the same decision.
const EnvVar = "DDSP_LOG_FORMAT"

// New returns a handler for a format at a level, writing to w.
//
// A format this package does not know is a **refusal, not a default**: a DM who
// asked for `jsonl` and got text would spend an afternoon wondering why their log
// shipper had nothing to parse, and the answer would be in a line they never see
// because the process started anyway. An unknown format stops the command.
func New(w io.Writer, format Format, level slog.Level) (slog.Handler, error) {
	options := &slog.HandlerOptions{Level: level}

	switch format {
	case FormatText, "":
		return slog.NewTextHandler(w, options), nil
	case FormatJSON:
		return slog.NewJSONHandler(w, options), nil
	default:
		return nil, fmt.Errorf("unknown log format %q: it is %q or %q", format, FormatText, FormatJSON)
	}
}

// Resolve is the format a command should use: what it was given, then the
// environment, then [FormatText].
//
// It is the same three-rule shape as `datadir.Resolve` and for the same reason: one
// source of truth per setting, decided in one place, so that a flag and a variable
// cannot disagree about precedence. The flag beats the environment because a DM who
// typed it meant it; the environment beats the default because a deployment that sets
// it once should not have to set it on every subcommand.
func Resolve(explicit string) (Format, error) {
	resolved := FormatText
	switch {
	case explicit != "":
		resolved = Format(explicit)
	case envOrEmpty(EnvVar) != "":
		resolved = Format(envOrEmpty(EnvVar))
	}

	// Validated here rather than left to [New], because the two are called from
	// different places and a command that resolved a format and then built a handler
	// with it would be the one place an unknown value got through. It is also the
	// better moment: a command that cannot start because of its log format says so
	// before it opens a database, rather than after.
	if resolved != FormatText && resolved != FormatJSON {
		return "", fmt.Errorf("unknown log format %q: it is %q or %q",
			resolved, FormatText, FormatJSON)
	}

	return resolved, nil
}

// envOrEmpty is `os.Getenv` with the empty string made explicit, so [Resolve] reads
// as the three rules it is rather than as a call with a side effect.
func envOrEmpty(name string) string {
	return os.Getenv(name)
}
