package index

import (
	"fmt"
	"slices"
	"strings"
)

// Report is what one sync did.
//
// It exists because a sync that does nothing and says nothing is
// indistinguishable from a sync that is broken: both leave the index as it was.
// A DM who runs `wiki sync` needs to see that it read 240 pages and changed one,
// and a test needs to assert the same thing without reading the whole database.
type Report struct {
	// Indexed is the page paths whose rows were written, once each however many
	// passes wrote them.
	Indexed []string

	// Unchanged is how many pages were already in step, settled by the end. It is
	// worked out when the sync finishes, because a page that is rewritten by a
	// later pass was not in step during the first one.
	Unchanged int

	// Archived is the page paths whose files are gone, so their rows were
	// archived. They stay in the database: revisions and inbound links point at
	// them, and an archived page is recoverable.
	Archived []string

	// Skipped is the files that were not pages, or whose frontmatter could not be
	// read. The rest of the campaign was still indexed.
	Skipped []Skip

	// Passes is how many walks of the vault it took to settle the links. One is
	// the usual answer; more means a page links to a page the walk reaches
	// later, and the second pass finds it.
	Passes int

	// Rebuilt is how many rows a full reindex threw away before rebuilding them.
	// It is zero for a sync, and the number a caller prints after one.
	Rebuilt int

	// Refused is the pages that will not be indexed at all. See Refusal, which
	// is the other half of this pair and the opposite of Skip.
	Refused []Refusal

	// Ownership is the pages whose owner does not hold up: a `character:` key
	// naming a page that is not there, or a page under one character whose key
	// says another. These pages *are* indexed -- the ownership question does not
	// stop a page being readable -- and the problems are here because the owner
	// they will be given in M7 is wrong.
	Ownership []OwnershipProblem
}

// Err returns the skip and refusal reasons as one error, for a caller that
// wants to print them or fail on them. It is nil when there were none, so a
// successful sync is a nil error even when it indexed nothing.
func (r Report) Err() error {
	reasons := make([]string, 0, len(r.Skipped)+len(r.Refused)+len(r.Ownership))
	for _, skip := range r.Skipped {
		reasons = append(reasons, fmt.Sprintf("%s: %s", skip.Path, skip.Reason))
	}
	for _, refusal := range r.Refused {
		reasons = append(reasons, fmt.Sprintf("%s: %s", refusal.Path, refusal.Reason))
	}
	for _, problem := range r.Ownership {
		reasons = append(reasons, fmt.Sprintf("%s: %s", problem.Path, problem.Reason))
	}

	if len(reasons) == 0 {
		return nil
	}

	return fmt.Errorf("%d file(s) were not indexed:\n  %s", len(reasons), strings.Join(reasons, "\n  "))
}

// Changed is how many rows this sync touched, which is the number a caller
// prints and a test asserts. It is not the whole answer: a file the sync
// skipped or refused means the index is not in step either, however few rows
// would change.
func (r Report) Changed() int {
	return len(r.Indexed) + len(r.Archived)
}

// InStep reports whether the index says exactly what the files say, and
// everything in the vault could be indexed.
//
// It is the boolean `wiki sync --check` exits on, and it counts a skip and a
// refusal as out of step. A page whose `visibility` the application cannot read
// changes no rows when it is refused, so a check that counted only rows would say
// "in step" about a page the DM cannot see -- which is the one answer that must
// never be given.
func (r Report) InStep() bool {
	return r.Changed() == 0 && len(r.Skipped) == 0 && len(r.Refused) == 0
}

// Problems is how many pages the sync could not do anything sensible with, which
// is the number a DM wants printed and the number a script wants non-zero on.
func (r Report) Problems() int {
	return len(r.Skipped) + len(r.Refused) + len(r.Ownership)
}

// seen and written are the report's private bookkeeping, because Sync runs
// several passes over the same vault and a report that described the last one
// would say "nothing changed" immediately after indexing a campaign.
type reportSets struct {
	seen    map[string]bool
	written map[string]bool
}

// add folds one page's outcome into the report, across every pass.
func (r *Report) add(one outcome, sets *reportSets) {
	sets.seen[one.path] = true

	switch {
	case one.indexed:
		if !sets.written[one.path] {
			sets.written[one.path] = true
			r.Indexed = append(r.Indexed, one.path)
		}
	case one.archived:
		if !slices.Contains(r.Archived, one.path) {
			r.Archived = append(r.Archived, one.path)
		}
	case one.skip != nil:
		if !slices.ContainsFunc(r.Skipped, func(s Skip) bool { return s.Path == one.skip.Path }) {
			r.Skipped = append(r.Skipped, *one.skip)
		}
	case one.refusal != nil:
		if !slices.ContainsFunc(r.Refused, func(x Refusal) bool { return x.Path == one.refusal.Path }) {
			r.Refused = append(r.Refused, *one.refusal)
		}
	}
}

// finalise works out how many pages were never written, which is only knowable
// once every pass has run.
func (r *Report) finalise(sets *reportSets) {
	r.Unchanged = len(sets.seen) - len(sets.written)
	if r.Unchanged < 0 {
		r.Unchanged = 0
	}
}

// newReportSets is the bookkeeping for one sync.
func newReportSets() *reportSets {
	return &reportSets{seen: map[string]bool{}, written: map[string]bool{}}
}
