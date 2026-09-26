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
// a string cannot see a symlink; a Vault can, because it knows where the
// campaign directory actually is on this machine, right now.
type Vault struct {
	// root is the absolute, symlink-resolved campaign directory. Every path
	// this package touches is resolved against it and then checked to be
	// inside it, so a symlink planted inside the vault cannot reach outside.
	root string
}

// Open returns a Vault over an existing campaign directory.
//
// The root is resolved through any symlinks itself, because a symlinked data
// directory is a legitimate thing to have and the containment check is only
// meaningful if both sides of the comparison have been resolved.
func Open(root string) (*Vault, error) {
	if root == "" {
		return nil, fmt.Errorf("%w: no vault directory was given", ErrPath)
	}

	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", root, err)
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

	return &Vault{root: resolved}, nil
}

// Root returns the resolved campaign directory.
func (v *Vault) Root() string {
	return v.root
}

// Resolve returns the absolute path of a page's file, having checked that the
// path is a usable page path and that the location it names is inside the
// vault.
//
// The containment check is the reason this is a method and not a function. A
// symlink inside the vault, or a directory component that is one, points
// somewhere else entirely; the deepest existing ancestor of the target is
// resolved and required to be inside the root, which is what makes
// `characters/aria -> /etc` a refusal rather than a read.
func (v *Vault) Resolve(pagePath string) (string, error) {
	checked, err := CheckPagePath(pagePath)
	if err != nil {
		return "", err
	}
	// A page's identity has no extension and its file has one. Adding it here
	// rather than in resolveInside keeps attachments, which are named rather
	// than addressed, out of it.
	return v.resolveInside(checked + pageExtension)
}

// ResolveAttachment returns the absolute path of an attachment, whether it is
// named by its bare name (an attachment lives in one directory) or by the
// vault-relative path a page actually writes, which is what
// `![[_attachments/map-rivergate.png]]` refers to.
func (v *Vault) ResolveAttachment(reference string) (string, error) {
	if reference == "" {
		return "", fmt.Errorf("%w: an attachment reference is empty", ErrPath)
	}

	// A reference is a path first and a bare name second, because that is
	// what a page contains: `![[_attachments/map-rivergate.png]]`.
	if strings.ContainsRune(reference, '/') {
		checked, err := checkReferencePath(reference)
		if err != nil {
			return "", err
		}
		return v.resolveInside(checked)
	}

	name, err := CheckFileName(reference)
	if err != nil {
		return "", err
	}
	return v.resolveInside(attachmentsDir + "/" + name)
}

// resolveInside joins a validated path to the root and refuses anything that
// lands outside it, however it got there.
func (v *Vault) resolveInside(clean string) (string, error) {
	target := filepath.Join(v.root, filepath.FromSlash(clean))

	// filepath.Join cleans, and Join(root, "../x") leaves the root, so this is
	// a real check and not a formality.
	inside, err := contained(v.root, target)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", fmt.Errorf("%w: %q is outside the vault at %s", ErrPath, clean, v.root)
	}

	// Now the part a string check cannot do: resolve the deepest ancestor that
	// exists and require *it* to be inside too. A file that does not exist yet
	// is fine; a directory that is a symlink out of the vault is not.
	resolvedAncestor, err := resolveExisting(target)
	if err != nil {
		return "", err
	}

	inside, err = contained(v.root, resolvedAncestor)
	if err != nil {
		return "", err
	}
	if !inside {
		return "", fmt.Errorf("%w: %q resolves to %s, which is outside the vault at %s",
			ErrPath, clean, resolvedAncestor, v.root)
	}

	return target, nil
}

// contained reports whether target is root or is inside it. The comparison is
// by relative path rather than by string prefix, so /vault-evil does not count
// as being inside /vault.
func contained(root, target string) (bool, error) {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false, fmt.Errorf("comparing %s with %s: %w", target, root, err)
	}
	if relative == "." {
		return true, nil
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && relative != "..", nil
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
			// exists, which cannot happen for a path under a directory the
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

// checkReferencePath validates a vault-relative path a page wrote, which is
// either an attachment reference or something else entirely.
func checkReferencePath(reference string) (string, error) {
	if strings.HasPrefix(reference, "/") {
		return "", fmt.Errorf("%w: %q is absolute; a page refers to things by their path inside the vault", ErrPath, reference)
	}
	if strings.ContainsRune(reference, '\\') {
		return "", fmt.Errorf("%w: %q contains a backslash", ErrPath, reference)
	}
	if strings.ContainsRune(reference, 0) {
		return "", fmt.Errorf("%w: %q contains a NUL byte", ErrPath, reference)
	}

	segments := strings.Split(reference, "/")
	for i, segment := range segments {
		switch segment {
		case "":
			return "", fmt.Errorf("%w: %q has an empty segment at position %d", ErrPath, reference, i+1)
		case ".", "..":
			return "", fmt.Errorf("%w: %q has a %q segment, which does not name a file", ErrPath, reference, segment)
		}
	}

	// A reference is a path, so a page cannot reach into the revision
	// directory or Obsidian's own, whatever it spells.
	switch strings.ToLower(segments[0]) {
	case historyDir, obsidianDir:
		return "", fmt.Errorf("%w: %q points inside the reserved directory %q", ErrPath, reference, segments[0])
	}

	if len(reference) > maxPathBytes {
		return "", fmt.Errorf("%w: a reference may be at most %d bytes, this one is %d", ErrPath, maxPathBytes, len(reference))
	}

	return reference, nil
}
