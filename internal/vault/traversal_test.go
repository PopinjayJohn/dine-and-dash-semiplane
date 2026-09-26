package vault_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// FuzzCheckPagePath is the path sanitiser's fuzz target, and the invariant it
// defends is one sentence: **a path this package accepts names a file inside
// the vault, and a path it refuses never becomes one.**
//
// A path arrives from a request, a filename, a rename, an import. The fuzzer
// gets to invent them, and the check afterwards is the one that matters: take
// whatever was accepted, join it to a real vault, and require the result to be
// inside. If a string ever slips through the checks and lands outside, this is
// the test that says so.
func FuzzCheckPagePath(f *testing.F) {
	for _, seed := range []string{
		"",
		".",
		"..",
		"...",
		"/",
		"//",
		"a",
		"a/b",
		"a/../b",
		"a/./b",
		"../etc/passwd",
		"/etc/passwd",
		"C:/Windows",
		`C:\Windows`,
		`..\..\windows`,
		"//server/share",
		"_attachments/x",
		"_history/x",
		".obsidian/x",
		".hidden",
		"a/",
		"/a",
		"a//b",
		"a\\b",
		"a\x00b",
		"a\nb",
		"a b",
		"a.",
		"a ",
		"nul",
		"CON.md",
		"aux/thing",
		"locations/" + strings.Repeat("x", 300),
		strings.Repeat("a/", 600) + "b",
		"locations/rivergate",
		"ünïcödé/päge",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		checked, err := vault.CheckPagePath(in)
		if err != nil {
			// A refused path is not a path, so there is nothing to join to a
			// vault. That is the whole contract for this branch.
			if checked != "" {
				t.Errorf("CheckPagePath(%q) returned %q and an error", in, checked)
			}
			return
		}

		// The returned path is the input, not a cleaned version of it: two
		// spellings of one page would be a duplicate identity.
		if checked != in {
			t.Errorf("CheckPagePath(%q) = %q, want the path unchanged", in, checked)
		}

		root := t.TempDir()
		v, err := vault.Open(root)
		if err != nil {
			t.Fatalf("vault.Open: %v", err)
		}

		resolved, err := v.Resolve(checked)
		if err != nil {
			t.Errorf("CheckPagePath accepted %q but Resolve refused it: %v", in, err)
			return
		}

		// The invariant. A path that passes the checks must name a file inside
		// the vault: same directory, and no way out of it.
		relative, relErr := filepath.Rel(root, resolved)
		if relErr != nil {
			t.Fatalf("comparing %s with %s: %v", resolved, root, relErr)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("CheckPagePath accepted %q, which resolved to %s, outside the vault at %s", in, resolved, root)
		}

		// And a sibling directory whose name shares a prefix with the vault is
		// not inside it: /vault-evil is not /vault.
		sibling := root + "-evil"
		if strings.HasPrefix(resolved, sibling) {
			t.Fatalf("a path in %s was accepted as being in %s", resolved, root)
		}
	})
}

// FuzzCheckFileName is the same invariant for a single name, which is what an
// attachment is: a name that passes cannot be a path, and therefore cannot be
// a way out of the attachments directory.
func FuzzCheckFileName(f *testing.F) {
	for _, seed := range []string{
		"",
		".",
		"..",
		"a.png",
		"a/b.png",
		"../a.png",
		"/a.png",
		`C:\a.png`,
		`a\b.png`,
		".env",
		"nul.png",
		"a\x00.png",
		"a\nb.png",
		"a .png",
		"a.png ",
		strings.Repeat("a", 300) + ".png",
		"ünïcödé.png",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		checked, err := vault.CheckFileName(name)
		if err != nil {
			if checked != "" {
				t.Errorf("CheckFileName(%q) returned %q and an error", name, checked)
			}
			return
		}

		if checked != name {
			t.Errorf("CheckFileName(%q) = %q, want the name unchanged", name, checked)
		}

		// An accepted name is one segment: joining it to a directory can only
		// produce a file in that directory.
		if strings.ContainsAny(checked, `/\`) {
			t.Fatalf("CheckFileName accepted %q, which is a path rather than a name", name)
		}

		root := t.TempDir()
		v, err := vault.Open(root)
		if err != nil {
			t.Fatalf("vault.Open: %v", err)
		}

		resolved, err := v.ResolveAttachment(checked)
		if err != nil {
			t.Errorf("CheckFileName accepted %q but ResolveAttachment refused it: %v", name, err)
			return
		}

		relative, relErr := filepath.Rel(filepath.Join(root, "_attachments"), resolved)
		if relErr != nil {
			t.Fatalf("comparing %s: %v", resolved, relErr)
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("CheckFileName accepted %q, which resolved to %s, outside the attachments directory", name, resolved)
		}
	})
}

// TestResolveAttachment checks the two ways a page names an attachment, and
// that a page cannot reach anything else through one.
func TestResolveAttachment(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	v := newVault(t, root)

	tests := map[string]struct {
		in      string
		want    string
		wantErr string
	}{
		"a bare name, which is how a DM names an image": {
			in:   "map-rivergate.png",
			want: filepath.Join(root, "_attachments", "map-rivergate.png"),
		},
		"a vault-relative path, which is what a page embeds": {
			in:   "_attachments/map-rivergate.png",
			want: filepath.Join(root, "_attachments", "map-rivergate.png"),
		},
		"a page elsewhere in the vault": {
			in:   "locations/rivergate.png",
			want: filepath.Join(root, "locations", "rivergate.png"),
		},
		"empty": {
			in:      "",
			wantErr: "an attachment reference is empty",
		},
		"one level up": {
			in:      "../secrets.png",
			wantErr: `has a ".." segment`,
		},
		"an absolute path": {
			in:      "/etc/passwd",
			wantErr: "is absolute",
		},
		"into the revision directory": {
			in:      "_history/locations/rivergate/1-2026-02-14T19-03-00Z.md",
			wantErr: "reserved directory",
		},
		"into Obsidian's own directory": {
			in:      ".obsidian/app.json",
			wantErr: "reserved directory",
		},
		"a backslash where a name was expected": {
			in:      `_attachments\map-rivergate.png`,
			wantErr: "contains a separator",
		},
		"a backslash inside a path, which is a separator on Windows": {
			in:      `_attachments/sub\map-rivergate.png`,
			wantErr: "contains a backslash",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := v.ResolveAttachment(tt.in)
			if tt.wantErr != "" {
				assertPathError(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("ResolveAttachment(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ResolveAttachment(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestResolveRefusesASymlinkedAttachmentDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()

	if err := os.Symlink(outside, filepath.Join(root, "_attachments")); err != nil {
		t.Skipf("this machine will not let the test make a symlink: %v", err)
	}

	v := newVault(t, root)

	_, err := v.ResolveAttachment("map-rivergate.png")
	assertPathError(t, err, "outside the vault")
}
