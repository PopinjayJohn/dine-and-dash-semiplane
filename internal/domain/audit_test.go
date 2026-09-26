package domain_test

import (
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestAuditEntryValidate(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

	tests := []struct {
		name    string
		in      domain.AuditEntry
		wantErr string
	}{
		{
			name: "an entry naming a campaign, a principal and a page is valid",
			in: domain.AuditEntry{
				CampaignID:  "01J8Z6Q0M4T5N6P7R8S9T0V1W2",
				PrincipalID: "01J8Z6Q0M4T5N6P7R8S9T0V1W9",
				PageID:      "01J8Z6Q0M4T5N6P7R8S9T0V1W3",
				Action:      domain.AuditPageSaved,
				At:          at,
			},
		},
		{
			name: "an entry with no campaign, principal or page is valid: a failed boot is still worth a line",
			in: domain.AuditEntry{
				Action: domain.AuditShareLinkUsed,
				At:     at,
			},
		},
		{
			name:    "an entry with no action is refused: a line that says nothing happened is worse than none",
			in:      domain.AuditEntry{At: at},
			wantErr: "audit action is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assertError(t, tt.in.Validate(), tt.wantErr)
		})
	}
}

func TestAuditActionString(t *testing.T) {
	t.Parallel()

	if got := domain.AuditPageSaved.String(); got != "page_saved" {
		t.Errorf("AuditPageSaved.String() = %q, want %q", got, "page_saved")
	}
}

// TestCoreAuditActionsAreDistinct is the test that catches a copy-paste in the
// action list. Two constants with the same value would silently merge two
// events in a log somebody reads during an incident.
func TestCoreAuditActionsAreDistinct(t *testing.T) {
	t.Parallel()

	actions := []domain.AuditAction{
		domain.AuditPageSaved,
		domain.AuditPageDeleted,
		domain.AuditPageViewed,
		domain.AuditRevisionRestored,
		domain.AuditShareLinkUsed,
		domain.AuditPrincipalIssued,
		domain.AuditPrincipalRevoked,
	}

	seen := make(map[domain.AuditAction]bool, len(actions))
	for _, action := range actions {
		switch {
		case action == "":
			t.Error("a core audit action is empty")
		case seen[action]:
			t.Errorf("core audit action %q is declared twice", action)
		}
		seen[action] = true
	}
}
