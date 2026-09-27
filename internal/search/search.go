// Package search is the query language, the two queries, and the fusion of their
// answers.
//
// # The shape of it
//
// A search reads two indexes and merges what they found:
//
//	typed text -> Query (this package, pure) -> two ranked lists (internal/store)
//	          -> Reciprocal Rank Fusion (this package, pure) -> []Hit
//
// The split is ADR 0009 and the reason is a page that is readable and a block
// inside it that is not. A player searching for a name the DM wrote inside a
// `[!SECRET]` callout is allowed to read that page, so filtering by page and then
// showing a snippet has already put the secret in the snippet buffer: `snippet()`
// reads the *indexed* text, and a match position inside a secret is itself a
// disclosure. So the public index is built only from text nobody is barred from
// seeing, and the text that is barred lives in a second index which is reachable
// exclusively through the read predicate.
//
// # What is pure and what is not
//
// Parse, Fuse and Hit are pure: no handle, no clock, no ambient state. That is
// what lets the relevance and ordering tests run with no database, and it is why
// the FTS5 query string is built in internal/store — the grammar belongs to the
// package that is the only one that knows SQLite exists.
//
// Searcher below is the seam. It is declared here, by the consumer, and
// satisfied by the store; a test can hand Run something else and check the fusion
// against a fixture corpus without a database anywhere in sight.

package search

import (
	"context"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The limits on one search, and the reason each one exists.
const (
	// DefaultLimit is how many hits a search returns when the caller does not say.
	// Ten is a screenful; a search that returned four hundred would be a page a
	// browser had to be trusted to paginate.
	DefaultLimit = 10

	// MaxLimit is the ceiling. It is here because the limit arrives from a
	// request, and "return everything" is a way to ask a wiki to do the work of
	// a dump.
	MaxLimit = 200
)

// Hit is one result: a page, an excerpt of why it matched, and where it ranked.
//
// The two index halves produce hits of the same shape on purpose. A DM whose
// secret matched and a player whose public body matched are looking at the same
// list, and a result list that changed shape depending on why something matched
// would be a UI wrinkle the search package should not be deciding.
type Hit struct {
	// PageID, Path, Title and Type identify the page. The id is what every other
	// row in the database points at, and the path is what a URL will hold.
	PageID string
	Path   string
	Title  string
	Type   string

	// Snippet is a short excerpt of the text that matched, as plain text with no
	// markup in it. Plain rather than highlighted because FTS5's markers would
	// have to be HTML, and an excerpt of a DM's own markdown going into a
	// response with a `<script>` in it is a sanitiser decision this package
	// should not be making silently. Highlighting is the view's, and it has the
	// query terms already.
	Snippet string

	// Rank is the position in the list this hit came from, counting from one.
	// It is what the fusion sums over, and it is the only thing a score from
	// one index means in the presence of another.
	Rank int

	// Score is the fused score, and it is only set once the lists have been
	// merged. A hit straight out of an index has a rank and no score, because a
	// BM25 score from one index means nothing next to one from the other.
	Score float64

	// FromSecrets records which index this hit came from. It is the reason the
	// excerpt is safe to show at all: a hit in the private index was admitted by
	// the stricter scope, which asks whether the principal may see that page's
	// secrets and not only whether they may read the page.
	FromSecrets bool

	// Visibility and OwnerCharacterPageID are the two facts a *plugin's* access
	// policy needs about a hit, and they are here because without them a policy
	// cannot narrow a search result.
	//
	// They were not here for M5, when the only answer a hit needed was "which pages
	// may this principal read" and the scope had already answered it in SQL. They are
	// here for M11, where a policy narrows *on top of* that scope — and a policy that
	// cannot see a hit's audience can only say "hides everything", which is not a
	// policy, it is a wiki with an empty dropdown.
	//
	// Two more columns in a SELECT that joins `pages` anyway. A `WHERE` clause
	// cannot do this: FTS5's column set is fixed when the table is created, and a
	// policy's rule is a Go function, so the answer to "is this hit still visible"
	// is computed in Go from something the row has to carry.
	//
	// The scope is unaffected. `SearchPublic` and `SearchSecrets` return exactly what
	// they returned before, and a policy can only take rows away from that list.
	Visibility           string
	OwnerCharacterPageID string
}

// Searcher is what a search needs from the store, and nothing more.
//
// Two methods rather than one with a flag, because the two are not the same
// query with a different column list: they read different tables, they are
// filtered by different scopes, and only one of them may be called for a
// principal who is not entitled to it. A flag would make the unsafe call a
// one-character mistake.
type Searcher interface {
	// SearchPublic searches the index every principal may read, filtered by the
	// audience scope.
	SearchPublic(ctx context.Context, campaignID string, as domain.Principal, q Query, limit int) ([]Hit, error)

	// SearchSecrets searches the private index, filtered by the stricter scope.
	SearchSecrets(ctx context.Context, campaignID string, as domain.Principal, q Query, limit int) ([]Hit, error)
}
