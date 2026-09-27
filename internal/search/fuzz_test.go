package search_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
)

// FuzzParse is the target the query language is allowed to be wrong in only one
// way: by refusing. It parses arbitrary bytes, so the properties it asserts are
// the ones a hostile or broken search box would break first.
//
//   - It never panics. A panic in a request handler is a 500 for whoever typed
//     it, and the input is a search box.
//   - What comes out is a query whose every clause is a string the fuzzer did
//     not get to choose in any other way: the parser does not carry a slice of
//     the input across in a form the store would not quote.
//   - It is stable. Parsing what String produced gives the query back, so the
//     renderer the tests print with cannot disagree with the parser.
//
// The last one is the property with teeth. Round-tripping is what proves the
// quoting is total: a value that String rendered unquoted and Parse then read as
// two clauses would show up here as a query that changes under itself.
func FuzzParse(f *testing.F) {
	seeds := []string{
		"",
		"toll",
		"toll collector",
		`"toll collector"`,
		`"toll`,
		`tag:hub`,
		`tag:"two words"`,
		"type:npc",
		"is:dm-only",
		"is:plyers",
		"is:",
		`"the \"toll\" house"`,
		"AND OR NOT",
		`- * ( ) :`,
		"http://example.com",
		"rivergåte",
		"Ölbach",
		"\x00\x01",
		strings400(),
		strings.Join([]string{"tag:a", "tag:b", `"a b"`, "c", "is:players"}, "  "),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		parsed, err := search.Parse(input)
		if err != nil {
			// Refusing is allowed; what is not allowed is refusing for a
			// reason other than the language, and ErrQuery is the only reason
			// Parse has.
			if !errors.Is(err, search.ErrQuery) {
				t.Fatalf("Parse(%q) returned an error that is not ErrQuery: %v", input, err)
			}
			// A refused query is empty. Handing back a half-built query
			// alongside an error would let a caller that logs the error and
			// carries on use the parts that survived.
			if !reflect.DeepEqual(parsed, search.Query{}) {
				t.Errorf("Parse(%q) returned %+v with the error %v", input, parsed, err)
			}
			return
		}

		// Every clause is bounded, and the bounds are the ones the language
		// publishes. A fuzzer that found a way past one would be a way to hand
		// FTS5 an expression it refuses.
		if got := parsed.Clauses(); got > search.MaxTerms {
			t.Errorf("Parse(%q) produced %d clauses, more than the %d the language allows",
				input, got, search.MaxTerms)
		}
		if got := len(parsed.Tags); got > search.MaxTags {
			t.Errorf("Parse(%q) produced %d tags, more than the %d the language allows",
				input, got, search.MaxTags)
		}

		// Invalid UTF-8 is what a browser can hand a form with, and a lone
		// surrogate reaches a Go string as replacement characters. Neither may
		// panic, and neither may smuggle a byte into a clause that the store
		// would then quote as if it were a character.
		if !utf8.ValidString(input) {
			for _, clause := range allClauses(parsed) {
				if !utf8.ValidString(clause) {
					t.Errorf("Parse(%q) produced the invalid clause %q", input, clause)
				}
			}
		}

		round, err := search.Parse(parsed.String())
		if err != nil {
			t.Fatalf("Parse(%q) rendered as %q, which did not parse: %v",
				input, parsed.String(), err)
		}
		if !reflect.DeepEqual(parsed, round) {
			t.Errorf("Parse(%q) = %+v, rendered as %q and parsed to %+v",
				input, parsed, parsed.String(), round)
		}
	})
}

// allClauses is every string the query carries, so a fuzzer assertion can look
// at all of them without repeating itself three times.
func allClauses(q search.Query) []string {
	clauses := make([]string, 0, len(q.Terms)+len(q.Phrases)+len(q.Tags)+2)
	clauses = append(clauses, q.Terms...)
	clauses = append(clauses, q.Phrases...)
	clauses = append(clauses, q.Tags...)
	if q.Type != "" {
		clauses = append(clauses, q.Type.String())
	}
	if q.Visibility != "" {
		clauses = append(clauses, q.Visibility.String())
	}
	return clauses
}

// strings400 is a clause long enough to hit a limit somewhere. It exists in the
// seed corpus because a bound that nothing in the corpus reaches is a bound no
// test has shown to work.
func strings400() string {
	out := make([]byte, 0, 512)
	for range 400 {
		out = append(out, 'a'+byte(len(out)%26))
		if len(out)%7 == 0 {
			out = append(out, ' ')
		}
	}
	return string(out)
}
