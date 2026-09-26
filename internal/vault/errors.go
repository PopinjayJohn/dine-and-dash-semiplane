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

	// ErrNotFound means the file is not in the vault. It is not a failure in
	// the way a read error is: "this page is not here" is the answer a sync
	// pass expects for most of the pages it asks about.
	ErrNotFound = errors.New("vault: not found")

	// ErrPath means a path is not usable as a page's identity, or a name is
	// not usable as a file's.
	//
	// Nothing here is a "best effort" case. A path that could be made to point
	// outside the vault is refused, and a path that would be two spellings of
	// one page is refused, because a vault where a page has two identities is a
	// vault where a rename loses a link.
	ErrPath = errors.New("vault: the path is not usable")
)
