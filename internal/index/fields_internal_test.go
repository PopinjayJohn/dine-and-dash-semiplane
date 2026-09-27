// This file is inside the package because it asks `extraColumn` directly, and
// asking directly is the point: the alternative is a whole vault, a whole store and
// a sync to observe a function that takes a page and returns a string. A test that
// needs a database to check a string is a test that will be skipped one day.
package index

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The canary is this project's secret canary, the same string the secret-leak sweeps
// use. A field test that asserted "the secret is not in the index" with a string of
// its own would be a test that could pass while the real one failed.
const canary = "CANARY-SECRET-3f9a2b"

// counting is the plugin under test: every page contributes `words`, which is the
// whole reason the capability exists — a derived value, findable by search, with no
// column of its own.
type counting struct {
	pages  map[string]int
	broken map[string]bool
}

func (c *counting) Fields(_ context.Context, page domain.Page) (map[string]string, error) {
	if c.broken[page.Path] {
		return nil, errors.New("the word counter fell over")
	}
	words, found := c.pages[page.Path]
	if !found {
		return nil, nil
	}
	return map[string]string{"words": itoa(words)}, nil
}

// panicking is a plugin whose indexer crashes, which is the failure the recovery is
// for.
type panicking struct{}

func (panicking) Fields(context.Context, domain.Page) (map[string]string, error) {
	panic("a counter with no counter in it")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// extraFor is the extra column one indexer produces for the fixture page, with the
// logger thrown away.
func extraFor(t *testing.T, indexer Indexer) string {
	t.Helper()

	return extraColumn(t.Context(), pageFixture("locations/rivergate"), quietLog(), []SearchField{
		{Plugin: "wordcount", Indexer: indexer},
	})
}

// pageFixture is one ordinary page, and it is `domain.Page` rather than a file
// because what `extraColumn` reads is the derived value — a plugin gets the page the
// sync derived, not the markdown.
func pageFixture(path string) domain.Page {
	return domain.Page{
		ID:         "page-1",
		CampaignID: "blackwater",
		Path:       path,
		Title:      "Rivergate",
		Type:       domain.PageTypeLocation,
		Visibility: domain.VisibilityPlayers,
		Body:       "# Rivergate\n\nA fortified town.\n",
	}
}

// quietLog throws away the log lines, so a test's failure output is the assertion
// and not a plugin complaining.
func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(&strings.Builder{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

// TestAPluginsFieldReachesTheExtraColumn is the property, and it is the reason the
// capability exists: a plugin contributes a value, the value is findable, and it
// costs no column.
func TestAPluginsFieldReachesTheExtraColumn(t *testing.T) {
	t.Parallel()

	extra := extraFor(t, &counting{pages: map[string]int{"locations/rivergate": 1200}})

	if !strings.Contains(extra, "words 1200") {
		t.Errorf("the extra column = %q, want it to carry the plugin's field", extra)
	}
}

// TestTwoSearchFieldClausesAreBothFindable is the reason the value is written as
// `name value` rather than as a bare value.
//
// `extra:"words 1200"` finds the pair and a bare `1200` finds the other half of it,
// and both spellings find the page. The alternative -- a bare value with no name --
// would make a plugin's value indistinguishable from a word in the page's own body,
// which is how a plugin's derived number starts outranking a title match.
func TestTwoSearchFieldClausesAreBothFindable(t *testing.T) {
	t.Parallel()

	extra := extraFor(t, &counting{pages: map[string]int{"locations/rivergate": 1200}})

	for _, want := range []string{"words", "1200"} {
		if !strings.Contains(extra, want) {
			t.Errorf("the extra column = %q, want it to carry %q so a search for it finds the page", extra, want)
		}
	}
}

// TestAPluginsFieldGoesThroughTheSameRedactionABodyDoes is the security half, and it
// is a contract rather than a guarantee: `PublicText` removes *blocks*, not words,
// so a plugin that wanted to leak could just type the secret. What it catches is the
// accident — a plugin that indexes a slice of the body it was handed, callouts and
// all.
func TestAPluginsFieldGoesThroughTheSameRedactionABodyDoes(t *testing.T) {
	t.Parallel()

	leaky := fieldFunc(func(context.Context, domain.Page) (map[string]string, error) {
		return map[string]string{"excerpt": "the vault\n\n> [!SECRET] The vault\n> " + canary}, nil
	})

	extra := extraFor(t, leaky)

	if strings.Contains(extra, canary) {
		t.Errorf("a plugin's field put the secret in the public index: %q", extra)
	}
	if !strings.Contains(extra, "excerpt") {
		t.Errorf("the extra column = %q, want the field to still be there with the secret removed", extra)
	}
}

// TestAPluginThatCannotRunContributesNothingRatherThanHalfATruth is the failure
// direction, and "nothing" is a deliberate answer: a *partial* set of fields would
// put some of a plugin's values in the index and not others, and the settle check
// would then rewrite the row for ever because the row on disk and the row the index
// wants would disagree about whether the plugin ran.
func TestAPluginThatCannotRunContributesNothingRatherThanHalfATruth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		indexer Indexer
	}{
		{
			name:    "it returns an error",
			indexer: &counting{broken: map[string]bool{"locations/rivergate": true}},
		},
		{
			name:    "it panics",
			indexer: panicking{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			extra := extraFor(t, test.indexer)
			if extra != "" {
				t.Errorf("the extra column = %q, want empty: a plugin that could not run contributes nothing", extra)
			}
		})
	}
}

// TestNoSearchFieldsIndexesExactlyWhatABuildWithoutPluginsIndexes: the zero value
// pays nothing, which is the property that let every existing row stay settled
// through this migration.
func TestNoSearchFieldsIndexesExactlyWhatABuildWithoutPluginsIndexes(t *testing.T) {
	t.Parallel()

	page := pageFixture("locations/rivergate")

	without := extraColumn(t.Context(), page, quietLog(), nil)
	with := extraColumn(t.Context(), page, quietLog(), []SearchField{
		{Plugin: "wordcount", Indexer: &counting{pages: map[string]int{}}},
	})

	if without != with {
		t.Errorf("a plugin with nothing to say changed the row: %q against %q", without, with)
	}
	if without != "" {
		t.Errorf("the extra column = %q with no plugins at all, want empty", without)
	}
}

// fieldFunc adapts a function to [index.Indexer], because a test indexer has no
// configuration to carry past the closure and a struct with one field is a struct.
type fieldFunc func(context.Context, domain.Page) (map[string]string, error)

func (f fieldFunc) Fields(ctx context.Context, page domain.Page) (map[string]string, error) {
	return f(ctx, page)
}
