package houserules_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin/contract"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/houserules"
)

// TestHouseRulesPassesTheContract is the line every plugin in this repository has to
// be able to write, and it is one line because the suite is one function.
func TestHouseRulesPassesTheContract(t *testing.T) {
	t.Parallel()

	contract.Run(t, houserules.New(slog.Default()))
}

// TestTheHookListsTheRulesAndNothingElse: the feature, and both halves of it. The
// list has to contain the rules, and the hook has to leave a page with no rules
// byte for byte as it found it — because the alternative is an empty box on every
// page in the campaign.
func TestTheHookListsTheRulesAndNothingElse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "two rules",
			body: "The bridge.\n\n> [!houserule] No talking during a toll\n\n" +
				"Some prose.\n\n> [!houserule] Split the party before Thornford\n",
			want: []string{"No talking during a toll", "Split the party before Thornford"},
		},
		{
			name: "a callout that is not a house rule",
			body: "> [!note] The weather is bad\n",
			want: nil,
		},
		{
			name: "a rule with no text yet",
			body: "> [!houserule]\n",
			want: nil,
		},
		{
			name: "a page with nothing on it",
			body: "Just prose.\n",
			want: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := houserules.Rules(test.body)
			if len(got) != len(test.want) {
				t.Fatalf("Rules = %q, want %q", got, test.want)
			}
			for i := range test.want {
				if got[i] != test.want[i] {
					t.Errorf("Rules[%d] = %q, want %q", i, got[i], test.want[i])
				}
			}
		})
	}
}

// TestTheHookAppendsNothingToAPageWithNoRules is the other half of the feature's
// usefulness, and it is a test of the *absence* of a box rather than its presence.
func TestTheHookAppendsNothingToAPageWithNoRules(t *testing.T) {
	t.Parallel()

	hook := houserules.New(slog.Default())
	var out bytes.Buffer

	page := render.Page{
		Campaign: "blackwater", Path: "locations/rivergate",
		Body: "A fortified town.\n", ContentHash: "h1",
	}
	if err := hook.AfterRender(t.Context(), page, render.Grant(), &out); err != nil {
		t.Fatalf("AfterRender: %v", err)
	}

	if out.String() != "" {
		t.Errorf("a page with no house rules got %q", out.String())
	}
}

// TestTheHookSaysNothingToAReaderWhoCannotSeeTheSecrets is the security half, and it
// is the reason the hook checks the decision at all: `render.Page.Body` is the whole
// file with the `[!SECRET]` blocks still in it, so a hook that read the rules without
// asking the decision would put a rule from inside a secret block in front of a
// player.
func TestTheHookSaysNothingToAReaderWhoCannotSeeTheSecrets(t *testing.T) {
	t.Parallel()

	hook := houserules.New(slog.Default())
	var out bytes.Buffer

	page := render.Page{
		Campaign: "blackwater", Path: "locations/rivergate",
		Body:        "> [!houserule] The toll-collector's real name is Marrow\n",
		ContentHash: "h1",
	}
	if err := hook.AfterRender(t.Context(), page, render.NewDecision(), &out); err != nil {
		t.Fatalf("AfterRender: %v", err)
	}

	if out.String() != "" {
		t.Errorf("a player who cannot see the secrets got %q", out.String())
	}
}

// TestTheHookAppendsForAReaderWhoCan is the positive half of the same test, because a
// hook that says nothing to everybody is a hook that does not work.
func TestTheHookAppendsForAReaderWhoCan(t *testing.T) {
	t.Parallel()

	hook := houserules.New(slog.Default())
	var out bytes.Buffer

	page := render.Page{
		Campaign: "blackwater", Path: "locations/rivergate",
		Body:        "> [!houserule] No talking during a toll\n",
		ContentHash: "h1",
	}
	if err := hook.AfterRender(t.Context(), page, render.Grant(), &out); err != nil {
		t.Fatalf("AfterRender: %v", err)
	}

	for _, want := range []string{"callout-house-rules", "No talking during a toll"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the hook's output does not carry %q: %s", want, out.String())
		}
	}
}

var _ = context.Background
