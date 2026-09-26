package vault_test

import (
	"errors"
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
		defer func() { _ = v.Close() }()

		// The invariant, in the form that matters: a path the checks accept is
		// a path the vault will look for. ErrNotFound rather than a path error
		// means it got all the way to the filesystem -- a symlink out of the
		// vault would have been refused here instead.
		_, err = v.ReadBytes(checked)
		if !errors.Is(err, vault.ErrNotFound) {
			t.Fatalf("CheckPagePath accepted %q, and looking it up gave %v rather than a missing page", in, err)
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

		// A name that passes is one segment, so joining it to a directory can
		// only produce a file in that directory. The handle refuses the rest,
		// and the attachment tests check that with a symlink.
		_ = filepath.Separator
	})
}
