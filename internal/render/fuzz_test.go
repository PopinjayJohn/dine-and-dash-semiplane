package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

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
		}
	})
}
