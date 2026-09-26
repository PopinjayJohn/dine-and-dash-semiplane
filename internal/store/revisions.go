package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// revisionColumns is the column list every revision query selects, in the order
// scanRevision expects.
const revisionColumns = `id, page_id, rev, markdown, content_hash,
	author_principal_id, message, created_at`

// AppendRevision adds a revision to a page's history and returns it as stored.
//
// A revision number the caller left zero is assigned: the next one for that
// page, inside the same transaction as the insert. That is what makes the
// numbering correct when a DM and a player save the same page at the same
// moment — the read and the write happen on the single write connection, so
// they cannot interleave with another writer's.
//
// Numbering starts at 1. A page with no revisions has none rather than a
// revision zero, because a revision zero would be a thing that never happened.
func (s *Store) AppendRevision(ctx context.Context, r domain.PageRevision) (domain.PageRevision, error) {
	if r.ID == "" {
		r.ID = s.mint()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.now()
	}
	if r.Rev == 0 {
		var next int
		const query = `SELECT COALESCE(MAX(rev), 0) + 1 FROM page_revisions WHERE page_id = ?`
		if err := s.write.QueryRowContext(ctx, query, r.PageID).Scan(&next); err != nil {
			return domain.PageRevision{}, fmt.Errorf("finding the next revision number of page %s: %w", r.PageID, err)
		}
		r.Rev = next
	}

	if err := r.Validate(); err != nil {
		return domain.PageRevision{}, err
	}

	const query = `INSERT INTO page_revisions (` + revisionColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.write.ExecContext(ctx, query,
		r.ID, r.PageID, r.Rev, r.Markdown, r.ContentHash,
		nullableString(r.AuthorPrincipalID), nullableString(r.Message),
		r.CreatedAt.UTC().Format(timeLayout))
	if err != nil {
		return domain.PageRevision{}, writeError(fmt.Sprintf("appending revision %d of page %s", r.Rev, r.PageID), err)
	}

	return r, nil
}

// ListRevisions returns a page's revisions oldest first. A page with no
// revisions returns an empty slice, never nil: a caller rendering a history
// panel should not have to tell "no revisions" from "query failed".
func (s *Store) ListRevisions(ctx context.Context, pageID string) ([]domain.PageRevision, error) {
	const query = `SELECT ` + revisionColumns + ` FROM page_revisions WHERE page_id = ? ORDER BY rev`

	rows, err := s.read.QueryContext(ctx, query, pageID)
	if err != nil {
		return nil, fmt.Errorf("listing revisions of page %s: %w", pageID, err)
	}
	defer func() { _ = rows.Close() }()

	revisions := []domain.PageRevision{}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, fmt.Errorf("listing revisions of page %s: %w", pageID, err)
		}
		revisions = append(revisions, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing revisions of page %s: %w", pageID, err)
	}

	return revisions, nil
}

// GetRevision returns one revision of a page.
func (s *Store) GetRevision(ctx context.Context, pageID string, rev int) (domain.PageRevision, error) {
	const query = `SELECT ` + revisionColumns + ` FROM page_revisions WHERE page_id = ? AND rev = ?`

	r, err := scanRevision(s.read.QueryRowContext(ctx, query, pageID, rev))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PageRevision{}, notFound("revision", fmt.Sprintf("%s@%d", pageID, rev))
	}
	if err != nil {
		return domain.PageRevision{}, fmt.Errorf("reading revision %d of page %s: %w", rev, pageID, err)
	}
	return r, nil
}

func scanRevision(row rowScanner) (domain.PageRevision, error) {
	var (
		r         domain.PageRevision
		author    sql.NullString
		message   sql.NullString
		createdAt string
	)

	err := row.Scan(&r.ID, &r.PageID, &r.Rev, &r.Markdown, &r.ContentHash,
		&author, &message, &createdAt)
	if err != nil {
		return domain.PageRevision{}, err
	}

	r.AuthorPrincipalID = author.String
	r.Message = message.String
	if r.CreatedAt, err = requiredTime("page_revisions.created_at", createdAt); err != nil {
		return domain.PageRevision{}, err
	}

	return r, nil
}

// nullableString stores an empty string as NULL, so "no message" and "a message
// that happens to be empty" cannot both exist in the row. An author's id goes
// the other way: a revision with no author is a real fact, and a NULL column
// is how it is recorded.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
