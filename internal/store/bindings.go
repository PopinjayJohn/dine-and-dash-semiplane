package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// ReplacePrincipalCharacters sets a principal's character pages to exactly these.
//
// A replace, because a binding is a statement about what a player owns *now* and
// the alternative is an add-only table in which unbinding is a delete somebody
// has to remember to make. A player who is given a new character and loses the old
// one has to stop reading the old one's pages on their next request, and a table
// that can only grow is a table where that does not happen.
//
// It is one transaction because the ACL reads it while deciding, and a read that
// saw half of a replacement is a read of a principal who owns a character they
// were just told they do not.
//
// The page ids are not checked for ownership-by-path. That is `internal/index`'s
// rule and it is enforced there, at the point a page is written; a binding a DM
// makes through the UI is trusted, and a binding the sync makes from a
// `characters/<slug>/` path is checked by the code that resolved the path.
func (s *Store) ReplacePrincipalCharacters(ctx context.Context, principalID string, pageIDs []string) error {
	normalised := make([]string, 0, len(pageIDs))
	for _, pageID := range pageIDs {
		trimmed := strings.TrimSpace(pageID)
		if trimmed == "" {
			continue
		}
		// De-duplicated here rather than left to the primary key, because a
		// duplicate would otherwise be a constraint violation reported as an error
		// for something the caller did not do wrong — and the caller is usually a
		// sync, which would then fail a whole campaign over one page bound twice.
		if contains(normalised, trimmed) {
			continue
		}
		normalised = append(normalised, trimmed)
	}

	return s.inTx(ctx, func(tx *sql.Tx) error {
		const unbind = `DELETE FROM principal_characters WHERE principal_id = ?`
		if _, err := tx.ExecContext(ctx, unbind, principalID); err != nil {
			return writeError("clearing the character bindings of the principal "+principalID, err)
		}

		const query = `INSERT OR IGNORE INTO principal_characters (principal_id, character_page_id)
			VALUES (?, ?)`
		for _, pageID := range normalised {
			if _, err := tx.ExecContext(ctx, query, principalID, pageID); err != nil {
				return writeError("binding page "+pageID+" to the principal "+principalID, err)
			}
		}
		return nil
	})
}

// PrincipalCharacters returns the page ids a principal owns, in no particular
// order.
//
// No particular order, because there is nothing to order them by that the caller
// asked for: a set of page ids is what the ACL compares against and what the
// active-now view counts.
func (s *Store) PrincipalCharacters(ctx context.Context, principalID string) ([]string, error) {
	const query = `SELECT character_page_id FROM principal_characters
		WHERE principal_id = ? ORDER BY character_page_id`

	rows, err := s.read.QueryContext(ctx, query, principalID)
	if err != nil {
		return nil, fmt.Errorf("listing the character bindings of the principal %s: %w", principalID, err)
	}
	defer func() { _ = rows.Close() }()

	pageIDs := []string{}
	for rows.Next() {
		var pageID string
		if err := rows.Scan(&pageID); err != nil {
			return nil, fmt.Errorf("listing the character bindings of the principal %s: %w", principalID, err)
		}
		pageIDs = append(pageIDs, pageID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the character bindings of the principal %s: %w", principalID, err)
	}
	return pageIDs, nil
}

// CharacterOwners returns the principals who own a page.
//
// The other direction of the same table, and the one a DM asks: "why can Alice
// not see her own character page" is answered by there being no row here, and the
// debugging starts by finding out whether the binding was ever made.
func (s *Store) CharacterOwners(ctx context.Context, pageID string) ([]string, error) {
	const query = `SELECT principal_id FROM principal_characters
		WHERE character_page_id = ? ORDER BY principal_id`

	rows, err := s.read.QueryContext(ctx, query, pageID)
	if err != nil {
		return nil, fmt.Errorf("listing the owners of page %s: %w", pageID, err)
	}
	defer func() { _ = rows.Close() }()

	ownerIDs := []string{}
	for rows.Next() {
		var principalID string
		if err := rows.Scan(&principalID); err != nil {
			return nil, fmt.Errorf("listing the owners of page %s: %w", pageID, err)
		}
		ownerIDs = append(ownerIDs, principalID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the owners of page %s: %w", pageID, err)
	}
	return ownerIDs, nil
}

// OwnerExists reports whether a principal owns a page.
//
// It is a single boolean where the two functions above return a list, because the
// one caller that wants a boolean is a read deciding between one page and another,
// and a predicate that selects a list to compare it to a row does more work than
// the question is worth. The EXISTS below is the shape SQLite wants here, and it
// is the same shape the ACL's own ownership clause uses.
//
// A blank principal id is false rather than an error, because a request that
// failed to identify its caller is exactly the case where the answer has to be
// no: there is no principal, so nobody owns the page, so the `dm-and-owner`
// branch of the predicate is empty. Returning an error here would turn "not
// logged in" into a 500, and the caller that must not see the page is the one
// who would get it.
func (s *Store) OwnerExists(ctx context.Context, principalID, pageID string) (bool, error) {
	if principalID == "" || pageID == "" {
		return false, nil
	}

	const query = `SELECT EXISTS(
		SELECT 1 FROM principal_characters
		WHERE principal_id = ? AND character_page_id = ?)`

	var owned bool
	if err := s.read.QueryRowContext(ctx, query, principalID, pageID).Scan(&owned); err != nil {
		return false, fmt.Errorf("checking whether the principal %s owns page %s: %w", principalID, pageID, err)
	}
	return owned, nil
}
