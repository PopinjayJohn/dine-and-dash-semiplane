package render_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// appender is the shape nearly every plugin hook has: it adds something to the end
// of the output and returns nil.
//
// It is a test type rather than a real plugin because what is under test is the
// pipeline's *placement* and its isolation, not anybody's house rules.
type appender struct {
	markup string
	calls  int
	err    error
}

func (a *appender) AfterRender(_ context.Context, _ render.Page, _ render.Decision, out *bytes.Buffer) error {
	a.calls++
	if a.err != nil {
		return a.err
	}
	out.WriteString(a.markup)
	return nil
}

// panicker throws on purpose, which is the only way to test the recovery without a
// plugin that is genuinely broken.
type panicker struct{}

func (p *panicker) AfterRender(_ context.Context, _ render.Page, _ render.Decision, _ *bytes.Buffer) error {
	panic("a house rule with no name")
}

func (p *panicker) BeforeRender(_ context.Context, _ render.Page, _ render.Decision, doc ast.Node) (ast.Node, error) {
	panic("a house rule with no name")
}

// beforeFunc adapts a function to [render.BeforeRenderer], because a test hook has no
// configuration to carry and a struct with one field is a struct.
type beforeFunc func(context.Context, render.Page, render.Decision, ast.Node) (ast.Node, error)

func (f beforeFunc) BeforeRender(
	ctx context.Context, page render.Page, decision render.Decision, doc ast.Node,
) (ast.Node, error) {
	return f(ctx, page, decision, doc)
}

func withHooks(hooks ...render.RenderHook) render.Hooks {
	return render.Hooks{Hooks: hooks}
}

func page(body string) render.Page {
	return render.Page{
		Campaign: "blackwater",
		Path:     "locations/rivergate",
		Body:     body,
		// A stable hash, because the cache is keyed on it and a test that rendered
		// the same body twice would otherwise be reading its own first answer.
		ContentHash: "hash-of-" + body,
	}
}

func granted() render.Decision { return render.Grant() }

// player is the decision that strips every secret: the renderer's own name for the
// zero value, used here so that "a player" in a test name is the same thing as "a
// decision that hides" in the code.
func player() render.Decision { return render.NewDecision() }

func logInto(lines *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(lines, nil))
}

// TestAPluginSeesThePageAndCanChangeTheOutput: the hook runs, and its bytes are in
// the result.
//
// Without this, a hook set that is quietly never called still satisfies every other
// test in this file, because every one of them asserts about a *broken* hook and a
// hook that never runs is also a hook whose contribution is absent.
func TestAPluginSeesThePageAndCanChangeTheOutput(t *testing.T) {
	t.Parallel()

	hook := &appender{markup: `<p class="callout callout-note">a house rule</p>`}
	renderer := render.NewWith(render.Options{Hooks: withHooks(render.RenderHook{
		Plugin: "house-rules",
		After:  hook,
	})})

	result, err := renderer.Render(t.Context(), page("The toll bridge."), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if hook.calls != 1 {
		t.Errorf("the hook ran %d times, want 1", hook.calls)
	}
	if !strings.Contains(result.HTML, "a house rule") {
		t.Errorf("the hook's markup is not in the output:\n%s", result.HTML)
	}
}

// TestAPluginRunsBeforeTheSanitiserIsTheWholeArgumentForThisFile is the named
// security test.
//
// Invariant 4 says rendered markdown is sanitised for every author, DM included, and
// the tempting place to put a plugin is *after* the sanitiser — which would be a way
// for a plugin to put unsanitised HTML on a page a player reads. The payload is the
// one the XSS corpus uses, put there by a plugin rather than by a DM.
func TestAPluginRunsBeforeTheSanitiserIsTheWholeArgumentForThisFile(t *testing.T) {
	t.Parallel()

	const payload = `<script>alert("xss")</script>`

	tests := []struct {
		name string
		hook render.RenderHook
	}{
		{
			name: "from the HTML hook",
			hook: render.RenderHook{Plugin: "house-rules", After: &appender{markup: payload}},
		},
		{
			name: "from a tree hook's HTML",
			hook: render.RenderHook{
				Plugin: "house-rules",
				Before: beforeFunc(func(
					_ context.Context, _ render.Page, _ render.Decision, doc ast.Node,
				) (ast.Node, error) {
					// The tree cannot hold raw HTML, so a tree hook asks for it the
					// way a DM does: as text in the source. This is the same escape
					// hatch the renderer has for the DM's own `<script>` in a
					// markdown file, and the sanitiser is what has to carry it.
					paragraph := ast.NewParagraph()
					paragraph.Lines().Append(text.NewSegment(0, len(payload)))
					doc.AppendChild(doc, paragraph)
					return doc, nil
				}),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			renderer := render.NewWith(render.Options{Hooks: withHooks(test.hook)})
			result, err := renderer.Render(t.Context(), page(payload), player())
			if err != nil {
				t.Fatalf("Render: %v", err)
			}

			// Not "the script element is gone" — the *text* is gone, because a
			// neutralised payload that survives as visible text is a payload that
			// reached a player's screen.
			if strings.Contains(result.HTML, "alert(") {
				t.Errorf("a plugin's script reached the response:\n%s", result.HTML)
			}
			if strings.Contains(result.HTML, "<script") {
				t.Errorf("a plugin's script element reached the response:\n%s", result.HTML)
			}
		})
	}
}

// TestAPluginHookIsSkippedWhenItFailsAndThePageStillRenders is failure isolation at
// the render path, for the error case and the panic case together — because they are
// the same event to the render, and treating them differently would be a surprise.
func TestAPluginHookIsSkippedWhenItFailsAndThePageStillRenders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		hook render.RenderHook
	}{
		{
			name: "the hook returns an error",
			hook: render.RenderHook{
				Plugin: "house-rules",
				After:  &appender{err: errors.New("no house rules configured")},
			},
		},
		{
			name: "the hook panics",
			hook: render.RenderHook{Plugin: "house-rules", After: &panicker{}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lines := &strings.Builder{}
			renderer := render.NewWith(render.Options{
				Hooks: withHooks(test.hook),
				Log:   logInto(lines),
			})

			result, err := renderer.Render(t.Context(), page("The toll bridge."), granted())
			if err != nil {
				t.Fatalf("Render returned an error for a broken plugin: %v", err)
			}
			if !strings.Contains(result.HTML, "The toll bridge") {
				t.Errorf("the page did not render:\n%s", result.HTML)
			}

			// And the DM is told, because a hook that fails on every page is a wiki
			// that is quietly missing a feature and only the DM can fix it.
			written := lines.String()
			if !strings.Contains(written, "house-rules") {
				t.Errorf("nothing was logged about the failing hook:\n%s", written)
			}
		})
	}
}

// TestOneBrokenPluginDoesNotTakeTheOthersDownWithIt: isolation means the *next*
// plugin still runs, and it is the property a DM would actually notice — their word
// count works and the spoiler box does not.
func TestOneBrokenPluginDoesNotTakeTheOthersDownWithIt(t *testing.T) {
	t.Parallel()

	first := &appender{markup: "<p>first</p>"}
	third := &appender{markup: "<p>third</p>"}

	renderer := render.NewWith(render.Options{
		Log: logInto(&strings.Builder{}),
		Hooks: withHooks(
			render.RenderHook{Plugin: "a-first", After: first},
			render.RenderHook{Plugin: "b-broken", After: &panicker{}},
			render.RenderHook{Plugin: "c-third", After: third},
		),
	})

	result, err := renderer.Render(t.Context(), page("Body."), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(result.HTML, "first") || !strings.Contains(result.HTML, "third") {
		t.Errorf("a panicking hook took the others with it:\n%s", result.HTML)
	}
}

// TestATreeHookNeverSeesASecret is the placement test.
//
// The tree hook runs after the stripper, so the tree it is handed has no secret text
// in it. A hook that ran *before* the stripper could lift a `[!SECRET]` callout's
// contents into the open body, and the stripper would then have nothing left to
// remove — which is not a bug in a plugin, it is the capability the placement would
// hand it.
func TestATreeHookNeverSeesASecret(t *testing.T) {
	t.Parallel()

	const pageBody = "Open to everyone.\n\n> [!SECRET]\n> " + canary + "\n"

	var saw string
	hook := beforeFunc(func(_ context.Context, page render.Page, _ render.Decision, doc ast.Node) (ast.Node, error) {
		_ = page
		saw = textOf(doc, []byte(pageBody))
		return doc, nil
	})

	renderer := render.NewWith(render.Options{Hooks: withHooks(render.RenderHook{
		Plugin: "house-rules",
		Before: hook,
	})})

	result, err := renderer.Render(t.Context(), page(
		"Open to everyone.\n\n> [!SECRET]\n> "+canary+"\n"), player())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(saw, canary) {
		t.Errorf("a tree hook was handed the secret text: %q", saw)
	}
	if !strings.Contains(saw, "Open to everyone") {
		t.Errorf("the hook was not handed the open text either: %q", saw)
	}
	// The page itself, as well as the tree: a hook cannot be the reason a secret
	// reaches a response, and the test says so about the response as well as the
	// tree it was given.
	if strings.Contains(result.HTML, canary) {
		t.Errorf("the canary is in the response:\n%s", result.HTML)
	}
}

// TestATreeHookContributesToTheTableOfContents: the TOC is built from the tree
// *after* the tree hooks, so a heading a plugin adds is a heading the contents links
// to.
//
// Building the TOC first would produce a page whose contents lists a section that is
// not on the page, and a DM reading a preview would be reading a lie about their own
// notes.
func TestATreeHookContributesToTheTableOfContents(t *testing.T) {
	t.Parallel()

	const body = "The bridge.\n\nHouse rules\n===========\n"
	const headingStart = len("The bridge.\n\n")

	hook := beforeFunc(func(_ context.Context, _ render.Page, _ render.Decision, doc ast.Node) (ast.Node, error) {
		heading := ast.NewHeading(2)
		heading.Lines().Append(text.NewSegment(headingStart, headingStart+len("House rules")))
		doc.AppendChild(doc, heading)
		return doc, nil
	})

	renderer := render.NewWith(render.Options{Hooks: withHooks(render.RenderHook{
		Plugin: "house-rules",
		Before: hook,
	})})

	result, err := renderer.Render(t.Context(), page(body), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	found := false
	for _, entry := range result.TOC {
		if entry.Text == "House rules" {
			found = true
		}
	}
	if !found {
		t.Errorf("the plugin's heading is missing from the table of contents: %+v", result.TOC)
	}
}

// TestAPluginCanContributeAGoldmarkExtension is the other half of Hooks: the
// extensions go into the pipeline at construction, so a plugin can change how a page
// is parsed and rendered before any hook runs.
func TestAPluginCanContributeAGoldmarkExtension(t *testing.T) {
	t.Parallel()

	renderer := render.NewWith(render.Options{Hooks: render.Hooks{
		Exts: []goldmark.Extender{hardWrapExtension{}},
	}})

	result, err := renderer.Render(t.Context(), page("Listen.\nLoudly."), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(result.HTML, "<br") {
		t.Errorf("the contributed extension did not run:\n%s", result.HTML)
	}
}

// hardWrapExtension is a whole goldmark extension in three lines, and it is
// deliberately the simplest one that is observable.
//
// Hard line breaks are off by default — §11 says why, in the comment `New` used to
// carry — so a `<br>` in the output can only have come from the extension. A fixture
// with a node type and a parser of its own would be testing goldmark.
type hardWrapExtension struct{}

func (hardWrapExtension) Extend(md goldmark.Markdown) {
	md.Renderer().AddOptions(html.WithHardWraps())
}

// TestNoHooksIsTheBuildWithoutPlugins: the zero value of Hooks renders byte for byte
// what a build before plugins existed rendered.
//
// This is the property that let every existing golden file stay valid, and it is a
// test because a hook mechanism that changed the bytes of a page nobody had asked it
// to change would be a hook mechanism nobody could review.
func TestNoHooksIsTheBuildWithoutPlugins(t *testing.T) {
	t.Parallel()

	const body = "# Rivergate\n\nA toll bridge.\n\n> [!note] Weather\n> It rained.\n"

	without := render.NewWith(render.Options{})
	empty := render.NewWith(render.Options{Hooks: render.Hooks{
		Exts:  []goldmark.Extender{},
		Hooks: []render.RenderHook{},
	}})

	first, err := without.Render(t.Context(), page(body), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := empty.Render(t.Context(), page(body), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if first.HTML != second.HTML {
		t.Errorf("an empty hook set changed the bytes:\n%s\n---\n%s", first.HTML, second.HTML)
	}
	if !slices.Equal(first.TOC, second.TOC) {
		t.Errorf("an empty hook set changed the table of contents: %+v / %+v", first.TOC, second.TOC)
	}
}

// TestHooksReportsWhatContributed is the answer to "what is in this page's output",
// which is a question `wiki help plugins` and a debug footer both need and which the
// render cache deliberately does not.
func TestHooksReportsWhatContributed(t *testing.T) {
	t.Parallel()

	hooks := render.Hooks{
		Exts: []goldmark.Extender{hardWrapExtension{}},
		Hooks: []render.RenderHook{
			{Plugin: "zebra", After: &appender{}},
			{Plugin: "alpha", Before: beforeFunc(nil)},
			{Plugin: "zebra", After: &appender{}},
			{Plugin: "", After: &appender{}},
		},
	}

	got := hooks.Contributing()
	want := []string{"alpha", "zebra"}

	if len(got) != len(want) {
		t.Fatalf("Contributing() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Contributing()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTwoRenderersWithDifferentHooksDoNotShareACache is the standing version of the
// argument in cache.go: the key has no plugin field because a renderer's hooks are
// fixed at construction and each renderer owns its cache.
func TestTwoRenderersWithDifferentHooksDoNotShareACache(t *testing.T) {
	t.Parallel()

	const body = "The bridge."

	plain := render.NewWith(render.Options{})
	decorated := render.NewWith(render.Options{Hooks: withHooks(render.RenderHook{
		Plugin: "house-rules",
		After:  &appender{markup: "<p>a house rule</p>"},
	})})

	// Render the same page through both, and then the other way round. Whichever
	// order they are asked in, each has to give its own answer, because they are two
	// caches rather than one cache with two sets of keys.
	firstPlain, err := plain.Render(t.Context(), page(body), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	firstDecorated, err := decorated.Render(t.Context(), page(body), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	secondPlain, err := plain.Render(t.Context(), page(body), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	secondDecorated, err := decorated.Render(t.Context(), page(body), granted())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if firstPlain.HTML != secondPlain.HTML {
		t.Errorf("the plain renderer is not stable:\n%s\n---\n%s", firstPlain.HTML, secondPlain.HTML)
	}
	if firstDecorated.HTML != secondDecorated.HTML {
		t.Errorf("the decorated renderer is not stable:\n%s\n---\n%s",
			firstDecorated.HTML, secondDecorated.HTML)
	}
	if strings.Contains(firstPlain.HTML, "a house rule") {
		t.Errorf("the plain renderer served the decorated render:\n%s", firstPlain.HTML)
	}
	if !strings.Contains(firstDecorated.HTML, "a house rule") {
		t.Errorf("the decorated renderer served the plain render:\n%s", firstDecorated.HTML)
	}
}

// textOf walks a tree and returns every text segment in it, which is how a test
// asks "was the secret in that tree" without depending on how the tree is shaped.
//
// It takes the source because a goldmark text segment is a pair of offsets into one
// and the tree does not carry it — see hook.go, which says the same thing for a
// plugin author.
func textOf(doc ast.Node, source []byte) string {
	var out strings.Builder
	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if segment, isText := node.(*ast.Text); isText {
			out.Write(segment.Segment.Value(source))
		}
		return ast.WalkContinue, nil
	})
	return out.String()
}
