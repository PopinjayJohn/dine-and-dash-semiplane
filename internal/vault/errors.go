package vault

import "errors"

// The errors this package returns, so a caller can tell what went wrong
// without reading a message. Everything else that comes back is a filesystem
// error, wrapped with the path that caused it.
var (
	// ErrEncoding means the bytes are not a markdown file this application can
	// hold: not UTF-8, or with a NUL in it.
	ErrEncoding = errors.New("vault: the file is not encodable")

	// ErrFrontmatter means the frontmatter block could not be understood: it
	// is not valid YAML, it is not a set of keys, or one of the keys the
	// application owns holds a shape it cannot read.
	//
	// A file that produces this is refused rather than indexed, because
	// interpreting it would mean guessing — and the guess that costs the most
	// is the permissive reading of a visibility key.
	ErrFrontmatter = errors.New("vault: the frontmatter is not usable")

	// ErrKey means a caller asked for a key the application does not own, or
	// tried to store a value of the wrong shape under one that it does.
	ErrKey = errors.New("vault: the frontmatter key is not one the application owns")
)
