package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// canary is the string that must not appear in a page a player may read. It is
// in the fixture deliberately many times over, in every shape a secret can
// take, so that one shape being handled wrongly is one grep away from a failure.
const canary = "CANARY-SECRET-3f9a2b"

// TestSecretsAreStrippedForAPlayer is the render path's half of the spec's
// TestSecretStrippedFromAllSurfaces: the canary is absent from the bytes, not
// merely hidden in the DOM.
func TestSecretsAreStrippedForAPlayer(t *testing.T) {
	t.Parallel()

	body := readInput(t, "secrets.md")
	result := renderWith(t, body, render.Decision{})

	// The fixture has six callouts, one of which is revealed and therefore shown
	// on purpose. Two of them carry the canary twice, so the canary count and the
	// stripped count are deliberately different numbers.
	if want := 5; result.SecretsStripped() != want {
		t.Errorf("the render stripped %d secrets, want %d", result.SecretsStripped(), want)
	}

	if count := strings.Count(result.HTML, canary); count != 1 {
		t.Errorf("the page a player may read contains the canary %d times, want 1 (the revealed secret)\n%s",
			count, result.HTML)
	}

	// And what is left where a secret was is visible, so a player can see
	// there is one and ask about it.
	if count := strings.Count(result.HTML, "A secret is hidden here."); count != 5 {
		t.Errorf("the page shows %d placeholders, want 5\n%s", count, result.HTML)
	}
	if strings.Contains(result.HTML, "The hound") {
		t.Error("the title of a stripped secret is in the page: the title of a secret is the DM's writing about it")
	}
	if strings.Contains(result.HTML, "With a heading") {
		t.Error("the title of a stripped secret with a heading in it is in the page")
	}
}

// TestSecretsAreShownWhenTheDecisionPermits: the same page for a DM, where the
// decision says the secrets may be read.
func TestSecretsAreShownWhenTheDecisionPermits(t *testing.T) {
	t.Parallel()

	body := readInput(t, "secrets.md")
	result := renderWith(t, body, render.Decision{CanSeeSecrets: true})

	if result.SecretsStripped() != 0 {
		t.Errorf("a render that may see secrets stripped %d", result.SecretsStripped())
	}
	if count := strings.Count(result.HTML, canary); count != 7 {
		t.Errorf("the DM's page contains the canary %d times, want all 7\n%s", count, result.HTML)
	}
	if strings.Contains(result.HTML, "stripped") {
		t.Error("the DM's page contains a placeholder for a secret that was not stripped")
	}
}

// TestSecretsInEveryShapeAreStripped is the table: each shape a secret can take
// in markdown, and what has to happen to it. A shape missing from this table is
// a shape nobody has checked, and the canary in each is the same one so that a
// new shape is one row rather than a new fixture.
func TestSecretsInEveryShapeAreStripped(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body string

		wantStripped int
		// wantPresent is whether the canary survives, which is only ever true
		// for a secret the DM revealed or a decision that permits it.
		wantPresent bool
	}{
		"a plain callout": {
			body:         "> [!SECRET] Title\n> " + canary + "\n",
			wantStripped: 1,
		},
		"a callout with no title": {
			body:         "> [!secret]\n> " + canary + "\n",
			wantStripped: 1,
		},
		"a callout with a fold marker": {
			body:         "> [!SECRET]- Title\n> " + canary + "\n",
			wantStripped: 1,
		},
		"an upper-case type": {
			body:         "> [!SeCrEt] Title\n> " + canary + "\n",
			wantStripped: 1,
		},
		"a callout with a trailing space after the type": {
			body:         "> [!SECRET ] Title\n> " + canary + "\n",
			wantStripped: 1,
		},
		"a callout inside a list item": {
			body:         "- an item\n\n  > [!SECRET] Title\n  > " + canary + "\n",
			wantStripped: 1,
		},
		"a callout inside a blockquote": {
			body:         "> > [!SECRET] Title\n> > " + canary + "\n",
			wantStripped: 1,
		},
		"a callout after a paragraph": {
			body:         "Some prose.\n\n> [!SECRET] Title\n> " + canary + "\n",
			wantStripped: 1,
		},
		"two secrets in a row": {
			body:         "> [!SECRET] One\n> " + canary + "\n\n> [!SECRET] Two\n> " + canary + "\n",
			wantStripped: 2,
		},
		"a secret with no space after the marker": {
			body:         ">[!SECRET] Title\n>" + canary + "\n",
			wantStripped: 1,
		},
		"an unclosed bracket, which nobody can parse": {
			body:         "> [!SECRET " + canary + "\n",
			wantStripped: 1,
		},
		"a secret that is only a fold marker": {
			body:         "> [!secret]-\n> " + canary + "\n",
			wantStripped: 1,
		},
		"a revealed secret is shown": {
			body:         "> [!SECRET]{.revealed} Title\n> " + canary + "\n",
			wantStripped: 0,
			wantPresent:  true,
		},
		"a revealed attribute with another attribute beside it is not the revealed form": {
			body:         "> [!SECRET]{.revealed .spoiler} Title\n> " + canary + "\n",
			wantStripped: 1,
		},
		"the word secret in prose is prose": {
			body: "The DM wrote [!SECRET] in a note, and meant it as an example.",
		},
		"a callout syntax inside a code fence is a code sample": {
			body:         "```markdown\n> [!SECRET] Title\n> " + canary + "\n```\n",
			wantStripped: 0,
			wantPresent:  true,
		},
		"an indented code block with callout syntax is a code sample": {
			body:         "Text.\n\n    > [!SECRET] Title\n    > " + canary + "\n",
			wantStripped: 0,
			wantPresent:  true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := renderWith(t, tt.body, render.Decision{})

			if result.SecretsStripped() != tt.wantStripped {
				t.Errorf("the render stripped %d secrets, want %d\nbody: %q\nhtml: %s",
					result.SecretsStripped(), tt.wantStripped, tt.body, result.HTML)
			}

			present := strings.Contains(result.HTML, canary)
			if present != tt.wantPresent {
				t.Errorf("the canary is present = %t, want %t\nbody: %q\nhtml: %s",
					present, tt.wantPresent, tt.body, result.HTML)
			}
		})
	}
}

// TestSecretsInTheTableOfContents: the text of a heading inside a secret is
// secret, whatever the text of the callout's own body is, and a table of
// contents is a place a player reads.
func TestSecretsInTheTableOfContents(t *testing.T) {
	t.Parallel()

	const secretHeading = "What the Toll-Collector Knows"
	body := "> [!SECRET] A secret\n> " + canary + "\n>\n> #### " + secretHeading + "\n"

	result := renderWith(t, body, render.Decision{})

	for _, heading := range result.TOC {
		if strings.Contains(heading.Text, secretHeading) {
			t.Errorf("the table of contents contains a heading from inside a secret: %q", heading.Text)
		}
	}
	if strings.Contains(result.HTML, secretHeading) {
		t.Error("the page contains a heading from inside a secret")
	}

	// And with permission it is there, because the table of contents has to be
	// the DM's as well as the player's.
	withSecrets := renderWith(t, body, render.Decision{CanSeeSecrets: true})
	found := false
	for _, heading := range withSecrets.TOC {
		if strings.Contains(heading.Text, secretHeading) {
			found = true
		}
	}
	if !found {
		t.Error("the DM's table of contents is missing a heading that is inside a secret they may read")
	}
}

// TestStrippedSecretHoldsNothing: the placeholder is a node rather than a
// deletion so the shape of the page survives, and a node that held the text it
// replaced would be a secret one refactor away from being rendered.
func TestStrippedSecretHoldsNothing(t *testing.T) {
	t.Parallel()

	body := "> [!SECRET] Title\n> " + canary + "\n"

	result := renderWith(t, body, render.Decision{})

	if strings.Contains(result.HTML, canary) {
		t.Fatal("the canary is in the HTML")
	}
	if strings.Contains(result.HTML, "Title") {
		t.Error("the secret's title is in the HTML")
	}
	// The only text the placeholder carries is its own, which says nothing about
	// what was in it.
	if !strings.Contains(result.HTML, "A secret is hidden here.") {
		t.Error("the placeholder does not say that a secret is there")
	}
}

func renderWith(t *testing.T, body string, decision render.Decision) render.Result {
	t.Helper()

	result, err := render.New().Render(context.Background(), render.Page{
		Path:        "locations/rivergate",
		Body:        body,
		ContentHash: vault.Hash([]byte(body)),
	}, decision)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return result
}
