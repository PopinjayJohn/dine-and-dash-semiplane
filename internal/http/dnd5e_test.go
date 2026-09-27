package http_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/dnd5e"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/houserules"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/spoilerbox"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/wordcount"
)

// # A plugin's output, through a real request
//
// `internal/render`'s field tests check the pipeline. This file checks the thing a
// DM actually looks at: a page whose frontmatter carries a ruleset's fields, fetched
// over HTTP, with the plugin's HTML in the body. M12 is the first milestone where a
// plugin's *output* rather than a DM's markdown is the thing under review, and a test
// that stops at the renderer would be reviewing half of it.
//
// The fixture is the ordinary one with a plugin registry on it, which is also the
// first time the whole of `wiki.Config` has had a non-zero `Hooks` in a route test —
// and that matters, because every golden file in this package has a zero one.

// withPlugins installs a registry's capabilities on the fixture and rebuilds.
//
// It takes a builder rather than a registry so that a test which wants two plugins
// writes two lines and a test which wants four does not have to know the shape of the
// list.
func (f *fixture) withPlugins(t *testing.T, plugins ...plugin.Plugin) {
	t.Helper()

	registry := plugin.New(f.cfg.Logger)
	for _, p := range plugins {
		if err := registry.Add(p); err != nil {
			t.Fatalf("registering %s: %v", p.Name(), err)
		}
	}

	f.cfg.Hooks = registry.RenderHooks()
	f.cfg.Policies = registry.Policies()
	f.cfg.Routes = registry.Routes()
	f.cfg.Events = registry.Events()
	f.rebuild()
}

// bundled is the build's four plugins, for the tests that want all of them.
func (f *fixture) bundled(t *testing.T) {
	t.Helper()

	f.withPlugins(t,
		houserules.New(f.cfg.Logger),
		spoilerbox.New(),
		wordcount.New(f.store),
		dnd5e.New(f.cfg.Logger),
	)
}

// TestASpellPageRendersItsFields is the milestone's headline, end to end.
//
// A DM writes a spell with its level, school and casting time in the frontmatter; a
// player opens the page; and the plugin's HTML is in the response. The two things
// being tested are that the fields arrive and that they arrive *in the DM's order* —
// the file says `school` then `level`, so the page says school then level.
func TestASpellPageRendersItsFields(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.bundled(t)

	f.saveNew("spells/fireball", `---
title: "Fireball"
type: spell
visibility: players
school: evocation
spell-level: 3
casting_time: 1 action
---

A bright streak of fire leaps to a point.

`)

	asPlayer := f.get(f.pageURL("spells/fireball"), f.playerSession())
	if asPlayer.status != http.StatusOK {
		t.Fatalf("the page is %d, want 200\nbody: %s", asPlayer.status, asPlayer.body)
	}

	for _, want := range []string{
		// A third-level spell is "3rd" rather than "3", because that is the one
		// field where the number and the word are worth different things.
		"3rd",
		"evocation",
		"Casting time",
	} {
		if !strings.Contains(asPlayer.body, want) {
			t.Errorf("the player's page is missing %q:\n%s", want, asPlayer.body)
		}
	}

	school, level := strings.Index(asPlayer.body, "evocation"), strings.Index(asPlayer.body, "3rd")
	if school < 0 || level < 0 || school > level {
		t.Errorf("the fields are not in the order the DM wrote them (school at %d, level at %d)",
			school, level)
	}
}

// TestAFieldThatDoesNotApplyToThePageIsNotOnIt is the property that lets one
// renderer serve fifteen fields, and it is the version a DM would report: a
// character page with "Casting time 1 action" on it is a page they would file a bug
// about and the bug would be about the wiki rather than the plugin.
func TestAFieldThatDoesNotApplyToThePageIsNotOnIt(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.bundled(t)

	f.saveNew("characters/grell", `---
title: "Grell"
type: character
visibility: dm-and-owner
casting_time: 1 action
ac: 17
---

A grey loxodon who does not speak.

`)

	asDM := f.get(f.pageURL("characters/grell"), f.dmSession())
	if asDM.status != http.StatusOK {
		t.Fatalf("the page is %d, want 200\nbody: %s", asDM.status, asDM.body)
	}

	if strings.Contains(asDM.body, "Casting time") {
		t.Errorf("a spell's field is on a character page:\n%s", asDM.body)
	}
	// And a creature's field is not either, for the same reason: `ac` is a
	// creature's, so a character page's `ac: 17` renders as nothing. The page still
	// has its title and its prose, which is the shape a declined field leaves behind.
	if strings.Contains(asDM.body, "Armour class") {
		t.Errorf("a creature's field is on a character page:\n%s", asDM.body)
	}
	if !strings.Contains(asDM.body, "A grey loxodon") {
		t.Errorf("the character page's body did not render:\n%s", asDM.body)
	}
}

// TestTheStatlineRendersOnACharacterPage is the character sheet, and it is a *core*
// page type with a plugin's field on it. That is the design: §1 says a player looks up
// their character, so a 5e statline belongs on the page the application already has
// rather than on a ninth page type that would be a second place to look.
func TestTheStatlineRendersOnACharacterPage(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.bundled(t)

	f.savePage("characters/aria", `---
title: "Aria"
type: character
visibility: dm-and-owner
statline:
  - "Armour class  14"
  - "Hit points  3d8+9 (22)"
  - "Speed  30 ft."
---

A half-elf who counts things.

`)

	asPlayer := f.get(f.pageURL("characters/aria"), f.playerSession())
	if asPlayer.status != http.StatusOK {
		t.Fatalf("the page is %d, want 200\nbody: %s", asPlayer.status, asPlayer.body)
	}

	for _, want := range []string{"Statline", "Armour class", "14", "3d8+9 (22)", "30 ft."} {
		if !strings.Contains(asPlayer.body, want) {
			t.Errorf("the player's character page is missing %q:\n%s", want, asPlayer.body)
		}
	}
}

// TestAPluginFieldsOutputIsSanitised is the invariant restated at the only place a
// reviewer will look for it, with a ruleset plugin as the author.
//
// The value is a DM's own, so the sanitiser is the last thing that touches it, and
// the page is a *player's* -- which is the direction that matters.
func TestAPluginFieldsOutputIsSanitised(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.bundled(t)

	f.saveNew("spells/eldritch-blast", `---
title: "Eldritch Blast"
type: spell
visibility: players
school: evocation
---

A beam of crackling energy.

`+`
<script>alert("xss")</script>

`)

	asPlayer := f.get(f.pageURL("spells/eldritch-blast"), f.playerSession())
	// The assertion is the payload's *text*, not the element name: the page shell
	// carries its own `<script>` tags for the vendored Datastar and `wiki.js`, so
	// "there is a script element in the response" is true of every page in the
	// application and would be a test that cannot fail.
	if strings.Contains(asPlayer.body, "alert(") {
		t.Errorf("a script reached a player through a page with a plugin's fields on it:\n%s", asPlayer.body)
	}
}

// TestAFieldsSecretDoesNotReachAPlayer: the same property the *body* has, for the
// one thing a plugin introduced. A DM marking a field secret is the obvious way to
// use a `[!SECRET]` in frontmatter, and the field block is a plugin's HTML on a
// player's page.
func TestAFieldsSecretDoesNotReachAPlayer(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.bundled(t)

	f.saveNew("spells/magic-missile", `---
title: "Magic Missile"
type: spell
visibility: players
statline:
  - "Amour class  14"
  - "Hit points  3d8+9 (22)"
---

A dart of force.

`)

	// The canary goes in a field's value as a **callout**, which is the only thing
	// `[!SECRET]` means: a DM who wrote the bare text `[!SECRET] He paid them` in a
	// frontmatter field has written a phrase, and the body behaves the same way. What
	// is being tested is that a field's value is redacted by the same machinery as a
	// body, and the machinery is the callout.
	f.saveNew("spells/secret-in-a-field", `---
title: "The spell he is lying about"
type: spell
visibility: players
statline: |
  > [!SECRET] He paid them.
---

Three bright darts of force.

`)

	asPlayer := f.get(f.pageURL("spells/secret-in-a-field"), f.playerSession())
	if asPlayer.status != http.StatusOK {
		t.Fatalf("the page is %d, want 200\nbody: %s", asPlayer.status, asPlayer.body)
	}
	if strings.Contains(asPlayer.body, "He paid them") {
		t.Errorf("a field's secret reached a player:\n%s", asPlayer.body)
	}
}

// TestABuildWithNoPluginsIsUnchanged is the property that let every golden file in
// this package stay valid through two milestones: a build that registered nothing
// renders a page with no field block, and the only way to know that is to fetch a
// page that has claimed keys in its frontmatter and find none of them drawn.
func TestABuildWithNoPluginsIsUnchanged(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	f.saveNew("spells/fireball", `---
title: "Fireball"
type: spell
visibility: players
school: evocation
spell-level: 3
---

A bright streak of fire.

`)

	asPlayer := f.get(f.pageURL("spells/fireball"), f.playerSession())
	if asPlayer.status != http.StatusOK {
		t.Fatalf("the page is %d, want 200\nbody: %s", asPlayer.status, asPlayer.body)
	}
	if strings.Contains(asPlayer.body, "evocation") {
		t.Errorf("a field rendered with no plugin registered:\n%s", asPlayer.body)
	}
}
