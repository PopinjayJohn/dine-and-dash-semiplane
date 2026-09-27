package search

import (
	"context"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// FusionDepth is how much deeper than the requested number of results each index
// is read to.
//
// It is a bound rather than a fit. RRF reorders a page that appears in both lists
// above a page that is first in one, so a fusion over two lists of exactly N
// cannot find the pages that ought to be in the top N unless each list was read
// further than N. Reading five times as deep is enough to let a page in at
// eleventh place in one list and first in the other be found, and is bounded so
// that a DM typing one word in a large campaign does not read the vault.
//
// It is not tuned against a corpus and should not be: the property it needs to
// hold is "a page in both lists is findable", and any depth above one gives that
// to some degree.
const FusionDepth = 5

// Run is a search: the two queries and the fusion of their answers.
//
// It takes the backend as an argument rather than holding one, so there is no
// handle in this package and the whole of Run is testable with a fixture and no
// database — which is the property ADR 0009 asks for and the reason the relevance
// tests are as many as they are.
//
// The order is: parse, refuse or stop early, then one query per index, then fuse,
// then cut. Stopping before the queries matters — an empty search box is not a
// search for every page, and running the queries for it would be the first half
// of answering one.
// Option is a setting on a search, and there is one because there is one thing
// worth setting: whether the last term is a prefix. A variadic rather than a
// parameter so that the six arguments a plain search needs are the six arguments a
// plain search takes, and a caller that wants a prefix says so by name.
type Option func(*Query)

// WithPrefixLastTerm makes the query's last text term match as a prefix, which is
// what a box somebody is typing into needs and what a page somebody has submitted
// does not.
func WithPrefixLastTerm() Option {
	return func(q *Query) { q.PrefixLastTerm = true }
}

func Run(ctx context.Context, backend Searcher, campaignID string, as domain.Principal, input string, limit int, opts ...Option) ([]Hit, error) {
	parsed, err := Parse(input)
	for _, opt := range opts {
		opt(&parsed)
	}
	if err != nil {
		return nil, err
	}
	if parsed.Empty() {
		return nil, nil
	}

	depth := bounded(limit) * FusionDepth
	if depth > MaxLimit {
		depth = MaxLimit
	}

	public, err := backend.SearchPublic(ctx, campaignID, as, parsed, depth)
	if err != nil {
		return nil, fmt.Errorf("searching the public index: %w", err)
	}

	// The private index is queried for every principal and filtered by the
	// stricter scope inside it, rather than being skipped for anybody who is not
	// entitled. A DM's search and a player's search for the same word therefore
	// ask the same question of the same code, and the difference in the answers
	// is the scope's doing rather than a branch somewhere above it — which is
	// what makes the canary test meaningful.
	private, err := backend.SearchSecrets(ctx, campaignID, as, parsed, depth)
	if err != nil {
		return nil, fmt.Errorf("searching the private index: %w", err)
	}

	fused := Fuse(public, private)

	if wanted := bounded(limit); len(fused) > wanted {
		fused = fused[:wanted]
	}
	return fused, nil
}

// bounded is the caller's limit, defaulted. The same rule as the store's own
// clamp, kept separate because the two are answers to different questions: the
// store clamps what it will *read*, and this clamps what the caller will be
// *given*.
func bounded(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	return limit
}
