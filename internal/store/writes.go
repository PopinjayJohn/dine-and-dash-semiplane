package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/access"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The write side of the rights matrix.
//
// Invariant 3 is about reads, and a read-only predicate with an open write path is
// a building with a locked front door and an unlocked back one: a player who cannot
// read a page can still write it, and the next sync will index what they wrote and
// the page will appear in somebody else's list.
//
// # Why the store asks rather than the caller
//
// The other shape is the one every web application has: the handler reads the
// page, asks `access.For`, and calls the same `UpsertPage`. It is a shape where
// the check that must not be forgotten is a line of code in a handler — so it is a
// line of code somebody can forget, in a file whose other job is turning a request
// into HTML.
//
// Putting it here means it is not a rule a handler can leave out, and the cost is
// an indexed lookup on a path that already has a transaction open.
//
// # What it does not check, and where that is
//
// It checks *ownership*, not *position*. A page whose `OwnerCharacterPageID` is
// empty is refused to a player, and a page whose owner is a character the player
// is not bound to is refused. What it does not check is that the page's *path*
// lies inside that character, which is the other half of §8's rule and is
// `internal/index`'s rule to apply — the same `OwnerOf` that decides it on a sync.
//
// Duplicating that rule here would be a second implementation of it, and this
// project has been refusing to do that since the predicate was written. The
// residual is worth naming: a caller that supplied its own character as the owner
// of a page somewhere else would be refused by the path check and not by this one.
// No caller can do that today — the only writer is the sync, which reads as the
// DM, and the player-facing one arrives with the editor.

// ErrNotAllowed says a principal may not do this to this page.
//
// It is a named error and deliberately not `ErrNotFound`: a player who tried to
// write somebody else's page should learn that they may not, because they were
// told that they could, and telling them the page does not exist sends them to
// the DM with a different question. The read side answers not-found and the
// asymmetry is deliberate — a read must not confirm that a page exists, and a
// write is a request about a page the caller already holds.
var ErrNotAllowed = errors.New("store: this principal may not do that to this page")

// CheckWritable is the gate, asked on its own.
//
// It is exported because M9's editor has to ask it *before* the file is written,
// and the reason is a hole rather than a convenience: the index watcher writes
// rows as the DM, so a player's file sitting on disk for the length of a refused
// save is a file the watcher indexes, as the DM, into a page the gate would not
// have allowed. The check has to come first and the file must not exist until it
// has passed.
//
// The page it is asked about is the *derived* one -- its owner came from the path
// and the frontmatter -- so a caller cannot assert an owner, and a page under one
// character claiming another is refused here for the same reason the sync reports
// it. `internal/index.CheckWritableAs` is the one caller, and it derives the page
// through the only code that derives pages.
func (s *Store) CheckWritable(ctx context.Context, p domain.Page, as domain.Principal) error {
	return s.checkMayWrite(ctx, p, as)
}

// checkMayWrite is the gate every write goes through.
func (s *Store) checkMayWrite(ctx context.Context, p domain.Page, as domain.Principal) error {
	// Nobody is nobody. A role this build does not know is not a DM and is not a
	// player, and the same error a player's gets is the right one rather than a
	// different shape for a case that means the same thing.
	if !as.Role.Valid() {
		return fmt.Errorf("%w: it needs a role to act with", ErrNotAllowed)
	}

	// A DM writes every page in the campaign, so the matrix has already answered
	// and the check would be two queries to learn nothing. This is the exemption
	// and it is not an exception: the DM's column in the matrix is yes six times.
	if as.Role == domain.RoleDM {
		return nil
	}

	owned, err := s.owns(ctx, as, p)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("%w: %s is not within this principal's character", ErrNotAllowed, p.Path)
	}
	return nil
}

// SetPageVisibility moves a page's audience, which is what "reveal" is: the owner
// moving a page one level less strict, a level change and not a second mechanism
// (§8).
//
// A DM may set any level. A player may only loosen their own page's audience, and
// "loosen" is checked rather than trusted: a method that could *tighten* one is a
// method a player could use to make their own notes vanish from the DM's page
// tree, which is a denial of service against the campaign.
//
// It deliberately does not touch the file. The vault is the source of truth
// (ADR 0001) and a visibility that lives only in the index is one a `wiki reindex
// --full` erases; the editor writes the frontmatter and the sync picks it up, and
// this is the gate that decides whether the write is allowed at all.
func (s *Store) SetPageVisibility(ctx context.Context, campaignID, path string, to domain.Visibility, as domain.Principal) error {
	if !to.Valid() {
		return fmt.Errorf("store: %q is not a visibility", to)
	}

	// The page is read as the DM, because this is a metadata operation the store
	// performs and the gate is the *write* check below. Reading it as the caller
	// instead would make the answer `ErrNotFound` for a principal who cannot read
	// the page, which is the wrong error: a player who asks to reveal a page is
	// asking about a page, and "you may not" is a truer answer than "there is
	// nothing here". The one thing that would leak is the existence of a page to
	// somebody who does not know its path, and the only caller with a path to
	// offer is the DM's own session.
	page, err := s.GetPage(ctx, campaignID, path, AsDM(campaignID))
	if err != nil {
		return err
	}
	if page.Audience() == to {
		return nil
	}

	if as.Role != domain.RoleDM {
		owned, ownsErr := s.owns(ctx, as, page)
		if ownsErr != nil {
			return ownsErr
		}
		if !owned {
			return fmt.Errorf("%w: %s is not this principal's to reveal", ErrNotAllowed, path)
		}
		if !loosens(page.Audience(), to) {
			return fmt.Errorf("%w: revealing makes a page less strict, and %s is already less strict than %s",
				ErrNotAllowed, to, page.Audience())
		}
	}

	const query = `UPDATE pages SET visibility = ?, updated_at = ? WHERE campaign_id = ? AND path = ? AND is_deleted = 0`
	_, err = s.write.ExecContext(ctx, query,
		to.String(), s.now().UTC().Format(timeLayout), campaignID, path)
	if err != nil {
		return writeError("setting the visibility of "+path, err)
	}
	return nil
}

// loosens is whether moving from one audience to another makes a page less
// private, which is the only direction a player may move it.
func loosens(from, to domain.Visibility) bool {
	return strictness(to) < strictness(from)
}

// strictness ranks the three audiences, most private first. A blank visibility is
// `players` and is therefore the least strict, which is the same convention
// everywhere else: a field nobody filled in has not been restricted.
func strictness(v domain.Visibility) int {
	switch v {
	case domain.VisibilityDMOnly:
		return 2
	case domain.VisibilityDMAndOwner:
		return 1
	default:
		return 0
	}
}

// owns is "does this principal own this page", answered the way the predicate
// answers it: a binding against the page's owner, and nothing else.
//
// It is deliberately the *only* write-side ownership question, so that the package
// has two implementations of it rather than three. The predicate is in acl.go and
// this is here.
func (s *Store) owns(ctx context.Context, as domain.Principal, p domain.Page) (bool, error) {
	// A page with no owner is owned by nobody, whatever the principal. A DM-owned
	// page arrives here on every ordinary write, and answering it from the empty
	// column is the whole of the answer.
	if p.OwnerCharacterPageID == "" {
		return false, nil
	}
	if as.ID == "" {
		return false, nil
	}

	owned, err := s.OwnerExists(ctx, as.ID, p.OwnerCharacterPageID)
	if err != nil {
		return false, fmt.Errorf("checking whether %s owns %s: %w", as.ID, p.Path, err)
	}
	return owned, nil
}

// PageMetaOf is the `access.PageMeta` for a page and a principal, which is how a
// caller that has a page in hand gets the resolver's answer without assembling the
// two facts itself.
//
// It is on the store because the ownership question has a store behind it, and
// assembling the meta by hand is how a caller ends up asking "does a character own
// this" instead of "does *this* principal's character own this" — the same
// confusion the two halves of ADR 0007 are about.
func (s *Store) PageMetaOf(ctx context.Context, page domain.Page, as domain.Principal) (access.PageMeta, error) {
	owned, err := s.owns(ctx, as, page)
	if err != nil {
		return access.PageMeta{}, err
	}
	return access.PageMeta{
		Visibility: page.Audience(),
		Owned:      owned,
		Archived:   page.IsDeleted,
	}, nil
}

// MayWrite is the resolver's answer for a caller that has a page in hand, so that
// a handler can ask "may I offer an edit button" without a second query and
// without assembling the two facts itself.
func (s *Store) MayWrite(ctx context.Context, page domain.Page, as domain.Principal) (access.Decision, error) {
	meta, err := s.PageMetaOf(ctx, page, as)
	if err != nil {
		return access.Decision{}, err
	}
	return access.For(access.PrincipalOf(as), meta), nil
}
