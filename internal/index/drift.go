package index

import (
	"context"
	"fmt"
)

// Drift is the index and the files disagreeing. It is a state, not an error:
// nothing has failed, the projection has simply stopped describing what it
// projects, and ADR 0001's answer to that is to rebuild it from the files.
//
// Everything here exists because a projection that can drift needs a way to say
// so, and because the ways it can drift are worth naming.

// Check reports what a sync *would* do, and writes nothing.
//
// It is the engine behind `wiki sync --check`, and it is the cheapest thing in
// this milestone: a DM can ask "is my index in step with my vault" without
// changing anything, a script can assert it, and a test can use it to prove that
// a sync actually did its work rather than reporting that it did.
//
// The report it returns is the same type a sync returns, so a caller can print
// one and check the other with the same code.
func (y *Syncer) Check(ctx context.Context) (Report, error) {
	report := Report{}
	sets := newReportSets()

	// The same shape as Sync, for the same reason: a pass is only finished when
	// it stops finding anything, so that "would change" and "changed" are
	// answers to the same question asked in the same order.
	for pass := 1; pass <= maxPasses; pass++ {
		changed, err := y.checkPass(ctx, &report, sets)
		if err != nil {
			report.finalise(sets)
			return report, err
		}

		report.Passes = pass
		if changed == 0 {
			break
		}
	}

	report.finalise(sets)
	return report, nil
}

// checkPass is Check's walk: it decides the same things Sync does and writes
// nothing.
func (y *Syncer) checkPass(ctx context.Context, report *Report, sets *reportSets) (int, error) {
	paths, err := y.vault.List()
	if err != nil {
		return 0, fmt.Errorf("listing the vault of %s: %w", y.campaign.Slug, err)
	}

	present := make(map[string]bool, len(paths))
	changed := 0

	for _, pagePath := range paths {
		present[pagePath] = true

		p, planErr := y.planFor(ctx, pagePath)
		if planErr != nil {
			return changed, planErr
		}

		// The same shape of answer Sync would give, from the same plan.
		one := outcome{path: pagePath, unchanged: true}
		switch {
		case p.skip != nil:
			one = outcome{path: pagePath, skip: p.skip}
		case p.refusal != nil:
			one = outcome{path: pagePath, refusal: p.refusal}
		case p.page.Path == "":
			// The file is gone, so a sync would archive the row.
			archived, archiveErr := y.archivedOutcome(ctx, pagePath)
			if archiveErr != nil {
				return changed, archiveErr
			}
			one = archived
		case !p.settled:
			one = outcome{path: pagePath, indexed: true}
		}

		if one.indexed || one.archived {
			changed++
		}
		report.add(one, sets)
	}

	// A page the index has and the vault does not would be archived by a sync,
	// and Check has to say so.
	indexed, err := y.store.ListPages(ctx, y.campaign.ID)
	if err != nil {
		return changed, fmt.Errorf("listing the indexed pages of %s: %w", y.campaign.Slug, err)
	}
	for _, page := range indexed {
		if present[page.Path] {
			continue
		}
		report.add(outcome{path: page.Path, archived: true}, sets)
		changed++
	}

	return changed, nil
}

// archivedOutcome is what an archive looks like when nothing is written: the row
// is there, the file is not, so a sync would archive it.
func (y *Syncer) archivedOutcome(ctx context.Context, pagePath string) (outcome, error) {
	one, err := y.archive(ctx, pagePath)
	if err != nil {
		return outcome{path: pagePath, archived: true}, err
	}
	return one, nil
}

// ReindexFull throws the index away and builds it again from the files.
//
// This is the repair of last resort, and it is what ADR 0001 means by a
// projection being disposable. It is also slow where a sync is not: every page
// is written whether it changed or not, and every link is re-resolved from
// nothing, which for a campaign of a few thousand pages is a second or two and
// for a very large one is a coffee break.
//
// What it is *not* is clever. A reindex that only rewrote what had drifted
// would be a sync, and the thing this is for is the case a sync cannot help:
// the index is wrong in a way nobody has described, and the answer is to make it
// right by making it again.
func (y *Syncer) ReindexFull(ctx context.Context) (Report, error) {
	report := Report{}
	sets := newReportSets()

	// Every page the index has for this campaign goes, including the archived
	// ones: an archived row is a claim about a file that is not there, and a
	// full reindex is the moment to stop making it. The rows go with them, by
	// cascade -- revisions, links and targets -- which is right, because they
	// were all written for files this campaign no longer has.
	pages, err := y.store.ListPages(ctx, y.campaign.ID)
	if err != nil {
		return report, fmt.Errorf("listing the indexed pages of %s: %w", y.campaign.Slug, err)
	}

	report.Rebuilt = len(pages)
	for _, page := range pages {
		if purgeErr := y.store.PurgePage(ctx, page.ID); purgeErr != nil {
			return report, fmt.Errorf("purging %s: %w", page.Path, purgeErr)
		}
	}

	synced, err := y.Sync(ctx)
	if err != nil {
		return report, err
	}

	// The report describes the rebuild, not the two syncs it went through: the
	// archived list is empty because everything was purged, and a caller asking
	// "what did you do" wants the answer for the campaign as a whole.
	report.Indexed = synced.Indexed
	report.Skipped = synced.Skipped
	report.Refused = synced.Refused
	report.Passes = synced.Passes
	report.finalise(sets)

	return report, nil
}
