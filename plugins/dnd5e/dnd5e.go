// Package dnd5e is the ruleset plugin: the page types a 5e vault actually has, the
// frontmatter fields they carry, and a statline for a character sheet.
//
// # What it demonstrates, and why it is the milestone
//
// M11's three plugins were demonstrations — a render hook, a policy, a search field.
// This one is a thing somebody would actually want, which makes it the first
// milestone where a plugin's **output** rather than a DM's markdown is the thing
// under review. A DM looking at a spell page is looking at a plugin's HTML, and the
// only way to know whether that HTML is any good is to read it.
//
// # The shape of the whole plugin
//
// **One renderer, many fields.** A 5e field means different things on different page
// types — `level` is a spell's level and nothing at all on a creature — so a renderer
// per key would have to know the page type anyway. There is one `dnd5eRenderer` and
// a table of field specs, and the renderer looks up the page's type and then the key.
//
// **A field that does not apply renders as nothing.** That is
// [render.FieldRenderer]'s contract and it is what stops `casting-time` appearing on
// a character page: the spec says the key is a spell's and the renderer declines. It
// is also the first real use of the "empty string for not mine" branch, and it is the
// reason one renderer is enough for sixteen fields.
//
// **The page types are the spec's four, and nothing else.** `spell`, `creature`,
// `feat` and `magic-item`. ADR 0010 made page types data precisely so this could be
// four registrations, and that it is that small is the argument for ADR 0010 rather
// than a counterexample to it.
//
// # What is not here, and §1's own list of why
//
// No dice roller, no combat tracker, no live rules engine, no map editor. A `5d6` in
// a statline is *text*, because this is a wiki a DM reads at the table and not an
// application that plays the game for them. The statline parses and lays out; it
// does not compute, roll, or track anything.
//
// The one thing that looks computed — a character's current hit points — is two
// fields and a sum the reader can see, and it is written as a *field* rather than
// derived because a derived number in a plugin is a number nobody can correct from
// Obsidian. There is no state here, and therefore no state to get out of step.
package dnd5e

import (
	"context"
	"fmt"
	"html"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// The four page types, and the one core type this plugin draws a field onto.
//
// `character` is core's, and that is the point of the character sheet: a player
// already looks up their character on a `character` page because §1 says a player
// "needs to look up their character", so a 5e statline belongs on the page the
// application already has rather than on a ninth page type that would be a second
// place to look and a second way to bind a player.
const (
	PageTypeSpell     = domain.PageType("spell")
	PageTypeCreature  = domain.PageType("creature")
	PageTypeFeat      = domain.PageType("feat")
	PageTypeMagicItem = domain.PageType("magic-item")
)

// The statline's own key.
//
// A page type constant beside it rather than a separate name, because a reader of
// `statline: ac 16, hp 34` in a frontmatter block is being told nothing about which
// kind of field it is, and the key is the only place that can say. The kind is
// carried separately and is a plugin's word, per `render.Field.Kind`.
const keyStatline = "statline"

// Plugin is the 5e ruleset plugin.
//
// It has no fields and no state. A ruleset is a table of facts — this key means this
// thing, on this kind of page — and the table is built per `Setup` and handed to the
// renderer, so a second copy of this plugin in one process would be two tables and
// two renderers rather than one table argued over.
type Plugin struct {
	log *slog.Logger
}

// New returns the plugin, logging through log. A nil logger means
// [slog.Default], as everywhere else in this project.
//
// It takes no store and no vault, and that is a claim about the plugin rather than an
// omission: everything it draws comes from a page it was handed. A ruleset that read
// the store would be a ruleset that could disagree with the page it is drawn on, and
// the page is the source of truth (ADR 0001).
func New(log *slog.Logger) *Plugin {
	if log == nil {
		log = slog.Default()
	}
	return &Plugin{log: log}
}

// Name is the plugin's identity.
func (p *Plugin) Name() string { return "dnd5e" }

// Version is this plugin's own version, and it is a date rather than a semver.
//
// A ruleset is versioned by edition, and "1.0.0" on a page that renders the 2014
// Player's Handbook's stat block is a number that means nothing to the person who
// has to judge whether the number is right. A handbook date is a number that means
// exactly one thing, and a DM reading a log line asking which rules this wiki uses
// gets an answer.
func (p *Plugin) Version() string { return "2014-09-19" }

// Setup registers the fields, the page types and the command.
//
// One renderer for all sixteen fields, so the loop below passes the same value
// sixteen times. The order of the loop does not matter and the comment on it says so,
// because a field's position on a page comes from the DM's frontmatter rather than
// from the order it was registered in — which is `internal/render`'s rule and the
// reason a field table can be reordered without moving a single statline.
func (p *Plugin) Setup(reg *plugin.Registry) error {
	specs := fields()
	renderer := newRenderer(specs)

	for _, spec := range specs {
		if err := reg.AddFieldType(spec.Field, renderer); err != nil {
			return err
		}
	}

	for _, pageType := range []domain.PageType{
		PageTypeSpell, PageTypeCreature, PageTypeFeat, PageTypeMagicItem,
	} {
		if err := reg.AddPageType(pageType, pageTypeSummary(pageType)); err != nil {
			return err
		}
	}

	return reg.AddCommand(plugin.Command{
		Name:    "character-sheet",
		Summary: "Print a character page's frontmatter, ready to paste into the vault.",
		Run:     p.runCharacterSheet,
	})
}

// pageTypeSummary is the one-line description a page type's claim carries.
//
// Written for a DM choosing a `type:` rather than for a rules manual, and the test
// is that each says what the page is *for* rather than what it is called.
func pageTypeSummary(t domain.PageType) string {
	switch t {
	case PageTypeSpell:
		return "A spell, with its level, school, casting time and classes."
	case PageTypeCreature:
		return "A creature, with its armour class, hit points, speed and challenge rating."
	case PageTypeFeat:
		return "A feat, with its prerequisite and the ability it improves."
	case PageTypeMagicItem:
		return "A magic item, with its rarity and what it requires."
	default:
		return string(t)
	}
}

// fieldSpec is one claimed frontmatter key: the claim, where it applies, and how it
// is drawn.
//
// It is a spec and not a `switch` on key names because a ruleset is data. A `switch`
// is where a second 5e plugin would end up, and the sixteen rows below are the whole
// of what a plugin adding a field to a 5e vault has to understand.
type fieldSpec struct {
	// Field is the claim: the key, the kind, and the one line a `wiki help` shows.
	Field plugin.FieldType

	// Types is the page types this field means something on, and empty means all of
	// them. `statline` is empty because a character sheet and a creature's stat block
	// are the same idea with different numbers in it.
	Types []domain.PageType

	// draw is the spec's own drawing.
	//
	// It returns a string and not a `(string, error)`, and the reason is that
	// **none of these three drawings can fail.** A labelled row cannot fail; a
	// statline of a list that parses by splitting lines cannot fail; a level that is
	// not a number is rendered as written rather than refused. Inventing an error to
	// satisfy a signature would be a lie about what can go wrong.
	//
	// [render.FieldRenderer] keeps its error because a *plugin's* drawing can fail —
	// one that reads a file, or asks a store, or rolls dice it should not. This
	// plugin takes no store and reads nothing, and its type says so.
	draw func(context.Context, render.Page, render.Decision, render.Field) string
}

// sentence is a key as a reader would see it: hyphens become spaces and the first
// letter is capitalised.
func sentence(name string) string {
	spaced := strings.ReplaceAll(name, "-", " ")
	if spaced == "" {
		return name
	}
	return strings.ToUpper(spaced[:1]) + spaced[1:]
}

// appliesTo is whether this field is part of this page type's answer.
//
// A field with no types applies everywhere and a field with types applies to exactly
// those. The comparison is over data rather than a `switch` for the reason the spec
// is data: adding a page type to a field is a row, not a case in two switches.
func (s fieldSpec) appliesTo(t domain.PageType) bool {
	if len(s.Types) == 0 {
		return true
	}
	for _, want := range s.Types {
		if want == t {
			return true
		}
	}
	return false
}

// fields is the 5e field table.
//
// It is a function rather than a package variable so that there is exactly one copy
// in the process, owned by the renderer that was handed it, and the only way to
// change a spec is to change this function.
func fields() []fieldSpec {
	return []fieldSpec{
		{
			// The statline, and the only field with its own drawing beyond the
			// spell's level. It is first in the table because it is the first thing a
			// reader of a character page looks at, and a character sheet without one
			// is a page of prose.
			Field: plugin.FieldType{
				Name:    vaultKey(keyStatline),
				Kind:    "statline",
				Summary: "A block of labelled numbers, laid out as a stat block.",
			},
			draw: drawStatline,
		},

		// The two spell fields that are not `Label: value`: the level because a
		// number and a word are worth different things, and nothing else.
		spellLevel("spell-level", "The spell's level, 0 for a cantrip.", PageTypeSpell),

		labelled("school", "text", "The spell's school, in the Player's Handbook's words.", PageTypeSpell),
		labelled("casting-time", "text", "How long the spell takes to cast.", PageTypeSpell),
		labelled("spell-range", "text", "The spell's range, in the Player's Handbook's words.", PageTypeSpell),
		labelled("components", "list", "The spell's components: v, s, m and what the material is.", PageTypeSpell),
		labelled("duration", "text", "How long the spell lasts.", PageTypeSpell),
		labelled("classes", "list", "Which classes can cast the spell.", PageTypeSpell),

		labelledAs("Armour class", "ac", "number", "The creature's armour class.", PageTypeCreature),
		labelled("hp", "text", "The creature's hit dice, and its average hit points.", PageTypeCreature),
		labelled("speed", "text", "How fast the creature moves.", PageTypeCreature),
		labelledAs("Challenge rating", "cr", "text", "The creature's challenge rating.", PageTypeCreature),

		labelled("prerequisite", "text", "What a character needs before they can take the feat.", PageTypeFeat),
		labelled("rarity", "text", "The item's rarity: common, uncommon, rare, very rare or legendary.", PageTypeMagicItem),
		labelled("attunement", "text", "Whether the item needs attunement, and by what it can be worn.", PageTypeMagicItem),
	}
}

// labelled is a field whose value is shown next to a name derived from its key.
//
// It is the shape thirteen of the sixteen fields want, which is the reason it is a
// constructor and not a template: a `Name: value` row is a paragraph with a
// `<strong>` in it, and every one of those fields is the same paragraph.
func labelled(name, kind, summary string, types ...domain.PageType) fieldSpec {
	return labelledAs(sentence(name), name, kind, summary, types...)
}

// labelledAs is [labelled] with a label of its own, for the two keys whose spelling
// does not make a label: `ac` is "Ac" and `cr` is "Cr", and a stat block with either
// in it is a stat block a reader has to work out.
//
// A separate constructor rather than a `Label` field on the spec, because the label
// is *captured* by the drawing and a field beside it would be a second source of
// truth for the same string.
func labelledAs(label, name, kind, summary string, types ...domain.PageType) fieldSpec {
	return fieldSpec{
		Field: plugin.FieldType{Name: vaultKey(name), Kind: kind, Summary: summary},
		Types: types,
		draw:  labeller(label),
	}
}

// spellLevel is the level field, and it is a constructor because it is the one field
// whose drawing is not a labelled row.
func spellLevel(name, summary string, types ...domain.PageType) fieldSpec {
	return fieldSpec{
		Field: plugin.FieldType{Name: vaultKey(name), Kind: "number", Summary: summary},
		Types: types,
		draw:  drawSpellLevel,
	}
}

// labeller is the drawing for a labelled row, closed over its label.
func labeller(label string) func(context.Context, render.Page, render.Decision, render.Field) string {
	return func(
		_ context.Context, _ render.Page, _ render.Decision, field render.Field,
	) string {
		if strings.TrimSpace(field.Value) == "" {
			return ""
		}
		return labelledRow(label, field.Value)
	}
}

// dnd5eRenderer is the [render.FieldRenderer] the plugin registers, once, for every
// key it claims.
//
// One renderer for the whole table rather than one per key, and the reason is that a
// field's meaning depends on the page it is on: `ac` is a creature's armour class and
// nothing anywhere else, and `statline` is a character sheet's and a creature's. A
// renderer per key would need the page type anyway, and a key this renderer does not
// know is a key it declines rather than a key some other renderer guesses at.
type dnd5eRenderer struct {
	specs map[string]fieldSpec
}

// newRenderer is the table as a renderer. One call site, so the map is built here
// rather than being a field a caller could forget to fill.
func newRenderer(specs []fieldSpec) *dnd5eRenderer {
	byName := make(map[string]fieldSpec, len(specs))
	for _, spec := range specs {
		byName[string(spec.Field.Name)] = spec
	}
	return &dnd5eRenderer{specs: byName}
}

// RenderField is the whole of the dispatch: find the spec, check the page type, draw.
//
// Both refusals return the empty string, which is [render.FieldRenderer]'s "not
// mine", and neither is an error. A key this build does not know and a key that does
// not apply to this page are the same answer for a reader — the field does not
// appear — and the second is not a failure of anything.
func (r *dnd5eRenderer) RenderField(
	ctx context.Context, page render.Page, decision render.Decision, field render.Field,
) (string, error) {
	spec, known := r.specs[field.Name]
	if !known || !spec.appliesTo(domain.PageType(page.Type)) {
		return "", nil
	}
	return spec.draw(ctx, page, decision, field), nil
}

// drawSpellLevel is the one field that needs a number to become a word.
//
// `0` is "Cantrip" to every player at the table and "0" to nobody, and a spell page
// that says **Level** 0 is a spell page a DM has to read twice. The spell's level is
// the only numeric field in a 5e vault where the number and the word are worth
// different things; `ac` is `16` for a reader and `16` for a DM.
func drawSpellLevel(
	_ context.Context, _ render.Page, _ render.Decision, field render.Field,
) string {
	trimmed := strings.TrimSpace(field.Value)
	if trimmed == "" {
		return ""
	}

	level, numeric := wholeNumber(trimmed)
	switch {
	case !numeric:
		// Not a number, and it is rendered as written rather than refused. A DM who
		// wrote "3rd" wrote it on purpose, and a field that vanishes is a field they
		// stop filling in.
		return labelledRow("Level", trimmed)
	case level == 0:
		return labelledRow("Level", "Cantrip")
	case level < 0:
		return labelledRow("Level", trimmed)
	default:
		return labelledRow("Level", ordinal(level))
	}
}

// wholeNumber is a value that is entirely a number, and whether it is.
//
// The "entirely" is the point: `strconv.Atoi` on ` 3 ` fails and `3rd` fails, and
// both of those are values a DM wrote on purpose. Only a value that is a number and
// nothing else is a level.
func wholeNumber(value string) (int, bool) {
	level, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return level, true
}

// labelledRow is one `**Name** value` paragraph, escaped.
//
// It is a function because three of the four drawings in this file need it and a
// `fmt.Sprintf` in three places is three places that will disagree about whether the
// value is escaped.
func labelledRow(label, value string) string {
	return fmt.Sprintf(`<p><strong>%s</strong> %s</p>`,
		html.EscapeString(label), html.EscapeString(value))
}

// ordinal is 1st through 20th, and it stops being clever past that.
//
// A spell's level is 0 to 9 in the 2014 Player's Handbook and a homebrew campaign
// might have more, so the suffixes carry on. What they do not carry on to is a
// `switch` with a case per number, which is what this replaces: the two irregulars
// are `1st` and `2nd` and everything between 3 and 20 is `th`.
func ordinal(n int) string {
	switch n {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	default:
		return strconv.Itoa(n) + suffixFor(n)
	}
}

// suffixFor is `st`, `nd`, `rd` or `th` for a whole number, which is the last two
// digits for everything past twenty and a short list below that.
//
// The list is a list rather than arithmetic because 111th is "th" and 112th is
// "th" and 113th is "th" — the rule people are taught, "add one to the last two
// unless they are 11, 12 or 13", is a rule with eleven exceptions below twenty and
// none above it.
func suffixFor(n int) string {
	if n <= 0 {
		return "th"
	}

	lastTwo := n % 100
	if lastTwo >= 11 && lastTwo <= 13 {
		return "th"
	}

	switch n % 10 {
	case 1:
		return "st"
	case 2:
		return "nd"
	case 3:
		return "rd"
	default:
		return "th"
	}
}

// runCharacterSheet is `wiki character-sheet <name>`.
//
// It prints a frontmatter block and nothing else, because the vault is the DM's and
// a command that wrote a file would be writing into somebody's campaign from a
// process that has not opened it. Printing is the whole of the affordance: a DM
// redirects it, or copies it, or reads it and types the two lines that matter.
//
// The output is a **scaffold and not a validation**. It is the fields the table knows
// and their defaults, and a DM who wants two hit points instead of a formula deletes
// a line. A command that refused to print a sheet with the wrong fields would be a
// rules engine, and §1's non-goals are the reason this is not one.
func (p *Plugin) runCharacterSheet(_ context.Context, args []string, stdout, _ io.Writer) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		_, err := io.WriteString(stdout, "usage: wiki character-sheet <name>\n")
		return err
	}

	name := strings.TrimSpace(args[0])

	_, err := fmt.Fprintf(stdout, `---
title: %q
type: character
visibility: dm-and-owner
`+keyStatline+`:
  - "Armour class  14"
  - "Hit points  3d8+9 (22)"
  - "Speed  30 ft."
  - "Proficiency bonus  2"
  - "Passive perception  12"
---

# %s

Put the character's notes here. The statline above is rendered from the
frontmatter; everything below this line is yours and is rendered as markdown.

`, name, name)
	return err
}

// drawStatline is the stat block, and it is the field a character sheet is.
//
// The value is a list of `Name value` rows -- one per frontmatter list item, or one
// per line of a scalar -- and the drawing is a list of them inside a callout. Two
// things about that shape are decisions:
//
//   - **A callout.** `callout-[a-z0-9-]+` is the one class shape the sanitiser
//     admits on purpose, so a stat block is a callout of type `dnd5e` and needs
//     nothing added to the allow-list. It is the same answer ADR 0022 recorded for
//     `house-rules`, and it is the answer because it was designed to be this one.
//   - **The rows are the DM's rows.** A statline of `Armour class 14` is one line in
//     one list item, and the statline draws it. It does not parse `14` out of it and
//     decide what a stat block is, because a DM who writes "AC 14 (16 with a shield)"
//     has written something a parser would have to be clever about.
//
// A row with no name -- an item that is just a value, or an empty line -- is
// **skipped**, not drawn as a nameless row. A stat block with a blank first column
// is a stat block a reader has to look at twice, and the answer a DM wanted is
// already in the value.
func drawStatline(
	_ context.Context, _ render.Page, _ render.Decision, field render.Field,
) string {
	rows := statlineRows(field.Value)
	if len(rows) == 0 {
		return ""
	}

	var out strings.Builder
	out.WriteString(`<section class="callout callout-dnd5e"><p><strong>Statline</strong></p><ul>`)
	for _, row := range rows {
		out.WriteString("<li>")
		out.WriteString(html.EscapeString(row.name))
		if row.value != "" {
			out.WriteString(" ")
			out.WriteString(html.EscapeString(row.value))
		}
		out.WriteString("</li>")
	}
	out.WriteString("</ul></section>")

	return out.String()
}

// statRow is one row of a statline: what it is called and what it says.
type statRow struct {
	name  string
	value string
}

// statlineRows is a statline's rows, in the order the DM wrote them.
//
// A value that is one line is split at the first run of spaces, so `ac 14` and
// `Armour class 14` both work and the DM is not asked to learn a syntax. A value with
// no space is a row with a name and no value -- which happens, and is a row worth
// drawing: a statline that says just "Shield" is a reminder.
func statlineRows(value string) []statRow {
	lines := strings.Split(value, "\n")

	rows := make([]statRow, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A list item reaches here as the whole value joined by the seam, so a
		// statline written as YAML is a handful of lines the same way a scalar one
		// is. The join used is a space, which is why a row's own spaces and a row
		// boundary are told apart by the newline the caller put in or did not.
		if name, rest, split := strings.Cut(trimmed, "  "); split && strings.TrimSpace(name) != "" {
			rows = append(rows, statRow{name: strings.TrimSpace(name), value: strings.TrimSpace(rest)})
			continue
		}
		rows = append(rows, statRow{name: trimmed})
	}

	if len(rows) == 0 {
		return nil
	}
	return rows
}

// vaultKey is a `vault.Key` for a field table row.
//
// It is a function rather than a constant because a `vault.Key` is a named string
// and a table that wrote `vault.Key("ac")` sixteen times is harder to read than
// `vaultKey("ac")`.
func vaultKey(name string) vault.Key { return vault.Key(name) }
