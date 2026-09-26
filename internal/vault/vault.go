package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A Vault is one campaign's directory of markdown files.
//
// It is the only way to reach the filesystem in this package, and the reason is
// that a path is only meaningful relative to something. A sanitiser that checks
// a string cannot see a symlink; a Vault can, because it holds a handle to the
// campaign directory and every file operation is resolved inside it.
//
// # The handle, and why it is not a path
//
// A Vault holds an *os.Root, which is a directory handle rather than a name.
// Every read, write and delete goes through it, and the operating system
// refuses any operation that would leave the tree -- following a symlink out of
// the vault included. That is a stronger property than checking a path and then
// opening it, because there is no window between the check and the open for a
// symlink to appear in. It is also why this package has no method that hands
// back an absolute path to use with os.ReadFile: a caller holding a path has
// left the guarantee behind, and the only reason to want one is a message.
//
// The checks in path.go still run first, for a better error than the operating
// system would give: "is outside the vault at /home/…/blackwater" tells a DM
// what happened, where "path escapes from parent" does not.
//
// # Writes are not serialised here
//
// A Vault does not lock. Two concurrent writes to the same page can leave one
// of them reporting that its temporary file went missing, which is a loud error
// rather than a lost or mixed page: the atomic sequence still holds, and the
// write that lost the race wrote nothing. Callers that write a lot in parallel
// hold the single-writer lock the data directory already has (ADR 0001).
type Vault struct {
	// root is the directory handle every file operation goes through.
	root *os.Root

	// path is the resolved directory, for messages and for Root.
	path string

	// crash is empty in production and names a step in the atomic write in
	// tests, so a test can watch a write die where a real one would. It is a
	// field rather than an argument because no caller outside this package has
	// any business crashing a write on purpose.
	crash crashPoint
}

// Open returns a Vault over an existing campaign directory.
//
// The directory is resolved through any symlinks first, because a symlinked
// data directory is a legitimate thing to have and a handle alone does not give
// a name to put in an error message.
func Open(dir string) (*Vault, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: no vault directory was given", ErrPath)
	}

	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", dir, err)
	}

	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("opening the vault at %s: %w", absolute, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrPath, absolute)
	}

	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", absolute, err)
	}

	root, err := os.OpenRoot(resolved)
	if err != nil {
		return nil, fmt.Errorf("opening the vault at %s: %w", resolved, err)
	}

	v := &Vault{root: root, path: resolved}

	// A crash can leave a temporary file behind, and this is the one moment
	// when removing one cannot race with a live write: a temporary file found
	// when a vault is opened belongs to a process that is no longer running,
	// because one data directory has one server (ADR 0011).
	v.sweepTemporaries()

	return v, nil
}

// Close releases the directory handle. A Vault is not safe to use afterwards,
// and the files it has written are already on disk: nothing is buffered here.
//
// A caller must close it. On Windows the handle is opened without
// FILE_SHARE_DELETE, because that is what Go's syscall.Open does, so an open
// Vault's directory cannot be deleted while the handle is held. That is the
// right behaviour for a data directory -- one process owns it while it is
// running (ADR 0011) -- and the reason every caller in this repository defers a
// close rather than leaving it to a finaliser.
func (v *Vault) Close() error {
	return v.root.Close()
}

// Root returns the resolved campaign directory, for a message or for a caller
// that wants to show a DM where their vault is. It is not a path to open files
// with: see the type comment.
func (v *Vault) Root() string {
	return v.path
}

// pageFile checks a page path and returns it as the slash-separated name this
// package uses inside the vault, extension included.
//
// It is the check half of every operation, and it returns the name the handle
// wants rather than an absolute path, because an absolute path is a name again
// and a name is what a symlink is for.
func (v *Vault) pageFile(pagePath string) (string, error) {
	checked, err := CheckPagePath(pagePath)
	if err != nil {
		return "", err
	}

	name := checked + pageExtension
	if err := v.checkInside(name); err != nil {
		return "", err
	}
	return name, nil
}

// checkInside refuses a name that lands outside the vault, however it gets
// there. It is a check and not the enforcement: the handle enforces it too, and
// this exists to say so in a sentence a DM can act on.
func (v *Vault) checkInside(name string) error {
	target := filepath.Join(v.path, filepath.FromSlash(name))

	// filepath.Join cleans, and Join(root, "../x") leaves the root, so this is
	// a real check and not a formality.
	inside, err := contained(v.path, target)
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("%w: %q is outside the vault at %s", ErrPath, name, v.path)
	}

	// And the part a string comparison cannot do: resolve the deepest ancestor
	// that exists and require *it* to be inside as well. A file that does not
	// exist yet is fine; a directory that is a symlink out of the vault is not.
	resolved, err := resolveExisting(target)
	if err != nil {
		return err
	}

	inside, err = contained(v.path, resolved)
	if err != nil {
		return err
	}
	if !inside {
		return fmt.Errorf("%w: %q resolves to %s, which is outside the vault at %s",
			ErrPath, name, resolved, v.path)
	}

	return nil
}

// contained reports whether target is the root or is inside it. The comparison
// is by relative path rather than by string prefix, so /vault-evil does not
// count as being inside /vault.
func contained(root, target string) (bool, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false, fmt.Errorf("comparing %s with %s: %w", target, root, err)
	}
	if relative == "." {
		return true, nil
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)), nil
}

// resolveExisting resolves the symlinks in the deepest existing ancestor of
// target and reattaches the rest, so a file that does not exist yet still
// yields a checkable path.
func resolveExisting(target string) (string, error) {
	current := target
	var missing []string

	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(append([]string{resolved}, reverse(missing)...)...), nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolving %s: %w", current, err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			// Reached the root of the filesystem without finding anything that
			// exists, which cannot happen for a path inside a directory the
			// caller opened.
			return target, nil
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func reverse(parts []string) []string {
	reversed := make([]string, 0, len(parts))
	for i := len(parts) - 1; i >= 0; i-- {
		reversed = append(reversed, parts[i])
	}
	return reversed
}
