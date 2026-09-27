package dnd5e_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin/contract"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/dnd5e"
)

// TestDnd5ePassesTheContract is the line every plugin in this repository has to be
// able to write, and it is one line because the suite is one function.
//
// For this plugin the contract is worth more than for the M11 three: it registers
// sixteen field claims, four page types and a command, and a plugin that registers
// that much is a plugin that can get one of them wrong.
func TestDnd5ePassesTheContract(t *testing.T) {
	t.Parallel()

	contract.Run(t, dnd5e.New(slog.Default()))
}

// render is the whole of the plugin's output for one field, through the registry, so
// that the tests go through the same composition a page render does rather than
// calling a drawing function directly.
func renderField(t *testing.T, page render.Page, frontmatterKey, value string) string {
	t.Helper()

	reg := plugin.New(slog.New(slog.NewTextHandler(&strings.Builder{},
		&slog.HandlerOptions{Level: slog.LevelError + 8})))
	if err := reg.Add(dnd5e.New(nil)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	spec, claimed := reg.RenderHooks().Fields[frontmatterKey]
	if !claimed {
		t.Fatalf("the plugin does not claim %q; it claims %v", frontmatterKey, claimNames(reg))
	}

	html, err := spec.Renderer.RenderField(t.Context(), page, render.Grant(), render.Field{
		Name:  frontmatterKey,
		Value: value,
		Kind:  spec.Kind,
	})
	if err != nil {
		t.Fatalf("RenderField(%s): %v", frontmatterKey, err)
	}
	return html
}

func claimNames(reg *plugin.Registry) []string {
	names := make([]string, 0, 16)
	for name := range reg.RenderHooks().Fields {
		names = append(names, name)
	}
	return names
}

func pageOf(t domain.PageType) render.Page {
	return render.Page{
		Campaign: "blackwater", Path: "spells/fireball", Body: "A ball of fire.",
		ContentHash: "h1", Type: t.String(),
	}
}

// TestTheFourPageTypesAreClaimedAndNothingElse: §12's table says `spell`, `creature`,
// `feat` and `magic-item`, and the value of a bounded list is that it is bounded —
// so the test asserts the count, not only that these four are present.
func TestTheFourPageTypesAreClaimedAndNothingElse(t *testing.T) {
	t.Parallel()

	reg := plugin.New(slog.Default())
	if err := reg.Add(dnd5e.New(nil)); err != nil {
		t.Fatalf("Add: %v", err)
	}

	claimed := reg.PageTypes()
	if len(claimed) != 4 {
		t.Errorf("the plugin claims %d page types, want 4: %v", len(claimed), claimed)
	}

	for _, want := range []domain.PageType{
		dnd5e.PageTypeSpell, dnd5e.PageTypeCreature, dnd5e.PageTypeFeat, dnd5e.PageTypeMagicItem,
	} {
		summary, ok := claimed[want]
		if !ok {
			t.Errorf("the plugin does not claim %q", want)
			continue
		}
		if summary == "" {
			t.Errorf("%q has no summary; a `type:` a DM cannot choose between is a type they guess at", want)
		}
	}
}

// TestAFieldThatDoesNotApplyToThePageRendersAsNothing is the first real use of
// `render.FieldRenderer`'s "empty string for not mine", and it is the property that
// lets one renderer serve sixteen fields: `casting-time` is a spell's and nothing
// else, and a character page that said "Casting time 1 action" would be a bug a DM
// reports as "the wiki is showing the wrong thing".
func TestAFieldThatDoesNotApplyToThePageRendersAsNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pageType domain.PageType
		key      string
		want     string
	}{
		{
			name:     "a spell's casting time on a spell",
			pageType: dnd5e.PageTypeSpell, key: "casting-time", want: "Casting time",
		},
		{
			name:     "a spell's casting time on a creature",
			pageType: dnd5e.PageTypeCreature, key: "casting-time", want: "",
		},
		{
			name:     "a creature's armour class on a character",
			pageType: domain.PageTypeCharacter, key: "ac", want: "",
		},
		{
			name:     "a creature's armour class on a creature",
			pageType: dnd5e.PageTypeCreature, key: "ac", want: "Armour class",
		},
		{
			name:     "an item's rarity on a feat",
			pageType: dnd5e.PageTypeFeat, key: "rarity", want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := renderField(t, pageOf(test.pageType), test.key, "1 action")
			if test.want == "" {
				if got != "" {
					t.Errorf("got %q, want nothing", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Errorf("got %q, want it to carry %q", got, test.want)
			}
		})
	}
}

// TestTheStatlineAppliesToEveryPageType is the other half, and it is why the statline
// has no `types`: a character sheet and a creature's stat block are the same idea
// with different numbers in it, and a field that only worked on one of them would be
// a field whose table somebody had to remember.
func TestTheStatlineAppliesToEveryPageType(t *testing.T) {
	t.Parallel()

	for _, pageType := range []domain.PageType{
		domain.PageTypeCharacter, dnd5e.PageTypeCreature, dnd5e.PageTypeSpell,
	} {
		got := renderField(t, pageOf(pageType), "statline", "Armour class  14\nHit points  22")
		if !strings.Contains(got, "Armour class") || !strings.Contains(got, "14") {
			t.Errorf("on a %s page the statline drew %q", pageType, got)
		}
	}
}

// TestASpellLevelIsAWordAndACantripIsNotAZero: the one field where the number and
// the word are worth different things, and the reason this plugin has a drawing
// function at all.
func TestASpellLevelIsAWordAndACantripIsNotAZero(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  string
	}{
		{value: "0", want: "Cantrip"},
		{value: "1", want: "1st"},
		{value: "2", want: "2nd"},
		{value: "3", want: "3rd"},
		{value: "4", want: "4th"},
		{value: "9", want: "9th"},
		{value: "11", want: "11th"},
		{value: "12", want: "12th"},
		{value: "13", want: "13th"},
		{value: "21", want: "21st"},
		{value: "111", want: "111th"},
		{value: "112", want: "112th"},
		// Not a number is rendered as written rather than refused: a DM who wrote
		// "3rd" wrote it on purpose, and a field that vanishes is a field they stop
		// filling in.
		{value: "3rd", want: "3rd"},
		{value: "-1", want: "-1"},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			t.Parallel()

			got := renderField(t, pageOf(dnd5e.PageTypeSpell), "spell-level", test.value)
			if !strings.Contains(got, test.want) {
				t.Errorf("level %q drew %q, want it to say %q", test.value, got, test.want)
			}
		})
	}
}

// TestAnEmptyValueDrawsNothing: a DM who has not filled in `school:` yet should not
// see a row with a label and nothing after it, on every spell in the campaign.
func TestAnEmptyValueDrawsNothing(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "   ", "\n"} {
		if got := renderField(t, pageOf(dnd5e.PageTypeSpell), "school", value); got != "" {
			t.Errorf("an empty %q drew %q, want nothing", value, got)
		}
	}
}

// TestEveryDrawnValueIsEscaped is the plugin-side half of the sanitiser guarantee: a
// statline is a DM's own text and a `[!SECRET]` in it arrives redacted, but a value
// that is *not* a secret can still contain a `<` and a quote, and this is the test
// that says the plugin escapes rather than relying on what comes after it.
func TestEveryDrawnValueIsEscaped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
		page  domain.PageType
	}{
		{name: "a statline row", key: "statline", value: `<b>Armour class</b>  14`,
			page: domain.PageTypeCharacter},
		{name: "a labelled value", key: "school", value: `evocation <script>alert(1)</script>`,
			page: dnd5e.PageTypeSpell},
		{name: "a level", key: "spell-level", value: `3 <em>rd</em>`, page: dnd5e.PageTypeSpell},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := renderField(t, pageOf(test.page), test.key, test.value)
			if strings.Contains(got, "<script") || strings.Contains(got, "<em>") ||
				strings.Contains(got, "<b>") {
				t.Errorf("the drawn field carries raw markup: %s", got)
			}
			// The escaped form has to be *present*, not merely the raw form absent: a
			// value that vanished is a value a DM stops filling in, and it passes a
			// test that only looks for the absence of markup.
			if !strings.Contains(got, "&lt;") {
				t.Errorf("the value is missing rather than escaped: %s", got)
			}
		})
	}
}

// TestTheStatlineKeepsTheDmsRows is the property that makes it a *statline* and not
// a stat block renderer: a DM who wrote "AC 14 (16 with a shield)" wrote something a
// parser would have to be clever about, and the plugin's job is to lay it out.
func TestTheStatlineKeepsTheDmsRows(t *testing.T) {
	t.Parallel()

	got := renderField(t, pageOf(dnd5e.PageTypeCreature), "statline",
		"Armour class  14\nHit points  7d8+21 (56)\nSpeed  30 ft.")

	for _, want := range []string{"Armour class", "14", "7d8+21 (56)", "30 ft."} {
		if !strings.Contains(got, want) {
			t.Errorf("the statline is missing %q:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "<ul>") || !strings.Contains(got, "callout-dnd5e") {
		t.Errorf("the statline is not a callout of a list:\n%s", got)
	}
}

// TestAStatlineRowWithNoNameIsSkipped: a row with a blank first column is a row a
// reader looks at twice, and the answer a DM wanted is already in the value.
func TestAStatlineRowWithNoNameIsSkipped(t *testing.T) {
	t.Parallel()

	got := renderField(t, pageOf(domain.PageTypeCharacter), "statline",
		"   \nArmour class  14")

	if strings.Count(got, "<li>") != 1 {
		t.Errorf("the statline drew %d rows, want 1:\n%s", strings.Count(got, "<li>"), got)
	}
}

// TestTheVersionIsAHandbookDate is a test of a decision rather than of behaviour: a
// ruleset is versioned by edition, and a DM reading a log line asking which rules
// this wiki uses gets an answer they can act on.
func TestTheVersionIsAHandbookDate(t *testing.T) {
	t.Parallel()

	got := dnd5e.New(nil).Version()
	if len(got) != len("2014-09-19") {
		t.Errorf("Version() = %q, want a handbook date like 2014-09-19", got)
	}
}

// context they do not use and a plugin author writing a drawing will too.
var _ = context.Background
