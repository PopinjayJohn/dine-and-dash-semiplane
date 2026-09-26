package domain_test

import (
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestParseRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    domain.Role
		wantErr string
	}{
		{name: "dm", in: "dm", want: domain.RoleDM},
		{name: "player", in: "player", want: domain.RolePlayer},
		{name: "an editor is refused: edit rights come from ownership, not from a role", in: "editor", wantErr: `"editor" is not one of dm, player`},
		{name: "an unset value is refused", in: "", wantErr: "is not one of"},
		{name: "case matters", in: "DM", wantErr: "is not one of"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseRole(tt.in)
			if tt.wantErr != "" {
				assertError(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("ParseRole(%q) returned an unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseRole(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestRoleValidIsDMAndString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		role  domain.Role
		valid bool
		isDM  bool
	}{
		{role: domain.RoleDM, valid: true, isDM: true},
		{role: domain.RolePlayer, valid: true, isDM: false},
		{role: "editor", valid: false, isDM: false},
		{role: "", valid: false, isDM: false},
	}

	for _, tt := range tests {
		t.Run(tt.role.String(), func(t *testing.T) {
			t.Parallel()

			if got := tt.role.Valid(); got != tt.valid {
				t.Errorf("%q.Valid() = %t, want %t", tt.role, got, tt.valid)
			}
			if got := tt.role.IsDM(); got != tt.isDM {
				t.Errorf("%q.IsDM() = %t, want %t", tt.role, got, tt.isDM)
			}
			if got := tt.role.String(); got != string(tt.role) {
				t.Errorf("%q.String() = %q", tt.role, got)
			}
		})
	}
}

func TestPrincipalValidate(t *testing.T) {
	t.Parallel()

	valid := func() domain.Principal {
		return domain.Principal{
			ID:         "01J8Z6Q0M4T5N6P7R8S9T0V1W9",
			CampaignID: "01J8Z6Q0M4T5N6P7R8S9T0V1W2",
			Label:      "Alice (Ranger)",
			Role:       domain.RolePlayer,
			TokenHash:  "3d2e1f0a9b8c7d6e5f4a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1",
			TokenHint:  "a1b2",
			CreatedAt:  time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC),
		}
	}

	tests := []struct {
		name    string
		mutate  func(*domain.Principal)
		wantErr string
	}{
		{name: "a complete principal is valid", mutate: func(*domain.Principal) {}},
		{
			name: "a principal that never expires and was never revoked is valid",
			mutate: func(p *domain.Principal) {
				p.ExpiresAt = time.Time{}
				p.RevokedAt = time.Time{}
				p.LastUsedAt = time.Time{}
			},
			wantErr: "",
		},
		{
			name:   "a second DM is a principal with role dm, not a new concept",
			mutate: func(p *domain.Principal) { p.Role = domain.RoleDM },
		},
		{
			name:    "an unset id is refused",
			mutate:  func(p *domain.Principal) { p.ID = "" },
			wantErr: "principal ID is required",
		},
		{
			name:    "an unset campaign is refused",
			mutate:  func(p *domain.Principal) { p.CampaignID = "" },
			wantErr: "principal campaign ID is required",
		},
		{
			name:    "an unset label is refused: the DM has to be able to tell two links apart",
			mutate:  func(p *domain.Principal) { p.Label = "" },
			wantErr: "principal label is required",
		},
		{
			name:    "a missing token hash is refused",
			mutate:  func(p *domain.Principal) { p.TokenHash = "" },
			wantErr: "principal token hash is required",
		},
		{
			name:    "an unknown role is refused",
			mutate:  func(p *domain.Principal) { p.Role = "editor" },
			wantErr: `"editor" is not one of dm, player`,
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

func TestPrincipalRevoked(t *testing.T) {
	t.Parallel()

	issued := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   domain.Principal
		want bool
	}{
		{name: "a fresh principal is not revoked", in: domain.Principal{RevokedAt: time.Time{}}, want: false},
		{
			name: "a principal revoked after issue is revoked",
			in:   domain.Principal{RevokedAt: issued.Add(24 * time.Hour)},
			want: true,
		},
		{
			name: "revocation is a fact and not a flag, so it survives a restart",
			in:   domain.Principal{RevokedAt: issued},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.in.Revoked(); got != tt.want {
				t.Errorf("Principal{RevokedAt: %v}.Revoked() = %t, want %t", tt.in.RevokedAt, got, tt.want)
			}
		})
	}
}

func TestSessionValidate(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

	valid := func() domain.Session {
		return domain.Session{
			ID:          "01J8Z6Q0M4T5N6P7R8S9T0V1WA",
			PrincipalID: "01J8Z6Q0M4T5N6P7R8S9T0V1W9",
			CreatedAt:   created,
			ExpiresAt:   created.Add(30 * 24 * time.Hour),
			UserAgent:   "Mozilla/5.0",
		}
	}

	tests := []struct {
		name    string
		mutate  func(*domain.Session)
		wantErr string
	}{
		{name: "a complete session is valid", mutate: func(*domain.Session) {}},
		{
			name: "a session with no user agent is valid: the header is optional",
			mutate: func(s *domain.Session) {
				s.UserAgent = ""
			},
			wantErr: "",
		},
		{
			name: "an expiry in the past is allowed: it is a row worth keeping, and Expired decides",
			mutate: func(s *domain.Session) {
				s.ExpiresAt = created.Add(-time.Hour)
			},
			wantErr: "",
		},
		{
			name:    "an unset id is refused",
			mutate:  func(s *domain.Session) { s.ID = "" },
			wantErr: "session ID is required",
		},
		{
			name:    "an unset principal is refused",
			mutate:  func(s *domain.Session) { s.PrincipalID = "" },
			wantErr: "session principal ID is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := valid()
			tt.mutate(&s)

			assertError(t, s.Validate(), tt.wantErr)
		})
	}
}

func TestSessionExpired(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   domain.Session
		now  time.Time
		want bool
	}{
		{
			name: "an expiry in the future has not passed",
			in:   domain.Session{ExpiresAt: now.Add(time.Hour)},
			now:  now,
			want: false,
		},
		{
			name: "an expiry in the past has passed",
			in:   domain.Session{ExpiresAt: now.Add(-time.Second)},
			now:  now,
			want: true,
		},
		{
			name: "an expiry exactly now has passed: a session is not valid for its last instant",
			in:   domain.Session{ExpiresAt: now},
			now:  now,
			want: true,
		},
		{
			name: "no expiry never passes: it is revoked by deleting the principal instead",
			in:   domain.Session{ExpiresAt: time.Time{}},
			now:  now,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.in.Expired(tt.now); got != tt.want {
				t.Errorf("Session{ExpiresAt: %v}.Expired(%v) = %t, want %t", tt.in.ExpiresAt, tt.now, got, tt.want)
			}
		})
	}
}
