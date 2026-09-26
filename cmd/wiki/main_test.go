package main

import (
	"bytes"
	"context"
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

func assertContains(t *testing.T, name, got string, want []string) {
	t.Helper()

	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s = %q, want it to contain %q", name, got, w)
		}
	}
}
