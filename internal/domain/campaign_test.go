package domain_test

import (
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestCampaignValidate(t *testing.T) {
	t.Parallel()

	valid := func() domain.Campaign {
		return domain.Campaign{
			ID:        "01J8Z6Q0M4T5N6P7R8S9T0V1W2",
			Slug:      "blackwater",
			Name:      "The Blackwater",
			System:    domain.DefaultSystem,
			VaultDir:  "vault/blackwater",
			CreatedAt: time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC),
			UpdatedAt: time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*domain.Campaign)
		wantErr string
	}{
		{name: "a complete campaign is valid", mutate: func(*domain.Campaign) {}},
		{
			name:    "system may be unset, because the column defaults it",
			mutate:  func(c *domain.Campaign) { c.System = "" },
			wantErr: "",
		},
		{
			name:    "an unset name is refused",
			mutate:  func(c *domain.Campaign) { c.Name = "" },
			wantErr: "campaign name is required",
		},
		{
			name:    "an unset id is refused",
			mutate:  func(c *domain.Campaign) { c.ID = "" },
			wantErr: "campaign ID is required",
		},
		{
			name:    "an unset vault directory is refused",
			mutate:  func(c *domain.Campaign) { c.VaultDir = "" },
			wantErr: "campaign vault directory is required",
		},
		{
			name:    "an empty slug is refused",
			mutate:  func(c *domain.Campaign) { c.Slug = "" },
			wantErr: "campaign slug slug is required",
		},
		{
			name:    "a slug that could be a path is refused",
			mutate:  func(c *domain.Campaign) { c.Slug = "../../etc" },
			wantErr: "campaign slug slug:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := valid()
			tt.mutate(&c)

			assertError(t, c.Validate(), tt.wantErr)
		})
	}
}

func TestCampaignSystemOrDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		system string
		want   string
	}{
		{name: "an unset system falls back to dnd5e", system: "", want: domain.DefaultSystem},
		{name: "a named system is kept", system: "homebrew", want: "homebrew"},
		{name: "dnd5e is kept as itself", system: "dnd5e", want: "dnd5e"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := (domain.Campaign{System: tt.system}).SystemOrDefault(); got != tt.want {
				t.Errorf("Campaign{System: %q}.SystemOrDefault() = %q, want %q", tt.system, got, tt.want)
			}
		})
	}
}
