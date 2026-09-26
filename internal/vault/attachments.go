package vault

import (
	"fmt"
	"io/fs"
	"strings"
)

// Attachments live in `_attachments/` inside the vault and are referred to by
// relative path (ADR 0005), so a vault stays portable when it is zipped or put
// in git. A page writes `![[_attachments/map-rivergate.png]]`; a DM drops a
// file in the folder in Obsidian; both end up in the same place.

// WriteAttachment stores a file in the attachments directory and returns the
// vault-relative reference a page would use to embed it.
//
// The name is checked rather than derived, because a name arrives from an upload
// or a request and a name can be a path. The bytes are written through the same
// atomic sequence as a page: an attachment a reader fetched halfway through a
// write is a broken image, and there is no way for a DM to tell it from one that
// is genuinely missing.
func (v *Vault) WriteAttachment(name string, data []byte) (string, error) {
	checked, err := CheckFileName(name)
	if err != nil {
		return "", err
	}

	reference := attachmentsDir + "/" + checked
	if err := v.writeAtomic(reference, data); err != nil {
		return "", err
	}

	return reference, nil
}

// ReadAttachment returns an attachment's bytes, by the name a page refers to it
// or by the name it has in the attachments directory.
func (v *Vault) ReadAttachment(reference string) ([]byte, error) {
	name, err := v.referenceFile(reference)
	if err != nil {
		return nil, err
	}

	data, err := v.readFile(name)
	if isNotExist(err) {
		return nil, fmt.Errorf("attachment %q: %w", reference, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("reading attachment %q: %w", reference, err)
	}
	return data, nil
}

// DeleteAttachment removes a file from the attachments directory.
//
// A page that embeds it will then render a broken embed, which is the correct
// outcome: the alternative is leaving the bytes and pretending the DM's delete
// did not happen. Obsidian's own behaviour for a missing embed is the same.
func (v *Vault) DeleteAttachment(reference string) error {
	name, err := v.referenceFile(reference)
	if err != nil {
		return err
	}

	if err := v.root.Remove(name); err != nil {
		if isNotExist(err) {
			return fmt.Errorf("attachment %q: %w", reference, ErrNotFound)
		}
		return fmt.Errorf("deleting attachment %q: %w", reference, err)
	}
	return nil
}

// Attachments returns the names of everything in the attachments directory,
// sorted. It is what a DM sees when they open the folder, and what `wiki
// export` will need in M12.
func (v *Vault) Attachments() ([]string, error) {
	names, err := v.readDir(attachmentsDir)
	if err != nil {
		return nil, err
	}

	files := make([]string, 0, len(names))
	for _, name := range names {
		if !strings.HasPrefix(name, tempPrefix) {
			files = append(files, name)
		}
	}

	return files, nil
}

// referenceFile checks a reference from a page -- an attachment, an image -- and
// returns it as the name to use inside the vault.
//
// A bare name is an attachment, which lives in one directory. A path is a path,
// because that is what a page writes: `![[_attachments/map-rivergate.png]]`.
func (v *Vault) referenceFile(reference string) (string, error) {
	if reference == "" {
		return "", fmt.Errorf("%w: an attachment reference is empty", ErrPath)
	}

	if !strings.ContainsRune(reference, '/') {
		name, err := CheckFileName(reference)
		if err != nil {
			return "", err
		}
		reference = attachmentsDir + "/" + name
	} else if _, err := checkReferencePath(reference); err != nil {
		return "", err
	}

	if err := v.checkInside(reference); err != nil {
		return "", err
	}
	return reference, nil
}

// checkReferencePath validates a vault-relative path a page wrote, which is
// either an attachment reference or something else entirely.
func checkReferencePath(reference string) (string, error) {
	switch {
	case strings.HasPrefix(reference, "/"):
		return "", fmt.Errorf("%w: %q is absolute; a page refers to things by their path inside the vault", ErrPath, reference)
	case strings.ContainsRune(reference, '\\'):
		return "", fmt.Errorf("%w: %q contains a backslash", ErrPath, reference)
	case strings.ContainsRune(reference, 0):
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

	// A reference is a path, so a page cannot reach into the revision directory
	// or Obsidian's own, whatever it spells.
	if first, _, _ := strings.Cut(reference, "/"); unreadableDirs[first] {
		return "", fmt.Errorf("%w: %q points inside the reserved directory %q", ErrPath, reference, first)
	}

	if len(reference) > maxPathBytes {
		return "", fmt.Errorf("%w: a reference may be at most %d bytes, this one is %d", ErrPath, maxPathBytes, len(reference))
	}

	return reference, nil
}

// readFile and readDir are the two reads the handle does, with the same
// path in both, so there is one place that knows a name is a name.
func (v *Vault) readFile(name string) ([]byte, error) {
	return v.root.ReadFile(name)
}

func (v *Vault) readDir(name string) ([]string, error) {
	entries, err := fs.ReadDir(v.root.FS(), name)
	if isNotExist(err) {
		// A directory that is not there is an empty one, not a failure: a
		// vault with no revisions yet has no _history.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", name, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	return names, nil
}
