package edit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/vault"
)

// # The rest of the writer
//
// A save is a page's ordinary life. Three operations are not, and they are here
// rather than in the handler because each of them touches other people's files:

//   - **Restore** puts back a revision, and is a save, so restoring is itself
//     undoable and the text it replaced is kept.
//   - **Rename** moves a page and follows every link that pointed at it. It is
//     DM-only, and it is the only operation in this package that writes a file
//     nobody asked to change.
//   - **Archive** removes a page's file, which leaves its row, its revisions and
//     its inbound links — and is therefore recoverable, where **purge** is not.

// ErrNoSuchRevision says there is no revision with that number.
var ErrNoSuchRevision = errors.New("edit: there is no revision with that number")

// Restore puts a revision's text back, as a save.
//
// It is a save and not a write of its own, and that is the whole design: the save
// checks the ETag, so a restore cannot overwrite a page that changed since the DM
// looked at the history; the save keeps the current text as a revision, so a
// restore can itself be restored; and the save goes through the gate, so a player
// can only restore their own page.
//
// `Expect` is the content hash the caller was looking at, exactly as for a save. A
// history panel that does not send it would restore over whatever is there now.
func (e *Editor) Restore(ctx context.Context, path string, rev int, expect string, as domain.Principal) (domain.Page, error) {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return domain.Page{}, fmt.Errorf("restoring %s: %w", path, err)
	}

	stored, err := e.store.GetPageArchived(ctx, e.campaign.ID, checked, store.AsDM(e.campaign.ID))
	if err != nil {
		return domain.Page{}, fmt.Errorf("restoring %s: %w", checked, err)
	}

	revision, err := e.store.GetRevision(ctx, stored.ID, rev)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return domain.Page{}, fmt.Errorf("restoring %s: %w: %d", checked, ErrNoSuchRevision, rev)
		}
		return domain.Page{}, fmt.Errorf("restoring %s: %w", checked, err)
	}

	page, _, err := e.Save(ctx, Save{
		Path:     checked,
		Markdown: revision.Markdown,
		Expect:   expect,
		Message:  "restored revision " + strconv.Itoa(rev),
		As:       as,
	})
	if err != nil {
		return domain.Page{}, fmt.Errorf("restoring %s to revision %d: %w", checked, rev, err)
	}
	return page, nil
}

// History is a page's revisions, newest first, for a panel that lists them.
//
// A page that has never been edited through this application has no revisions and
// an empty list rather than a nil one, for the same reason the store's list is
// never nil: a caller drawing a history panel should not have to tell "no
// revisions" from "the query failed".
func (e *Editor) History(ctx context.Context, path string) ([]domain.PageRevision, error) {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return nil, fmt.Errorf("the history of %s: %w", path, err)
	}

	// Archived too: a DM's history panel shows a page they archived, and a page
	// they can restore is a page whose history is on the panel.
	stored, err := e.store.GetPageArchived(ctx, e.campaign.ID, checked, store.AsDM(e.campaign.ID))
	if err != nil {
		return nil, fmt.Errorf("the history of %s: %w", checked, err)
	}

	revisions, err := e.store.ListRevisions(ctx, stored.ID)
	if err != nil {
		return nil, fmt.Errorf("the history of %s: %w", checked, err)
	}

	sort.SliceStable(revisions, func(i, j int) bool { return revisions[i].Rev > revisions[j].Rev })
	return revisions, nil
}

// Rename moves a page and follows every link that pointed at it.
//
// **DM-only, and the check is the store's** rather than one here: a rename moves a
// page out of whatever subtree it was in, which can take it out of a player's
// character folder and with it their rights to it, and the only question worth
// asking is "may this principal move pages at all". A player renaming their own
// page is refused, and §5 says the same thing.
//
// The order is the one that makes it recoverable:
//
//  1. the destination must be free, and a real page path;
//  2. the content is written at the new path, *before* the old one goes, so a
//     failure in the middle leaves two pages rather than none;
//  3. the old file is deleted, which makes the old path free;
//  4. the row follows, through the same derivation, as the caller;
//  5. every page that linked to the old path is rewritten — the *file*, not the
//     row, because the file is what the next sync would derive the row from.
//
// A failure after step 3 leaves the new page and a dangling link, and the next
// sync reports the unresolvable link as a drift. A failure before step 2 has
// changed nothing. The window is the whole of the rename and it is not
// transactional, which is recorded rather than hidden: a DM whose rename failed
// half way has a campaign with one page at the new name and some links that
// resolve, and `TestARenameThatFailsLeavesThePageReadable` is the test that says
// which.
// The pages whose links followed it come back with the page, because a DM who has
// just moved one page wants to know which six pages were rewritten, and a caller
// that had to re-derive that list would be re-running the query the rename has
// already run.
func (e *Editor) Rename(ctx context.Context, from, to string, as domain.Principal) (moved domain.Page, followed []string, err error) {
	fromPath, err := vault.CheckPagePath(from)
	if err != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s: %w", from, err)
	}
	toPath, err := vault.CheckPagePath(to)
	if err != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: %w", from, to, err)
	}
	if fromPath == toPath {
		return domain.Page{}, nil, fmt.Errorf("renaming %s: it is already called that", fromPath)
	}

	// A player may not rename, and the gate is the one place that knows why.
	if as.Role != domain.RoleDM {
		stored, getErr := e.store.GetPage(ctx, e.campaign.ID, fromPath, store.AsDM(e.campaign.ID))
		if getErr != nil {
			return domain.Page{}, nil, fmt.Errorf("renaming %s: %w", fromPath, getErr)
		}
		// The ownership check first, so a player renaming somebody else's page is
		// refused for the reason that is true about it rather than the one that is
		// easiest to say.
		if gateErr := e.store.CheckWritable(ctx, stored, as); gateErr != nil {
			return domain.Page{}, nil, fmt.Errorf("renaming %s: %w", fromPath, gateErr)
		}
		return domain.Page{}, nil, fmt.Errorf("renaming %s: %w: only a DM may move a page, because a move takes it out of whatever character folder it was in",
			fromPath, store.ErrNotAllowed)
	}

	source, err := e.vault.ReadBytes(fromPath)
	if err != nil {
		if errors.Is(err, vault.ErrNotFound) {
			return domain.Page{}, nil, fmt.Errorf("renaming %s: %w", fromPath, ErrNoSuchPage)
		}
		return domain.Page{}, nil, fmt.Errorf("renaming %s: %w", fromPath, err)
	}

	if exists, existErr := e.vault.Exists(toPath); existErr != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: %w", fromPath, toPath, existErr)
	} else if exists {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: there is already a page there", fromPath, toPath)
	}

	// The destination content, and the gate on it: a page that moves out of a
	// character folder stops being owned, and a page that moves into one becomes
	// owned. The owner follows the path, so the *new* path is what the gate must
	// see, and this is the step that asks.
	doc, err := vault.Parse(source)
	if err != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s: %w", fromPath, err)
	}
	if gateErr := e.sync.CheckWritableContentAs(ctx, toPath, source, as); gateErr != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: %w", fromPath, toPath, gateErr)
	}

	// The old page's history, which belongs to the page and travels with it. The
	// rows are keyed by page id and the upsert on the new path mints a new one, so
	// the history is copied rather than moved -- and the old row is left, archived,
	// so a restore from the old name still works for a while.
	history, err := e.History(ctx, fromPath)
	if err != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s: %w", fromPath, err)
	}

	// 2. The new file, before the old one goes.
	if writeErr := e.vault.Write(toPath, doc); writeErr != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: %w", fromPath, toPath, writeErr)
	}

	// 3. The old file, so the old path frees itself. The vault prunes the empty
	// directories it leaves, which is the tidiness half of a delete.
	if delErr := e.vault.Delete(fromPath); delErr != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: the new page is written but the old file could not be removed: %w", fromPath, toPath, delErr)
	}

	// 4. The rows. Both paths: the new one is created and the old one archived,
	// because a row whose file is gone is not a page any more and leaving it live
	// would be a page in the index that nobody can read.
	if _, syncErr := e.sync.SyncPathAs(ctx, toPath, as); syncErr != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: the file moved but the index did not follow: %w", fromPath, toPath, syncErr)
	}
	if _, archiveErr := e.sync.SyncPathAs(ctx, fromPath, as); archiveErr != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: the page moved but its old row could not be archived: %w", fromPath, toPath, archiveErr)
	}

	// 5. The links, in the files that point at the old path.
	followers, err := e.followLinks(ctx, fromPath, toPath, as)
	if err != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: the page moved but a link could not be followed: %w", fromPath, toPath, err)
	}

	moved, err = e.store.GetPage(ctx, e.campaign.ID, toPath, store.AsDM(e.campaign.ID))
	if err != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: reading it back: %w", fromPath, toPath, err)
	}

	// The history is copied last, so a failure copying it leaves a page with no
	// history rather than a history attached to nothing.
	if copyErr := e.copyHistory(ctx, fromPath, moved, history); copyErr != nil {
		return domain.Page{}, nil, fmt.Errorf("renaming %s to %s: %w", fromPath, toPath, copyErr)
	}

	return moved, followers, nil
}

// followLinks rewrites every page that links to `from` and returns their paths.
//
// It reads the *link graph* rather than every file, so the cost is a query and not
// a walk of the vault, and then it opens only the pages that actually link here.
// The rewrites are saves, so each of them is checked against the gate, kept as a
// revision, and re-derived — the same three things an ordinary save does.
func (e *Editor) followLinks(ctx context.Context, from, to string, as domain.Principal) ([]string, error) {
	sources, err := e.store.LinksToPath(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("finding the pages that link to %s: %w", from, err)
	}

	var followed []string
	for _, source := range sources {
		stored, getErr := e.store.GetPageByID(ctx, source.SrcPageID, store.AsDM(e.campaign.ID))
		if getErr != nil {
			// A link from a page that is not in the index is a link from a file
			// that is not a page, and there is nothing to follow.
			continue
		}
		if stored.IsDeleted {
			continue
		}

		original, readErr := e.vault.ReadBytes(stored.Path)
		if readErr != nil {
			return nil, fmt.Errorf("reading %s to follow its link: %w", stored.Path, readErr)
		}

		rewritten := rewriteLinks(original, from, to)
		if string(rewritten) == string(original) {
			continue
		}

		if _, _, saveErr := e.Save(ctx, Save{
			Path:     stored.Path,
			Markdown: string(rewritten),
			Expect:   vault.Hash(original),
			Message:  "followed a rename of " + from,
			As:       as,
		}); saveErr != nil {
			return nil, fmt.Errorf("following the link in %s: %w", stored.Path, saveErr)
		}
		followed = append(followed, stored.Path)
	}

	sort.Strings(followed)
	return followed, nil
}

// copyHistory puts a page's revisions onto its new row.
//
// A revision is a *fact about a page's text* and it is copied rather than moved,
// because the old row is archived and not deleted: a DM who renames back finds the
// history still there. The numbers are preserved, so the new page's revision 3 is
// the same text as the old page's revision 3 rather than a renumbering that makes
// two histories look like they are about different things.
func (e *Editor) copyHistory(ctx context.Context, from string, moved domain.Page, history []domain.PageRevision) error {
	// `History` hands back newest-first, which is right for a panel and wrong for a
	// copy: the store assigns the *next* number when the caller leaves it zero, and
	// this copy wants to keep the numbers it has. So the order is reversed here
	// rather than in the store, where numbering is a correctness question and not a
	// display one.
	oldest := make([]domain.PageRevision, len(history))
	for i, revision := range history {
		oldest[len(history)-1-i] = revision
	}

	for _, revision := range oldest {
		if _, err := e.store.AppendRevision(ctx, domain.PageRevision{
			PageID:            moved.ID,
			Rev:               revision.Rev,
			Markdown:          revision.Markdown,
			ContentHash:       revision.ContentHash,
			AuthorPrincipalID: revision.AuthorPrincipalID,
			Message:           revision.Message,
			CreatedAt:         revision.CreatedAt,
		}); err != nil {
			return fmt.Errorf("copying revision %d of %s: %w", revision.Rev, from, err)
		}
	}
	return nil
}

// Archive removes a page's file, which archives its row.
//
// The file goes and the row stays, because the file is the truth (ADR 0001) and a
// row whose file is gone is not a page. Everything that points at the page still
// does — revisions, inbound links, the DM's `_history` — so an archive is
// recoverable, and `Restore` plus a create is how it comes back.
//
// It is not a delete. A DM who wants a page gone for good purges it, and a purge is
// a different call that says so.
func (e *Editor) Archive(ctx context.Context, path string, as domain.Principal) error {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return fmt.Errorf("archiving %s: %w", path, err)
	}

	stored, err := e.store.GetPageArchived(ctx, e.campaign.ID, checked, store.AsDM(e.campaign.ID))
	if err != nil {
		return fmt.Errorf("archiving %s: %w", checked, err)
	}
	if gateErr := e.store.CheckWritable(ctx, stored, as); gateErr != nil {
		return fmt.Errorf("archiving %s: %w", checked, gateErr)
	}
	if stored.IsDeleted {
		// Already archived: the file is gone and the row says so, which is the
		// state the caller asked for. Saying so is the same as doing it, and the
		// alternative is a DM who archived a page twice being told it failed.
		return nil
	}

	if delErr := e.vault.Delete(checked); delErr != nil {
		if errors.Is(delErr, vault.ErrNotFound) {
			// Already gone, and the row is about to be archived, so this is the
			// state the caller asked for. Saying so is the same as doing it.
			return nil
		}
		return fmt.Errorf("archiving %s: %w", checked, delErr)
	}

	// The row, archived. As the caller, so the gate runs again -- and a player who
	// got here has already passed it, because their page is theirs to remove.
	if _, syncErr := e.sync.SyncPathAs(ctx, checked, as); syncErr != nil {
		return fmt.Errorf("archiving %s: the file is gone but the index did not follow: %w", checked, syncErr)
	}
	return nil
}

// Purge forgets a page for good.
//
// It is a delete of the *row* and nothing else, and the asymmetry with `Archive`
// is the whole point: the file is already gone (an archive is what the file's
// absence means), and this throws away the revisions, the inbound links and the
// ability to get it back. It is not idempotent in the way an archive is, and a
// second purge of the same page is `ErrNoSuchPage` rather than a quiet success —
// because a purge that reports success for a page that is not there is a purge a
// DM cannot trust to have happened.
func (e *Editor) Purge(ctx context.Context, path string, as domain.Principal) error {
	checked, err := vault.CheckPagePath(path)
	if err != nil {
		return fmt.Errorf("purging %s: %w", path, err)
	}

	// The archived row, because purging is what a DM does *after* archiving: the
	// two-step is the whole reason a purge can be deliberate.
	stored, err := e.store.GetPageArchived(ctx, e.campaign.ID, checked, store.AsDM(e.campaign.ID))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("purging %s: %w", checked, ErrNoSuchPage)
		}
		return fmt.Errorf("purging %s: %w", checked, err)
	}
	if gateErr := e.store.CheckWritable(ctx, stored, as); gateErr != nil {
		return fmt.Errorf("purging %s: %w", checked, gateErr)
	}

	if err := e.store.PurgePage(ctx, stored.ID); err != nil {
		return fmt.Errorf("purging %s: %w", checked, err)
	}
	return nil
}

// Revisions is the raw list, for a caller that has a page row in hand rather than a
// path. The link graph and the revisions are two of the things a purge throws away,
// and a panel that wants to warn a DM about it needs to count them.
func (e *Editor) Revisions(ctx context.Context, pageID string) ([]domain.PageRevision, error) {
	revisions, err := e.store.ListRevisions(ctx, pageID)
	if err != nil {
		return nil, err
	}
	if revisions == nil {
		return []domain.PageRevision{}, nil
	}
	return revisions, nil
}
