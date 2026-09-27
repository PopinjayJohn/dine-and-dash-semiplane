package spoilerbox_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin/contract"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/spoilerbox"
)

// TestSpoilerboxPassesTheContract is the line every plugin in this repository has to
// be able to write, and it is one line because the suite is one function.
func TestSpoilerboxPassesTheContract(t *testing.T) {
	t.Parallel()

	contract.Run(t, spoilerbox.New())
}

// TestThePolicyTakesTheSecretsAndNothingElse is the feature, and the table is the
// point: the rule has to hold for the DM, for a player who owns the page, and for
// every page that is not a `spoiler-note`.
func TestThePolicyTakesTheSecretsAndNothingElse(t *testing.T) {
	t.Parallel()

	reg := newRegistry(t)
	if err := reg.Add(spoilerbox.New()); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// The cases are chosen so that the *core's* answer differs between them, because
	// otherwise the table cannot tell a plugin that did nothing from a plugin that did
	// the right thing.
	//
	// A plain player on a `players` page never sees secrets either way — the matrix
	// already says so — so that case would pass with no plugin registered at all. The
	// case that bites is the one the core *grants*: a `dm-and-owner` page, whose owner
	// sees the secrets, which is a character page's whole point and a plot note's
	// opposite.
	tests := []struct {
		name        string
		principal   access.Principal
		page        access.PageMeta
		wantCanRead bool
		wantCanSee  bool
	}{
		{
			name:        "the DM keeps the secrets",
			principal:   access.RoleOf(domain.RoleDM),
			page:        access.PageMeta{Type: spoilerbox.PageType, Visibility: domain.VisibilityDMAndOwner},
			wantCanRead: true, wantCanSee: true,
		},
		{
			name:        "an owner loses the secrets on a spoiler note",
			principal:   access.RoleOf(domain.RolePlayer),
			page:        access.PageMeta{Type: spoilerbox.PageType, Visibility: domain.VisibilityDMAndOwner, Owned: true},
			wantCanRead: true, wantCanSee: false,
		},
		{
			name:        "an owner keeps the secrets on an ordinary page",
			principal:   access.RoleOf(domain.RolePlayer),
			page:        access.PageMeta{Type: domain.PageTypeCharacter, Visibility: domain.VisibilityDMAndOwner, Owned: true},
			wantCanRead: true, wantCanSee: true,
		},
		{
			name:        "a page nobody owns is not readable, and the plugin did not change that",
			principal:   access.RoleOf(domain.RolePlayer),
			page:        access.PageMeta{Type: spoilerbox.PageType, Visibility: domain.VisibilityDMAndOwner},
			wantCanRead: false, wantCanSee: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			decided := access.For(test.principal, test.page)
			got := reg.Policies().Apply(t.Context(), test.principal, test.page, decided)

			if got.CanRead != test.wantCanRead {
				t.Errorf("CanRead = %v, want %v", got.CanRead, test.wantCanRead)
			}
			if got.CanSeeSecrets != test.wantCanSee {
				t.Errorf("CanSeeSecrets = %v, want %v", got.CanSeeSecrets, test.wantCanSee)
			}
		})
	}
}

// TestTheNoticeGoesToTheReaderWhoMayNotSeeTheSecrets is the other half of the
// feature, and the condition is the page *type* rather than a path prefix — a DM
// keeps their spoiler notes wherever they keep them.
func TestTheNoticeGoesToTheReaderWhoMayNotSeeTheSecrets(t *testing.T) {
	t.Parallel()

	plugin := spoilerbox.New()

	tests := []struct {
		name     string
		page     render.Page
		decision render.Decision
		want     string
	}{
		{
			name:     "a spoiler note for a player",
			page:     render.Page{Path: "notes/toll", Type: spoilerbox.PageType.String()},
			decision: render.NewDecision(),
			want:     "holding something back",
		},
		{
			name:     "a spoiler note for the DM",
			page:     render.Page{Path: "notes/toll", Type: spoilerbox.PageType.String()},
			decision: render.Grant(),
			want:     "",
		},
		{
			name:     "an ordinary page for a player",
			page:     render.Page{Path: "locations/rivergate", Type: domain.PageTypeLocation.String()},
			decision: render.NewDecision(),
			want:     "",
		},
		{
			name: "a spoiler note the DM filed under locations",
			// The path is deliberately `locations/`, which is where a regexp-guessing
			// version of this hook would have got it wrong in the other direction.
			page:     render.Page{Path: "locations/gm-notes", Type: spoilerbox.PageType.String()},
			decision: render.NewDecision(),
			want:     "holding something back",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			if err := plugin.AfterRender(t.Context(), test.page, test.decision, &out); err != nil {
				t.Fatalf("AfterRender: %v", err)
			}

			if test.want == "" {
				if out.String() != "" {
					t.Fatalf("got %q, want nothing", out.String())
				}
				return
			}
			if !strings.Contains(out.String(), test.want) {
				t.Errorf("got %q, want it to carry %q", out.String(), test.want)
			}
		})
	}
}

// newRegistry is a registry that logs nothing, so a test's output is the assertion
// and not a plugin complaining.
func newRegistry(t *testing.T) *plugin.Registry {
	t.Helper()

	return plugin.New(slog.New(slog.NewTextHandler(&strings.Builder{},
		&slog.HandlerOptions{Level: slog.LevelError + 8})))
}
