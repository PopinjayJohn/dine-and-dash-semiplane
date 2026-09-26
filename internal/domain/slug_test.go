package domain_test

import (
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestNewSlug(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    domain.Slug
		wantErr string
	}{
		{
			name: "already a slug",
			in:   "blackwater",
			want: "blackwater",
		},
		{
			name: "spaces become hyphens",
			in:   "the blackwater",
			want: "the-blackwater",
		},
		{
			name: "mixed case is lowercased",
			in:   "TheBlackwater",
			want: "theblackwater",
		},
		{
			name: "runs of punctuation collapse to one hyphen",
			in:   "rivergate   (the _bridge_ town)",
			want: "rivergate-the-bridge-town",
		},
		{
			name: "dots are word boundaries, never path syntax",
			in:   "../../etc/passwd",
			want: "etc-passwd",
		},
		{
			name: "surrounding whitespace is trimmed",
			in:   "  rivergate  ",
			want: "rivergate",
		},
		{
			name: "digits are kept",
			in:   "session 14",
			want: "session-14",
		},
		{
			name:    "empty is refused",
			in:      "",
			wantErr: "slug is required",
		},
		{
			name:    "whitespace is refused",
			in:      "   ",
			wantErr: "slug is required",
		},
		{
			name:    "punctuation alone is refused",
			in:      "!!!",
			wantErr: "slug is required",
		},
		{
			name:    "dots alone are refused, so a relative path cannot become a slug",
			in:      "..",
			wantErr: "slug is required",
		},
		{
			name: "an absolute path is reduced to a name",
			in:   "/etc/passwd",
			want: "etc-passwd",
		},
		{
			name:    "over the length limit is refused",
			in:      strings.Repeat("a", domain.MaxSlugLength+1),
			wantErr: "limit is 64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.NewSlug(tt.in)
			if tt.wantErr != "" {
				assertError(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("NewSlug(%q) returned an unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("NewSlug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSlugValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      domain.Slug
		wantErr string
	}{
		{name: "simple", in: "blackwater"},
		{name: "hyphenated", in: "the-blackwater"},
		{name: "digits", in: "session-14"},
		{name: "at the length limit", in: domain.Slug(strings.Repeat("a", domain.MaxSlugLength))},
		{name: "empty", in: "", wantErr: "slug is required"},
		{name: "leading hyphen", in: "-blackwater", wantErr: "may not start or end with a hyphen"},
		{name: "trailing hyphen", in: "blackwater-", wantErr: "may not start or end with a hyphen"},
		{name: "uppercase", in: "Blackwater", wantErr: `contains 'B'`},
		{name: "path separator", in: "the/blackwater", wantErr: `contains '/'`},
		{name: "dot", in: "black.water", wantErr: `contains '.'`},
		{name: "underscore", in: "black_water", wantErr: `contains '_'`},
		{name: "space", in: "black water", wantErr: `contains ' '`},
		{name: "non-ascii", in: "café", wantErr: "contains 'é'"},
		{name: "traversal", in: "..", wantErr: `contains '.'`},
		{name: "over the limit", in: domain.Slug(strings.Repeat("a", domain.MaxSlugLength+1)), wantErr: "limit is 64"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertError(t, tt.in.Validate(), tt.wantErr)
		})
	}
}

func TestSlugString(t *testing.T) {
	t.Parallel()

	const want = "the-blackwater"

	if got := domain.Slug(want).String(); got != want {
		t.Errorf("Slug(%q).String() = %q, want %q", want, got, want)
	}
}

// TestSlugNormalisationIsIdempotent is the property that makes a slug usable as
// an identity: normalising an already-normal slug must not change it, or the
// same campaign would answer to two names after one round trip through a
// form.
func TestSlugNormalisationIsIdempotent(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"blackwater",
		"The Blackwater",
		"  RIVERGATE (the bridge town)  ",
		"session 14",
		"a/b\\c",
		"Ünderwegs",
		strings.Repeat("Mixed Case ", 3),
		"---leading",
	}

	for _, in := range inputs {
		once, err := domain.NewSlug(in)
		if err != nil {
			t.Errorf("NewSlug(%q) returned an unexpected error: %v", in, err)
			continue
		}

		twice, err := domain.NewSlug(once.String())
		if err != nil {
			t.Errorf("NewSlug(%q) on an already-normal slug returned an error: %v", once, err)
			continue
		}

		if twice != once {
			t.Errorf("NewSlug is not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func FuzzNewSlug(f *testing.F) {
	for _, seed := range []string{
		"blackwater",
		"The Blackwater",
		"../../etc/passwd",
		"",
		"/absolute",
		"Ünderwegs",
		"a\x00b",
		strings.Repeat("x", 200),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, in string) {
		slug, err := domain.NewSlug(in)
		if err != nil {
			return
		}

		// Whatever the input, an accepted slug is a valid slug: the check in
		// NewSlug and the check a caller makes on a value from a row agree.
		if err := slug.Validate(); err != nil {
			t.Fatalf("NewSlug(%q) returned %q, which then failed its own validation: %v", in, slug, err)
		}

		// And it can never be a path, an absolute reference or a hidden
		// file, whatever the DM typed.
		for _, forbidden := range []string{"..", "/", `\`, ".", "~"} {
			if strings.Contains(slug.String(), forbidden) {
				t.Errorf("NewSlug(%q) = %q, which contains %q", in, slug, forbidden)
			}
		}
		if strings.HasPrefix(slug.String(), "-") || strings.HasSuffix(slug.String(), "-") {
			t.Errorf("NewSlug(%q) = %q, which starts or ends with a hyphen", in, slug)
		}
	})
}
