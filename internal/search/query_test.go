package search_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
)

// The table below is the language, written out. Every row is a thing a DM or a
// player types, so the cases are chosen for what somebody would actually write
// rather than for what exercises a branch.
func TestParse(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		input string
		want  search.Query
	}{
		"an empty box asks for nothing": {
			input: "",
		},
		"whitespace alone asks for nothing": {
			input: "   \t\n ",
		},
		"one word": {
			input: "toll",
			want:  search.Query{Terms: []string{"toll"}},
		},
		"words are anded, in the order they were typed": {
			input: "toll collector bridge",
			want:  search.Query{Terms: []string{"toll", "collector", "bridge"}},
		},
		"a quoted span is one clause": {
			input: `"toll collector"`,
			want:  search.Query{Phrases: []string{"toll collector"}},
		},
		"a phrase and a word are both clauses": {
			input: `"toll collector" bridge`,
			want: search.Query{
				Terms:   []string{"bridge"},
				Phrases: []string{"toll collector"},
			},
		},
		"a quoted single word is the same clause as a bare one": {
			input: `"toll"`,
			want:  search.Query{Terms: []string{"toll"}},
		},
		"an unclosed quote takes the rest of the input": {
			input: `"toll collector`,
			want:  search.Query{Phrases: []string{"toll collector"}},
		},
		"a quote in the middle of a word splits it": {
			input: `toll"collector"bridge`,
			want:  search.Query{Terms: []string{"toll", "collector", "bridge"}},
		},
		"an escaped quote is a quote": {
			input: `"the \"toll\" house"`,
			want:  search.Query{Phrases: []string{`the "toll" house`}},
		},
		"an escaped backslash is a backslash": {
			input: `"toll\\"`,
			want:  search.Query{Terms: []string{`toll\`}},
		},
		"a tag filter": {
			input: "tag:hub",
			want:  search.Query{Tags: []string{"hub"}},
		},
		"tags are ored": {
			input: "tag:hub tag:revealed",
			want:  search.Query{Tags: []string{"hub", "revealed"}},
		},
		"a filter name is not case sensitive": {
			input: "Tag:hub TYPE:npc IS:players",
			want: search.Query{
				Tags:       []string{"hub"},
				Type:       domain.PageTypeNPC,
				Visibility: domain.VisibilityPlayers,
			},
		},
		"a quoted tag value": {
			input: `tag:"two words"`,
			want:  search.Query{Tags: []string{"two words"}},
		},
		"a type filter takes any name, because a plugin's type is as good as core's": {
			input: "type:homebrew-thing",
			want:  search.Query{Type: domain.PageType("homebrew-thing")},
		},
		"the last type typed wins": {
			input: "type:npc type:location",
			want:  search.Query{Type: domain.PageTypeLocation},
		},
		"an is filter": {
			input: "is:dm-only",
			want:  search.Query{Visibility: domain.VisibilityDMOnly},
		},
		"everything at once": {
			input: `tag:hub "toll collector" is:players npc`,
			want: search.Query{
				Tags:       []string{"hub"},
				Terms:      []string{"npc"},
				Phrases:    []string{"toll collector"},
				Visibility: domain.VisibilityPlayers,
			},
		},

		// A word FTS5 would read as syntax is a word, because quoting happens
		// in the store and it happens to everything.
		"a word that is an FTS5 operator is a word": {
			input: "AND OR NOT",
			want:  search.Query{Terms: []string{"AND", "OR", "NOT"}},
		},
		"punctuation on its own is dropped, not sent to the index": {
			input: `- * ( ) " " : ""`,
		},
		"a word with punctuation in it is kept whole": {
			input: "ilithya-marrow",
			want:  search.Query{Terms: []string{"ilithya-marrow"}},
		},
		"a URL is a word, not a filter on the second colon": {
			input: "http://example.com",
			want:  search.Query{Terms: []string{"http://example.com"}},
		},
		"an unknown field prefix is a word": {
			input: "colour:blue",
			want:  search.Query{Terms: []string{"colour:blue"}},
		},
		"a second colon makes the whole thing a word": {
			input: "is:is:dm-only",
			want:  search.Query{Terms: []string{"is:is:dm-only"}},
		},
		"a filter with no value is dropped, not searched for": {
			input: "tag: type:npc",
			want:  search.Query{Type: domain.PageTypeNPC},
		},
		"a word is accepted as a filter value": {
			input: "tag: hub type: npc",
			want: search.Query{
				Tags: []string{"hub"},
				Type: domain.PageTypeNPC,
			},
		},
		"an accented word is one token": {
			input: "rivergåte",
			want:  search.Query{Terms: []string{"rivergåte"}},
		},
		"a word in another script is one token": {
			input: "Ölbach",
			want:  search.Query{Terms: []string{"Ölbach"}},
		},

		// A byte that is not a character, and a NUL, are not words. Both cases
		// were found by FuzzParse: a byte that is not valid UTF-8 makes every
		// response that echoes the query invalid JSON, and a NUL inside a term
		// is a string the tokenizer reads to the end of.
		"a byte that is not a character is dropped": {
			input: "t\xffoll",
			want:  search.Query{Terms: []string{"toll"}},
		},
		"a NUL is dropped rather than ending the term": {
			input: "toll\x00nope",
			want:  search.Query{Terms: []string{"tollnope"}},
		},
		"a filter value that starts with a colon is a value": {
			input: "tAg: :00",
			want:  search.Query{Tags: []string{":00"}},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := search.Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.input, err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%q) =\n %+v\nwant\n %+v", tt.input, got, tt.want)
			}
			if want, got := len(tt.want.Terms) == 0 && len(tt.want.Phrases) == 0 &&
				len(tt.want.Tags) == 0 && tt.want.Type == "" && tt.want.Visibility == "",
				got.Empty(); want != got {
				t.Errorf("Empty() = %t, want %t", got, want)
			}
		})
	}
}

// A query that asks for an audience nobody can name is refused, and the error
// says which levels would have worked. This is the one input Parse rejects,
// and it is the one where a wrong answer is a search that returns nothing while
// looking like the index has nothing to say.
func TestParseRefusesAnUnknownVisibility(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"a misspelt level":     "is:plyers",
		"no level at all":      "is:",
		"an empty level":       `is:""`,
		"an empty level alone": "is:  ",
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := search.Parse(input)
			if !errors.Is(err, search.ErrQuery) {
				t.Fatalf("Parse(%q) returned %v, want an error matching ErrQuery", input, err)
			}
			// The message has to name the values that would have worked: an
			// error a DM cannot act on is the same as no error.
			if !strings.Contains(err.Error(), "dm-and-owner") {
				t.Errorf("Parse(%q) = %v, want the message to name the levels", input, err)
			}
		})
	}
}

// The bounds are there because FTS5 has limits of its own, and a limit reached
// there is a driver error rather than a smaller result. The bound keeps the
// earliest clauses, because a query typed left to right is a narrowing one and
// the first words are the ones a person meant first.
func TestParseBoundsTheClauses(t *testing.T) {
	t.Parallel()

	var words []string
	for i := range search.MaxTerms * 2 {
		words = append(words, "w"+strings.Repeat("x", i%3)+string(rune('a'+i%26)))
	}

	got, err := search.Parse(strings.Join(words, " "))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Clauses() != search.MaxTerms {
		t.Fatalf("a query of %d words parsed to %d clauses, want %d",
			len(words), got.Clauses(), search.MaxTerms)
	}
	for i, term := range got.Terms {
		if want := words[i]; term != want {
			t.Errorf("clause %d is %q, want %q: the first words typed are the ones kept", i, term, want)
		}
	}

	tags := make([]string, 0, search.MaxTags*2)
	for i := range search.MaxTags * 2 {
		tags = append(tags, "tag:t"+strings.Repeat("y", i%3)+string(rune('a'+i%26)))
	}

	withTags, err := search.Parse(strings.Join(tags, " "))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(withTags.Tags) != search.MaxTags {
		t.Errorf("a query of %d tags parsed to %d, want %d",
			len(tags), len(withTags.Tags), search.MaxTags)
	}
}

// String has to describe the query it came from, because it is what a test
// prints and what the search box echoes. A renderer that lost a phrase would be
// a helper quietly disagreeing with the parser.
func TestQueryStringRoundTrips(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"words and a phrase":      `toll "toll collector"`,
		"every filter":            `tag:hub type:npc is:dm-only`,
		"several tags":            `tag:hub tag:"two words"`,
		"a value with a quote in": `"the \"toll\" house"`,
		"an empty one":            ``,
		"a type with a space":     `type:"homebrew thing"`,
		"a tag that looks like a word with a colon": `tag::00`,
		"an accented value":                         `tag:rivergåte`,
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			once, err := search.Parse(input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", input, err)
			}

			twice, err := search.Parse(once.String())
			if err != nil {
				t.Fatalf("Parse(%q): %v", once.String(), err)
			}

			if !reflect.DeepEqual(once, twice) {
				t.Errorf("%q parsed to %+v, rendered as %q and parsed to %+v",
					input, once, once.String(), twice)
			}
		})
	}
}

// A duplicate is not doubled into two clauses of its own, and a word typed twice
// stays one word: the index collapses repeats, and the parser's job is to hand
// it the clauses in the order they were typed and nothing else.
func TestParseKeepsClausesInTypedOrder(t *testing.T) {
	t.Parallel()

	got, err := search.Parse("toll bridge toll")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !slices.Equal(got.Terms, []string{"toll", "bridge", "toll"}) {
		t.Errorf("Terms = %v, want the words in the order they were typed", got.Terms)
	}
}
