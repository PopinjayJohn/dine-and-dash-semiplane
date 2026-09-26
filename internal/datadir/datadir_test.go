package datadir_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/datadir"
)

func TestResolve(t *testing.T) {
	// Not parallel, and neither are its subtests: this sets an environment
	// variable, and t.Setenv panics under a parallel ancestor.
	tests := []struct {
		name     string
		explicit string
		env      string
		want     string
	}{
		{
			name:     "an explicit path wins over everything",
			explicit: filepath.Join("/data", "one"),
			env:      filepath.Join("/data", "two"),
			want:     filepath.Join("/data", "one"),
		},
		{
			name: "the environment is used when no path was given",
			env:  filepath.Join("/data", "two"),
			want: filepath.Join("/data", "two"),
		},
		{
			name:     "a trailing separator is cleaned away, so two spellings are one path",
			explicit: filepath.Join("/data", "one") + string(filepath.Separator),
			want:     filepath.Join("/data", "one"),
		},
		{
			name:     "a relative path is kept relative rather than made absolute",
			explicit: "data",
			want:     "data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(datadir.EnvVar, tt.env)

			got, err := datadir.Resolve(tt.explicit)
			if err != nil {
				t.Fatalf("Resolve(%q) with %s=%q: %v", tt.explicit, datadir.EnvVar, tt.env, err)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.explicit, got, tt.want)
			}
		})
	}
}

// TestResolveFallsBackToTheDefault checks the last rule without touching the
// filesystem: nothing in this package creates anything, and the caller decides
// when a data directory appears.
func TestResolveFallsBackToTheDefault(t *testing.T) {
	// t.Setenv("") unsets the variable, so the default is what is left.
	t.Setenv(datadir.EnvVar, "")
	t.Setenv("LOCALAPPDATA", "")

	got, err := datadir.Resolve("")
	if err != nil {
		t.Fatalf("Resolve(\"\"): %v", err)
	}

	want, err := datadir.Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	if got != want {
		t.Errorf("Resolve(\"\") = %q, want the default %q", got, want)
	}
	if !strings.HasSuffix(got, "dine-and-dash-semiplane") {
		t.Errorf("the default data directory is %q, want it to end in the application name", got)
	}
}

func TestDefaultOnWindowsAndUnix(t *testing.T) {
	tests := map[string]struct {
		localAppData string
		wantBase     string
	}{
		"windows uses the per-user application data directory": {
			localAppData: filepath.Join("C:", "Users", "a dm", "AppData", "Local"),
			wantBase:     filepath.Join("C:", "Users", "a dm", "AppData", "Local"),
		},
		"unix uses .local/share under the home directory": {
			localAppData: "",
			wantBase:     "",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// Not parallel: this sets an environment variable.
			t.Setenv("LOCALAPPDATA", tt.localAppData)

			got, err := datadir.Default()
			if err != nil {
				t.Fatalf("Default(): %v", err)
			}

			if !strings.HasSuffix(got, "dine-and-dash-semiplane") {
				t.Errorf("Default() = %q, want it to end in the application name", got)
			}
			if tt.wantBase == "" {
				if !strings.Contains(got, filepath.Join(".local", "share")) {
					t.Errorf("Default() = %q, want it under .local/share", got)
				}
				return
			}
			if !strings.HasPrefix(got, tt.wantBase) {
				t.Errorf("Default() = %q, want it under %q", got, tt.wantBase)
			}
		})
	}
}

func TestDatabaseFile(t *testing.T) {
	t.Parallel()

	got := datadir.DatabaseFile(filepath.Join("/data", "blackwater"))

	if want := "campaigns.db"; !strings.HasSuffix(got, want) {
		t.Errorf("DatabaseFile = %q, want it to end in %q", got, want)
	}
	if !strings.Contains(got, "blackwater") {
		t.Errorf("DatabaseFile = %q, want the data directory in it", got)
	}
}
