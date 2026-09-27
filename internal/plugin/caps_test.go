package plugin_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// drawsSomething is a field renderer that returns nothing, which is a legal
// `FieldRenderer`: it is what a plugin says for a key it claimed and has nothing to
// say about yet, and the registry's job is to hold the claim rather than to judge
// the drawing.
//
// It exists because the field tests in this file are about *claims* — the reserved
// names, the duplicates, the key's shape — and none of them should have to invent
// HTML to say so.
type drawsNothing struct{}

func (drawsNothing) RenderField(
	context.Context, render.Page, render.Decision, render.Field,
) (string, error) {
	return "", nil
}

var drawsSomething drawsNothing

// TestAPluginMayNotRedefineSomethingCoreOwns is the composition-not-override
// guarantee at the only place it can be structural: a name.
//
// The page types are the interesting half. `domain.PageType.IsCore` exists so that
// core can tell a plugin's type from its own, and a plugin that claimed `character`
// would make that function answer about a type whose ownership rule — a character
// page owns itself and its subtree follows it — does not apply to it. The
// collision is not a cosmetic one, and this is the test that says so.
func TestAPluginMayNotRedefineSomethingCoreOwns(t *testing.T) {
	t.Parallel()

	t.Run("page types", func(t *testing.T) {
		t.Parallel()

		for _, core := range domain.CorePageTypes() {
			reg := quiet(t)
			err := reg.Add(&stub{
				name:    "impostor",
				version: "1.0.0",
				setup:   func(r *plugin.Registry) error { return r.AddPageType(core, "mine now") },
			})
			if !errors.Is(err, plugin.ErrReservedName) {
				t.Errorf("AddPageType(%q) = %v, want ErrReservedName", core, err)
			}
			if !strings.Contains(err.Error(), "impostor") {
				t.Errorf("error %q does not name the plugin that asked", err)
			}
		}
	})

	t.Run("frontmatter keys", func(t *testing.T) {
		t.Parallel()

		// Every key core owns, not the two that decide access. `title:` being
		// reserved is as much a part of the contract as `visibility:` is, and a
		// table that listed only the interesting ones would let a future key be
		// added here without a test noticing.
		for _, core := range plugin.CoreFields() {
			reg := quiet(t)
			err := reg.Add(&stub{
				name:    "impostor",
				version: "1.0.0",
				setup: func(r *plugin.Registry) error {
					return r.AddFieldType(plugin.FieldType{
						Name: core, Kind: "text", Summary: "mine now",
					}, drawsSomething)
				},
			})
			if !errors.Is(err, plugin.ErrReservedName) {
				t.Errorf("AddFieldType(%q) = %v, want ErrReservedName", core, err)
			}
		}
	})
}

// TestTwoPluginsMayNotWantTheSameName: the collision that is a plugin's fault
// rather than the core's, and which is therefore a different error and a different
// fix.
func TestTwoPluginsMayNotWantTheSameName(t *testing.T) {
	t.Parallel()

	t.Run("page type", func(t *testing.T) {
		t.Parallel()

		reg := quiet(t)
		claim := func(r *plugin.Registry) error { return r.AddPageType("spell", "a spell") }
		if err := reg.Add(&stub{name: "dnd5e", version: "1.0.0", setup: claim}); err != nil {
			t.Fatalf("first: %v", err)
		}

		err := reg.Add(&stub{name: "other", version: "1.0.0", setup: claim})
		if !errors.Is(err, plugin.ErrDuplicateName) {
			t.Errorf("second claim = %v, want ErrDuplicateName", err)
		}
	})

	t.Run("frontmatter key", func(t *testing.T) {
		t.Parallel()

		reg := quiet(t)
		claim := func(r *plugin.Registry) error {
			return r.AddFieldType(plugin.FieldType{
				Name: "AC", Kind: "number", Summary: "armour class",
			}, drawsSomething)
		}
		if err := reg.Add(&stub{name: "dnd5e", version: "1.0.0", setup: claim}); err != nil {
			t.Fatalf("first: %v", err)
		}

		err := reg.Add(&stub{name: "other", version: "1.0.0", setup: claim})
		if !errors.Is(err, plugin.ErrDuplicateName) {
			t.Errorf("second claim = %v, want ErrDuplicateName", err)
		}
	})
}

// TestAClaimIsRefusedRatherThanAcceptedEmpty: a claim with nothing in it is a
// claim nobody can read the meaning of, and a summary is the only thing that
// makes a claimed name usable by the next person — the DM reading the authoring
// guide, or the one plugin that wants to look up what another plugin claimed.
func TestAClaimIsRefusedRatherThanAcceptedEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		claim func(*plugin.Registry) error
	}{
		{
			name:  "a page type with no name",
			claim: func(r *plugin.Registry) error { return r.AddPageType("", "something") },
		},
		{
			name:  "a page type with no summary",
			claim: func(r *plugin.Registry) error { return r.AddPageType("spell", "  ") },
		},
		{
			name: "a field with no name",
			claim: func(r *plugin.Registry) error {
				return r.AddFieldType(plugin.FieldType{Kind: "text", Summary: "something"}, drawsSomething)
			},
		},
		{
			name: "a field with no kind",
			claim: func(r *plugin.Registry) error {
				return r.AddFieldType(plugin.FieldType{Name: "ac", Summary: "armour class"}, drawsSomething)
			},
		},
		{
			name: "a field with no summary",
			claim: func(r *plugin.Registry) error {
				return r.AddFieldType(plugin.FieldType{Name: "ac", Kind: "number"}, drawsSomething)
			},
		},
		{
			// The M12 half. A claim with no renderer is a key that renders as
			// nothing, and refusing it at startup is the difference between a DM
			// finding out from a log line and a DM finding out from a blank space.
			name: "a field with no renderer",
			claim: func(r *plugin.Registry) error {
				return r.AddFieldType(plugin.FieldType{
					Name: "ac", Kind: "number", Summary: "armour class",
				}, nil)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := quiet(t).Add(&stub{
				name: "sloppy", version: "1.0.0", setup: test.claim,
			})
			if err == nil {
				t.Error("Add accepted a claim with a hole in it")
			}
			if errors.Is(err, plugin.ErrDuplicateName) {
				t.Errorf("error %v is about a duplicate, but nothing was registered yet", err)
			}
		})
	}
}

// TestAClaimedFieldKeyIsNormalisedToLowerCase: frontmatter keys are lower case in
// every one of the core keys, and `AC:` and `ac:` being two keys would be a page
// whose frontmatter says both.
func TestAClaimedFieldKeyIsNormalisedToLowerCase(t *testing.T) {
	t.Parallel()

	reg := quiet(t)
	err := reg.Add(&stub{
		name: "dnd5e", version: "1.0.0",
		setup: func(r *plugin.Registry) error {
			return r.AddFieldType(plugin.FieldType{
				Name: "Spell-Slot", Kind: "text", Summary: "which slot",
			}, drawsSomething)
		},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	claimed := reg.FieldTypes()
	if len(claimed) != 1 {
		t.Fatalf("FieldTypes() = %v, want one entry", claimed)
	}
	if _, ok := claimed[vault.Key("spell-slot")]; !ok {
		t.Errorf("FieldTypes() = %v, want it keyed by spell-slot", claimed)
	}
}

// TestTheRegistryHandsOutCopiesNotItsOwnMaps: the claims are read by a sync and a
// handler while other goroutines render pages, and a map handed out by reference is
// a map two goroutines are one write away from a race.
func TestTheRegistryHandsOutCopiesNotItsOwnMaps(t *testing.T) {
	t.Parallel()

	reg := quiet(t)
	if err := reg.Add(&stub{
		name: "dnd5e", version: "1.0.0",
		setup: func(r *plugin.Registry) error {
			if err := r.AddPageType("spell", "a spell"); err != nil {
				return err
			}
			return r.AddFieldType(plugin.FieldType{
				Name: "ac", Kind: "number", Summary: "armour class",
			}, drawsSomething)
		},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	pages := reg.PageTypes()
	fields := reg.FieldTypes()
	if len(pages) != 1 || len(fields) != 1 {
		t.Fatalf("got %d page types and %d field types, want one of each", len(pages), len(fields))
	}

	// Mutating the copy must not be visible to the next reader. Adding is what a
	// real caller would never do; deleting is what a map handed out by reference
	// would let a careless caller do by accident.
	delete(pages, domain.PageType("spell"))
	delete(fields, vault.Key("ac"))

	if got := len(reg.PageTypes()); got != 1 {
		t.Errorf("deleting from a returned map changed the registry: PageTypes() now has %d", got)
	}
	if got := len(reg.FieldTypes()); got != 1 {
		t.Errorf("deleting from a returned map changed the registry: FieldTypes() now has %d", got)
	}
}

// TestCoreFieldsIsTheVaultsOwnList: the reservation list is single-sourced from
// the vault precisely so that it cannot go stale, and this is the test that would
// notice if somebody reintroduced a private copy.
func TestCoreFieldsIsTheVaultsOwnList(t *testing.T) {
	t.Parallel()

	got := plugin.CoreFields()
	want := vault.CoreKeys()

	if len(got) != len(want) {
		t.Fatalf("CoreFields() has %d keys and vault.CoreKeys() has %d", len(got), len(want))
	}
	if !slices.Equal(got, want) {
		t.Errorf("CoreFields() = %v, want %v", got, want)
	}

	// And the answer is a copy: the caller's deletion must not reach the vault's
	// list, which is a package variable there.
	first := plugin.CoreFields()
	first[0] = "tampered"
	if second := plugin.CoreFields(); second[0] != want[0] {
		t.Errorf("CoreFields() handed out its own slice: got %q after a caller wrote %q", second[0], "tampered")
	}
}
