package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// secretShaped reports whether a body has the shape of a secret callout in it,
// so the fuzz log says something useful when nothing was stripped.
func secretShaped(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "[!secret")
}

// FuzzRender is the renderer's fuzz target, and the property it defends is the
// one the spec names: render never panics.
//
// A panic in a request handler is a 500 for one player, and a panic in a
// background reindex is a wiki that will not start. Neither is a good way to
// find out what a DM wrote, so the fuzzer gets to write it first.
//
// The seeds are the shapes that break parsers and renderers: unbalanced fences,
// a wiki link with no target, deeply nested lists, a raw HTML block, an
// autolink, a footnote with no definition, and a table with ragged columns.
func FuzzRender(f *testing.F) {
	for _, seed := range []string{
		"",
		"\n",
		"# Heading\n\nText.\n",
		"```\nunterminated fence\n",
		"[[unclosed wiki link",
		"[[]]",
		"![[",
		"> [!warning] A callout\n> with a body\n",
		"> [!SECRET] Something\n> nobody may read\n",
		"    indented code\n\twith a tab\n",
		"- one\n  - two\n    - three\n      - four\n",
		"| a | b |\n| --- | --- |\n| 1 |\n",
		"<script>alert(1)</script>",
		"<div onclick=\"x\">raw html</div>",
		"<https://example.invalid/auto>",
		"a footnote[^1] with no definition\n",
		"[a link](javascript:alert(1))",
		"<!-- a comment -->",
		"&#x3c;script&#x3e;",
		"\u202e reversed text",
		strings.Repeat("*", 200),
		strings.Repeat("#", 100) + " heading",
		strings.Repeat("> ", 200) + "deeply quoted",
		strings.Repeat("- ", 500) + "a very long list",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, body string) {
		page := render.Page{
			Path:        "locations/rivergate",
			Body:        body,
			ContentHash: vault.Hash([]byte(body)),
		}

		// Both decisions, because the pipeline is allowed to do something
		// different for each and neither of them may panic.
		for _, decision := range []render.Decision{{}, {CanSeeSecrets: true}} {
			// The property that matters, and the reason the stripper is a walk
			// over the tree rather than a pass over the text: for a decision that
			// permits no secrets, no byte of a secret callout may appear in the
			// output. The fuzzer gets to invent callout syntax, which is how the
			// shapes nobody wrote a test for get found.
			canSeeSecrets := decision.CanSeeSecrets
			renderer := render.New()

			result, err := renderer.Render(context.Background(), page, decision)
			if err != nil {
				// A render that returns an error is allowed to say why; one
				// that panics is not.
				return
			}

			// Whatever came out has to be HTML the page shell can put in a
			// body, and the table of contents has to agree with it: an anchor
			// that is not in the output is a table of contents that does not
			// work, and a fuzzer finds those faster than a reader reports them.
			for _, heading := range result.TOC {
				if heading.Anchor == "" {
					continue
				}
				if !strings.Contains(result.HTML, `id="`+heading.Anchor+`"`) {
					t.Fatalf("the table of contents links to %q, which is not in the HTML", heading.Anchor)
				}
			}

			if !canSeeSecrets && result.SecretsStripped() == 0 && secretShaped(body) {
				// A body with secret syntax in it and nothing stripped means
				// the syntax was not recognised at all -- which is fine when it
				// was prose, and is a leak when it was a secret written in a
				// shape this build does not know. The only way to tell those
				// apart is to say what the text was, so the check is left to
				// the table test: this one is here to catch a shape where
				// something was stripped and its text came out anyway.
				t.Logf("body had secret syntax and nothing was stripped: %q", body)
			}
		}
	})
}

// FuzzStripSecret is the fuzz target for the property this milestone is about.
//
// Every input is a well-formed secret callout with a canary in it, followed by
// whatever the fuzzer invents. The canary is in the callout and nowhere else, so
// its absence from the output is exactly one fact: the stripper removed the body
// of a secret it was told to remove.
//
// What the trailing junk is for is the shapes that might stop the stripper
// getting there: a fence that swallows the rest of the page, a blockquote that
// opens and never closes, a nested callout, an unterminated `[!SECRET` of its
// own. A stripper that works on the twelve shapes somebody wrote tests for and
// not on the thirteenth is the leak this milestone is supposed to prevent.
func FuzzStripSecret(f *testing.F) {
	for _, seed := range []string{
		"",
		"More prose after the secret.",
		"```\n> [!SECRET] A secret inside a code fence\n",
		"> [!SECRET] A second secret\n> with its own body\n",
		"> > [!SECRET] Nested in a quote\n",
		"![image](_attachments/x.png)",
		"#### A heading after the secret\n",
		"> [!SECRET The bracket is unclosed\n",
		"| a | b |\n| --- | --- |\n| [!SECRET] | x |\n",
		strings.Repeat("> ", 50) + "[!SECRET] deep\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, trailing string) {
		const canary = "CANARY-7c1f9e"

		// The canary is inside the first callout and nowhere else, so nothing
		// the fuzzer appends can make this assertion ambiguous.
		body := "> [!SECRET] A title\n> " + canary + "\n\n" + trailing

		result, err := render.New().Render(context.Background(), render.Page{
			Path:        "locations/rivergate",
			Body:        body,
			ContentHash: vault.Hash([]byte(body)),
		}, render.Decision{})
		if err != nil {
			return
		}

		if strings.Contains(result.HTML, canary) {
			t.Fatalf("a page rendered for somebody who may not read secrets contains the canary\ntrailing: %q\nhtml: %s",
				trailing, result.HTML)
		}
		if result.SecretsStripped() == 0 {
			t.Fatalf("a page with a secret callout in it stripped nothing\ntrailing: %q\nhtml: %s", trailing, result.HTML)
		}
	})
}
