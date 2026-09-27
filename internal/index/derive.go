package index

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/render"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// maxPasses is how many times one Sync will walk the vault.
//
// A single pass cannot finish the job, and the reason is ordering: a page is
// indexed when the walk reaches it, so a page that links to one the walk has not
// reached yet is left with an unresolved link -- and the walk does not come back
// to it. The second pass finds the target, and settles the page that wanted it.
//
// Two passes settle a vault and the third finds nothing, which is what makes it
// terminate: the second pass's writes only resolve links in the pages it
// rewrote, so there is nothing left for a third to find. The bound is belt and
// braces for a case the "nothing changed, so stop" rule already covers.
const maxPasses = 4

// plan is what one file says the index should hold, and whether the index
// already says it.
//
// The whole design is here: a page is **settled** when re-deriving it from its
// file would produce exactly the row, aliases and links that are already there.
// A sync writes the ones that are not, a check reports them, and because both ask
// the same question through this one function, "would change" and "changed"
// cannot disagree.
//
// It is tempting to settle on the content hash alone -- the schema has one, the
// spec says the indexer uses it to decide whether a file changed, and it is far
// cheaper than parsing. It is also wrong here, in a way that only shows up months
// later, in two separate cases:
//
//   - A row can rot in place. A bug that patches a title, a hand-typed
//     `sqlite3` session, a partially-applied migration: the file's hash is
//     unchanged, so every hash-based check says the page is current.
//   - The *derivation* can change. A milestone that alters how a body or a
//     frontmatter block is read leaves every row derived the old way, with
//     hashes that match their files perfectly. Nothing incremental would ever
//     repair them, so every campaign would need a manual full rebuild after
//     every such change.
//
// Comparing the derived fields costs a parse per page -- and the vault's parse
// is a YAML frontmatter and a string split, not a markdown parse -- so it is
// cheap, and it is the only version that can be trusted.
type plan struct {
	path string

	// page, links and doc are what should be in the index for this file. They are
	// filled in whether or not the page is settled, because a check wants to
	// compare and a sync wants to write, and both need the answer. The parsed
	// document travels with them so that nothing in this package parses a file
	// twice.
	page  domain.Page
	links []domain.PageLink
	doc   *vault.Document

	// owner is who this page belongs to, and ownerProblem is why that does not
	// hold up. The column for the owner arrives in M7; what arrives here is the
	// rule and the validation, which are the parts that are easy to get wrong.
	owner        Owner
	owned        bool
	ownerProblem *OwnershipProblem

	settled bool
	skip    *Skip
	refusal *Refusal
}

// planFor reads one file and works out what the index should hold for it.
//
// Every read of a file in this package goes through here, and it is the only
// place a page's content is derived. The consequence worth having is that
// nothing in this package can write a row that was not built by this function
// from the file, which is what makes "the files are the truth" a property
// rather than an intention.
func (y *Syncer) planFor(ctx context.Context, pagePath string) (plan, error) {
	p := plan{path: pagePath}

	// A path that is not a page is not an error. A DM's vault can contain a
	// file whose name is not a legal page path, and refusing to index the other
	// 240 pages because of one is not what anybody wants.
	if _, pathErr := vault.CheckPagePath(pagePath); pathErr != nil {
		p.skip = &Skip{Path: pagePath, Reason: pathErr.Error()}
		return p, nil //nolint:nilerr // a file that is not a page is a skip, not a failure
	}

	data, err := y.vault.ReadBytes(pagePath)
	switch {
	case err == nil:
	case errors.Is(err, vault.ErrNotFound):
		// The file is gone. There is nothing to derive, and the row for it
		// should be archived; that is the caller's decision, because the caller
		// can see the whole picture.
		return plan{path: pagePath}, nil
	default:
		return p, fmt.Errorf("reading %s: %w", pagePath, err)
	}

	doc, parseErr := vault.Parse(data)
	if parseErr != nil {
		p.skip = &Skip{Path: pagePath, Reason: parseErr.Error()}
		return p, nil //nolint:nilerr // unreadable frontmatter is a skip: the DM fixes it, nobody waits
	}

	// The visibility is read and checked before anything is derived, and then
	// recorded on the row. A page whose audience is unknown must not reach the
	// index at all, because a row that exists is a row a later render may treat
	// as `players`. And a page whose audience *is* known has to carry it, or the
	// read predicate has nothing to filter on and every `dm-only` page is a
	// `players` page: M4 read this key, refused the unreadable values and then
	// threw the readable one away, which is a security field validated and
	// discarded. See ADR 0015.
	visibility, err := doc.Visibility()
	if err != nil {
		p.refusal = &Refusal{Path: pagePath, Reason: err.Error()}
		return p, nil //nolint:nilerr // an unreadable audience is a refusal, not a failure: the file is fine
	}

	frontmatter, err := doc.FrontmatterText()
	if err != nil {
		// Parse would have refused the file already, so this is unreachable
		// rather than impossible. Reporting it as a skip is the honest shape for
		// "cannot index this file", and indexing the page without its block would
		// be a projection that silently lost the DM's own keys.
		p.skip = &Skip{Path: pagePath, Reason: err.Error()}
		return p, nil //nolint:nilerr // unreachable, and a skip is the honest answer if it is reached
	}

	p.page = domain.Page{
		CampaignID:      y.campaign.ID,
		Path:            pagePath,
		Title:           titleOf(doc, pagePath),
		Type:            pageTypeOf(doc),
		Visibility:      visibility,
		Frontmatter:     frontmatter,
		Body:            doc.Body(),
		ContentHash:     vault.Hash(data),
		RendererVersion: render.RendererVersion,
	}
	p.doc = doc
	p.page.ID = y.pageID(ctx, pagePath)

	if owner, owned := OwnerOf(pagePath, doc); owned {
		p.owner, p.owned = owner, true
		p.ownerProblem = y.checkOwner(ctx, pagePath, doc, owner)
	}
	p.links = y.linksFor(ctx, p.page.ID, doc)

	settled, err := y.isSettled(ctx, p)
	if err != nil {
		return p, err
	}
	p.settled = settled

	return p, nil
}

// pageID is the id the page's row has, or an empty string when it has none. A
// plan for a page the index has never seen has no id yet, and the write mints
// one.
func (y *Syncer) pageID(ctx context.Context, pagePath string) string {
	indexed, err := y.store.GetPage(ctx, y.campaign.ID, pagePath)
	if err != nil {
		return ""
	}
	return indexed.ID
}

// linksFor derives a page's outgoing links, resolving each destination against
// the index as it stands.
//
// A target that cannot be a page path cannot resolve to a page, and it is still
// recorded: `[[the toll on the river]]` is a name the DM may one day add as an
// alias, and a graph that held only resolved links could not answer "what
// mentions this" for a page that does not exist yet.
func (y *Syncer) linksFor(ctx context.Context, pageID string, doc *vault.Document) []domain.PageLink {
	refs, err := render.LinksOf(doc.Body())
	if err != nil {
		// LinksOf parses the body with the same pipeline the renderer uses, so
		// this cannot fail for a document that parsed. An empty link set is the
		// safe answer: the page is indexed, and its next sync fills in the graph.
		return nil
	}

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

		if _, err := vault.CheckPagePath(ref.Target); err == nil {
			if target, found, err := y.resolver.ResolvePage(ctx, ref.Target, ref.Heading); err == nil && found {
				link.DstPageID = target.ID
				link.DstPath = target.Path
			}
		}

		links = append(links, link)
	}

	return links
}

// isSettled compares what the file derives with what the index holds: the page
// row, the aliases, and the link graph.
//
// Every part is compared and not just the ones that are cheap, for the reasons
// in the plan comment. The link graph is compared by re-resolving it, which means
// resolving it twice on a page that is about to be written -- once to decide, once
// to write. That is the price of not trusting anything, and it is a few SELECTs.
func (y *Syncer) isSettled(ctx context.Context, p plan) (bool, error) {
	if p.page.ID == "" {
		return false, nil
	}

	indexed, err := y.store.GetPageByID(ctx, p.page.ID)
	if err != nil {
		return false, nil //nolint:nilerr // a failed read means "write it"
	}

	if indexed.IsDeleted {
		return false, nil
	}
	if indexed.ContentHash != p.page.ContentHash ||
		indexed.Title != p.page.Title ||
		indexed.Type != p.page.Type ||
		indexed.Visibility != p.page.Visibility ||
		indexed.Frontmatter != p.page.Frontmatter ||
		indexed.Body != p.page.Body ||
		indexed.RendererVersion != p.page.RendererVersion {
		return false, nil
	}

	// The aliases the index has, against the aliases in the file.
	targets, err := y.store.PageTargets(ctx, p.page.ID)
	if err != nil {
		return false, fmt.Errorf("reading the targets of %s: %w", p.path, err)
	}
	if !equalSets(targets["alias"], p.doc.Aliases()) {
		return false, nil
	}

	// And the links, by re-deriving them: the recorded destinations are resolved
	// paths, so comparing them to what the DM wrote would compare two different
	// things. Comparing to what we would write now is the only comparison that
	// means anything.
	recorded, err := y.store.LinksFrom(ctx, p.page.ID)
	if err != nil {
		return false, fmt.Errorf("reading the links of %s: %w", p.path, err)
	}
	return sameLinks(recorded, p.links), nil
}

// apply writes a plan. It is the only function in this package that writes a
// row, and it is called only with a plan that `isSettled` said was not already
// there.
func (y *Syncer) apply(ctx context.Context, p plan) (outcome, error) {
	stored, err := y.store.UpsertPage(ctx, p.page)
	if err != nil {
		return outcome{}, fmt.Errorf("indexing %s: %w", p.path, err)
	}

	// The page's own name target is the store's business: it derives it from the
	// path in the same transaction as the row.
	if err := y.store.ReplacePageAliases(ctx, stored.ID, p.doc.Aliases()); err != nil {
		return outcome{}, fmt.Errorf("recording the aliases of %s: %w", p.path, err)
	}

	// The links were derived with the id the page had when the plan was made,
	// which is the id the row has now: an upsert on an existing path keeps the
	// original id and mints one only for a page the index has never seen.
	links := p.links
	if stored.ID != p.page.ID {
		links = make([]domain.PageLink, 0, len(p.links))
		for _, link := range p.links {
			link.SrcPageID = stored.ID
			links = append(links, link)
		}
	}

	if err := y.store.ReplaceLinks(ctx, stored.ID, links); err != nil {
		return outcome{}, fmt.Errorf("recording the links of %s: %w", p.path, err)
	}

	return outcome{path: p.path, indexed: true}, nil
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

	if err := y.archiveRow(ctx, indexed); err != nil {
		return outcome{}, err
	}

	return outcome{path: pagePath, archived: true}, nil
}

// archiveRow archives one page's row, and treats "already gone" as done.
//
// The tolerance is for the interleaving two syncs can produce -- a watcher's
// pass and a `wiki sync` in the same campaign, each having listed the pages a
// moment before the other archived one. The second one reporting an error for
// work that is already done is the kind of error a DM learns to ignore, and
// then the next one too.
func (y *Syncer) archiveRow(ctx context.Context, page domain.Page) error {
	if err := y.store.DeletePage(ctx, page.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("archiving %s: %w", page.Path, err)
	}
	return nil
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

		if err := y.archiveRow(ctx, page); err != nil {
			return nil, err
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
// null. The name is used as it is, with its hyphens, because prettifying it here
// would put a title in the index that is in no file, and M8 would then render a
// heading the DM never wrote.
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

// sameLinks compares two link sets as sets.
//
// Not by position, and the reason is worth keeping: links are derived in
// document order and read back ordered by destination, so a positional
// comparison would report a difference on every page whose links were not
// alphabetical, and every sync would rewrite it for ever.
func sameLinks(recorded, derived []domain.PageLink) bool {
	if len(recorded) != len(derived) {
		return false
	}

	byTarget := make(map[string]domain.PageLink, len(recorded))
	for _, link := range recorded {
		byTarget[link.DstPath] = link
	}
	for _, link := range derived {
		other, found := byTarget[link.DstPath]
		if !found || other.DstPageID != link.DstPageID || other.Kind != link.Kind {
			return false
		}
	}

	return true
}

// equalSets compares two string sets, ignoring order and duplicates. The alias
// set is derived from a YAML sequence, whose order the DM wrote, and the index
// sorts what it stores: comparing them as sets is the only comparison that does
// not report a difference the DM did not make.
func equalSets(want []string, got []string) bool {
	unique := map[string]bool{}
	for _, item := range got {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			unique[trimmed] = true
		}
	}

	for _, item := range want {
		if trimmed := strings.TrimSpace(item); trimmed != "" && !unique[trimmed] {
			return false
		}
	}
	return true
}
