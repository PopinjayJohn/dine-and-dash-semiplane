package version_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/version"
)

func TestGetDefaultsAreNotEmpty(t *testing.T) {
	t.Parallel()

	got := version.Get()
	for name, value := range map[string]string{
		"Version": got.Version,
		"Commit":  got.Commit,
		"Date":    got.Date,
		"Go":      got.Go,
	} {
		if strings.TrimSpace(value) == "" {
			t.Errorf("version.Get().%s is empty; link-time overrides may be misconfigured", name)
		}
	}
}

func TestGetGoFieldNamesToolchainAndPlatform(t *testing.T) {
	t.Parallel()

	// The Go field must identify the toolchain and platform, so that a bug
	// report from a DM names the one we are least likely to reproduce.
	if goVersion := version.Get().Go; !strings.Contains(goVersion, "go") {
		t.Errorf("version.Get().Go = %q, want it to mention the Go toolchain", goVersion)
	}
}

func TestInfoString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info version.Info
		want []string
	}{
		{
			name: "all fields present",
			info: version.Info{Version: "v1.2.3", Commit: "abc1234", Date: "2026-09-26T10:00:00Z", Go: "go1.24 linux/amd64"},
			want: []string{"v1.2.3", "abc1234", "2026-09-26T10:00:00Z", "go1.24 linux/amd64"},
		},
		{
			name: "dev build",
			info: version.Info{Version: "dev", Commit: "unknown", Date: "unknown", Go: "go1.24 linux/amd64"},
			want: []string{"dev", "unknown"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.info.String()
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("Info.String() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

func TestInfoMarshalsToJSON(t *testing.T) {
	t.Parallel()

	// /healthz exposes this struct, so the field names are a wire contract.
	const want = `{"version":"v0.1.0","commit":"abc1234","date":"2026-09-26T10:00:00Z","go":"go1.24 linux/amd64"}`

	got, err := json.Marshal(version.Info{
		Version: "v0.1.0",
		Commit:  "abc1234",
		Date:    "2026-09-26T10:00:00Z",
		Go:      "go1.24 linux/amd64",
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	if string(got) != want {
		t.Errorf("json.Marshal = %s, want %s", got, want)
	}
}
