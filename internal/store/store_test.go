package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/clock"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/idgen"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/migrations"
)

func TestOpenCreatesTheDatabaseAndItsDirectory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data", "campaigns")
	path := filepath.Join(dir, "campaigns.db")

	s, err := store.Open(ctx, path, store.Options{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	}()

	// SQLite creates the file on first use rather than at open, so the test
	// asks the store to do something before looking for it.
	if _, err := s.SchemaVersion(ctx); err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("Open did not create %s: %v", path, err)
	}
}

func TestOpenRefusesAnUnusablePath(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path    string
		wantErr string
	}{
		"an empty path":   {path: "", wantErr: "no database path was given"},
		"a question mark": {path: "/data/what?.db", wantErr: "may not contain"},
		"a directory that is a file": {
			path:    filepath.Join(writeAFile(t), "campaigns.db"),
			wantErr: "creating",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := store.Open(context.Background(), tt.path, store.Options{})
			if s != nil {
				_ = s.Close()
				t.Fatal("Open returned a store for a path it should have refused")
			}
			assertErrorContains(t, err, tt.wantErr)
		})
	}
}

func writeAFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("this is a file, not a directory\n"), 0o600); err != nil {
		t.Fatalf("writing a file: %v", err)
	}
	return path
}

func TestMigrateAndSchemaVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "campaigns.db")
	clk := clock.NewFixed(time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC), time.Minute)

	s, err := store.Open(ctx, path, store.Options{Clock: clk, IDGen: idgen.NewSequence("id")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Errorf("closing the store: %v", closeErr)
		}
	}()

	// Open does not migrate: a program that rewrites a database on startup is
	// a program nobody can reason about at 2am.
	before, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion before migrating: %v", err)
	}
	if before.Version != 0 {
		t.Errorf("a freshly opened store reports schema version %d, want 0: Open migrated without being asked", before.Version)
	}

	state, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	latest, err := migrations.Latest()
	if err != nil {
		t.Fatalf("migrations.Latest: %v", err)
	}
	if state.Version != latest {
		t.Errorf("Migrate left the schema at version %d, want the latest shipped version %d", state.Version, latest)
	}

	// Migrating again is a no-op, which is what makes it safe on every boot.
	again, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if again != state {
		t.Errorf("the second Migrate reported %v, want the first result %v", again, state)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "campaigns.db"), store.Options{})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Closing twice is a no-op rather than an error, because a test that
	// fails part way through closes through a deferred call after Open has
	// already closed on its own error path.
	if err := s.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestDefaultOptionsAreUsable(t *testing.T) {
	t.Parallel()

	// Options{} has to work: production wires in the system clock and random
	// UUIDs by leaving them unset, and a store that panicked on a nil clock
	// would be found in a smoke test rather than in a unit test.
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "campaigns.db"), store.Options{})
	if err != nil {
		t.Fatalf("store.Open with default options: %v", err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("closing the store: %v", err)
		}
	}()

	if _, err := s.Migrate(ctx); err != nil {
		t.Errorf("migrating with the default options: %v", err)
	}
}

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()

	switch {
	case err == nil:
		t.Fatalf("no error, want one containing %q", want)
	case !strings.Contains(err.Error(), want):
		t.Fatalf("error %q, want one containing %q", err, want)
	}
}
