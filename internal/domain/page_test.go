package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestPageValidate(t *testing.T) {
	t.Parallel()

	valid := func() domain.Page {
		return domain.Page{
			ID:              "01J8Z6Q0M4T5N6P7R8S9T0V1W3",
			CampaignID:      "01J8Z6Q0M4T5N6P7R8S9T0V1W2",
			Path:            "locations/rivergate",
			Title:           "Rivergate",
			Type:            domain.PageTypeLocation,
			Frontmatter:     "title: Rivergate\ntype: location\n",
			Body:            "A fortified town at the confluence of the [[Blackwater]].\n",
			ContentHash:     "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			RendererVersion: 1,
			CreatedAt:       time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC),
			UpdatedAt:       time.Date(2026, 2, 16, 20, 11, 0, 0, time.UTC),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*domain.Page)
		wantErr string
	}{
		{name: "a complete page is valid", mutate: func(*domain.Page) {}},
		{
			name:    "an empty body is valid: a stub page is a real page",
			mutate:  func(p *domain.Page) { p.Body = "" },
			wantErr: "",
		},
		{
			name:    "an empty frontmatter block is valid: frontmatter is optional",
			mutate:  func(p *domain.Page) { p.Frontmatter = "" },
			wantErr: "",
		},
		{
			name:    "a renderer version of zero is valid: nothing has rendered it yet",
			mutate:  func(p *domain.Page) { p.RendererVersion = 0 },
			wantErr: "",
		},
		{
			name:    "an unset id is refused",
			mutate:  func(p *domain.Page) { p.ID = "" },
			wantErr: "page ID is required",
		},
		{
			name:    "an unset campaign is refused",
			mutate:  func(p *domain.Page) { p.CampaignID = "" },
			wantErr: "page campaign ID is required",
		},
		{
			name:    "an unset path is refused",
			mutate:  func(p *domain.Page) { p.Path = "" },
			wantErr: "page path is required",
		},
		{
			name:    "an unset title is refused",
			mutate:  func(p *domain.Page) { p.Title = "" },
			wantErr: "page title is required",
		},
		{
			name:    "an unset type is refused",
			mutate:  func(p *domain.Page) { p.Type = "" },
			wantErr: "page type is required",
		},
		{
			name: "a missing content hash is refused, because a page that cannot be compared to a file is reindexed forever",
			mutate: func(p *domain.Page) {
				p.ContentHash = ""
			},
			wantErr: "page content hash is required",
		},
		{
			name:    "a negative renderer version is refused",
			mutate:  func(p *domain.Page) { p.RendererVersion = -1 },
			wantErr: "page renderer version: -1, must not be negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := valid()
			tt.mutate(&p)

			assertError(t, p.Validate(), tt.wantErr)
		})
	}
}

func TestPageTypeIsCore(t *testing.T) {
	t.Parallel()

	// The eight types core owns, from ADR 0010. A test that lists them
	// explicitly is how core is kept honest: adding a type here is a
	// deliberate act with a consequence, not a drive-by edit.
	coreTypes := []domain.PageType{
		domain.PageTypeNote,
		domain.PageTypeLocation,
		domain.PageTypeNPC,
		domain.PageTypeQuest,
		domain.PageTypeItem,
		domain.PageTypeFaction,
		domain.PageTypeSessionLog,
		domain.PageTypeCharacter,
	}

	for _, pageType := range coreTypes {
		if !pageType.IsCore() {
			t.Errorf("%q.IsCore() = false, want true", pageType)
		}
	}

	for _, pageType := range []domain.PageType{"spell", "creature", "house-rule", "", "Location"} {
		if pageType.IsCore() {
			t.Errorf("%q.IsCore() = true, want false: a plugin type or a typo is not a core type", pageType)
		}
	}
}

func TestCorePageTypesAreDistinctAndWellFormed(t *testing.T) {
	t.Parallel()

	seen := make(map[domain.PageType]bool)
	for _, pageType := range []domain.PageType{
		domain.PageTypeNote,
		domain.PageTypeLocation,
		domain.PageTypeNPC,
		domain.PageTypeQuest,
		domain.PageTypeItem,
		domain.PageTypeFaction,
		domain.PageTypeSessionLog,
		domain.PageTypeCharacter,
	} {
		switch {
		case pageType == "":
			t.Error("a core page type is empty")
		case seen[pageType]:
			t.Errorf("core page type %q is declared twice", pageType)
		case pageType != domain.PageType(strings.ToLower(pageType.String())):
			t.Errorf("core page type %q is not lowercase: frontmatter and directory names are matched literally", pageType)
		}
		seen[pageType] = true
	}
}
