package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// These tests are in package store rather than store_test because the thing
// under test is deliberately unexported: which connection is asked which
// pragma, and how big each pool is. A test that could only see the store from
// outside would be asserting on behaviour rather than on configuration, and
// the configuration is the part ADR 0004 is about.

func openStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "campaigns.db"), Options{
		Clock: fixedClock(),
		IDGen: sequenceIDs(),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return s
}

// TestPragmasAreSetOnEveryConnection is the assertion ADR 0004 asks for, and
// it is per connection on purpose. A pragma set once with an Exec after opening
// applies to whichever connection database/sql happened to use, and a pool of
// four readers would then have three connections with foreign keys off.
func TestPragmasAreSetOnEveryConnection(t *testing.T) {
	t.Parallel()

	s := openStore(t)
	ctx := context.Background()

	// A pool of one will not give a test a second connection, so this asks
	// for more connections than queries at once: every one of them has to
	// report the settings.
	s.read.SetMaxOpenConns(4)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		reads = 24
	)

	for range reads {
		wg.Add(1)
		go func() {
			defer wg.Done()

			if err := verifyPragmas(ctx, s.read, "read"); err != nil {
				mu.Lock()
				t.Errorf("a pooled read connection is not configured correctly: %v", err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if err := verifyPragmas(ctx, s.write, "write"); err != nil {
		t.Errorf("the write connection is not configured correctly: %v", err)
	}
}

func TestPoolSizes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts        Options
		wantReadMax int
	}{
		"the default read pool is DefaultReadPoolSize": {
			opts:        Options{},
			wantReadMax: DefaultReadPoolSize,
		},
		"an explicit read pool size is honoured": {
			opts:        Options{ReadPoolSize: 2},
			wantReadMax: 2,
		},
		"a nonsensical read pool size falls back to the default rather than deadlocking": {
			opts:        Options{ReadPoolSize: -1},
			wantReadMax: DefaultReadPoolSize,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			s, err := Open(context.Background(), filepath.Join(t.TempDir(), "campaigns.db"), tt.opts)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() {
				if err := s.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			}()

			// One write connection is the whole point of ADR 0004's write
			// pool: two writers cannot collide, so SQLITE_BUSY cannot happen
			// between them.
			if got := s.write.Stats().MaxOpenConnections; got != 1 {
				t.Errorf("the write pool allows %d connections, want exactly 1", got)
			}
			if got := s.read.Stats().MaxOpenConnections; got != tt.wantReadMax {
				t.Errorf("the read pool allows %d connections, want %d", got, tt.wantReadMax)
			}
		})
	}
}

func TestDSN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		want    string
		wantErr string
	}{
		{
			name: "every pragma ADR 0004 names is in the connection string",
			path: "/data/campaigns.db",
			want: "file:/data/campaigns.db" +
				"?_pragma=journal_mode(WAL)" +
				"&_pragma=foreign_keys(1)" +
				"&_pragma=busy_timeout(5000)" +
				"&_pragma=synchronous(1)",
		},
		{
			name: "a path with a space is passed through rather than escaped",
			path: "/home/a dm/Campaign Data/campaigns.db",
			want: "file:/home/a dm/Campaign Data/campaigns.db" +
				"?_pragma=journal_mode(WAL)" +
				"&_pragma=foreign_keys(1)" +
				"&_pragma=busy_timeout(5000)" +
				"&_pragma=synchronous(1)",
		},
		{
			name:    "a question mark would truncate the path, so it is refused",
			path:    "/data/what?.db",
			wantErr: "may not contain '?' or '#'",
		},
		{
			name:    "a hash would truncate the path, so it is refused",
			path:    "/data/campaigns#.db",
			wantErr: "may not contain '?' or '#'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := dsn(tt.path)
			if tt.wantErr != "" {
				assertErrorContains(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("dsn(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("dsn(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
