package fields_test

import (
	"context"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/fields"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
)

// A field's value is plain text by the time a renderer sees it, so a fixture
// renderer that reports what it was handed is enough to test the whole seam: the
// name, the order, and the value's redaction all arrive here and nowhere earlier.
//
// It is a struct rather than a function type because the tests need to *inspect*
// what it saw, and a closure over a slice would be the same thing with more
// indirection.
type recorder struct {
	seen  []render.Field
	order []string
}

func (r *recorder) RenderField(
	_ context.Context, _ render.Page, _ render.Decision, field render.Field,
) (string, error) {
	r.seen = append(r.seen, field)
	r.order = append(r.order, field.Name)
	return "<p>" + field.Name + "</p>", nil
}

// claimHooks builds a hook set whose claimed keys are all drawn by one recorder that
// throws the values away, for the tests that care about *which* fields came back and
// not what a renderer saw.
func claimHooks(names ...string) render.Hooks {
	set := make(map[string]render.FieldSpec, len(names))
	for _, name := range names {
		set[name] = render.FieldSpec{Kind: "text", Renderer: &recorder{}}
	}
	return render.Hooks{Fields: set}
}

// TestTheOrderIsTheOrderTheDmWrote is the property, and it is the reason `vault.Fields`
// returns a slice as well as a map: a page whose fields rearrange themselves between
// builds is a page nobody can screenshot, and a map is unordered by construction.
func TestTheOrderIsTheOrderTheDmWrote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		frontmatter string
		claimed     []string
		want        []string
	}{
		{
			name:        "the file's order, not the claims' order",
			frontmatter: "school: evocation\nlevel: 3\ncasting_time: 1 action",
			claimed:     []string{"level", "school", "casting-time"},
			want:        []string{"school", "level", "casting-time"},
		},
		{
			name:        "a key nobody claimed is not a field",
			frontmatter: "mood: grim\nlevel: 3",
			claimed:     []string{"level"},
			want:        []string{"level"},
		},
		{
			name:        "a claimed key the file does not have is not a field",
			frontmatter: "level: 3",
			claimed:     []string{"level", "school"},
			want:        []string{"level"},
		},
		{
			name:        "a list is joined rather than refused",
			frontmatter: "components:\n  - v\n  - s",
			claimed:     []string{"components"},
			want:        []string{"components"},
		},
		{
			// A key with nothing after it is a key the DM has not filled in, and a
			// renderer that cannot tell it from an absent one renders a row of
			// nothing for every spell somebody has not finished.
			name:        "a key with no value is a field with an empty value",
			frontmatter: "school:",
			claimed:     []string{"school"},
			want:        []string{"school"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			rec := &recorder{}
			set := render.Hooks{Fields: make(map[string]render.FieldSpec, len(test.claimed))}
			for _, name := range test.claimed {
				set.Fields[name] = render.FieldSpec{Kind: "text", Renderer: rec}
			}

			claimed := fields.Of(set, test.frontmatter)

			var got []string
			for _, field := range claimed {
				got = append(got, field.Name)
			}
			if strings.Join(got, ",") != strings.Join(test.want, ",") {
				t.Errorf("fields.Of = %v, want %v", got, test.want)
			}
		})
	}
}

// TestTheValueIsAJoinForAList: `components: v, s, m` is the obvious way for a DM to
// write three values and a plugin that wanted three would rather have "v s m" than
// an error.
func TestTheValueIsAJoinForAList(t *testing.T) {
	t.Parallel()

	set := claimHooks("components")
	claimed := fields.Of(set, "components:\n  - v\n  - s\n  - m")

	if len(claimed) != 1 {
		t.Fatalf("fields.Of returned %d fields, want 1", len(claimed))
	}
	if claimed[0].Value != "v s m" {
		t.Errorf("Value = %q, want %q", claimed[0].Value, "v s m")
	}
}

// TestABuildWithNoPluginsHasNoFields is the property that kept every existing golden
// file valid through two milestones: a build that registered nothing renders a page
// with no field block, byte for byte, and the cost is one length check.
func TestABuildWithNoPluginsHasNoFields(t *testing.T) {
	t.Parallel()

	frontmatter := "level: 3\nschool: evocation"

	for _, set := range []render.Hooks{{}, {Hooks: nil, Fields: nil}} {
		if got := fields.Of(set, frontmatter); got != nil {
			t.Errorf("fields.Of on an empty hook set = %v, want nil", got)
		}
	}

	// And a hook set with claims but a page with no frontmatter at all.
	set := claimHooks("level")
	if got := fields.Of(set, ""); got != nil {
		t.Errorf("fields.Of with no frontmatter = %v, want nil", got)
	}
	if got := fields.Of(set, "   \n  "); got != nil {
		t.Errorf("fields.Of with blank frontmatter = %v, want nil", got)
	}
}

// TestAMappingIsNotAField: there is no sensible flattening of a nested map into a
// string, and a plugin that wants structure wants a different capability.
func TestAMappingIsNotAField(t *testing.T) {
	t.Parallel()

	set := claimHooks("spellcasting")
	if got := fields.Of(set, "spellcasting:\n  school: evocation\n  level: 3"); got != nil {
		t.Errorf("fields.Of on a mapping value = %v, want nil", got)
	}
}

// TestARepeatedKeyTakesTheLastValueAndTheFirstPosition: a repeated key is a YAML
// error a DM's editor would have caught, and the *last* one is what most YAML
// readers use. Matching that is the least surprising answer.
func TestARepeatedKeyTakesTheLastValueAndTheFirstPosition(t *testing.T) {
	t.Parallel()

	set := claimHooks("level")
	claimed := fields.Of(set, "level: 1\nschool: evocation\nlevel: 3")

	if len(claimed) != 1 {
		t.Fatalf("fields.Of returned %d fields, want 1: %v", len(claimed), claimed)
	}
	if claimed[0].Value != "3" {
		t.Errorf("Value = %q, want the last one (%q)", claimed[0].Value, "3")
	}
}

// TestTheKindReachesTheRenderer: it is the plugin's own word, passed through rather
// than interpreted, and a core that dropped it would make every renderer guess.
func TestTheKindReachesTheRenderer(t *testing.T) {
	t.Parallel()

	rec := &recorder{}
	set := render.Hooks{Fields: map[string]render.FieldSpec{
		"statline": {Kind: "statline", Renderer: rec},
	}}

	claimed := fields.Of(set, "statline:\n  - ac 16\n  - hp 34")
	if len(claimed) != 1 {
		t.Fatalf("fields.Of returned %d fields, want 1", len(claimed))
	}
	if claimed[0].Kind != "statline" {
		t.Errorf("Kind = %q, want %q", claimed[0].Kind, "statline")
	}
}

// TestTheKeyIsFoldedOnBothSides is the finding that made this package necessary in
// its own right.
//
// A claim is written by a plugin author against a slug, because `AddFieldType`
// normalises one the way it normalises a plugin's own name. A key is written by a DM
// in whatever their editor produced, and `casting_time` is what half of them write.
// The first version of this seam compared the two strings, and a plugin claiming
// `casting-time` rendered nothing on a page whose frontmatter said
// `casting_time` — with no error anywhere, because a key that does not match is a
// key the page does not have.
//
// So both sides are folded, and the fold is ADR 0013's "read is forgiving; write is
// conventional": the application is a guest in the DM's file and is a guest in the
// plugin's build, and neither was wrong.
func TestTheKeyIsFoldedOnBothSides(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		claim string
		wrote string
	}{
		{name: "case", claim: "ac", wrote: "AC"},
		{name: "an underscore for a hyphen", claim: "casting-time", wrote: "casting_time"},
		{name: "a dot for a hyphen", claim: "hit-points", wrote: "hit.points"},
		{name: "a space for a hyphen", claim: "armour-class", wrote: "armour class"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			set := claimHooks(test.claim)
			got := fields.Of(set, test.wrote+": 16")
			if len(got) != 1 {
				t.Fatalf("a claim on %q did not match a file key of %q: %v", test.claim, test.wrote, got)
			}
			// The *claim's* name is what a renderer is handed, so a renderer
			// matching on the slug it was given works whatever the DM wrote.
			if got[0].Name != test.claim {
				t.Errorf("Name = %q, want the claim's own name %q", got[0].Name, test.claim)
			}
		})
	}
}

// TestTwoKeysThatFoldToTheSameNameTakeTheFirstPosition: a page with
// `casting_time` and `castingTime` is a YAML duplicate a DM's editor would have
// caught. The answer is *defined* -- the first in the file, and the first one's
// value -- rather than whichever the map happened to yield.
func TestTwoKeysThatFoldToTheSameNameTakeTheFirstPosition(t *testing.T) {
	t.Parallel()

	set := claimHooks("casting-time")
	got := fields.Of(set, "casting_time: 1 action\ncastingTime: 1 bonus action")

	if len(got) != 1 {
		t.Fatalf("fields.Of returned %d fields, want 1: %v", len(got), got)
	}
	if got[0].Value != "1 action" {
		t.Errorf("Value = %q, want the first key in the file (%q)", got[0].Value, "1 action")
	}
}

// TestAValueIsWhatTheDmWroteIncludingTheSecrets is the half that is *not* this
// package's business and must not be quietly wrong: the value arrives raw, because
// the redaction happens in the render under the decision.
//
// If a future change redacts here, this test fails — which is the intent. A
// redaction outside the render is a redaction under somebody else's decision, and
// there is exactly one decision. `TestAFieldsValueIsRedactedBeforeAPluginSeesIt` in
// `internal/render` is where the redaction is tested.
func TestAValueIsWhatTheDmWroteIncludingTheSecrets(t *testing.T) {
	t.Parallel()

	// A DM marking a field secret is the obvious way to use a `[!SECRET]` in
	// frontmatter, and the value arrives with it still in: the redaction happens in
	// the render, under the decision, and this test is what says so.
	const canary = "ULTRAMARINE-FOXTRAP-7742"
	set := claimHooks("mood")

	claimed := fields.Of(set, `mood: "[!SECRET] he is lying about `+canary+`"`)
	if len(claimed) != 1 {
		t.Fatalf("fields.Of returned %d fields, want 1", len(claimed))
	}
	if !strings.Contains(claimed[0].Value, canary) {
		t.Errorf("the value arrived already redacted (%q); the render is where that happens", claimed[0].Value)
	}
}
