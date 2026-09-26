package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		wantStdout   []string
		wantStderr   []string
		wantErr      string
		wantNothing  bool
		wantExitCode int
	}{
		{
			name:        "no arguments prints usage to stdout",
			args:        nil,
			wantStdout:  []string{"Usage:", "wiki <command> [flags]", "version", "help"},
			wantNothing: true,
		},
		{
			name:       "version prints build info",
			args:       []string{"version"},
			wantStdout: []string{"dev", "go1."},
		},
		{
			name:       "version ignores extra arguments",
			args:       []string{"version", "--ignored"},
			wantStdout: []string{"dev"},
		},
		{
			name:       "help flag prints usage",
			args:       []string{"--help"},
			wantStdout: []string{"Usage:", "version"},
		},
		{
			name:       "help for a known command",
			args:       []string{"help", "version"},
			wantStdout: []string{"wiki version: print the build version"},
		},
		{
			name:       "help with no command prints usage",
			args:       []string{"help"},
			wantStdout: []string{"Usage:"},
		},
		{
			name:         "help for an unknown command errors",
			args:         []string{"help", "nope"},
			wantStdout:   []string{"Usage:"},
			wantErr:      `unknown command "nope"`,
			wantExitCode: 1,
		},
		{
			name:         "unknown command writes usage to stderr and errors",
			args:         []string{"nope"},
			wantStderr:   []string{"Usage:"},
			wantErr:      `unknown command "nope"`,
			wantExitCode: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			err := run(context.Background(), tt.args, &stdout, &stderr)

			if tt.wantNothing && err != nil {
				t.Fatalf("run(%q) = %v, want nil error", tt.args, err)
			}
			if tt.wantErr == "" && err != nil {
				t.Fatalf("run(%q) = %v, want nil error", tt.args, err)
			}
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("run(%q) = nil, want error containing %q", tt.args, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("run(%q) error = %q, want it to contain %q", tt.args, err, tt.wantErr)
				}
			}

			assertContains(t, "stdout", stdout.String(), tt.wantStdout)
			assertContains(t, "stderr", stderr.String(), tt.wantStderr)
		})
	}
}

// TestUsageListsEveryCommand is the guard against a subcommand being added to
// the map without appearing in the usage text.
func TestUsageListsEveryCommand(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("run(help) = %v, want nil", err)
	}

	for name, cmd := range commands {
		if !strings.Contains(out.String(), name) {
			t.Errorf("usage does not mention command %q", name)
		}
		if cmd.summary == "" {
			t.Errorf("command %q has an empty summary", name)
		}
		if cmd.run == nil {
			t.Errorf("command %q has a nil run function", name)
		}
	}
}

func TestRunHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	// main() swallows context.Canceled so that a Ctrl-C exits quietly. This
	// pins that a cancelled context does not print a scary error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := run(ctx, []string{"version"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("run(version) with cancelled context = %v, want nil", err)
	}
}

// TestExecute covers the arguments a player can actually type.
func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout []string
		wantStderr []string
	}{
		{
			name:       "version succeeds",
			args:       []string{"version"},
			wantCode:   exitOK,
			wantStdout: []string{"dev"},
		},
		{
			name:       "no arguments succeeds and prints usage",
			args:       nil,
			wantCode:   exitOK,
			wantStdout: []string{"Usage:"},
		},
		{
			name:       "unknown command fails and is reported once",
			args:       []string{"nope"},
			wantCode:   exitFailure,
			wantStderr: []string{`wiki: unknown command "nope"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			got := execute(context.Background(), tt.args, &stdout, &stderr)

			if got != tt.wantCode {
				t.Errorf("execute(%q) = %d, want %d", tt.args, got, tt.wantCode)
			}
			assertContains(t, "stdout", stdout.String(), tt.wantStdout)
			assertContains(t, "stderr", stderr.String(), tt.wantStderr)
		})
	}
}

// TestExitCode is the error-to-exit-code mapping. No M0 subcommand blocks, so
// this is the only place a cancellation can be produced on purpose.
func TestExitCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr []string
	}{
		{
			name:     "no error succeeds",
			err:      nil,
			wantCode: exitOK,
		},
		{
			name:     "an ordinary error fails and is reported",
			err:      errors.New("database is locked"),
			wantCode: exitFailure,
			// The message is prefixed and keeps the underlying text, so a
			// player can report it verbatim.
			wantStderr: []string{"wiki: database is locked"},
		},
		{
			name:     "cancellation fails without a message",
			err:      context.Canceled,
			wantCode: exitFailure,
		},
		{
			// A wrapped cancellation is still a cancellation; a long-running
			// command that fails on a drained stream must not scare anyone.
			name:     "wrapped cancellation fails without a message",
			err:      fmt.Errorf("draining streams: %w", context.Canceled),
			wantCode: exitFailure,
		},
		{
			// Matching is by error identity, not by text, so an unrelated
			// failure that merely says "canceled" is still worth reporting.
			name:       "an error that merely mentions cancellation is reported",
			err:        errors.New("context canceled: connection reset by peer"),
			wantCode:   exitFailure,
			wantStderr: []string{"wiki: context canceled: connection reset by peer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			got := exitCode(tt.err, &stderr)

			if got != tt.wantCode {
				t.Errorf("exitCode(%v) = %d, want %d", tt.err, got, tt.wantCode)
			}
			assertContains(t, "stderr", stderr.String(), tt.wantStderr)

			if len(tt.wantStderr) == 0 && stderr.Len() != 0 {
				t.Errorf("exitCode(%v) wrote %q, want nothing on stderr", tt.err, stderr.String())
			}
		})
	}
}

// brokenWriter fails every write, standing in for a closed pipe or a full
// disk. Every write in the CLI is checked, so every one of these paths has to
// reach the caller as an error rather than as a silently truncated page.
type brokenWriter struct{}

var errBroken = errors.New("write failed")

func (brokenWriter) Write([]byte) (int, error) { return 0, errBroken }

func TestWriteFailuresAreReported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		brokenOn string // "stdout" or "stderr"
	}{
		{name: "usage to stdout", args: nil, brokenOn: "stdout"},
		{name: "version to stdout", args: []string{"version"}, brokenOn: "stdout"},
		{name: "help to stdout", args: []string{"help"}, brokenOn: "stdout"},
		{name: "help for a command to stdout", args: []string{"help", "version"}, brokenOn: "stdout"},
		{name: "help for an unknown command to stdout", args: []string{"help", "nope"}, brokenOn: "stdout"},
		{name: "unknown command usage to stderr", args: []string{"nope"}, brokenOn: "stderr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stdout, stderr := io.Writer(&bytes.Buffer{}), io.Writer(&bytes.Buffer{})
			if tt.brokenOn == "stdout" {
				stdout = brokenWriter{}
			} else {
				stderr = brokenWriter{}
			}

			err := run(context.Background(), tt.args, stdout, stderr)
			if !errors.Is(err, errBroken) {
				t.Errorf("run(%q) = %v, want it to report the failed write", tt.args, err)
			}
		})
	}
}

// TestExitCodeIgnoresAFailedReport pins the one case where a write error is
// dropped: there is nowhere left to report it to, and a failure to print a
// failure is not a second failure.
func TestExitCodeIgnoresAFailedReport(t *testing.T) {
	t.Parallel()

	if got := exitCode(errors.New("database is locked"), brokenWriter{}); got != exitFailure {
		t.Errorf("exitCode with a broken stderr = %d, want %d", got, exitFailure)
	}
}

func assertContains(t *testing.T, name, got string, want []string) {
	t.Helper()

	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s = %q, want it to contain %q", name, got, w)
		}
	}
}
