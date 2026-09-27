package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/config"
)

// write is a config file with the given contents, in a data directory of its own.
func write(t *testing.T, contents string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return dir
}

// TestThereIsNoConfigAndThatIsFine is the ordinary case, and it is a test because
// `internal/datadir` deliberately declined to read a config file at all for six
// milestones on the grounds that a loader which silently ignored one would be worse
// than no loader. Now there is a loader, and the thing it must not do is fail.
func TestThereIsNoConfigAndThatIsFine(t *testing.T) {
	t.Parallel()

	got, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load with no config file: %v", err)
	}

	if got.Listen != config.DefaultListen {
		t.Errorf("Listen = %q, want the default %q", got.Listen, config.DefaultListen)
	}
	if got.BaseURL != "" {
		t.Errorf("BaseURL = %q, want empty so the served address is used", got.BaseURL)
	}
	if len(got.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want none: a deployment that trusted nothing is "+
			"the safe default", got.TrustedProxies)
	}
}

// TestTheFileIsRead is the feature, and the assertion is that the three settings land
// — a loader that read the file and applied none of it is the failure this package's
// doc comment is about.
func TestTheFileIsRead(t *testing.T) {
	t.Parallel()

	dir := write(t, `
listen: 0.0.0.0:9000
base_url: https://wiki.example.com
trusted_proxies:
  - 10.0.0.1
  - 192.168.1.0/24
`)

	got, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.Listen != "0.0.0.0:9000" {
		t.Errorf("Listen = %q, want 0.0.0.0:9000", got.Listen)
	}
	if got.BaseURL != "https://wiki.example.com" {
		t.Errorf("BaseURL = %q, want https://wiki.example.com", got.BaseURL)
	}
	if len(got.TrustedProxies) != 2 || got.TrustedProxies[0] != "10.0.0.1" {
		t.Errorf("TrustedProxies = %v, want the file's two entries in order", got.TrustedProxies)
	}
}

// TestTheEnvironmentBeatsTheFile is ADR 0011's sentence, and the whole of it: the
// environment overrides, rather than merging field by field with a different
// precedence per key.
func TestTheEnvironmentBeatsTheFile(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		file  string
		check func(*testing.T, config.Settings)
	}{
		{
			name: "listen from the environment",
			env:  map[string]string{config.EnvListen: "0.0.0.0:9999"},
			file: "listen: 127.0.0.1:1234\n",
			check: func(t *testing.T, got config.Settings) {
				if got.Listen != "0.0.0.0:9999" {
					t.Errorf("Listen = %q, want the environment's", got.Listen)
				}
			},
		},
		{
			name: "base_url from the environment",
			env:  map[string]string{config.EnvBaseURL: "https://env.example.com"},
			file: "base_url: https://file.example.com\n",
			check: func(t *testing.T, got config.Settings) {
				if got.BaseURL != "https://env.example.com" {
					t.Errorf("BaseURL = %q, want the environment's", got.BaseURL)
				}
			},
		},
		{
			// The per-key merge ADR 0011 rules out: the environment's list *replaces*
			// the file's rather than joining it, and the file's entry is gone
			// entirely. A join is a merge, and the merge is the thing forbidden — not
			// the per-key override, which is what the ADR actually says.
			name: "the environment replaces the list rather than joining it",
			env:  map[string]string{config.EnvTrustedProxies: "172.16.0.1, 172.16.0.2"},
			file: "trusted_proxies:\n  - 10.0.0.1\n",
			check: func(t *testing.T, got config.Settings) {
				if len(got.TrustedProxies) != 2 {
					t.Fatalf("TrustedProxies = %v, want the environment's two entries", got.TrustedProxies)
				}
				for _, proxy := range got.TrustedProxies {
					if proxy == "10.0.0.1" {
						t.Errorf("TrustedProxies = %v, want the file's entry replaced, not joined", got.TrustedProxies)
					}
				}
			},
		},
		{
			name: "an environment value that is whitespace is not a value",
			env:  map[string]string{config.EnvListen: "   "},
			file: "listen: 127.0.0.1:1234\n",
			check: func(t *testing.T, got config.Settings) {
				if got.Listen != "127.0.0.1:1234" {
					t.Errorf("Listen = %q, want the file's: a variable set to spaces is unset",
						got.Listen)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// `t.Setenv` and `t.Parallel` do not mix, and this is a process-wide
			// setting, so the table is sequential.
			for name, value := range test.env {
				t.Setenv(name, value)
			}

			got, err := config.Load(write(t, test.file))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			test.check(t, got)
		})
	}
}

// TestAMisspelledKeyIsRefused is the property that makes a config file worth having:
// a DM who writes `base_url_` and finds that their `base_url` did not take effect has
// no way to debug that, and the whole reason for the file is that they did not have
// to read this source.
func TestAMisspelledKeyIsRefused(t *testing.T) {
	t.Parallel()

	_, err := config.Load(write(t, "base_url_: https://typo.example.com\n"))
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), config.FileName) {
		t.Errorf("the error does not name the file: %v", err)
	}
	if !strings.Contains(err.Error(), "base_url_") {
		t.Errorf("the error does not name the key: %v", err)
	}
}

// TestBrokenYAMLIsAnErrorAndSaysWhichFile is the other half of "a loader that
// silently ignored a file would be worse than no loader", and the message has to
// carry the path because the DM has three data directories and one of them has the
// typo in it.
func TestBrokenYAMLIsAnErrorAndSaysWhichFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
	}{
		{name: "not YAML at all", contents: "listen: [unclosed\n"},
		{name: "a scalar where a mapping belongs", contents: "just a string\n"},
		{name: "a mapping under a key that is not one", contents: "listen:\n  - a\n  - b\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := config.Load(write(t, test.contents))
			if err == nil {
				t.Fatal("a broken config file was accepted")
			}
			if !strings.Contains(err.Error(), config.FileName) {
				t.Errorf("the error does not name the file: %v", err)
			}
		})
	}
}

// TestAConfigFileThatIsADirectoryIsAnError is a small one, and it is here because
// `os.ErrNotExist` is the *only* "there is no file" answer this package accepts, and
// the alternative — carrying on with defaults — is a DM with a config file the wiki
// has decided does not exist.
func TestAConfigFileThatIsADirectoryIsAnError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, config.FileName), 0o700); err != nil {
		t.Fatalf("making a directory named config.yaml: %v", err)
	}

	if _, err := config.Load(dir); err == nil {
		t.Error("a config.yaml that is a directory was accepted")
	}
}

// TestTheDefaultListenIsLoopback is a test of a constant, and it is here because the
// constant is a security decision rather than a convenience: this is a wiki holding a
// campaign's secrets with a share link in front of it, and every interface is the
// right default for a service and the wrong one for this.
func TestTheDefaultListenIsLoopback(t *testing.T) {
	t.Parallel()

	if config.DefaultListen != "127.0.0.1:8080" {
		t.Errorf("DefaultListen = %q, want 127.0.0.1:8080", config.DefaultListen)
	}
}
