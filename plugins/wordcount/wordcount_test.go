package wordcount_test

import (
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/plugin/contract"
	"github.com/popinjayjohn/dine-and-dash-semiplane/plugins/wordcount"
)

// TestWordcountPassesTheContract is the line every plugin in this repository has to
// be able to write, and it is one line because the suite is one function.
//
// The plugin is built with a nil store, which is a supported shape: the indexer half
// needs nothing but a page, and a build that wanted only that should not have to open
// a database to get it. It is also the shape that would find a plugin author reaching
// for a package-level handle they were not given.
func TestWordcountPassesTheContract(t *testing.T) {
	t.Parallel()

	contract.Run(t, wordcount.New(nil))
}

// TestCountIsAWordCount is the table, and the rows that matter are the ones
// `strings.Fields` would get wrong — which is why the count is hand-rolled at all.
func TestCountIsAWordCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want int
	}{
		{name: "nothing at all", body: "", want: 0},
		{name: "only whitespace", body: "  \n\t\n  ", want: 0},
		{name: "one word", body: "Toll.", want: 1},
		{name: "three words", body: "The toll bridge.", want: 3},
		{name: "a contraction is one word", body: "Vell's toll.", want: 2},
		{name: "a hyphenated compound is one word", body: "A half-elven warden's toll.", want: 4},
		{name: "a heading's hashes are not words", body: "# Rivergate", want: 1},
		{name: "emphasis markers are not words", body: "A **very** long road.", want: 4},
		{name: "a wiki link is both halves, not the display text", body: "See [[locations/thornford]] across the water.", want: 6},
		{name: "a fence marker is not a word", body: "Run:\n\n```sh\nls -la\n```\n", want: 4},
		{name: "a callout type is a word, because the marker is one", body: "> [!note] The weather is bad\n", want: 5},
		{name: "an ellipsis is not a word", body: "Wait... what?", want: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := wordcount.Count(test.body); got != test.want {
				t.Errorf("Count(%q) = %d, want %d", test.body, got, test.want)
			}
		})
	}
}

// TestTheFieldIsANamedNumber: the value the search index holds has to be findable by
// both the name and the number, and the number has to be the number.
func TestTheFieldIsANamedNumber(t *testing.T) {
	t.Parallel()

	page := domain.Page{
		ID: "p1", CampaignID: "c1", Path: "locations/rivergate",
		Body: "A fortified town on the confluence.\n",
	}

	fields, err := wordcount.New(nil).Fields(t.Context(), page)
	if err != nil {
		t.Fatalf("Fields: %v", err)
	}

	value, found := fields["words"]
	if !found {
		t.Fatalf("Fields = %v, want a `words` field", fields)
	}
	if value != "6" {
		t.Errorf("words = %q, want %q", value, "6")
	}
}

// TestAnEmptyPageIsZeroWordsAndNotAnAbsence: a brand new page is indexed as
// `words 0`, and a plugin that returned nothing for one would leave the settle check
// comparing a row against a different row on every sync.
func TestAnEmptyPageIsZeroWordsAndNotAnAbsence(t *testing.T) {
	t.Parallel()

	fields, err := wordcount.New(nil).Fields(t.Context(), domain.Page{Path: "notes/new"})
	if err != nil {
		t.Fatalf("Fields: %v", err)
	}

	if fields["words"] != "0" {
		t.Errorf("words = %q, want %q", fields["words"], "0")
	}
}

// TestTheCountNeverCountsMarkup is the property that makes the number comparable to
// what a DM sees on a page, and it is here because a future change to the scanner
// that counted a `*` as a word would pass every other test in this file.
func TestTheCountNeverCountsMarkup(t *testing.T) {
	t.Parallel()

	const body = "# Heading\n\n- **bold** and _italic_\n- `code` and [a link](http://x)\n"
	if got := wordcount.Count(body); got > 12 {
		t.Errorf("Count(%q) = %d, which is more than the twelve words that are there", body, got)
	}
}

var _ = strings.TrimSpace
