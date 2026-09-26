package vault_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

func TestCheckPagePath(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      string
		want    string
		wantErr string
	}{
		"a plain page": {
			in:   "notes",
			want: "notes",
		},
		"a nested page": {
			in:   "locations/rivergate",
			want: "locations/rivergate",
		},
		"a deeply nested page": {
			in:   "characters/aria/quests/the-toll",
			want: "characters/aria/quests/the-toll",
		},
		"a name with dots in it, which is a date and not an extension": {
			in:   "sessions/2026-02-14-dragon-heist",
			want: "sessions/2026-02-14-dragon-heist",
		},
		"a name with a space": {
			in:   "npcs/the drowned hound",
			want: "npcs/the drowned hound",
		},
		"a name with a non-ASCII letter": {
			in:   "locations/Ölbach",
			want: "locations/Ölbach",
		},
		"empty": {
			in:      "",
			wantErr: "a page path is empty",
		},
		"one level up": {
			in:      "../secrets",
			wantErr: `has a ".." segment`,
		},
		"one level up, in the middle": {
			in:      "locations/../../etc/passwd",
			wantErr: `has a ".." segment`,
		},
		"the current directory": {
			in:      "locations/.",
			wantErr: `has a "." segment`,
		},
		"a path that only needs cleaning to be valid, which would be two spellings of one page": {
			in:      "locations/./rivergate",
			wantErr: `has a "." segment`,
		},
		"an empty segment from a doubled separator": {
			in:      "locations//rivergate",
			wantErr: "an empty segment",
		},
		"an absolute path": {
			in:      "/etc/passwd",
			wantErr: "is absolute",
		},
		"a Windows absolute path, which names a volume": {
			in:      "C:/Windows/System32",
			wantErr: "names a volume",
		},
		"a Windows volume-relative path": {
			in:      "C:secrets",
			wantErr: "names a volume",
		},
		"a UNC path": {
			in:      "//server/share/secrets",
			wantErr: "is absolute",
		},
		"a backslash, which is a separator on Windows and an ordinary character here": {
			in:      `locations\rivergate`,
			wantErr: "contains a backslash",
		},
		"a trailing separator": {
			in:      "locations/",
			wantErr: "ends in a separator",
		},
		"a NUL byte": {
			in:      "locations/rivergate\x00.md",
			wantErr: "NUL byte",
		},
		"a control character": {
			in:      "locations/river\ngate",
			wantErr: "control character",
		},
		"a hidden file": {
			in:      ".hidden",
			wantErr: "starts with a dot",
		},
		"a hidden directory": {
			in:      ".config/rivergate",
			wantErr: "starts with a dot",
		},
		"inside the attachments directory": {
			in:      "_attachments/map-rivergate",
			wantErr: "reserved directory",
		},
		"inside the history directory": {
			in:      "_history/locations/rivergate/1",
			wantErr: "reserved directory",
		},
		"Obsidian's own directory": {
			in:      ".obsidian/plugins",
			wantErr: "starts with a dot",
		},
		"the attachments directory spelled in another case, because a filesystem may not care": {
			in:      "_ATTACHMENTS/map-rivergate",
			wantErr: "reserved directory",
		},
		"a device name": {
			in:      "locations/nul",
			wantErr: "device name",
		},
		"a device name with an extension": {
			in:      "locations/COM1.md",
			wantErr: "device name",
		},
		"a segment ending in a space, which Windows rewrites": {
			in:      "locations/rivergate ",
			wantErr: "ending in a dot or a space",
		},
		"a segment ending in a dot, which Windows drops": {
			in:      "locations/rivergate.",
			wantErr: "ending in a dot or a space",
		},
		"a segment over the length limit": {
			in:      "locations/" + strings.Repeat("a", 300),
			wantErr: "the limit is 200",
		},
		"a path over the length limit": {
			in:      strings.Repeat("segment/", 200) + "page",
			wantErr: "may be at most 1024 bytes",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := vault.CheckPagePath(tt.in)
			if tt.wantErr != "" {
				assertPathError(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("CheckPagePath(%q) returned an unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("CheckPagePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCheckFileName(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		in      string
		want    string
		wantErr string
	}{
		"an image": {
			in:   "map-rivergate.png",
			want: "map-rivergate.png",
		},
		"a name with a space": {
			in:   "the drowned hound.png",
			want: "the drowned hound.png",
		},
		"a path is not a name": {
			in:      "_attachments/map-rivergate.png",
			wantErr: "contains a separator",
		},
		"one level up": {
			in:      "../map-rivergate.png",
			wantErr: "contains a separator",
		},
		"a Windows path is not a name": {
			in:      `C:\map-rivergate.png`,
			wantErr: "contains a separator",
		},
		"empty": {
			in:      "",
			wantErr: "an attachment name is empty",
		},
		"a dot": {
			in:      ".",
			wantErr: "is not a file name",
		},
		"two dots": {
			in:      "..",
			wantErr: "is not a file name",
		},
		"a hidden file": {
			in:      ".env",
			wantErr: "starts with a dot",
		},
		"a NUL byte": {
			in:      "map\x00.png",
			wantErr: "control character",
		},
		"a device name": {
			in:      "nul.png",
			wantErr: "device name",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := vault.CheckFileName(tt.in)
			if tt.wantErr != "" {
				assertPathError(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("CheckFileName(%q) returned an unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("CheckFileName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestReadRefusesWhatCheckPagePathRefuses is the half of the contract that is
// about errors: a path the sanitiser refuses does not reach the filesystem, and
// a path it accepts is looked for and reported as missing rather than refused.
func TestReadRefusesWhatCheckPagePathRefuses(t *testing.T) {
	t.Parallel()

	v := newVault(t, t.TempDir())

	tests := map[string]struct {
		in      string
		wantErr string
	}{
		"a page that is not in the vault yet": {
			in: "locations/rivergate",
		},
		"a page at the top of the vault": {
			in: "campaign",
		},
		"a traversal is refused before anything is looked at": {
			in:      "../outside",
			wantErr: `has a ".." segment`,
		},
		"an absolute path is refused": {
			in:      "/etc/passwd",
			wantErr: "is absolute",
		},
		"a page inside the attachments directory is refused": {
			in:      "_attachments/map-rivergate",
			wantErr: "reserved directory",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := v.ReadBytes(tt.in)
			if tt.wantErr != "" {
				assertPathError(t, err, tt.wantErr)
				return
			}
			if !errors.Is(err, vault.ErrNotFound) {
				t.Errorf("ReadBytes(%q) = %v, want an error matching ErrNotFound: the path was accepted", tt.in, err)
			}
		})
	}
}

// TestResolveRefusesASymlinkOutOfTheVault is the reason Resolve is a method and
// not a function. No string check can see this one: the path is a clean, valid,
// relative page path that resolves, on this machine, to a directory outside the
// campaign.
func TestResolveRefusesASymlinkOutOfTheVault(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("not the DM's campaign\n"), 0o600); err != nil {
		t.Fatalf("writing the file outside the vault: %v", err)
	}

	// A directory inside the vault that is really a way out of it.
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("this machine will not let the test make a symlink: %v", err)
	}

	v := newVault(t, root)

	_, err := v.ReadBytes("escape/secret")
	assertPathError(t, err, "outside the vault")

	// And the file outside is exactly where it was put: nothing was read or
	// moved, the path was refused.
	if _, err := os.Stat(filepath.Join(outside, "secret.md")); err != nil {
		t.Fatalf("the file outside the vault is not where it was put: %v", err)
	}
}

// TestResolveRefusesASymlinkedFile is the same hole with a file at the end of
// it, which a check that only looked at directories would miss.
func TestResolveRefusesASymlinkedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()

	target := filepath.Join(outside, "elsewhere.md")
	if err := os.WriteFile(target, []byte("not the DM's campaign\n"), 0o600); err != nil {
		t.Fatalf("writing the file outside the vault: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(root, "rivergate.md")); err != nil {
		t.Skipf("this machine will not let the test make a symlink: %v", err)
	}

	v := newVault(t, root)

	_, err := v.ReadBytes("rivergate")
	assertPathError(t, err, "outside the vault")
}

func TestOpenRefuses(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		root    func(t *testing.T) string
		wantErr string
	}{
		"no directory at all": {
			root:    func(*testing.T) string { return "" },
			wantErr: "no vault directory was given",
		},
		"a directory that is not there": {
			root:    func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
			wantErr: "opening the vault at",
		},
		"a path that is a file": {
			root: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "campaigns")
				if err := os.WriteFile(path, []byte("a file\n"), 0o600); err != nil {
					t.Fatalf("writing a file: %v", err)
				}
				return path
			},
			wantErr: "is not a directory",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			v, err := vault.Open(tt.root(t))
			if err == nil {
				// Close before the failure, for the same reason every other
				// open in this package closes: a leaked handle is a directory
				// the operating system may refuse to delete.
				_ = v.Close()
				t.Fatal("Open accepted something it should have refused")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q, want one containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestOpenResolvesTheRootItself(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	campaign := filepath.Join(base, "campaign")
	if err := os.Mkdir(campaign, 0o700); err != nil {
		t.Fatalf("creating the campaign directory: %v", err)
	}

	link := filepath.Join(base, "linked")
	if err := os.Symlink(campaign, link); err != nil {
		t.Skipf("this machine will not let the test make a symlink: %v", err)
	}

	// A symlinked data directory is a legitimate thing to have, so the
	// containment check compares resolved paths on both sides.
	v := newVault(t, link)
	defer func() { _ = v.Close() }()

	// The expectation is resolved the same way the vault resolves it, which is
	// the whole reason this comparison is written this way and not against the
	// path the test happened to be given. On macOS a temporary directory is
	// reached through /var, which is a symlink to /private/var, and on Windows
	// the runner's home directory has both a long and an 8.3 spelling of
	// itself. A resolved path differs from the one handed in on both, and the
	// test that compared the two said so out loud on a Linux runner.
	resolved, err := filepath.EvalSymlinks(campaign)
	if err != nil {
		t.Fatalf("resolving the campaign directory: %v", err)
	}

	if v.Root() != resolved {
		t.Errorf("Root() = %q, want the resolved directory %q", v.Root(), resolved)
	}
	if v.Root() == link {
		t.Error("Root() is the symlink rather than the directory it points at: the containment check would be comparing against something that can be moved")
	}

	// A page inside it is a missing page, not a refused path.
	if _, err := v.ReadBytes("locations/rivergate"); !errors.Is(err, vault.ErrNotFound) {
		t.Errorf("ReadBytes through a symlinked root = %v, want ErrNotFound", err)
	}
}

// newVault opens a vault and closes it when the test ends. The close is a
// cleanup rather than a defer because a Vault holds an open directory handle,
// and a leaked one is a directory the operating system may refuse to delete.
func newVault(t *testing.T, root string) *vault.Vault {
	t.Helper()

	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("vault.Open(%q): %v", root, err)
	}

	t.Cleanup(func() { _ = v.Close() })

	return v
}

func assertPathError(t *testing.T, err error, want string) {
	t.Helper()

	switch {
	case err == nil:
		t.Fatalf("no error, want one containing %q", want)
	case !strings.Contains(err.Error(), want):
		t.Fatalf("error %q, want one containing %q", err, want)
	}
}
