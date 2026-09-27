package store

import (
	"strings"
	"testing"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
)

// FTS5 has a query language of its own — operators, prefixes, column filters,
// parentheses, NEAR — and a search box is a second language layered on top of it.
// A hostile query is therefore a security problem before it is a relevance one,
// so the corpus below is the shapes that would break a builder which pasted a
// caller's text into the statement, and the assertions are the two that matter:
// the statement built is one of four known strings, and the expression handed to
// MATCH is a conjunction of literals with nothing left over.

// TestSearchStatementsAreTheFourKnownOnes pins every statement this package can
// run against a search.
//
// A test that reads a constant back is usually a test of nothing, and this is
// not that: the statements are assembled, and the failure mode being guarded
// against is a change to how — a missing scope, a table spliced into the wrong
// query, an argument that stops being bound. Printing them means a reviewer can
// read all the SQL in one place, and pinning them means the next change to it is
// a diff in this test rather than a diff in behaviour.
func TestSearchStatementsAreTheFourKnownOnes(t *testing.T) {
	t.Parallel()

	dm := domain.Principal{ID: "p-1", Role: domain.RoleDM}
	public := publicIndexQuery.withScope(readable("campaign-1", dm))
	secret := secretIndexQuery.withScope(readableWithSecrets("campaign-1", dm))

	tests := map[string]struct {
		query indexQuery
		match bool
		want  string
	}{
		"the public index, with a match": {
			query: public, match: true,
			want: `SELECT p.id, p.path, p.title, p.type, snippet(pages_fts, -1, '', '', '…', 12)
		FROM pages_fts
		JOIN pages p ON p.id = pages_fts.page_id
		WHERE pages_fts MATCH ? AND (p.is_deleted = 0
		AND p.campaign_id = ?
		AND (p.visibility = 'players'
				OR ? = 'dm'
				OR ( p.visibility = 'dm-and-owner' AND EXISTS (SELECT 1 FROM principal_characters pc WHERE pc.principal_id = ? AND pc.character_page_id = p.id) )))
		ORDER BY bm25(pages_fts, 10.0, 3.0, 4.0, 1.0, 2.0), p.path
		LIMIT ?`,
		},
		"the public index, filters only": {
			query: public, match: false,
			want: `SELECT p.id, p.path, p.title, p.type, ''
		FROM pages_fts
		JOIN pages p ON p.id = pages_fts.page_id
		WHERE 1 = 1 AND (p.is_deleted = 0
		AND p.campaign_id = ?
		AND (p.visibility = 'players'
				OR ? = 'dm'
				OR ( p.visibility = 'dm-and-owner' AND EXISTS (SELECT 1 FROM principal_characters pc WHERE pc.principal_id = ? AND pc.character_page_id = p.id) )))
		ORDER BY p.path
		LIMIT ?`,
		},
		"the private index, with a match": {
			query: secret, match: true,
			want: `SELECT p.id, p.path, p.title, p.type, snippet(pages_secrets_fts, -1, '', '', '…', 12)
		FROM pages_secrets_fts
		JOIN pages p ON p.id = pages_secrets_fts.page_id
		WHERE pages_secrets_fts MATCH ? AND (p.is_deleted = 0
		AND p.campaign_id = ?
		AND (p.visibility = 'players'
				OR ? = 'dm'
				OR ( p.visibility = 'dm-and-owner' AND EXISTS (SELECT 1 FROM principal_characters pc WHERE pc.principal_id = ? AND pc.character_page_id = p.id) ))
		AND ( ? = 'dm' OR EXISTS (SELECT 1 FROM principal_characters pc WHERE pc.principal_id = ? AND pc.character_page_id = p.id) ))
		ORDER BY bm25(pages_secrets_fts), p.path
		LIMIT ?`,
		},
		"the private index, filters only": {
			query: secret, match: false,
			want: `SELECT p.id, p.path, p.title, p.type, ''
		FROM pages_secrets_fts
		JOIN pages p ON p.id = pages_secrets_fts.page_id
		WHERE 1 = 1 AND (p.is_deleted = 0
		AND p.campaign_id = ?
		AND (p.visibility = 'players'
				OR ? = 'dm'
				OR ( p.visibility = 'dm-and-owner' AND EXISTS (SELECT 1 FROM principal_characters pc WHERE pc.principal_id = ? AND pc.character_page_id = p.id) ))
		AND ( ? = 'dm' OR EXISTS (SELECT 1 FROM principal_characters pc WHERE pc.principal_id = ? AND pc.character_page_id = p.id) ))
		ORDER BY p.path
		LIMIT ?`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tt.query.statement(tt.match); got != tt.want {
				t.Errorf("the statement is\n%s\n\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestSearchStatementsCarryTheirReadScope is invariant 3, as a property of the
// statements rather than of a result set: every one of them filters through a
// scope, and the private one through the stricter of the two.
func TestSearchStatementsCarryTheirReadScope(t *testing.T) {
	t.Parallel()

	dm := domain.Principal{ID: "p-1", Role: domain.RoleDM}

	public := publicIndexQuery.withScope(readable("campaign-1", dm)).statement(true)
	secret := secretIndexQuery.withScope(readableWithSecrets("campaign-1", dm)).statement(true)

	for name, statement := range map[string]string{"public": public, "private": secret} {
		for _, term := range []string{"p.is_deleted = 0", "p.campaign_id = ?", "p.visibility = 'players'"} {
			if !strings.Contains(statement, term) {
				t.Errorf("the %s statement does not contain %q:\n%s", name, term, statement)
			}
		}
	}

	if strings.Contains(public, "OR 1 = 0 ) )") {
		t.Error("the public statement carries the secret scope's ownership test")
	}
	if !strings.Contains(secret, "AND ( ? = 'dm' OR "+aclOwnership+" )") {
		t.Errorf("the private statement does not carry the secret scope's own test:\n%s", secret)
	}
}

// The three filters are a SQL tail rather than part of the match expression, and
// the tail differs between the two indexes only in the subquery it carries. This
// pins all six statements a filtered search can run, because "the filter is
// applied" is not the same claim as "the filter is applied in both queries with
// the same subquery", and only one of those is what keeps a `tag:` filter from
// asking the private index for a column it does not have.
func TestFilteredSearchStatements(t *testing.T) {
	t.Parallel()

	dm := domain.Principal{ID: "p-1", Role: domain.RoleDM}
	parsed, err := search.Parse(`tag:hub tag:revealed type:npc is:players toll "a phrase"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	public := publicIndexQuery.withScope(readable("campaign-1", dm)).withQuery(parsed)
	secret := secretIndexQuery.withScope(readableWithSecrets("campaign-1", dm)).withQuery(parsed)

	tests := map[string]struct {
		query     indexQuery
		withMatch bool
		want      string
	}{
		"the public index, with a match and every filter": {
			query: public, withMatch: true,
			want: ` AND p.id IN (SELECT page_id FROM pages_fts WHERE pages_fts MATCH ?) AND p.type = ? AND p.visibility = ?`,
		},
		"the public index, filters only": {
			query: public, withMatch: false,
			want: ` AND p.id IN (SELECT page_id FROM pages_fts WHERE pages_fts MATCH ?) AND p.type = ? AND p.visibility = ?`,
		},
		"the private index, with a match and every filter": {
			query: secret, withMatch: true,
			want: ` AND p.id IN (SELECT page_id FROM pages_fts WHERE pages_fts MATCH ?) AND p.type = ? AND p.visibility = ?`,
		},
		"the private index, filters only": {
			query: secret, withMatch: false,
			want: ` AND p.id IN (SELECT page_id FROM pages_fts WHERE pages_fts MATCH ?) AND p.type = ? AND p.visibility = ?`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := filters(tt.query); got != tt.want {
				t.Errorf("the filter tail is\n%q\n\nwant\n%q", got, tt.want)
			}
		})
	}

	// The tail is in both statements, which is the part that matters: a filter
	// applied to one query and not the other is a search that answers a different
	// question depending on which index a match came from.
	for name, query := range map[string]indexQuery{"public": public, "private": secret} {
		for _, withMatch := range []bool{true, false} {
			statement := query.statement(withMatch)
			if !strings.Contains(statement, "p.id IN (SELECT page_id FROM pages_fts") {
				t.Errorf("the %s statement does not carry the tag subquery:\n%s", name, statement)
			}
			if !strings.Contains(statement, "AND p.type = ?") {
				t.Errorf("the %s statement does not carry the type filter:\n%s", name, statement)
			}
			if !strings.Contains(statement, "AND p.visibility = ?") {
				t.Errorf("the %s statement does not carry the visibility filter:\n%s", name, statement)
			}
		}
	}

	// And one argument per placeholder, in the order the clause puts them.
	placeholders := strings.Count(filters(public)+public.match(true)+public.sc.where, "?")
	if got := len(public.filterArgs()) + 1 /* the match */ + len(public.sc.args); got != placeholders {
		t.Errorf("the statement has %d placeholders and %d arguments", placeholders, got)
	}
	// The match, the campaign, the role, the principal, the tags, the type, the
	// audience and the limit: eight, in that order. The principal is the one the
	// ownership clause added, and it is the one a missing argument would turn into
	// a driver error on a player's request rather than a failed test here.
	if got, want := strings.Count(public.statement(true), "?"), 8; got != want {
		t.Errorf("a fully filtered statement has %d placeholders, want %d", got, want)
	}
}

// FuzzSearchMatchExpression is the target for the FTS5 grammar. It is here rather
// than in internal/search because the grammar is this package's: Parse decides
// what a clause *is*, and ftsMatch decides how it is *spelled*, and a spelling
// bug is only visible against a real tokenizer.
//
// The assertion is the strong one. Every clause in the expression must be an
// FTS5 literal or a column filter over a literal, so removing all the literals
// and all the `tags :` filters must leave nothing but `AND`, `OR` and brackets —
// no word, no operator, no prefix, no bare value. A clause that got through
// unquoted would be a word in what is left, and that is the whole failure.
func FuzzSearchMatchExpression(f *testing.F) {
	seeds := []string{
		"",
		"toll",
		"toll collector",
		`"toll collector"`,
		"tag:hub",
		"tag:hub tag:npc",
		`tag:"two words"`,
		"type:npc",
		"is:dm-only",
		"AND",
		"NOT OR",
		"- * ( ) :",
		`"unclosed`,
		`"a\"b"`,
		`a"b"c`,
		"http://example.com",
		"*",
		"^toll",
		"tag:hub AND NOT npc",
		`tags : "hub"`,
		"rivergåte",
		"toll\x00nope",
		"toll\xffnope",
		strings.Repeat("word ", search.MaxTerms*2),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		parsed, err := search.Parse(input)
		if err != nil {
			return
		}

		expression := ftsMatch(parsed)
		if expression == "" {
			// An empty expression is right for a query made only of filters --
			// `type:npc`, `is:dm-only` -- because those are SQL and not text. It
			// is wrong for a query with a text clause in it, which would then
			// match nothing while looking like a query that found nothing.
			if parsed.Clauses() > 0 {
				t.Errorf("Parse(%q) gave %+v, which has %d text clauses, and an empty match expression",
					input, parsed, parsed.Clauses())
			}
			return
		}

		assertOnlyOperatorsSurvive(t, input, expression)
	})
}

// assertOnlyOperatorsSurvive is the assertion, in one place, for the table above
// and the fuzzer below.
//
// What is left after the literals are removed and the operators this builder is
// allowed to emit are taken away must be nothing. A word is a clause that got
// through unquoted; a `:` is a column filter the caller wrote by hand; a `*`, a
// `^` or a `-` is an operator. Each of those is FTS5 syntax the application does
// not offer and a caller should not be able to reach.
func assertOnlyOperatorsSurvive(t *testing.T, input, expression string) {
	t.Helper()

	// The operators this builder emits: AND between clauses, OR inside the tag
	// group, and the brackets around that group. Everything else that is not a
	// literal is a bug.
	residue := stripLiterals(expression)
	for _, operator := range []string{"AND", "OR", "(", ")"} {
		residue = strings.ReplaceAll(residue, operator, "")
	}

	if hasWord(residue) {
		t.Errorf("Parse(%q) produced the match expression %q, which has an unquoted word in it: %q",
			input, expression, residue)
	}
	for _, syntax := range []string{":", "^", "*", "-", "{", "}"} {
		if strings.Contains(residue, syntax) {
			t.Errorf("Parse(%q) produced the match expression %q, which still has FTS5 syntax %q outside a literal: %q",
				input, expression, syntax, residue)
		}
	}
}

// stripLiterals removes every `"..."` from an expression, along with the
// `tags :` that introduces the column filters.
func stripLiterals(expression string) string {
	var out strings.Builder
	quoted := false
	for i := 0; i < len(expression); i++ {
		switch c := expression[i]; {
		case c == '"':
			quoted = !quoted
		case quoted:
			// Inside a literal, which is where a caller's characters live and
			// where nothing has to be escaped because nothing escapes it.
		default:
			out.WriteByte(c)
		}
	}
	return strings.ReplaceAll(out.String(), "tags : ", "")
}

// hasWord reports whether a string holds anything that is not punctuation, which
// after stripLiterals means a clause got through unquoted.
func hasWord(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return true
		}
	}
	return false
}

// The corpus itself, as a table rather than only as a fuzzer corpus: a list of
// hostile inputs with the shape of the danger named, so a reader can see what was
// thought about without running anything.
func TestSearchMatchExpressionQuotesEveryClause(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"an operator is a word":                   `AND OR NOT NEAR`,
		"a prefix star":                           `* toll ^river`,
		"brackets and colons":                     `- ( ) : { }`,
		"an unbalanced quote":                     `"unclosed`,
		"an escaped quote inside a quote":         `"a\"b"`,
		"a URL":                                   `http://example.com`,
		"a column filter by hand":                 `tags : "hub"`,
		"a negating column filter by hand":        `NOT tags : "hub"`,
		"a near-two query":                        `toll NEAR/2 collector`,
		"a wildcard after a quoted phrase":        `"toll collector"*`,
		"a caret anchor":                          `^toll`,
		"a column filter on the unindexed column": `page_id : "x"`,
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			parsed, err := search.Parse(input)
			if err != nil {
				return // refused at the language level, which is also fine
			}

			expression := ftsMatch(parsed)
			if expression == "" {
				return
			}

			assertOnlyOperatorsSurvive(t, input, expression)
		})
	}
}
