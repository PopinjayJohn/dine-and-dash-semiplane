package vault

import (
	"fmt"
	"path"
	"strings"
)

// Page paths are the security boundary of this package. Everything else here is
// about a DM's prose; a page path is what a request, a file on disk and a
// database row all have to agree on, and a path that can be made to point
// outside the vault is a DM's whole campaign readable by whoever finds the hole.
//
// The rules, in the order they are checked, and why each exists:
//
//   - No empty segments, and no "." or ".." among them. `../` is the obvious
//     one; `.` is the same thing written differently, and a path that cleans
//     to a different path than it says is a path nobody can reason about.
//   - Forward slashes only. A backslash is a separator on Windows and an
//     ordinary character on Unix, so a path containing one is a different file
//     depending on which machine wrote it — which is how a vault committed on a
//     laptop ends up writing somewhere else entirely.
//   - No leading slash and no volume name, so a path is always relative to the
//     campaign directory and never absolute.
//   - No control characters and no NUL, because a NUL truncates a path in
//     every C library underneath this one.
//   - No segment starting with a dot, which keeps the application's own
//     reserved directories — and Obsidian's `.obsidian` — out of the page
//     namespace.
//   - No reserved directory as a first segment, so a page cannot be written
//     inside `_attachments` or `_history` and shadow an attachment or a
//     revision.
//   - No Windows device name as a segment, and no segment ending in a dot or a
//     space, because Windows silently rewrites both and the file you asked for
//     is not the file that appears.
//   - A bounded length, so an over-long path is a clear error here rather than
//     an ENAMETOOLONG from the kernel after the DM has typed it.

const (
	// pageExtension is what a page's file is called. It is one value rather
	// than a set because there is one: a vault is markdown, and a .txt in it is
	// a file the DM left for something else.
	pageExtension = ".md"

	// attachmentsDir is where attachments live (ADR 0005).
	attachmentsDir = "_attachments"

	// historyDir is where revisions live (ADR 0005).
	historyDir = "_history"

	// obsidianDir is Obsidian's own. It is not ours to write, and a page
	// inside it is not a page.
	obsidianDir = ".obsidian"

	// maxSegmentBytes is the longest a single path segment may be. Most
	// filesystems stop at 255; this is under that on purpose, in bytes,
	// because that is what the kernel counts.
	maxSegmentBytes = 200

	// maxPathBytes is the longest a whole page path may be. ext4 allows
	// 4096 for an absolute path including its prefix, so 1024 leaves room
	// for a data directory under a long home directory.
	maxPathBytes = 1024
)

// reservedDirs are the first-segment names the application owns. A page path
// that starts with one of them is a page pretending to be something else: a
// page inside _attachments would shadow an attachment, and a page inside
// _history would be a revision that looks editable.
var reservedDirs = map[string]bool{
	attachmentsDir: true,
	historyDir:     true,
	obsidianDir:    true,
}

// unreadableDirs are the directories a page may not reach into with a
// reference. _attachments is deliberately absent: `![[_attachments/map.png]]`
// is the reference this project exists to support, and a page that may not
// point at an attachment may not have one.
var unreadableDirs = map[string]bool{
	historyDir:  true,
	obsidianDir: true,
}

// deviceNames are the names Windows treats as devices rather than files.
// Writing one creates something that is not a file, and reading one reads the
// device, and neither is what a DM meant.
var deviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// CheckPagePath reports whether a path is usable as a page's identity.
//
// It returns the path, cleaned and slashed the one way this package writes
// paths, so that a caller cannot accidentally persist a differently-spelled
// path than the one it validated. A path that needs cleaning to become valid is
// refused rather than cleaned: `a/./b` and `a/b` naming two pages would be a
// duplicate-identity bug, and one that only shows up on a rename.
func CheckPagePath(pagePath string) (string, error) {
	if pagePath == "" {
		return "", fmt.Errorf("%w: a page path is empty", ErrPath)
	}
	if len(pagePath) > maxPathBytes {
		return "", fmt.Errorf("%w: a page path may be at most %d bytes, this one is %d",
			ErrPath, maxPathBytes, len(pagePath))
	}
	if strings.ContainsRune(pagePath, 0) {
		return "", fmt.Errorf("%w: a page path cannot contain a NUL byte", ErrPath)
	}
	for _, r := range pagePath {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: a page path cannot contain the control character %q", ErrPath, r)
		}
	}
	if strings.ContainsRune(pagePath, '\\') {
		return "", fmt.Errorf("%w: %q contains a backslash, which is a separator on Windows and an ordinary character here", ErrPath, pagePath)
	}
	if strings.HasPrefix(pagePath, "/") {
		return "", fmt.Errorf("%w: %q is absolute; a page path is relative to the campaign directory", ErrPath, pagePath)
	}
	if strings.HasSuffix(pagePath, "/") {
		return "", fmt.Errorf("%w: %q ends in a separator", ErrPath, pagePath)
	}
	if hasVolumeName(pagePath) {
		return "", fmt.Errorf("%w: %q names a volume rather than a page", ErrPath, pagePath)
	}

	segments := strings.Split(pagePath, "/")
	for i, segment := range segments {
		switch {
		case segment == "":
			return "", fmt.Errorf("%w: %q has an empty segment at position %d", ErrPath, pagePath, i+1)
		case segment == "." || segment == "..":
			return "", fmt.Errorf("%w: %q has a %q segment, which does not name a page", ErrPath, pagePath, segment)
		case strings.HasPrefix(segment, "."):
			return "", fmt.Errorf("%w: %q starts with a dot, which is where the application keeps its own directories", ErrPath, pagePath)
		case strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " "):
			return "", fmt.Errorf("%w: %q has a segment ending in a dot or a space, which Windows rewrites", ErrPath, pagePath)
		case len(segment) > maxSegmentBytes:
			return "", fmt.Errorf("%w: a segment of %q is %d bytes, the limit is %d",
				ErrPath, pagePath, len(segment), maxSegmentBytes)
		}
	}

	first := segments[0]
	if reservedDirs[strings.ToLower(first)] {
		return "", fmt.Errorf("%w: %q is inside the reserved directory %q", ErrPath, pagePath, first)
	}

	for _, segment := range segments {
		base := segment
		if dot := strings.IndexByte(base, '.'); dot > 0 {
			base = base[:dot]
		}
		if deviceNames[strings.ToUpper(base)] {
			return "", fmt.Errorf("%w: %q is a device name on Windows", ErrPath, pagePath)
		}
	}

	// The path has to be its own cleaned form. If it is not, two spellings
	// would be two pages.
	if cleaned := path.Clean(pagePath); cleaned != pagePath {
		return "", fmt.Errorf("%w: %q is not in its simplest form, which is %q", ErrPath, pagePath, cleaned)
	}

	return pagePath, nil
}

// CheckFileName reports whether a single name is usable as an attachment's
// name. It is the same set of rules as a path segment, minus the ones about
// position, and the reason attachments need it is the same as the reason pages
// need it: a name arrives from a request, and a name can be a path.
func CheckFileName(name string) (string, error) {
	switch {
	case name == "":
		return "", fmt.Errorf("%w: an attachment name is empty", ErrPath)
	case strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\'):
		return "", fmt.Errorf("%w: %q contains a separator; an attachment is named, not placed", ErrPath, name)
	case name == "." || name == "..":
		return "", fmt.Errorf("%w: %q is not a file name", ErrPath, name)
	case strings.HasPrefix(name, "."):
		return "", fmt.Errorf("%w: %q starts with a dot, which is hidden on Unix and a system directory on Windows", ErrPath, name)
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return "", fmt.Errorf("%w: %q ends in a dot or a space, which Windows rewrites", ErrPath, name)
	case len(name) > maxSegmentBytes:
		return "", fmt.Errorf("%w: an attachment name may be at most %d bytes, this one is %d",
			ErrPath, maxSegmentBytes, len(name))
	}

	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: %q contains the control character %q", ErrPath, name, r)
		}
	}

	base := name
	if dot := strings.IndexByte(base, '.'); dot > 0 {
		base = base[:dot]
	}
	if deviceNames[strings.ToUpper(base)] {
		return "", fmt.Errorf("%w: %q is a device name on Windows", ErrPath, name)
	}

	return name, nil
}

// hasVolumeName reports whether a path names a Windows volume, which is
// absolute however it is written: C:/x, C:x and //server/share all of them.
func hasVolumeName(pagePath string) bool {
	if len(pagePath) < 2 || pagePath[1] != ':' {
		return strings.HasPrefix(pagePath, "//")
	}

	letter := pagePath[0]
	return letter >= 'a' && letter <= 'z' || letter >= 'A' && letter <= 'Z'
}
