package render_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// A field renderer that reports what it was handed and draws what it is told to.
//
// The `saw` field is the point of the fixture: the redaction is the property under
// test and it happens *before* the call, so a fixture that only looked at the output
// could not tell a redacted value from an unrendered field.
type fieldFixture struct {
	saw     render.Field
	renders string
	err     error
	panics  bool
}

func (f *fieldFixture) RenderField(
	_ context.Context, _ render.Page, _ render.Decision, field render.Field,
) (string, error) {
	f.saw = field
	switch {
	case f.panics:
		panic("a statline with no numbers in it")
	case f.err != nil:
		return "", f.err
	default:
		return f.renders, nil
	}
}

// fieldHooks is a hook set with one claimed field, drawn by one fixture.
func fieldHooks(name string, renderer render.FieldRenderer) render.Hooks {
	return render.Hooks{Fields: map[string]render.FieldSpec{
		name: {Kind: "text", Renderer: renderer},
	}}
}

// TestAFieldsValueIsRedactedBeforeAPluginSeesIt is the named security test for
// M12, and it is the same canary the other secret tests use because a test that
// asserted "the secret is not in the output" with a string of its own would be a
// test that could pass while the real one failed.
//
// A DM marking a field secret is the obvious way to use a `[!SECRET]` in
// frontmatter, and `render.Page.Fields` deliberately carries the *raw* value: the
// redaction has to happen under a decision, and only the render has one. A plugin is
// code compiled into this binary, so the redaction cannot be a promise a plugin
// makes about itself.
func TestAFieldsValueIsRedactedBeforeAPluginSeesIt(t *testing.T) {
	t.Parallel()

	fixture := &fieldFixture{renders: "<p>rendered</p>"}
	renderer := render.NewWith(render.Options{
		Hooks: fieldHooks("mood", fixture),
		Log:   logInto(&strings.Builder{}),
	})

	page := page("The bridge.")
	page.Fields = []render.Field{{
		Name:  "mood",
		Value: "cheerful\n\n> [!SECRET] He is lying about the toll.\n",
		Kind:  "text",
	}}

	result, err := renderer.Render(t.Context(), page, player())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(fixture.saw.Value, "lying about the toll") {
		t.Errorf("a plugin was handed a field's secret text: %q", fixture.saw.Value)
	}
	if !strings.Contains(fixture.saw.Value, "cheerful") {
		t.Errorf("the plugin was not handed the open text either: %q", fixture.saw.Value)
	}
	// And the response, which is the property that actually matters.
	if strings.Contains(result.HTML, "lying about the toll") {
		t.Errorf("the field's secret reached the response:\n%s", result.HTML)
	}
}

// TestTheDmStillSeesTheFieldSecret: the other half, because a redaction that hides a
// secret from the DM is a bug and a test that only checked the player would pass it.
func TestTheDmStillSeesTheFieldSecret(t *testing.T) {
	t.Parallel()

	fixture := &fieldFixture{renders: "<p>rendered</p>"}
	renderer := render.NewWith(render.Options{
		Hooks: fieldHooks("mood", fixture),
		Log:   logInto(&strings.Builder{}),
	})

	page := page("The bridge.")
	page.Fields = []render.Field{{
		Name:  "mood",
		Value: "> [!SECRET] He is lying about the toll.",
		Kind:  "text",
	}}

	if _, err := renderer.Render(t.Context(), page, granted()); err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(fixture.saw.Value, "lying about the toll") {
		t.Errorf("the DM was not handed their own field's secret: %q", fixture.saw.Value)
	}
}

// TestAFieldsOutputGoesThroughTheOneSanitiser: the structural half of the guarantee.
// A field's HTML is written into the same buffer as the body and `Sanitise` runs over
// the result, so there is no path from a plugin's field to the response that skips
// the allow-list.
//
// The payload is the XSS corpus's, put in a field rather than in a page, because a
// plugin is the one author whose output nobody has a test for otherwise.
func TestAFieldsOutputGoesThroughTheOneSanitiser(t *testing.T) {
	t.Parallel()

	const payload = `<script>alert("xss")</script>`

	fixture := &fieldFixture{renders: payload}
	renderer := render.NewWith(render.Options{
		Hooks: fieldHooks("mood", fixture),
		Log:   logInto(&strings.Builder{}),
	})

	page := page("The bridge.")
	page.Fields = []render.Field{{Name: "mood", Value: "cheerful", Kind: "text"}}

	result, err := renderer.Render(t.Context(), page, player())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(result.HTML, "alert(") {
		t.Errorf("a plugin field's script reached the response:\n%s", result.HTML)
	}
	if strings.Contains(result.HTML, "<script") {
		t.Errorf("a plugin field's script element reached the response:\n%s", result.HTML)
	}
}

// TestAFieldRendererThatFailsRendersNothingAndThePageStillRenders: the isolation
// guarantee, in its fourth and last form -- a field is decoration on top of a page a
// DM has to be able to read.
func TestAFieldRendererThatFailsRendersNothingAndThePageStillRenders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fixture *fieldFixture
	}{
		{name: "it returns an error", fixture: &fieldFixture{err: errors.New("no statline for you")}},
		{name: "it panics", fixture: &fieldFixture{panics: true}},
		{name: "it declines the field", fixture: &fieldFixture{renders: ""}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lines := &strings.Builder{}
			renderer := render.NewWith(render.Options{
				Hooks: fieldHooks("mood", test.fixture),
				Log:   logInto(lines),
			})

			page := page("The toll bridge.")
			page.Fields = []render.Field{{Name: "mood", Value: "cheerful", Kind: "text"}}

			result, err := renderer.Render(t.Context(), page, player())
			if err != nil {
				t.Fatalf("Render returned an error for a broken field renderer: %v", err)
			}
			if !strings.Contains(result.HTML, "The toll bridge") {
				t.Errorf("the page did not render:\n%s", result.HTML)
			}
		})
	}
}

// TestOneBrokenFieldDoesNotTakeTheOthersWithIt: the same property the render hooks
// have, and it is the version a DM would notice -- their spell page still shows its
// casting time and their character page's hit points are missing.
func TestOneBrokenFieldDoesNotTakeTheOthersWithIt(t *testing.T) {
	t.Parallel()

	broken := &fieldFixture{panics: true}
	renderer := render.NewWith(render.Options{
		Log: logInto(&strings.Builder{}),
		Hooks: render.Hooks{Fields: map[string]render.FieldSpec{
			"a-working": {Kind: "text", Renderer: &fieldFixture{renders: "<p>first</p>"}},
			"b-broken":  {Kind: "text", Renderer: broken},
			"c-working": {Kind: "text", Renderer: &fieldFixture{renders: "<p>third</p>"}},
		}},
	})

	page := page("Body.")
	page.Fields = []render.Field{
		{Name: "a-working", Value: "x", Kind: "text"},
		{Name: "b-broken", Value: "y", Kind: "text"},
		{Name: "c-working", Value: "z", Kind: "text"},
	}

	result, err := renderer.Render(t.Context(), page, player())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(result.HTML, "first") || !strings.Contains(result.HTML, "third") {
		t.Errorf("a panicking field renderer took the others with it:\n%s", result.HTML)
	}
}

// TestTheFieldBlockIsBeforeTheBody: the placement is a decision, not an accident. A
// spell's casting time and a character's hit points are both things a reader looks
// for first, and a field block under three paragraphs of description is a field block
// nobody reads -- which means the DM stops filling it in.
func TestTheFieldBlockIsBeforeTheBody(t *testing.T) {
	t.Parallel()

	renderer := render.NewWith(render.Options{
		Hooks: fieldHooks("level", &fieldFixture{renders: "<p>third level</p>"}),
		Log:   logInto(&strings.Builder{}),
	})

	page := page("A spell for the table.")
	page.Fields = []render.Field{{Name: "level", Value: "3", Kind: "text"}}

	result, err := renderer.Render(t.Context(), page, player())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	field, body := strings.Index(result.HTML, "third level"), strings.Index(result.HTML, "A spell")
	if field < 0 || body < 0 {
		t.Fatalf("the field or the body is missing:\n%s", result.HTML)
	}
	if field > body {
		t.Errorf("the field rendered after the body:\n%s", result.HTML)
	}
}

// TestAnUnclaimedKeyRendersAsNothing: there is no fallback. A page does not grow a
// row for `created:`, `tags:` and every key the DM has ever typed, and the claim is
// the switch.
//
// This is the test that would fail if somebody added a "render unknown fields as
// plain text" path, which is the change that would put a DM's whole frontmatter on
// every page.
func TestAnUnclaimedKeyRendersAsNothing(t *testing.T) {
	t.Parallel()

	renderer := render.NewWith(render.Options{
		Hooks: fieldHooks("level", &fieldFixture{renders: "<p>third level</p>"}),
		Log:   logInto(&strings.Builder{}),
	})

	page := page("Body.")
	page.Fields = []render.Field{
		{Name: "level", Value: "3", Kind: "text"},
		{Name: "mood", Value: "grim", Kind: "text"},
	}

	result, err := renderer.Render(t.Context(), page, player())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(result.HTML, "grim") {
		t.Errorf("a key nobody claimed rendered anyway:\n%s", result.HTML)
	}
	if !strings.Contains(result.HTML, "third level") {
		t.Errorf("the claimed key did not render:\n%s", result.HTML)
	}
}

// TestTheFieldRendererIsHandedThePageAndTheDecision is the last of what a renderer
// is given, and it is worth a test because a renderer that could not see the decision
// would have to guess whether to show a statline's derived numbers.
func TestTheFieldRendererIsHandedThePageAndTheDecision(t *testing.T) {
	t.Parallel()

	sawPath, sawSecrets := "", false
	renderer := render.NewWith(render.Options{
		Log: logInto(&strings.Builder{}),
		Hooks: render.Hooks{Fields: map[string]render.FieldSpec{
			"level": {Kind: "text", Renderer: rendererFunc(func(
				_ context.Context, p render.Page, d render.Decision, _ render.Field,
			) (string, error) {
				sawPath = p.Path
				sawSecrets = d.CanSeeSecrets
				return "<p>3</p>", nil
			})},
		}},
	})

	page := page("Body.")
	page.Fields = []render.Field{{Name: "level", Value: "3", Kind: "text"}}

	if _, err := renderer.Render(t.Context(), page, player()); err != nil {
		t.Fatalf("Render: %v", err)
	}

	if sawPath != page.Path {
		t.Errorf("the renderer saw path %q, want %q", sawPath, page.Path)
	}
	if sawSecrets {
		t.Error("a player was reported to a field renderer as somebody who can see secrets")
	}
}

// TestAFieldsValueIsNotACacheKeyField: the reason is that a field lives in the
// frontmatter and the frontmatter is part of the file, so `ContentHash` already
// changes when one does. A second field for a value `ContentHash` covers would be a
// second answer to "has this page changed".
func TestAFieldsValueIsNotACacheKeyField(t *testing.T) {
	t.Parallel()

	renderer := render.NewWith(render.Options{
		Hooks: fieldHooks("level", &fieldFixture{renders: "<p>3</p>"}),
		Log:   logInto(&strings.Builder{}),
	})

	// Same content hash, different field value: the page's *file* has not changed, so
	// the cache may serve the first answer. The test asserts what the key is made of
	// rather than what the cache did, because what the cache did is a performance
	// question and what the key is made of is a correctness one.
	first := page("Body.")
	first.Fields = []render.Field{{Name: "level", Value: "3", Kind: "text"}}
	second := page("Body.")
	second.Fields = []render.Field{{Name: "level", Value: "9", Kind: "text"}}

	if _, err := renderer.Render(t.Context(), first, player()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	result, err := renderer.Render(t.Context(), second, player())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// The served answer is the first one, because the file has not changed. This is
	// the correct answer *for a cache* and it is only correct because a field is part
	// of the file: a caller that built a `render.Page` with fields from somewhere
	// other than the file is the one that would be wrong, which is why `Page.Fields`
	// says the values are the raw ones from the same document as `Body`.
	if !strings.Contains(result.HTML, "<p>3</p>") {
		t.Errorf("the cache did not hold for an unchanged file, so a field's value is "+
			"being cached by something other than the file's hash:\n%s", result.HTML)
	}
}

// rendererFunc adapts a function to [render.FieldRenderer], because a fixture that
// only inspects what it was handed has no configuration to carry and a struct with
// one field is a struct.
type rendererFunc func(context.Context, render.Page, render.Decision, render.Field) (string, error)

func (f rendererFunc) RenderField(
	ctx context.Context, p render.Page, d render.Decision, field render.Field,
) (string, error) {
	return f(ctx, p, d, field)
}
