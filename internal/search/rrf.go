package search

import "sort"

// Reciprocal Rank Fusion. A page's score is the sum, over the ranked lists it
// appears in, of 1 / (K + rank).
//
//	score(d) = Σ  1 / (K + rank_i(d))
//
// It is rank-based and not score-based, and that is the whole decision (ADR
// 0009). Two BM25 scores from two indexes over two corpora of different sizes are
// not comparable, and any normalisation that made them agree would be a constant
// fitted to the corpus it was fitted on. RRF needs no shared scale at all: it uses
// only each list's own ordering, which is the one thing a list is certain about.
//
// The property that makes it worth running two queries: a page that appears in
// *both* lists scores more than a page that is first in one of them. A page whose
// title matches and whose secret contains the phrase is a better answer than a
// page whose title matches, and a merge that compared scores could not know that.

// K is the smoothing constant from the RRF literature, and it is the published
// default of 60 rather than a value of our own.
//
// It is not tuned and must not be tuned. The number asks how quickly a deep rank
// stops mattering: at rank 1 a hit contributes 1/61 and at rank 10 it
// contributes 1/70, so a tenth-place hit is worth nine-tenths of a first — a deep
// rank is small but not nothing, and a first is not so overwhelming that one match
// in the other index cannot lift it. A "tuned" constant here would be fitted
// against whichever fixture corpus it was fitted to, and would then be wrong on
// the campaign it was not fitted on, in a way nothing would notice.
const K = 60

// ranked is one page as the fusion is accumulating it.
type ranked struct {
	hit Hit

	// score is the fused score so far.
	score float64

	// bestRank is the best position any list gave this page, kept because it is
	// the tie-break: two pages that tie on score should not tie on "where did
	// they place", and the one that placed first did better.
	bestRank int
}

// Fuse merges ranked lists into one, best first.
//
// Every list is a ranking of the *same* principal's results. A hit in one list and
// a hit in another have to be comparable, and they are comparable only when they
// answer the same question for the same reader: a DM's secret hits fused with a
// different principal's secret hits would be a page one of them may read and the
// other may not, with an excerpt neither of them should have.
//
// The fused hits come back with their ranks renumbered from one, because after
// the merge the old ranks mean nothing: rank 4 of two lists is not comparable to
// rank 4 of one.
func Fuse(lists ...[]Hit) []Hit {
	fused := map[string]*ranked{}
	order := []string{}

	for _, list := range lists {
		for position, hit := range list {
			rank := hit.Rank
			if rank < 1 {
				// A list that arrives without ranks is a list nobody ranked, and
				// treating every entry as rank 1 would make every page in it tie
				// for the top. The position is the best guess available and the
				// only one that needs no second convention.
				rank = position + 1
			}

			entry, seen := fused[hit.PageID]
			if !seen {
				entry = &ranked{hit: hit, bestRank: rank}
				fused[hit.PageID] = entry
				// The insertion order is the tie-break after the score, before the
				// id: two pages that tie on score and best rank have appeared in
				// the same list order, and keeping that is more faithful than
				// falling back on an id that means nothing to a reader.
				order = append(order, hit.PageID)
			}

			entry.score += 1 / (K + float64(rank))
			if rank < entry.bestRank {
				entry.bestRank = rank
			}

			// The excerpt from the private index wins where a page matched in
			// both, and that is safe rather than convenient: a page is in the
			// private list only because the stricter scope admitted it for this
			// principal, so if they matched the secret they may see it. It is also
			// the more useful one — a match inside a secret is a more specific
			// answer than a match in a title.
			if hit.FromSecrets {
				entry.hit.Snippet = hit.Snippet
				entry.hit.FromSecrets = true
			}
		}
	}

	sort.SliceStable(order, func(i, j int) bool {
		left, right := fused[order[i]], fused[order[j]]
		if left.score != right.score {
			return left.score > right.score
		}
		if left.bestRank != right.bestRank {
			return left.bestRank < right.bestRank
		}
		return i < j
	})

	results := make([]Hit, 0, len(order))
	for position, id := range order {
		hit := fused[id].hit
		hit.Score = fused[id].score
		hit.Rank = position + 1
		results = append(results, hit)
	}

	return results
}
