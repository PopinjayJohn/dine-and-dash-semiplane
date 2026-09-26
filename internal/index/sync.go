package index

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
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
// what it wrote -- a test, or a `wiki sync --check` that reports what it found.
func (y *Syncer) Store() *store.Store {
	return y.store
}

// Vault is the vault this syncer reads.
func (y *Syncer) Vault() *vault.Vault {
	return y.vault
}

// outcome is what happened to one page, before it becomes part of a Report.
type outcome struct {
	path      string
	indexed   bool
	archived  bool
	unchanged bool
	skip      *Skip
	refusal   *Refusal
}

// maxPasses is how many times one Sync will walk the vault.
//
// A single pass cannot finish the job, and the reason is ordering: a page is
// indexed when the walk reaches it, so a page that links to one the walk has not
// reached yet is left with an unresolved link -- and the walk does not come back
// to it. The second pass finds the target, and settles the page that wanted it.
//
// Two passes settle a vault, and the third finds nothing, because the second
// pass's writes only resolve links in the pages it rewrote. The bound is belt
// and braces for a case the "nothing changed, so stop" rule already covers.
const maxPasses = 4

// Sync reads the whole vault and brings the index into step with it.
//
// A full walk every time and incremental writes: the content hash is what says
// which of the files changed, and a DM's vault is a few thousand files rather
// than a few million. A watcher's events come through SyncPath, which is the same
// per-page work with the walk skipped.
//
// The walk repeats while the previous one changed something, so one
// `wiki sync` is enough for a fresh vault rather than a number of runs the DM
// has to be told about. Report.Passes says how many it took, and a vault that
// is already in step takes one.
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
			// The pass wrote nothing, so the index now says exactly what the
			// files say and another walk would read the same files and write the
			// same nothing. Stopping here is what makes this terminate.
			break
		}
	}

	report.finalise(sets)
	return report, nil
}

// syncPass is one walk of the vault, adding what it did to the report. It
// returns how many rows it wrote, which is how Sync knows whether another pass
// could settle anything.
func (y *Syncer) syncPass(ctx context.Context, report *Report, sets *reportSets) (int, error) {
	paths, err := y.vault.List()
	if err != nil {
		return 0, fmt.Errorf("listing the vault of %s: %w", y.campaign.Slug, err)
	}

	present := make(map[string]bool, len(paths))
	changed := 0

	for _, pagePath := range paths {
		present[pagePath] = true

		one, syncErr := y.syncPage(ctx, pagePath)
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

// SyncPath brings one page into step, which is what a watcher event carries.
//
// A path that is not a page is skipped and a path whose file has gone archives
// the row, so a watcher does not have to know which of the two it saw.
func (y *Syncer) SyncPath(ctx context.Context, pagePath string) (Report, error) {
	report := Report{}
	sets := &reportSets{seen: map[string]bool{}, written: map[string]bool{}}

	one, err := y.syncPage(ctx, pagePath)
	if err != nil {
		return report, err
	}
	report.add(one, sets)
	report.finalise(sets)
	report.Passes = 1

	return report, nil
}

// syncPage is the work for one path, and it is the only place in this package
// that reads a file. Everything else is bookkeeping around what it decided.
func (y *Syncer) syncPage(ctx context.Context, pagePath string) (outcome, error) {
	// A path that is not a page is not an error. A DM's vault can contain a file
	// whose name is not a legal page path, and refusing to index the other 240
	// pages because of one is not what anybody wants.
	if _, err := vault.CheckPagePath(pagePath); err != nil {
		return skipped(pagePath, err.Error()), nil
	}

	data, err := y.vault.ReadBytes(pagePath)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			return y.archive(ctx, pagePath)
		}
		return outcome{}, fmt.Errorf("reading %s: %w", pagePath, err)
	}

	hash := vault.Hash(data)

	// An unchanged page needs nothing written unless one of its links would
	// resolve today, which is what `settled` answers.
	settled, settledErr := y.settled(ctx, pagePath, hash)
	if settledErr != nil {
		return outcome{}, settledErr
	}
	if settled {
		return outcome{path: pagePath, unchanged: true}, nil
	}

	doc, err := vault.Parse(data)
	if err != nil {
		return skipped(pagePath, err.Error()), nil
	}

	// The visibility is read and checked before anything is written. A row that
	// exists is a row a later render may treat as `players`, so a page whose
	// audience is unknown must not reach the index at all.
	if _, visibilityErr := doc.Visibility(); visibilityErr != nil {
		return refused(pagePath, visibilityErr.Error()), nil
	}

	frontmatter, err := doc.FrontmatterText()
	if err != nil {
		// Parse would have refused the file already, so this is unreachable
		// rather than impossible. Reporting it as a skip is the honest shape for
		// "cannot index this file", and indexing the page without its block
		// would be a projection that silently lost the DM's own keys.
		return skipped(pagePath, err.Error()), nil
	}

	stored, err := y.store.UpsertPage(ctx, domain.Page{
		CampaignID:      y.campaign.ID,
		Path:            pagePath,
		Title:           titleOf(doc, pagePath),
		Type:            pageTypeOf(doc),
		Frontmatter:     frontmatter,
		Body:            doc.Body(),
		ContentHash:     hash,
		RendererVersion: render.RendererVersion,
	})
	if err != nil {
		return outcome{}, fmt.Errorf("indexing %s: %w", pagePath, err)
	}

	if err := y.store.ReplacePageAliases(ctx, stored.ID, doc.Aliases()); err != nil {
		return outcome{}, fmt.Errorf("recording the aliases of %s: %w", pagePath, err)
	}

	if _, err := y.writeLinks(ctx, pagePath, stored.ID, doc); err != nil {
		return outcome{}, err
	}

	return outcome{path: pagePath, indexed: true}, nil
}

// settled reports whether a page's row is already current *and* its link graph
// is what the file would say today.
//
// The second half is the interesting one, and the obvious version of it is wrong.
// "Unchanged hash and no unresolved links" looks like it settles, and it settles
// nothing for a page with a link to a page the DM has not written yet -- which
// is most pages in an early campaign. Every sync would rewrite all of them, for
// ever, with the same bytes.
//
// So an unresolved link is not by itself a reason to look again. The reason is
// that the link *would resolve now*, and that is a question worth asking: it is
// asked only of the links that are unresolved, which is the only set that can
// have changed. A link to a page that still does not exist leaves the page
// settled, and the next sync says "unchanged" honestly.
func (y *Syncer) settled(ctx context.Context, pagePath, hash string) (bool, error) {
	indexed, err := y.store.GetPage(ctx, y.campaign.ID, pagePath)
	if err != nil {
		return false, nil //nolint:nilerr // a failed read means "re-index it"
	}
	if indexed.ContentHash != hash {
		return false, nil
	}

	links, err := y.store.LinksFrom(ctx, indexed.ID)
	if err != nil {
		return false, fmt.Errorf("reading the links of %s: %w", pagePath, err)
	}

	for _, link := range links {
		if link.Resolved() {
			continue
		}

		if _, found, err := y.resolver.ResolvePage(ctx, link.DstPath, ""); err != nil {
			return false, fmt.Errorf("re-resolving %q for %s: %w", link.DstPath, pagePath, err)
		} else if found {
			return false, nil
		}
	}

	return true, nil
}

// writeLinks replaces a page's outgoing links, resolving each destination
// against the index as it stands, and returns how many it resolved -- which is
// what tells Sync whether another pass could settle more.
func (y *Syncer) writeLinks(ctx context.Context, pagePath, pageID string, doc *vault.Document) (int, error) {
	refs, err := render.LinksOf(doc.Body())
	if err != nil {
		return 0, fmt.Errorf("reading the links out of %s: %w", pagePath, err)
	}

	resolved := 0

	links := make([]domain.PageLink, 0, len(refs))
	for _, ref := range refs {
		link := domain.PageLink{
			SrcPageID: pageID,
			DstPath:   ref.Target,
			Kind:      domain.LinkKindLink,
		}
		if ref.Embed {
			link.Kind = domain.LinkKindEmbed
		}

		// A target that cannot be a page path cannot resolve to a page, and it
		// is still worth recording: `[[the toll on the river]]` is a name the DM
		// may one day add as an alias, and a graph that held only resolved links
		// could not answer "what links to a page nobody has written yet".
		if _, err := vault.CheckPagePath(ref.Target); err == nil {
			target, found, err := y.resolver.ResolvePage(ctx, ref.Target, ref.Heading)
			if err != nil {
				return 0, fmt.Errorf("resolving %q for %s: %w", ref.Target, pagePath, err)
			}
			if found {
				resolved++
				// The *id* goes in dst_page_id, which is what the column
				// references; the path is what the DM wrote, resolved to the
				// page it names. Writing the path into an id column is a
				// foreign key violation, which is this milestone's first
				// honest bug.
				link.DstPageID = target.ID
				link.DstPath = target.Path
			}
		}

		links = append(links, link)
	}

	if err := y.store.ReplaceLinks(ctx, pageID, links); err != nil {
		return 0, fmt.Errorf("recording the links of %s: %w", pagePath, err)
	}

	return resolved, nil
}

// archive archives one page's row, for a file that is no longer there.
func (y *Syncer) archive(ctx context.Context, pagePath string) (outcome, error) {
	indexed, err := y.store.GetPage(ctx, y.campaign.ID, pagePath)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			// Nothing indexed and no file: a path that was never a page, which
			// is not worth a report line.
			return outcome{path: pagePath}, nil
		}
		return outcome{}, fmt.Errorf("looking up %s to archive it: %w", pagePath, err)
	}

	if err := y.store.DeletePage(ctx, indexed.ID); err != nil {
		return outcome{}, fmt.Errorf("archiving %s: %w", pagePath, err)
	}

	return outcome{path: pagePath, archived: true}, nil
}

// archiveMissing archives every indexed page whose file is gone.
func (y *Syncer) archiveMissing(ctx context.Context, present map[string]bool) ([]string, error) {
	pages, err := y.store.ListPages(ctx, y.campaign.ID)
	if err != nil {
		return nil, fmt.Errorf("listing the indexed pages of %s: %w", y.campaign.Slug, err)
	}

	var archived []string
	for _, page := range pages {
		if present[page.Path] {
			continue
		}

		if err := y.store.DeletePage(ctx, page.ID); err != nil {
			return nil, fmt.Errorf("archiving %s: %w", page.Path, err)
		}
		archived = append(archived, page.Path)
	}

	return archived, nil
}

// pageTypeOf is the page's type, and `note` when the file does not say.
//
// The default is the least claiming type there is. A DM who wrote no `type:` has
// not claimed a page as an NPC or a quest, and guessing one would give the page
// behaviour it never asked for: an `npc` page is something the M11 plugins and
// the Secrets panel treat specially. `note` is what the type list starts with in
// docs/spec.md §6, and a page that says nothing is a note.
func pageTypeOf(doc *vault.Document) domain.PageType {
	if declared := doc.PageType(); declared != "" {
		return declared
	}
	return domain.PageTypeNote
}

// titleOf is the page's title, falling back to the last segment of its path.
//
// The fallback is Obsidian's, and it is the only one available: a DM who wrote
// no `title:` has a page called the name of its file, and the column is not
// null. The name is used as it is, with its hyphens, because prettifying it
// here would put a title in the index that is in no file, and M8 would then
// render a heading the DM never wrote.
func titleOf(doc *vault.Document, pagePath string) string {
	if title := strings.TrimSpace(doc.Title()); title != "" {
		return title
	}

	// The last segment, which is the whole path when the page is at the top of
	// the vault. `strings.Cut` would return the *empty* string there, because
	// there is nothing after a separator that is not there.
	name := pagePath
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	return name
}

func skipped(path, reason string) outcome {
	return outcome{path: path, skip: &Skip{Path: path, Reason: reason}}
}

func refused(path, reason string) outcome {
	return outcome{path: path, refusal: &Refusal{Path: path, Reason: reason}}
}
