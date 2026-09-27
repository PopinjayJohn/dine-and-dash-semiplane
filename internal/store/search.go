package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/search"
)

// The FTS5 half of a search: the match expression, the two statements, and the
// ranking.
//
// # Nothing a caller typed is ever concatenated into SQL
//
// The match expression is built here, from the parsed query, and it is then
// handed to SQLite as a *single bound parameter*. A search box is an
// injection surface with its own grammar — FTS5 has operators, prefixes, column
// filters and parentheses — and a query string is a second grammar on top of
// that one. So every clause is quoted with FTS5's own string quoting, which
// doubles an inner quote and makes every other character literal, and the
// expression as a whole is a bound value. There is no path from a typed character
// to the shape of the statement.
//
// That is also why the parser is allowed to be this forgiving. `AND`, `NOT`, `-`,
// `(`, `*` and a bare `"` are all words or dropped clauses in internal/search,
// and by the time a clause reaches this file it is a quoted literal: `"AND"`,
// `"not"`, `"(("`, `"*"`. A user cannot write an operator, because the language
// does not have one.

// An indexQuery is the shape of one search: which index, filtered by which
// scope, and how to rank it.
//
// It is a struct rather than four arguments because the mistake worth preventing
// is not a typo, it is a *pair*: the public table read through the private
// scope's absence, or the private scope applied to nothing. Two arguments can be
// swapped at a call site; a struct built from two constants next to each other
// cannot.
type indexQuery struct {
	// table is one of the two FTS indexes.
	table string

	// sc is the read scope. There are two, and the private one is the stricter:
	// see acl.go.
	sc scope

	// weights is the bm25 weight list, including its leading comma, or empty for
	// an index with one indexed column and nothing to weigh.
	weights string

	// tags, pageType and visibility are the query's three filters, carried
	// alongside the scope so that the clause and its arguments are built from one
	// value rather than from a call site that has to remember them.
	tags       []string
	pageType   string
	visibility string

	// secrets marks which of the two this is, for the hit field that says where
	// an excerpt came from.
	secrets bool
}

// withQuery carries a parsed query's filters into the statement, and returns the
// query unchanged if it has no filters.
func (q indexQuery) withQuery(parsed search.Query) indexQuery {
	q.tags = parsed.Tags
	q.pageType = parsed.Type.String()
	q.visibility = parsed.Visibility.String()
	return q
}

// filterArgs are the filter arguments, in the order `filters` puts them.
func (q indexQuery) filterArgs() []any {
	args := make([]any, 0, len(q.tags)+2)
	if len(q.tags) > 0 {
		args = append(args, tagMatch(q.tags))
	}
	if q.pageType != "" {
		args = append(args, q.pageType)
	}
	if q.visibility != "" {
		args = append(args, q.visibility)
	}
	return args
}

// The FTS table is not aliased anywhere in this file.

// The public index's ranking weights, one per column, in the order the table
// declares them -- page_id, title, aliases, body, tags, kind -- with the page_id
// weight ignored, because the column is UNINDEXED and contributes nothing to a
// score.
//
// A title match beats a tag, an alias or a body match. Those are not numbers
// fitted to a corpus; they are an ordering of where in a page the thing somebody
// typed is most likely to *be* the page they wanted. A DM who types "rivergate"
// is nearly always looking for the page called Rivergate, and a weighting that
// let a passing mention in somebody's session notes outrank it would be a search
// that answers the question nobody asked.
//
// The private index has one indexed column and no weights, so it has nothing to
// weigh against anything.
const bm25Weights = ", 10.0, 3.0, 4.0, 1.0, 2.0"

// The two queries, by name. The statements themselves are built by indexQuery's
// methods; a test prints all four of them so a reader can see every statement
// this package runs in one place.
var (
	publicIndexQuery = indexQuery{
		table:   publicIndex,
		sc:      scope{}, // filled in per call: the scope names a campaign
		weights: bm25Weights,
	}

	secretIndexQuery = indexQuery{
		table:   secretIndex,
		sc:      scope{},
		weights: "",
		secrets: true,
	}
)

// snippetText is the excerpt expression, as plain text.
//
// FTS5's markers are empty strings rather than `<mark>` tags on purpose. The
// excerpt is a slice of the DM's own markdown, it is built by the index engine
// before any Go code sees it, and a marker with HTML in it would put markup in
// the middle of a value the caller then has to decide whether to escape. Plain
// text is the shape that cannot be wrong: a view that wants to highlight the
// match has the query terms in hand and can do it in the browser, where a marker
// is a DOM node rather than a decision.
//
// -1 is "the best column", which is the one FTS5 picks, and 12 tokens is about a
// line and a half.
const snippetText = `snippet(%s, -1, '', '', '…', 12)`

// noExcerpt stands in for the excerpt when there is no MATCH to point it at.
//
// FTS5's auxiliary functions are only available in a query that matches the FTS
// table, so a filter-only search -- "every page tagged hub" -- cannot ask for one
// even though the table is being read. An empty string is the right stand-in and
// not a NULL: a NULL would have to be scanned into a nullable column, and a hit
// with no excerpt is a hit with an empty excerpt, not a hit with an unknown one.
const noExcerpt = `''`

// statement renders the query, with or without the MATCH clause.
//
// The two forms are one function rather than four constants because a filter-only
// search differs from a text search in three places at once -- no MATCH, no
// excerpt, and no bm25 order -- and three places is one too many to keep in step
// by hand across four statements. A test prints all four.
func (q indexQuery) statement(withMatch bool) string {
	return `SELECT p.id, p.path, p.title, p.type, p.visibility, COALESCE(p.owner_character_page_id, ''), ` + q.excerpt(withMatch) +
		`
		FROM ` + q.table + `
		JOIN pages p ON p.id = ` + q.table + `.page_id
		WHERE ` + q.match(withMatch) + ` AND (` + q.sc.where + `)` + filters(q) + `
		ORDER BY ` + q.order(withMatch) + `
		LIMIT ?`
}

// match is the FTS5 clause, or something always true.
//
// `1 = 1` rather than an empty string so that the clause after it is still
// followed by `AND`, and the shape of the statement does not change with the
// shape of the query.
func (q indexQuery) match(withMatch bool) string {
	if !withMatch {
		return `1 = 1`
	}
	return q.table + ` MATCH ?`
}

func (q indexQuery) excerpt(withMatch bool) string {
	if !withMatch {
		return noExcerpt
	}
	return fmt.Sprintf(snippetText, q.table)
}

// order is the ranking, and a filter-only search has none to rank by.
func (q indexQuery) order(withMatch bool) string {
	if !withMatch {
		return `p.path`
	}
	return `bm25(` + q.table + q.weights + `), p.path`
}

// filters is the tail of the WHERE clause: the three query-language filters, none
// of which is an FTS5 match.
//
// They are all SQL, and that is a decision rather than a convenience. An FTS5
// column filter is itself a match — the value is tokenised and the tokens have to
// be adjacent — so `type:homebrew-thing` as a column filter would be the phrase
// "homebrew thing", and a filter that quietly means something adjacent to what was
// asked for cannot be debugged from the results.
//
// `tag:` has a second reason, which is that tags are a property of the *page* and
// the public index is where a page's tags are indexed. Applying it as a column
// filter on whichever index the query happens to be reading would ask the private
// index for a column it does not have, and asking for one page's tags inside the
// search for that page's secret text is a question only the public index can
// answer. It is asked as a subquery against the public index, and a subquery is
// also what keeps the tag value a bound parameter rather than a spliced one.
func filters(q indexQuery) string {
	tail := ""

	if len(q.tags) > 0 {
		tail += ` AND p.id IN (SELECT page_id FROM ` + publicIndex + ` WHERE ` + publicIndex + ` MATCH ?)`
	}
	if q.pageType != "" {
		tail += ` AND p.type = ?`
	}
	if q.visibility != "" {
		tail += ` AND p.visibility = ?`
	}
	return tail
}

// The FTS table is not aliased anywhere in this file. `MATCH`'s left operand has
// to name the FTS5 table itself, and an alias would be one more name that could
// mean the table or the result set.

// SearchPublic runs a search against the index every principal may read.
//
// The audience scope is the whole `WHERE` clause as far as access control is
// concerned, and it is the same clause the page tree and the tag list will use
// when they arrive. Everything a DM can read is here; nothing a DM cannot read is,
// even its title.
func (s *Store) SearchPublic(ctx context.Context, campaignID string, as domain.Principal, q search.Query, limit int) ([]search.Hit, error) {
	return s.search(ctx, publicIndexQuery.withScope(readable(campaignID, as)), q, limit)
}

// SearchSecrets runs a search against the private index: the text of the
// `[!SECRET]` callouts, for a principal the stricter scope admits.
//
// The scope is what makes this safe and the two clauses of it are the whole
// argument. `readable` alone would be wrong: a `players` page is readable by
// every player, and the secret inside it is not theirs. So the private index is
// read through a scope that asks both, and the excerpt is built from the private
// index — which means a secret excerpt can only ever be produced for a principal
// that scope admitted, rather than for one who was merely allowed to open the page.
func (s *Store) SearchSecrets(ctx context.Context, campaignID string, as domain.Principal, q search.Query, limit int) ([]search.Hit, error) {
	return s.search(ctx, secretIndexQuery.withScope(readableWithSecrets(campaignID, as)), q, limit)
}

// The store is the Searcher the search package expects. Adding a method to one
// side of that pair without the other fails the build, which is the moment to
// find out.
var _ search.Searcher = (*Store)(nil)

// withScope is the query with a campaign and a principal in it. The scope is the
// only part that varies per call, and it is applied here rather than at the call
// site so the table and the scope cannot be separated.
func (q indexQuery) withScope(sc scope) indexQuery {
	q.sc = sc
	return q
}

// search runs one of the two queries.
//
// Everything that can differ between them is in the indexQuery; everything that
// must not is a constant in this file. The read scope is the whole `WHERE` clause
// as far as access control is concerned, and it is the same clause the page tree
// and the tag list will use when they arrive.
func (s *Store) search(ctx context.Context, q indexQuery, parsed search.Query, limit int) ([]search.Hit, error) {
	// An empty query asks for no pages at all, and that has to be a decision
	// rather than a default. "Show me everything" is a legitimate question with
	// a legitimate answer — a page tree, a tag cloud — but a *search* with an
	// empty box in it is a request for a list of every title in the campaign, and
	// that list is as disclosing as the pages themselves. Query.Empty is the
	// check, and the caller that wants a listing asks for one.
	if parsed.Empty() {
		return nil, nil
	}

	match := ftsMatch(parsed)
	withMatch := match != ""

	q = q.withQuery(parsed)

	args := make([]any, 0, len(q.sc.args)+len(q.tags)+3)
	if withMatch {
		args = append(args, match)
	}
	args = append(args, q.sc.args...)
	args = append(args, q.filterArgs()...)
	args = append(args, boundedLimit(limit))

	rows, err := s.read.QueryContext(ctx, q.statement(withMatch), args...)
	if err != nil {
		return nil, fmt.Errorf("searching %s: %w", q.table, err)
	}
	defer func() { _ = rows.Close() }()

	hits, err := scanHits(rows, q.secrets, "searching "+q.table)
	if err != nil {
		return nil, err
	}
	return hits, nil
}

// scanHits reads the rows of either query, and is the only place a result list is
// built.
//
// Rank is assigned here, from the row order, and it is the only thing the fusion
// uses from this index. A BM25 score is not carried: two scores from two indexes
// over two corpora of different sizes are not comparable, and passing one around
// as though it were is how a merge ends up comparing them.
func scanHits(rows *sql.Rows, secrets bool, what string) ([]search.Hit, error) {
	hits := []search.Hit{}
	for rank := 1; rows.Next(); rank++ {
		var hit search.Hit
		if err := rows.Scan(
			&hit.PageID, &hit.Path, &hit.Title, &hit.Type,
			&hit.Visibility, &hit.OwnerCharacterPageID,
			&hit.Snippet,
		); err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		hit.Rank = rank
		hit.FromSecrets = secrets
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	return hits, nil
}

// ftsMatch renders a parsed query's *text* as an FTS5 match expression, or the
// empty string when it has none. The three filters are not here: they are SQL, and
// filters builds them.
//
// Every clause goes through ftsLiteral, which is FTS5's own string quoting: a
// double quote around it and any quote inside it doubled. Inside that quoting
// there are no operators, no prefixes, no column filters and no parentheses, so
// what comes out of here is a conjunction of literals and there is nothing for a
// caller to have escaped.
//
// **The last text term is a prefix when the query asked for one.** M10 added a
// search box, and a box that matches whole words only is not a search box:
// somebody typing `fort` gets nothing, `forti` gets nothing, and `fortified` gets
// the page, so every result appears after the last character of the word that
// would find it.
//
// Only the *last* term. Every term becoming a prefix would make `toll collector`
// match `tolerance` and `collect`, and a search that widens as you type gets
// *wider*, not closer. The last term is the one still being typed; the earlier
// ones were finished and confirmed by the next keystroke.
//
// And only when `Query.PrefixLastTerm` says so, because the `*` is the one piece
// of FTS5 syntax this expression emits and `TestSearchMatchExpressionQuotesEveryClause`
// holds the line that nothing else can. A `*` appended to a closed literal is the
// right way to write it: inside the quotes there is no operator, so a `*` in there
// would be a literal asterisk.
func ftsMatch(q search.Query) string {
	clauses := make([]string, 0, q.Clauses())

	for _, term := range q.Terms {
		clauses = append(clauses, ftsLiteral(term))
	}
	if q.PrefixLastTerm {
		if i := len(clauses) - 1; i >= 0 {
			clauses[i] += "*"
		}
	}
	for _, phrase := range q.Phrases {
		clauses = append(clauses, ftsLiteral(phrase))
	}

	return strings.Join(clauses, " AND ")
}

// tagMatch renders the tag filters as one expression, to be bound as a single
// parameter of the subquery in `filters`.
//
// The tags are ORed inside parentheses, and the parentheses are the point: `OR`
// binds looser than `AND` in FTS5, so a bare `a OR b AND c` is `a OR (b AND c)`
// and a query with text in it would then require the text to be on the second
// tag.
func tagMatch(tags []string) string {
	clauses := make([]string, 0, len(tags))
	for _, tag := range tags {
		clauses = append(clauses, "tags : "+ftsLiteral(tag))
	}
	if len(clauses) == 1 {
		return clauses[0]
	}
	return "(" + strings.Join(clauses, " OR ") + ")"
}

// ftsLiteral quotes a value as an FTS5 string.
//
// The doubling of an inner quote is FTS5's own rule and it is the whole reason
// this function exists rather than a `strconv.Quote`: FTS5 has no escape
// character inside a string, so a value carrying a double quote has to carry two,
// and a value carrying one that is *not* doubled ends the literal early and makes
// the rest of the value into syntax. A value with no quote in it is the common
// case and is written out longhand for the same reason the rest of this package
// spells its constants: so that the shape is checkable.
func ftsLiteral(value string) string {
	if !strings.Contains(value, `"`) {
		return `"` + value + `"`
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

// boundedLimit clamps a caller's limit into the range the search package
// publishes. Zero and below mean "the default", because a limit of zero that meant
// "no results" would be a caller's bug silently becoming a feature.
func boundedLimit(limit int) int {
	switch {
	case limit <= 0:
		return search.DefaultLimit
	case limit > search.MaxLimit:
		return search.MaxLimit
	default:
		return limit
	}
}
