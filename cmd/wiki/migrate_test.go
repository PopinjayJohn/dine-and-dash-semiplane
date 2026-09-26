package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
	"github.com/popinjayjohn/dine-and-dash-semiplane/migrations"
)

// runMigrateCommand runs the CLI the way a user does, through the same entry
// point every other subcommand goes through, and returns what came out.
func runMigrateCommand(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	var out, errOut bytes.Buffer
	code = execute(context.Background(), append([]string{"migrate"}, args...), &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestMigrateAppliesMigrations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runMigrateCommand(t, "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki migrate exited %d: %s", code, stderr)
	}

	// The command says which database it touched, every time. A migration
	// command that only says "migrated" gets run twice in the wrong place.
	want := filepath.Join(dir, "campaigns.db")
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want it to name the database it touched (%q)", stdout, want)
	}

	latest, err := migrations.Latest()
	if err != nil {
		t.Fatalf("migrations.Latest: %v", err)
	}
	if wantState := "version " + strconv.Itoa(latest); !strings.Contains(stdout, wantState) {
		t.Errorf("stdout = %q, want it to report %q", stdout, wantState)
	}
	if !strings.Contains(stdout, "clean") {
		t.Errorf("stdout = %q, want it to report a clean schema", stdout)
	}

	if _, err := os.Stat(want); err != nil {
		t.Errorf("wiki migrate did not create %s: %v", want, err)
	}

	// Running it again is a no-op, and says the same thing, because a DM will
	// run it again.
	again, _, secondCode := runMigrateCommand(t, "-data-dir", dir)
	if secondCode != 0 {
		t.Fatalf("the second wiki migrate exited %d", secondCode)
	}
	if again != stdout {
		t.Errorf("the second run reported %q, the first %q", again, stdout)
	}
}

func TestMigrateStatusChangesNothing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	database := filepath.Join(dir, "campaigns.db")

	// Before there is a database, asking is an error rather than a quiet
	// creation of an empty one.
	_, stderr, code := runMigrateCommand(t, "-status", "-data-dir", dir)
	if code != exitFailure {
		t.Errorf("wiki migrate -status on a missing database exited %d, want %d", code, exitFailure)
	}
	if !strings.Contains(stderr, "no database at") {
		t.Errorf("stderr = %q, want it to say there is no database", stderr)
	}
	if _, err := os.Stat(database); err == nil {
		t.Error("wiki migrate -status created the database: asking a question wrote to disk")
	}

	if _, _, migrateCode := runMigrateCommand(t, "-data-dir", dir); migrateCode != 0 {
		t.Fatalf("wiki migrate exited %d", migrateCode)
	}

	before, err := os.Stat(database)
	if err != nil {
		t.Fatalf("stat after migrating: %v", err)
	}

	stdout, _, code := runMigrateCommand(t, "-status", "-data-dir", dir)
	if code != 0 {
		t.Fatalf("wiki migrate -status exited %d", code)
	}
	if !strings.Contains(stdout, "clean") {
		t.Errorf("stdout = %q, want the schema version of the migrated database", stdout)
	}

	after, err := os.Stat(database)
	if err != nil {
		t.Fatalf("stat after the status query: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("wiki migrate -status wrote to the database file")
	}
}

func TestMigrateUsesTheEnvironmentWhenNoPathIsGiven(t *testing.T) {
	// Not parallel: the test sets an environment variable, and t.Setenv
	// forbids that alongside t.Parallel.
	dir := t.TempDir()
	t.Setenv(datadir.EnvVar, dir)

	stdout, stderr, code := runMigrateCommand(t)
	if code != 0 {
		t.Fatalf("wiki migrate exited %d: %s", code, stderr)
	}

	want := filepath.Join(dir, "campaigns.db")
	if !strings.Contains(stdout, want) {
		t.Errorf("stdout = %q, want the database under %s", stdout, dir)
	}
}

func TestMigrateRefuses(t *testing.T) {
	// Every case names its own data directory, so nothing a test does lands on
	// a real machine's data directory. The cases that would otherwise get
	// there do not reach the store at all, which is the point of them.
	tests := map[string]struct {
		args         []string
		dataDir      string
		wantStderr   string
		wantExitCode int
	}{
		"an unknown flag is named": {
			args:         []string{"-datadir", "/tmp/nope"},
			wantStderr:   "flag provided but not defined",
			wantExitCode: exitFailure,
		},
		"an unknown flag also shows what the flags are": {
			args:         []string{"-nope"},
			wantStderr:   "-data-dir",
			wantExitCode: exitFailure,
		},
		"a positional argument is not a data directory": {
			args:         []string{"/tmp/nope"},
			wantStderr:   "migrate takes no arguments",
			wantExitCode: exitFailure,
		},
		"a path the connection string cannot carry is refused before anything is created": {
			dataDir:      "/tmp/what?.db",
			wantStderr:   "may not contain",
			wantExitCode: exitFailure,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dir := tt.dataDir
			if dir == "" {
				dir = t.TempDir()
			}

			args := append([]string{"-data-dir", dir}, tt.args...)

			_, stderr, code := runMigrateCommand(t, args...)
			if code != tt.wantExitCode {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.wantExitCode, stderr)
			}
			if !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantStderr)
			}
		})
	}
}

func TestMigrateHelp(t *testing.T) {
	t.Parallel()

	stdout, _, code := runMigrateCommand(t, "-h")
	if code != 0 {
		t.Errorf("wiki migrate -h exited %d, want 0", code)
	}
	for _, want := range []string{"wiki migrate", "-data-dir", "-status", datadir.EnvVar} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want it to mention %q", stdout, want)
		}
	}
}

func TestMigrateAppearsInUsage(t *testing.T) {
	t.Parallel()

	// The usage text is generated from the commands map, so a subcommand that
	// is not in the map cannot be documented and one that is cannot be
	// missing. This is the test that keeps that true.
	var out bytes.Buffer
	if err := usage(&out); err != nil {
		t.Fatalf("usage: %v", err)
	}

	for _, want := range []string{"migrate", "apply database migrations"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("usage does not mention %q:\n%s", want, out.String())
		}
	}
}
