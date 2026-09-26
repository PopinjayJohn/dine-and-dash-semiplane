// Package vault is the on-disk half of the application: the markdown files a
// DM keeps in Obsidian, read and written the way Obsidian wrote them.
//
// # The files win
//
// ADR 0001 makes these files the source of truth and the database a
// projection of them. Every decision in this package follows from one
// consequence of that: **a file the application did not change must come back
// out byte for byte.** Not semantically equal — byte for byte. A serialiser
// that tidies a DM's YAML is a serialiser that puts a diff in their git
// history, and the first time they see that they stop trusting the tool with
// their campaign.
//
// So a Document keeps the bytes it was parsed from and hands them back
// unchanged until something actually changes. Only then is anything
// re-serialised, and then only the frontmatter block, from a parse tree that
// preserves key order, comments, quote styles and every key the application
// does not understand.
//
// # Encoding
//
// UTF-8, no BOM written, one trailing newline written, LF written. Read is
// more forgiving than write: a file with CRLF endings, or a BOM, or no
// trailing newline, is read as it is and written back as it is. A DM on
// Windows is not a broken DM, and normalising their whole vault is not this
// application's business.
package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	// fence opens and closes a frontmatter block. The closing fence may also
	// be YAML's "..." document-end marker, which is what a YAML tool that has
	// rewritten the file will write.
	fence      = "---"
	fenceEnd   = "..."
	utf8BOM    = "\ufeff"
	crlf       = "\r\n"
	lf         = "\n"
	maxBOMSize = 3
)

// Document is one markdown file: an optional YAML frontmatter block and a
// body.
//
// A Document is immutable except through Set, Remove and SetBody, and the only
// way to read its bytes is Bytes. Nothing reaches into raw.
type Document struct {
	// raw is the file exactly as it was read, or exactly as it was last
	// serialised. It is the answer to Bytes for as long as the document is
	// unmodified, which is what makes a zero-byte diff a property of the
	// design rather than a thing to test for.
	raw []byte

	// lineEnding is the ending the file used, kept so an unmodified document
	// that is re-serialised for any reason does not gain a diff on the first
	// line it changed.
	lineEnding string

	// front is the parsed frontmatter, or nil for a file that has no block.
	front *frontmatter

	body string

	// modified is set by every mutator. It is the only reason Bytes ever
	// re-serialises.
	modified bool
}

// Parse reads a markdown file.
//
// A file that is not valid UTF-8, or whose frontmatter is not valid YAML, or
// whose frontmatter is not a mapping, is an error rather than a document with
// the damage repaired: a page that cannot be understood is a page whose
// visibility this application would have to guess at, and guessing is how a
// `[!SECRET]` block ends up in a response.
//
// A file with no frontmatter block is not an error. Most pages in a new vault
// have none, and a DM writing prose should not have to add YAML to be indexed.
func Parse(data []byte) (*Document, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: a markdown file has to be valid UTF-8", ErrEncoding)
	}

	// A NUL is a sign the file is not the text file it claims to be, and it
	// survives a round trip through a database and an index without anybody
	// noticing until it breaks something. Refusing it here names the file.
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, fmt.Errorf("%w: a markdown file cannot contain a NUL byte", ErrEncoding)
	}

	doc := &Document{
		raw:        bytes.Clone(data),
		lineEnding: detectLineEnding(data),
	}

	// Parsing works on a normalised copy. The original bytes stay in raw,
	// because a file with CRLF endings or a BOM is a file the DM wrote, and
	// it comes back out as it went in.
	text := strings.TrimPrefix(string(data), utf8BOM)
	text = strings.ReplaceAll(text, crlf, lf)

	front, body := splitFrontmatter(text)
	doc.body = body

	if front == "" && !hasFrontmatter(text) {
		return doc, nil
	}

	parsed, err := parseFrontmatter(front)
	if err != nil {
		return nil, err
	}
	doc.front = parsed

	return doc, nil
}

// Bytes returns the file's bytes: exactly what was read, unless the document
// has been modified, in which case the deterministic serialisation of the
// modified document.
//
// The error is from re-serialising the frontmatter, which cannot fail for a
// tree this package built from values it checked. It is reported rather than
// swallowed because "it cannot fail" is how a serialiser ends up writing an
// empty file, and an empty file over a DM's page is not a small thing.
func (d *Document) Bytes() ([]byte, error) {
	if !d.modified {
		return bytes.Clone(d.raw), nil
	}

	serialised, err := d.serialise()
	if err != nil {
		return nil, err
	}
	return []byte(serialised), nil
}

// ContentHash is the SHA-256 of the file's bytes, which is how the sync engine
// answers "did this change?" without parsing anything. The schema makes it
// mandatory, because a row that cannot be compared to a file is reindexed on
// every run and the index quietly stops meaning anything.
func (d *Document) ContentHash() (string, error) {
	data, err := d.Bytes()
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// HasFrontmatter reports whether the file has a frontmatter block, including
// an empty one.
func (d *Document) HasFrontmatter() bool {
	return d.front != nil
}

// FrontmatterText returns the canonical YAML of the frontmatter block, without
// the fences. It is what goes in the pages.frontmatter column: unknown keys
// included, because that column is a projection of the file and not a summary
// of the keys the application happens to read.
func (d *Document) FrontmatterText() (string, error) {
	if d.front == nil {
		return "", nil
	}
	return d.front.text()
}

// Body returns the markdown with the frontmatter removed.
func (d *Document) Body() string {
	return d.body
}

// SetBody replaces the body and marks the document modified.
//
// The body's trailing whitespace is normalised to exactly one newline, because
// that is what the application writes. A body the application did not touch is
// left exactly as it was, which is the promise that outranks the convention.
//
// It returns an error for symmetry with Set, and there is no way for it to fail
// today. A caller that ignores it misses nothing now, and will not have to
// change when that stops being true.
func (d *Document) SetBody(body string) error {
	if body == d.body {
		return nil
	}
	d.body = strings.TrimRight(body, " \t\r\n") + lf
	d.modified = true
	return nil
}

// Modified reports whether anything has changed since the file was read. A
// caller that is deciding whether to write at all is asking this.
func (d *Document) Modified() bool {
	return d.modified
}

// serialise renders the file the way the application writes one: no BOM, the
// document's own line ending, and exactly one trailing newline.
//
// It is built entirely in LF and converted once at the end. Converting as it
// goes would turn the line endings it had already written into "\r\r\n",
// which is a bug that only appears on a DM's Windows machine.
func (d *Document) serialise() (string, error) {
	var b strings.Builder

	if d.front != nil {
		b.WriteString(fence)
		b.WriteString(lf)

		// An empty block writes nothing between its fences. Serialising an
		// empty mapping would write "{}", which is a valid YAML spelling of
		// nothing and an odd-looking way to say a page has no frontmatter.
		if !d.front.empty() {
			text, err := d.front.text()
			if err != nil {
				return "", err
			}
			b.WriteString(text)
		}

		b.WriteString(fence)
		b.WriteString(lf)
	}

	// A document with no body ends at its closing fence, with nothing after
	// it: a trailing blank line is not a trailing newline.
	if body := strings.TrimRight(d.body, " \t\r\n"); body != "" {
		b.WriteString(body)
		b.WriteString(lf)
	}

	return strings.ReplaceAll(b.String(), lf, d.lineEnding), nil
}

// splitFrontmatter separates the frontmatter block from the body.
//
// A file whose first line is a fence and which has a later line that is a
// fence has frontmatter. A file whose first line is a fence and which has no
// later one does not: the rest of the file is the body, because "---" is also a
// thematic break, and reading an unterminated block as a block would swallow a
// DM's prose.
func splitFrontmatter(text string) (front, body string) {
	if !hasFrontmatter(text) {
		return "", text
	}

	rest := strings.TrimPrefix(text, fence+lf)
	if rest == text {
		// The file is nothing but a fence, so there is no block.
		return "", text
	}

	offset := 0
	for line := range strings.SplitSeq(rest, lf) {
		if line == fence || line == fenceEnd {
			front = rest[:offset]
			// The body starts after the closing fence line and the newline
			// that ends it, and after the blank line the fence left behind:
			// a document's body is markdown, not a document's whitespace.
			after := offset + len(line) + 1
			if after >= len(rest) {
				return front, ""
			}
			return front, strings.TrimPrefix(rest[after:], lf)
		}
		offset += len(line) + 1
	}

	return "", text
}

// hasFrontmatter reports whether text opens with a fence, tolerating trailing
// whitespace and a carriage return, because a DM on Windows writes "--- \r\n"
// if their editor is feeling generous and that is still a fence.
func hasFrontmatter(text string) bool {
	first, _, found := strings.Cut(text, lf)
	if !found {
		return strings.TrimRight(first, " \t\r") == fence
	}
	return strings.TrimRight(first, " \t\r") == fence
}

// detectLineEnding reports the file's line ending, preferring CRLF when the
// file has any, because a mixed-ending file is one where CRLF is what a
// Windows tool meant.
func detectLineEnding(data []byte) string {
	if bytes.Contains(data, []byte(crlf)) {
		return crlf
	}
	return lf
}

// parseFrontmatter decodes the block into a tree that keeps everything the
// YAML said, including the things the application does not understand.
func parseFrontmatter(text string) (*frontmatter, error) {
	doc := &frontmatter{raw: text}
	if strings.TrimSpace(text) == "" {
		return doc, nil
	}

	if err := yaml.Unmarshal([]byte(text), &doc.node); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFrontmatter, err)
	}

	// A block that is not a set of keys is refused here rather than at the
	// first lookup, so a file with a list where its frontmatter should be is
	// one error with the file's name on it, not a page that mysteriously has
	// no title.
	if _, err := doc.mapping(); err != nil {
		return nil, err
	}

	return doc, nil
}

// Timestamp is the layout of the created and updated keys: RFC 3339, which is
// what Obsidian and every other tool writes. A vault that mixes zones is a
// vault whose "which came first" is a guess, so a value the application writes
// is always in UTC.
const Timestamp = "2006-01-02T15:04:05Z07:00"

// parseTime reads a frontmatter timestamp, accepting the date-only form that
// a DM writing "created: 2026-02-14" means, and refusing anything else.
func parseTime(value string) (time.Time, error) {
	for _, layout := range []string{Timestamp, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: %q is not a timestamp", ErrFrontmatter, value)
}
