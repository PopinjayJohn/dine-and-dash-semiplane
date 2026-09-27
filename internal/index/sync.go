package index

import (
	"context"
	"fmt"
	"slices"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// Syncer keeps one campaign's index in step with its vault.
//
// It reads files and writes rows, and it never writes a file: see the package
// comment. The zero value is not usable; build one with New.
type Syncer struct {
	vault    *vault.Vault
	store    *store.Store
	campaign domain.Campaign
	resolver *Resolver
}

// New returns a syncer for one campaign of one vault.
func New(v *vault.Vault, s *store.Store, campaign domain.Campaign) *Syncer {
	return &Syncer{
		vault:    v,
		store:    s,
		campaign: campaign,
		resolver: NewResolver(s, campaign.ID),
	}
}

// Campaign is the campaign this syncer keeps in step.
func (y *Syncer) Campaign() domain.Campaign {
	return y.campaign
}

// Store is the store this syncer writes to, for a caller that has to look at
// what it wrote -- a test, or a `wiki sync --check` reporting what it found.
func (y *Syncer) Store() *store.Store {
	return y.store
}

// Vault is the vault this syncer reads.
func (y *Syncer) Vault() *vault.Vault {
	return y.vault
}

// Sync reads the whole vault and brings the index into step with it.
//
// A full walk every time: the content hash says which files changed, and the
// derived fields say whether the row is what the file would produce. A DM's
// vault is a few thousand files rather than a few million, and a sync that only
// looked at the files it knew had changed would miss a row that rotted in place
// (see the plan comment in derive.go, which is where the argument is).
//
// The walk repeats while the previous one wrote something, because a page
// indexed late in a pass is the target of a page the pass had already passed.
// Report.Passes says how many it took, and a campaign already in step takes one.
func (y *Syncer) Sync(ctx context.Context) (Report, error) {
	report := Report{}
	sets := newReportSets()

	for pass := 1; pass <= maxPasses; pass++ {
		changed, err := y.syncPass(ctx, &report, sets)
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

// SyncPath brings one page into step, which is what a watcher event carries.
//
// A path that is not a page is skipped and a path whose file has gone archives
// the row, so a watcher does not have to know which of the two it saw.
func (y *Syncer) SyncPath(ctx context.Context, pagePath string) (Report, error) {
	report := Report{}
	sets := newReportSets()

	one, err := y.syncPath(ctx, &report, pagePath)
	if err != nil {
		report.finalise(sets)
		return report, err
	}
	report.add(one, sets)
	report.finalise(sets)
	report.Passes = 1

	return report, nil
}

// syncPass is one walk of the vault. It returns how many rows it wrote, which is
// how Sync knows whether another pass could settle anything.
func (y *Syncer) syncPass(ctx context.Context, report *Report, sets *reportSets) (int, error) {
	paths, err := y.vault.List()
	if err != nil {
		return 0, fmt.Errorf("listing the vault of %s: %w", y.campaign.Slug, err)
	}

	present := make(map[string]bool, len(paths))
	changed := 0

	for _, pagePath := range paths {
		present[pagePath] = true

		one, syncErr := y.syncPath(ctx, report, pagePath)
		if syncErr != nil {
			return changed, syncErr
		}
		if one.indexed || one.archived {
			changed++
		}
		report.add(one, sets)
	}

	// A page whose file is gone is archived. It is not purged: revisions and
	// inbound links point at it, and a DM who deleted a file by accident should
	// be able to get it back. Purging is a separate, deliberate act, and it
	// happens in M9.
	//
	// M9's *archive* action deletes the file, which is what makes this correct
	// rather than circular: the file is the truth (ADR 0001), so a page whose
	// file is still there is not archived whatever a row says.
	archived, err := y.archiveMissing(ctx, present)
	if err != nil {
		return changed, err
	}
	changed += len(archived)

	// A union across passes, like everything else in the report: the second pass
	// finds nothing left to archive, and a report that said so would be
	// describing the last pass rather than the sync.
	for _, pagePath := range archived {
		if !slices.Contains(report.Archived, pagePath) {
			report.Archived = append(report.Archived, pagePath)
		}
	}

	return changed, nil
}

// outcome is what happened to one page, before it becomes part of a Report. It
// is the vocabulary the two walkers -- Sync and Check -- have in common.
type outcome struct {
	path      string
	indexed   bool
	archived  bool
	unchanged bool
	skip      *Skip
	refusal   *Refusal
}

// syncPath plans one page and, if the index is not already what the file says,
// writes it. The planning is in derive.go and is the only place a page's
// contents are read.
func (y *Syncer) syncPath(ctx context.Context, report *Report, pagePath string) (outcome, error) {
	p, err := y.planFor(ctx, pagePath)
	if err != nil {
		return outcome{}, err
	}

	if p.ownerProblem != nil {
		report.Ownership = append(report.Ownership, *p.ownerProblem)
	}

	switch {
	case p.skip != nil:
		return outcome{path: pagePath, skip: p.skip}, nil
	case p.refusal != nil:
		return outcome{path: pagePath, refusal: p.refusal}, nil
	case p.page.Path == "":
		// The file is not there. That is the caller's other case.
		return y.archive(ctx, pagePath)
	case p.settled:
		return outcome{path: pagePath, unchanged: true}, nil
	}

	return y.apply(ctx, p)
}
