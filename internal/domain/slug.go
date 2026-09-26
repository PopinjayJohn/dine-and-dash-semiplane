package domain

import (
	"fmt"
	"strings"
)

// MaxSlugLength is the longest slug a campaign may have. It is generous for a
// name a DM types and short enough to keep a slug legible in a URL, a
// directory name and a filename.
const MaxSlugLength = 64

// Slug is a campaign's URL and directory name: a lowercase word that survives
// being pasted into a Discord message, a browser address bar and a filesystem
// path. It is the one part of a campaign's identity that cannot change, so
// every inbound wiki link and every shared URL keeps working.
type Slug string

// NewSlug normalises s into a slug, or returns an error if nothing usable is
// left.
//
// Normalising rather than rejecting is deliberate: a DM typing "The Blackwater"
// should get a campaign, not a validation error. What the result can never be
// is empty, dotted, absolute, or containing a path separator — those are the
// forms that make a slug a filesystem or URL problem rather than a name, and
// they are exactly what the property test in slug_test.go pins down.
func NewSlug(s string) (Slug, error) {
	trimmed := strings.TrimSpace(s)

	var b strings.Builder
	b.Grow(len(trimmed))
	pendingSeparator := false
	for _, r := range strings.ToLower(trimmed) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingSeparator && b.Len() > 0 {
				b.WriteRune('-')
			}
			pendingSeparator = false
			b.WriteRune(r)
		default:
			// Every other rune, including a separator, a dot and a space, is
			// a word boundary as far as a slug is concerned.
			pendingSeparator = true
		}
	}

	slug := Slug(b.String())
	if err := slug.Validate(); err != nil {
		return "", err
	}
	return slug, nil
}

// Validate reports whether s is a usable slug. It is exported because a slug
// arrives from three directions — from a DM, from a request path and from a
// row read back from the database — and all three have to be checked.
func (s Slug) Validate() error {
	switch {
	case s == "":
		return required("slug")
	case len(s) > MaxSlugLength:
		return fmt.Errorf("slug: %d characters, limit is %d", len(s), MaxSlugLength)
	case strings.HasPrefix(string(s), "-"), strings.HasSuffix(string(s), "-"):
		return fmt.Errorf("slug: %q may not start or end with a hyphen", s)
	}

	for _, r := range s {
		isAlphanumeric := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if !isAlphanumeric && r != '-' {
			return fmt.Errorf("slug: %q contains %q, want only a-z, 0-9 and hyphens", s, r)
		}
	}

	return nil
}

// String returns the slug as it appears in a URL and on disk.
func (s Slug) String() string {
	return string(s)
}
