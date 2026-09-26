package render_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// The golden files are the contract for what a page turns into. They are read
// and rendered on every run, and they are regenerated with -update and read as
// a diff: a surprise golden diff is a bug until somebody says otherwise.
var update = flag.Bool("update", false, "regenerate the golden HTML under testdata/render")

// TestGoldenFiles renders every .md under testdata/render and compares the
// result with the .html beside it.
//
// The comparison is a byte comparison on purpose. "Contains" and "looks about
// right" are how a rendering regression survives a code review.
func TestGoldenFiles(t *testing.T) {
	t.Parallel()

	for _, name := range renderInputs(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			body := readInput(t, name)
			got := renderBody(t, body)

			golden := filepath.Join("testdata", "render", strings.TrimSuffix(name, ".md")+".html")

			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
					t.Fatalf("writing %s: %v", golden, err)
				}
				return
			}

			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading %s: %v (run go test -update to write it)", golden, err)
			}

			if got != string(want) {
				t.Errorf("the rendered HTML is not the golden file\n got: %q\nwant: %q\n\nrun go test -update and read the diff",
					got, want)
			}
		})
	}
}

// TestGoldenFilesAreComplete is the other half: a new .md with no .html beside
// it is a fixture nobody rendered, and a fixture nobody rendered is a fixture
// that is not testing anything.
func TestGoldenFilesAreComplete(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(filepath.Join("testdata", "render"))
	if err != nil {
		t.Fatalf("reading the render fixtures: %v", err)
	}

	inputs := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		inputs++

		golden := filepath.Join("testdata", "render", strings.TrimSuffix(entry.Name(), ".md")+".html")
		if _, err := os.Stat(golden); err != nil {
			t.Errorf("fixture %s has no golden file beside it: %v", entry.Name(), err)
		}
	}

	if inputs == 0 {
		t.Fatal("there are no fixtures, so the golden test is asserting that nothing happens")
	}
}

// TestRenderIsDeterministic: the same page, rendered twice, is the same bytes.
// A renderer that embeds a timestamp or a random id in its output cannot be
// cached, and a cache that misses every time is a cache that hides bugs.
func TestRenderIsDeterministic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := render.New()
	page := render.Page{
		Path:        "locations/rivergate",
		Body:        readInput(t, "prose.md"),
		ContentHash: vault.Hash([]byte("prose.md")),
	}

	first, err := r.Render(ctx, page, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for range 5 {
		again, err := r.Render(ctx, page, render.Decision{})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if again.HTML != first.HTML {
			t.Fatalf("two renders of one page differ:\nfirst:  %q\nsecond: %q", first.HTML, again.HTML)
		}
	}
}

func TestRenderTOC(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body string
		want []render.Heading
	}{
		"the headings of a page, in document order": {
			body: "# One\n\n## Two\n\n### Three\n",
			want: []render.Heading{
				{Level: 1, Text: "One", Anchor: "one"},
				{Level: 2, Text: "Two", Anchor: "two"},
				{Level: 3, Text: "Three", Anchor: "three"},
			},
		},
		"a heading with formatting in it is plain text": {
			body: "## The **toll** and the `bridge`\n",
			want: []render.Heading{
				{Level: 2, Text: "The toll and the bridge", Anchor: "the-toll-and-the-bridge"},
			},
		},
		"two headings with the same text get different anchors": {
			body: "## Two\n\n## Two\n",
			want: []render.Heading{
				{Level: 2, Text: "Two", Anchor: "two"},
				{Level: 2, Text: "Two", Anchor: "two-1"},
			},
		},
		"a page with no headings has an empty list, not a nil one": {
			body: "Just a paragraph.\n",
			want: []render.Heading{},
		},
		"an empty page has an empty list": {
			body: "",
			want: []render.Heading{},
		},
		"a setext heading is a heading": {
			body: "One\n===\n\nTwo\n---\n",
			want: []render.Heading{
				{Level: 1, Text: "One", Anchor: "one"},
				{Level: 2, Text: "Two", Anchor: "two"},
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renderResult(t, tt.body).TOC

			if len(got) != len(tt.want) {
				t.Fatalf("the table of contents has %d entries, want %d: %+v", len(got), len(tt.want), got)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("entry %d is %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestTOCAnchorsAreInTheHTML: an anchor in the table of contents that is not in
// the rendered page is a table of contents that does not work, and the two are
// built from different code, so this is the test that holds them together.
func TestTOCAnchorsAreInTheHTML(t *testing.T) {
	t.Parallel()

	for _, name := range renderInputs(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			result := renderResult(t, readInput(t, name))
			for _, heading := range result.TOC {
				if heading.Anchor == "" {
					continue
				}
				if !strings.Contains(result.HTML, `id="`+heading.Anchor+`"`) {
					t.Errorf("the table of contents links to %q, which is not an id in the rendered HTML", heading.Anchor)
				}
			}
		})
	}
}

// helpers

func renderInputs(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join("testdata", "render"))
	if err != nil {
		t.Fatalf("reading the render fixtures: %v", err)
	}

	names := []string{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
			names = append(names, entry.Name())
		}
	}
	return names
}

func readInput(t *testing.T, name string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "render", name))
	if err != nil {
		t.Fatalf("reading the fixture %s: %v", name, err)
	}
	return string(data)
}

func renderResult(t *testing.T, body string) render.Result {
	t.Helper()

	return renderPage(t, body)
}

func renderBody(t *testing.T, body string) string {
	t.Helper()

	return renderPage(t, body).HTML
}

func renderPage(t *testing.T, body string) render.Result {
	t.Helper()

	result, err := render.New().Render(context.Background(), render.Page{
		Path:        "locations/rivergate",
		Body:        body,
		ContentHash: vault.Hash([]byte(body)),
	}, render.Decision{})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return result
}
