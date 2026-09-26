package domain_test

import (
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

func TestParseVisibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    domain.Visibility
		wantErr string
	}{
		{name: "dm-only", in: "dm-only", want: domain.VisibilityDMOnly},
		{name: "dm-and-owner", in: "dm-and-owner", want: domain.VisibilityDMAndOwner},
		{name: "players", in: "players", want: domain.VisibilityPlayers},
		{
			name:    "a typo is refused rather than defaulted to the permissive level",
			in:      "plyers",
			wantErr: `"plyers" is not one of dm-only, dm-and-owner, players`,
		},
		{name: "an unset value is refused, because absent frontmatter is not the same as players", in: "", wantErr: "is not one of"},
		{name: "case matters", in: "DM-Only", wantErr: "is not one of"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := domain.ParseVisibility(tt.in)
			if tt.wantErr != "" {
				assertError(t, err, tt.wantErr)
				return
			}
			if err != nil {
				t.Fatalf("ParseVisibility(%q) returned an unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseVisibility(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestVisibilityValid(t *testing.T) {
	t.Parallel()

	for _, v := range []domain.Visibility{
		domain.VisibilityDMOnly,
		domain.VisibilityDMAndOwner,
		domain.VisibilityPlayers,
	} {
		if !v.Valid() {
			t.Errorf("%q.Valid() = false, want true", v)
		}
	}

	for _, v := range []domain.Visibility{"", "dm", "owner", "players "} {
		if v.Valid() {
			t.Errorf("%q.Valid() = true, want false", v)
		}
	}
}

func TestVisibilityString(t *testing.T) {
	t.Parallel()

	const want = "dm-and-owner"

	if got := domain.Visibility(want).String(); got != want {
		t.Errorf("Visibility(%q).String() = %q, want %q", want, got, want)
	}
}

func TestVisibilityStricter(t *testing.T) {
	t.Parallel()

	// The order is strictest to least strict, and "reveal" is one step along
	// it. A table rather than a comparison per case, because the whole
	// accessor rests on this order.
	levels := []domain.Visibility{
		domain.VisibilityDMOnly,
		domain.VisibilityDMAndOwner,
		domain.VisibilityPlayers,
	}

	tests := []struct {
		name string
		a, b domain.Visibility
		want bool
	}{
		{name: "dm-only is stricter than dm-and-owner", a: domain.VisibilityDMOnly, b: domain.VisibilityDMAndOwner, want: true},
		{name: "dm-and-owner is stricter than players", a: domain.VisibilityDMAndOwner, b: domain.VisibilityPlayers, want: true},
		{name: "dm-only is stricter than players", a: domain.VisibilityDMOnly, b: domain.VisibilityPlayers, want: true},
		{name: "players is not stricter than dm-and-owner", a: domain.VisibilityPlayers, b: domain.VisibilityDMAndOwner, want: false},
		{name: "a level is not stricter than itself", a: domain.VisibilityDMAndOwner, b: domain.VisibilityDMAndOwner, want: false},
		{name: "an unknown level is not stricter than a known one", a: "everyone", b: domain.VisibilityPlayers, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.a.Stricter(tt.b); got != tt.want {
				t.Errorf("%q.Stricter(%q) = %t, want %t", tt.a, tt.b, got, tt.want)
			}
		})
	}

	// The chain must be consistent with the table: every level is stricter
	// than the next one down, which is what makes the loop below terminate on
	// dm-only.
	for i := 1; i < len(levels); i++ {
		if !levels[i-1].Stricter(levels[i]) {
			t.Errorf("%q should be stricter than %q", levels[i-1], levels[i])
		}
	}
}

func TestVisibilityMoreVisible(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     domain.Visibility
		want   domain.Visibility
		wantOK bool
	}{
		{name: "dm-only reveals to dm-and-owner", in: domain.VisibilityDMOnly, want: domain.VisibilityDMAndOwner, wantOK: true},
		{name: "dm-and-owner reveals to players", in: domain.VisibilityDMAndOwner, want: domain.VisibilityPlayers, wantOK: true},
		{name: "players is already the least strict", in: domain.VisibilityPlayers, wantOK: false},
		{name: "an unknown level reveals to nothing", in: "everyone", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := tt.in.MoreVisible()
			if ok != tt.wantOK {
				t.Fatalf("%q.MoreVisible() ok = %t, want %t", tt.in, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("%q.MoreVisible() = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestVisibilityRevealWalksToTheEnd is the property behind the reveal
// actions: no sequence of reveals can widen a page's audience past players,
// and every page can get there.
func TestVisibilityRevealWalksToTheEnd(t *testing.T) {
	t.Parallel()

	for _, start := range []domain.Visibility{
		domain.VisibilityDMOnly,
		domain.VisibilityDMAndOwner,
		domain.VisibilityPlayers,
	} {
		current := start
		for steps := 0; ; steps++ {
			if steps > 3 {
				t.Fatalf("revealing a %q page did not settle: still %q", start, current)
			}

			next, ok := current.MoreVisible()
			if !ok {
				if current != domain.VisibilityPlayers {
					t.Errorf("revealing a %q page stopped at %q, want players", start, current)
				}
				break
			}
			current = next
		}
	}
}
