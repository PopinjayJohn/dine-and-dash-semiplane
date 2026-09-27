package search

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// Query is one parsed search, as the DM or a player typed it.
//
// It is a value and nothing more: no database handle, no clock, no ambient
// state. That is what makes the relevance tests runnable without a database and
// what makes this file fuzzerable at all, and it is the reason the FTS5 query
// string is *not* built here. The grammar of FTS5 belongs to the store, which is
// the only package that knows SQLite exists; what lives here is the language the
// application offers on top of it, and the rules for turning typed text into it.
type Query struct {
	// Terms are the bare words, ANDed together. A DM typing `toll collector`
	// means a page about both, not a page about either.
	Terms []string

	// Phrases are the quoted spans, ANDed with the terms and with each other.
	// `"toll collector"` is one term in the index's eyes, which is the only way
	// to search for a name with a space in it.
	Phrases []string

	// Tags are the `tag:` filters, ORed with each other and ANDed with
	// everything else. A page with two of the listed tags matches.
	Tags []string

	// Type is the `type:` filter. It is a single value because a page has one
	// type, and two of them would match nothing rather than something narrower.
	Type domain.PageType

	// Visibility is the `is:` filter. It is a filter and not a bypass: it is
	// applied inside the same read predicate as everything else, so a player who
	// searches `is:dm-only` gets no results rather than a page (ADR 0009).
	Visibility domain.Visibility

	// PrefixLastTerm makes the last text term a prefix match rather than a whole
	// word, and it is a *field on the query* rather than a flag on the store's
	// method for one reason: the match expression's invariant is that nothing
	// outside a quoted literal is FTS5 syntax, and a prefix `*` is syntax. So the
	// widening is something a caller asks for by name, lands in the expression from
	// exactly one place, and a search that did not ask for one cannot grow one.
	//
	// M10's search box asks for it and nothing else does. A box that matches whole
	// words only is not a box: somebody typing `fort` gets nothing, `forti` gets
	// nothing, and `fortified` gets the page, so every result appears after the
	// last character of the word that would find it.
	PrefixLastTerm bool
}

// ErrQuery is what a search that cannot be answered as typed returns, so a
// handler can turn it into a 400 and a message rather than matching on the text
// of an error about a visibility level.
var ErrQuery = errors.New("search: the query could not be read")

// The bounds on a parsed query. Both are about the same thing: FTS5 has its own
// limits and hitting one produces a driver error rather than a smaller result.
// A search box a DM can paste a paragraph into has to answer with something, and
// "the first few things you typed" is the answer a person wants.
const (
	// MaxTerms is how many text clauses one query may carry. FTS5 refuses an
	// expression deeper than its own limit, and a query of a few hundred ANDed
	// words is not a search anybody means.
	MaxTerms = 32

	// MaxTags is how many `tag:` filters one query may carry, for the same
	// reason: each one is another clause in the same expression.
	MaxTags = 16
)

// Parse reads the query language.
//
// The language is small on purpose — words, `"phrases"`, and three filters — and
// anything it does not recognise becomes a word rather than an error, because a
// search box that refuses input is worse than one that searches for a strange
// word. The one exception is `is:`, and it is the exception because the value
// names something the caller is not entitled to: an unknown level has to be an
// error, since quietly searching for nothing is the kind of answer that looks
// like the index has nothing to say.
func Parse(input string) (Query, error) {
	var q Query
	scanner := &queryScanner{input: input}

	// pending is a `tag:` or `type:` whose value is a quoted span starting after
	// the colon, as in `tag:"two words"`. `is:` does not get that treatment:
	// see the end of this function.
	var pending filterKind

	for {
		next, ok := scanner.next()
		if !ok {
			break
		}

		switch next.kind {
		case itemPhrase:
			if pending == filterNone {
				q.addTerm(next.text)
				continue
			}
			if err := q.addFilter(pending, next.text); err != nil {
				return Query{}, err
			}
			pending = filterNone

		case itemFilter:
			name, value := splitFilter(next.text)
			if value != "" {
				if err := q.addFilter(name, value); err != nil {
					return Query{}, err
				}
				continue
			}
			// A prefix with nothing after it. The value is meant to be coming,
			// quoted; if it never arrives the filter is dropped, because
			// `tag:` read as the word "tag:" would be a worse answer than a
			// filter that quietly does nothing.
			pending = name

		case itemWord:
			// A word where a filter's value was expected. `tag: hub` and
			// `is: dm-only` are the same filters as `tag:hub` and `is:dm-only`,
			// and a search box does not owe anybody a lesson about colons.
			if pending != filterNone {
				if err := q.addFilter(pending, next.text); err != nil {
					return Query{}, err
				}
				pending = filterNone
				continue
			}
			q.addTerm(next.text)
		}
	}

	// `is:` with no level, and `is:plyers` with a misspelt one, are the same
	// mistake: the caller asked about an audience and named none they can have.
	// Saying so is better than a search that finds nothing.
	if pending == filterIs {
		return Query{}, fmt.Errorf("%w: is: needs a level, one of %s",
			ErrQuery, strings.Join(visibilityNames(), ", "))
	}

	return q, nil
}

// Empty reports whether the query asks for nothing at all: no text and no
// filter.
//
// It is the difference between "the search box is empty" and "show me every
// page tagged `hub`", and the two must not be the same query. An empty search
// box that returned the whole vault would put every title in the campaign in one
// response, which is both a scrape and a page list a principal is not entitled
// to.
func (q Query) Empty() bool {
	return len(q.Terms) == 0 && len(q.Phrases) == 0 && len(q.Tags) == 0 &&
		q.Type == "" && q.Visibility == ""
}

// Clauses reports how many ANDed text clauses the query carries, which is what
// MaxTerms counts. Tags are not clauses: a tag is a filter on a column rather
// than a term, and the index handles a hundred of them differently from a
// hundred terms.
func (q Query) Clauses() int {
	return len(q.Terms) + len(q.Phrases)
}

// addTerm records one text clause, as a phrase when it holds a space.
//
// A quoted span and a bare word are the same clause to the index and differ
// only in how the user wrote it, and keeping them apart would mean a query that
// says two things where the index hears one.
func (q *Query) addTerm(text string) {
	text = cleanClause(text)
	if !searchable(text) {
		return
	}
	if q.Clauses() >= MaxTerms {
		return
	}

	if strings.ContainsAny(text, " \t") {
		q.Phrases = append(q.Phrases, text)
		return
	}
	q.Terms = append(q.Terms, text)
}

// cleanClause reduces a typed span to something this application is willing to
// index and to put in a response.
//
// Two things are removed, and neither is a word:
//
//   - Bytes that are not valid UTF-8. A browser form can carry them, a Go
//     string will hold them, and a clause that is not valid UTF-8 makes every
//     response that echoes the query invalid JSON. The bytes are dropped rather
//     than replaced so that `a<bad>b` does not silently become a search for
//     `ab`.
//   - Control characters, NUL above all. A NUL inside a term is a string the
//     driver will store and a tokenizer that will read to the end of the string,
//     so the two halves of a query would stop meaning the same thing.
func cleanClause(text string) string {
	cleaned := strings.ToValidUTF8(text, "")

	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, cleaned)
}

// addFilter records one filter.
//
// It returns an error for exactly one case -- an `is:` value that is not one of
// the three levels -- and the reason it is not a silently-ignored value is that
// a player watching a search return nothing cannot tell a typo from a secret.
func (q *Query) addFilter(name filterKind, value string) error {
	value = cleanClause(value)

	// `is:` skips the "is there anything here" test below, because `is:` with
	// nothing after it is a question about an audience that the caller did not
	// answer, and the answer they get should say so rather than quietly search
	// for nothing.
	if name != filterIs && !searchable(value) {
		return nil
	}

	switch name {
	case filterTag:
		if len(q.Tags) < MaxTags {
			q.Tags = append(q.Tags, value)
		}
	case filterType:
		// ADR 0010: a page type is data, so a plugin's type is as valid as
		// core's and there is nothing to validate this against. The last one
		// typed wins, because a page has one type and a second filter would
		// match nothing rather than something narrower.
		q.Type = domain.PageType(value)
	case filterIs:
		// The one filter that is validated, and the only validated one, because
		// a typo here silently produces an empty result set and a typo in a
		// tag does not.
		visibility, err := domain.ParseVisibility(value)
		if err != nil {
			return fmt.Errorf("%w: is: %q is not a level, want one of %s",
				ErrQuery, value, strings.Join(visibilityNames(), ", "))
		}
		q.Visibility = visibility
	case filterNone:
		// Not a filter at all: the word is searched for instead, which is what
		// the caller did by not spelling a filter.
	}

	return nil
}

// visibilityNames is the three levels, in the order the spec's table lists them
// so an error message reads the same way the documentation does.
func visibilityNames() []string {
	return []string{
		domain.VisibilityDMOnly.String(),
		domain.VisibilityDMAndOwner.String(),
		domain.VisibilityPlayers.String(),
	}
}

// searchable reports whether a string holds anything the tokenizer will turn
// into a token.
//
// It is deliberately narrow. A letter or a digit, in any script, is a token;
// punctuation is not, and a clause made only of punctuation is a clause FTS5
// either ignores or rejects. Dropping it here means the store never has to know
// which.
func searchable(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// The three filter names, as `field:` prefixes. They are matched without regard
// to case, because a search box is typed into with the shift key down half the
// time and `Type:` is not a different filter from `type:`.
const (
	filterTag  filterKind = "tag"
	filterType filterKind = "type"
	filterIs   filterKind = "is"
)

type filterKind string

// filterNone is "this was a word, not a filter", and it is a named value rather
// than the zero of filterKind so that the zero cannot be mistaken for one.
const filterNone filterKind = ""

// splitFilter reads a leading `field:` off a token.
//
// A token with more than one colon is a word: `http://example.com` names a
// scheme, and reading the part after the first colon as a filter value would
// turn a link a DM pasted into a search for a tag called "example.com".
func splitFilter(text string) (filterKind, string) {
	colon := strings.IndexByte(text, ':')
	if colon <= 0 {
		return filterNone, ""
	}
	if strings.ContainsRune(text[colon+1:], ':') {
		return filterNone, ""
	}

	switch name := filterKind(strings.ToLower(text[:colon])); name {
	case filterTag, filterType, filterIs:
		return name, text[colon+1:]
	default:
		return filterNone, ""
	}
}

// The three things a scan can yield.
const (
	itemWord = iota
	itemPhrase
	itemFilter
)

type item struct {
	kind int
	text string
}

// queryScanner walks the input once, in the order the language defines.
type queryScanner struct {
	input string
	at    int
}

// next returns the next word, quoted phrase or filter, and whether there was
// one.
//
// A quote is not a delimiter in the middle of a token: `tag:"two words"` is one
// filter whose value is a phrase, and the scanner reaches that by reading `tag:`
// and then handing back the span the quote opened. A quote that is never closed
// takes the rest of the input, which is what someone typing `"toll` meant, and
// cannot be a way to smuggle anything past the quoting the store applies.
func (s *queryScanner) next() (item, bool) {
	for s.at < len(s.input) && isQuerySpace(s.input[s.at]) {
		s.at++
	}
	if s.at >= len(s.input) {
		return item{}, false
	}

	if s.input[s.at] == '"' {
		return item{kind: itemPhrase, text: s.readQuoted()}, true
	}

	start := s.at
	for s.at < len(s.input) && !isQuerySpace(s.input[s.at]) && s.input[s.at] != '"' {
		s.at++
	}

	word := s.input[start:s.at]
	if name, _ := splitFilter(word); name != filterNone {
		return item{kind: itemFilter, text: word}, true
	}
	return item{kind: itemWord, text: word}, true
}

// readQuoted reads a quoted span, honouring `\"`, and reports an unterminated
// one as running to the end of the input.
func (s *queryScanner) readQuoted() string {
	s.at++ // the opening quote

	var span strings.Builder
	for s.at < len(s.input) {
		switch c := s.input[s.at]; c {
		case '\\':
			// A backslash quotes whatever follows it, and a backslash at the
			// end of the input quotes nothing -- which is a trailing backslash
			// the DM typed and not the start of an escape that swallows the
			// rest of the query.
			s.at++
			if s.at < len(s.input) {
				span.WriteByte(s.input[s.at])
				s.at++
			}
		case '"':
			s.at++
			return span.String()
		default:
			span.WriteByte(c)
			s.at++
		}
	}
	return span.String()
}

// isQuerySpace reports whether a byte separates two clauses.
//
// ASCII whitespace only, and that is the rule rather than an oversight: the
// input is UTF-8, and a byte above 0x7f is not whitespace in any of the scripts
// this application indexes. Folding the multi-byte sequences into separators
// would split a word in the middle of a letter.
func isQuerySpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

// String renders the query back into the language.
//
// It exists for the tests and for the search box, and it is round-trip stable:
// parsing what String produced gives the query back. The fuzzer asserts that,
// because a renderer that lost a phrase would be a test helper quietly disagreeing
// with the parser it is meant to describe.
func (q Query) String() string {
	parts := make([]string, 0, len(q.Tags)+4+len(q.Terms)+len(q.Phrases))

	for _, tag := range q.Tags {
		parts = append(parts, string(filterTag)+":"+quoteValue(tag))
	}
	if q.Type != "" {
		parts = append(parts, string(filterType)+":"+quoteValue(q.Type.String()))
	}
	if q.Visibility != "" {
		parts = append(parts, string(filterIs)+":"+q.Visibility.String())
	}
	for _, term := range q.Terms {
		parts = append(parts, quoteValue(term))
	}
	for _, phrase := range q.Phrases {
		parts = append(parts, quoteValue(phrase))
	}

	return strings.Join(parts, " ")
}

// quoteValue renders one value as a clause, quoting it when it would otherwise
// read as more than one.
//
// The rule is "quote anything that is not obviously one token", and the reason
// it is not simply "quote anything with a space in it" is that a filter value is
// the case that bites: `tag::00` reads as the word ":00", so a tag whose name
// starts with a colon has to come back as `tag:":00"` or the query does not
// survive a round trip.
func quoteValue(value string) string {
	if plainValue(value) {
		return value
	}

	var quoted strings.Builder
	quoted.WriteByte('"')
	for i := range len(value) {
		if value[i] == '"' || value[i] == '\\' {
			quoted.WriteByte('\\')
		}
		quoted.WriteByte(value[i])
	}
	quoted.WriteByte('"')
	return quoted.String()
}

// plainValue reports whether a value is one token in the language, so that it
// can be written without quotes and read back as itself.
//
// Letters and digits in any script, and the four marks a name in a path or a
// slug carries. Everything else is quoted, which is the safe direction: an
// unnecessary pair of quotes changes how a value *looks* and never what it
// matches.
func plainValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '-', r == '_', r == '.', r == '/', r == '\'':
		default:
			return false
		}
	}
	return true
}
