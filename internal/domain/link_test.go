package domain_test

import (
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestParseLinkKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    domain.LinkKind
		wantErr string
	}{
		{name: "link", in: "link", want: domain.LinkKindLink},
		{name: "embed", in: "embed", want: domain.LinkKindEmbed},
		{
			name:    "anything else is refused, and the error names what was allowed",
			in:      "transclusion",
			wantErr: `"transclusion" is not one of link, embed`,
		},
		{name: "empty is refused", in: "", wantErr: "is not one of"},
		{name: "case matters", in: "Link", wantErr: "is not one of"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseLinkKind(tt.in)
			if tt.wantErr != "" {
				assertError(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("ParseLinkKind(%q) returned an unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseLinkKind(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLinkKindValidAndString(t *testing.T) {
	t.Parallel()

	for _, kind := range []domain.LinkKind{domain.LinkKindLink, domain.LinkKindEmbed} {
		if !kind.Valid() {
			t.Errorf("%q.Valid() = false, want true", kind)
		}
		if got := kind.String(); got != string(kind) {
			t.Errorf("%q.String() = %q, want %q", kind, got, kind)
		}
	}

	for _, kind := range []domain.LinkKind{"", "image", "Link"} {
		if kind.Valid() {
			t.Errorf("%q.Valid() = true, want false", kind)
		}
	}
}

func TestPageLinkValidate(t *testing.T) {
	t.Parallel()

	valid := func() domain.PageLink {
		return domain.PageLink{
			SrcPageID: "01J8Z6Q0M4T5N6P7R8S9T0V1W3",
			DstPath:   "npcs/garros-ironbar",
			DstPageID: "01J8Z6Q0M4T5N6P7R8S9T0V1W4",
			Kind:      domain.LinkKindEmbed,
		}
	}

	tests := []struct {
		name    string
		mutate  func(*domain.PageLink)
		wantErr string
	}{
		{name: "a resolved embed is valid", mutate: func(*domain.PageLink) {}},
		{
			name: "an unresolved link is valid: the DM writes links to pages that do not exist yet",
			mutate: func(l *domain.PageLink) {
				l.Kind = domain.LinkKindLink
				l.DstPageID = ""
			},
			wantErr: "",
		},
		{
			name:    "an unset source is refused",
			mutate:  func(l *domain.PageLink) { l.SrcPageID = "" },
			wantErr: "link source page ID is required",
		},
		{
			name:    "an unset destination path is refused",
			mutate:  func(l *domain.PageLink) { l.DstPath = "" },
			wantErr: "link destination path is required",
		},
		{
			name:    "an unknown kind is refused",
			mutate:  func(l *domain.PageLink) { l.Kind = "mention" },
			wantErr: `"mention" is not one of link, embed`,
		},
		{
			name:    "an unset kind is refused",
			mutate:  func(l *domain.PageLink) { l.Kind = "" },
			wantErr: "link kind:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			l := valid()
			tt.mutate(&l)

			assertError(t, l.Validate(), tt.wantErr)
		})
	}
}

func TestPageLinkResolved(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   domain.PageLink
		want bool
	}{
		{
			name: "a target that exists is resolved",
			in:   domain.PageLink{DstPageID: "01J8Z6Q0M4T5N6P7R8S9T0V1W4"},
			want: true,
		},
		{
			name: "a target that does not exist yet is not resolved",
			in:   domain.PageLink{DstPath: "npcs/not-written-yet"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.in.Resolved(); got != tt.want {
				t.Errorf("PageLink{DstPageID: %q}.Resolved() = %t, want %t", tt.in.DstPageID, got, tt.want)
			}
		})
	}
}

func TestPageRevisionValidate(t *testing.T) {
	t.Parallel()

	valid := func() domain.PageRevision {
		return domain.PageRevision{
			ID:                "01J8Z6Q0M4T5N6P7R8S9T0V1W5",
			PageID:            "01J8Z6Q0M4T5N6P7R8S9T0V1W3",
			Rev:               3,
			Markdown:          "---\ntitle: Rivergate\n---\n\nA fortified town.\n",
			ContentHash:       "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			AuthorPrincipalID: "01J8Z6Q0M4T5N6P7R8S9T0V1W9",
			Message:           "tolls are too high",
			CreatedAt:         time.Date(2026, 2, 16, 20, 11, 0, 0, time.UTC),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*domain.PageRevision)
		wantErr string
	}{
		{name: "a complete revision is valid", mutate: func(*domain.PageRevision) {}},
		{
			name: "a revision with no author is valid: a file edited in Obsidian has none",
			mutate: func(r *domain.PageRevision) {
				r.AuthorPrincipalID = ""
			},
			wantErr: "",
		},
		{
			name: "a revision with no message is valid: an autosave has nothing to say",
			mutate: func(r *domain.PageRevision) {
				r.Message = ""
			},
			wantErr: "",
		},
		{
			name:    "an unset id is refused",
			mutate:  func(r *domain.PageRevision) { r.ID = "" },
			wantErr: "revision ID is required",
		},
		{
			name:    "an unset page is refused",
			mutate:  func(r *domain.PageRevision) { r.PageID = "" },
			wantErr: "revision page ID is required",
		},
		{
			name:    "a missing content hash is refused",
			mutate:  func(r *domain.PageRevision) { r.ContentHash = "" },
			wantErr: "revision content hash is required",
		},
		{
			name:    "a negative revision number is refused",
			mutate:  func(r *domain.PageRevision) { r.Rev = -1 },
			wantErr: "revision number: -1, must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := valid()
			tt.mutate(&r)

			assertError(t, r.Validate(), tt.wantErr)
		})
	}
}
