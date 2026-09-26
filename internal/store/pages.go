package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// pageColumns is the column list every page query selects, in the order
// scanPage expects.
const pageColumns = `id, campaign_id, path, title, type, frontmatter, body,
	content_hash, renderer_version, created_at, updated_at, is_deleted`

// UpsertPage stores a page, inserting it or replacing the row already at that
// path, and returns the row as stored.
//
// The conflict target is (campaign_id, path) because the path is the page's
// identity. An id the caller supplies for a path that already exists is
// therefore ignored: revisions and links point at the id, and moving it would
// break both. Renaming a page means changing its path, which is a different
// and much more deliberate operation — one that rewrites inbound links — and
// that is M9's job, not this method's.
//
// IsDeleted is taken from the caller rather than preserved, because the file
// is the source of truth: a page the DM un-deleted on disk comes back.
func (s *Store) UpsertPage(ctx context.Context, p domain.Page) (domain.Page, error) {
	if p.ID == "" {
		p.ID = s.mint()
	}
	now := s.now()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}

	if err := p.Validate(); err != nil {
		return domain.Page{}, err
	}

	const query = `INSERT INTO pages (` + pageColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (campaign_id, path) DO UPDATE SET
			title            = excluded.title,
			type             = excluded.type,
			frontmatter      = excluded.frontmatter,
			body             = excluded.body,
			content_hash     = excluded.content_hash,
			renderer_version = excluded.renderer_version,
			updated_at       = excluded.updated_at,
			is_deleted       = excluded.is_deleted`

	_, err := s.write.ExecContext(ctx, query,
		p.ID, p.CampaignID, p.Path, p.Title, p.Type.String(), p.Frontmatter, p.Body,
		p.ContentHash, p.RendererVersion,
		p.CreatedAt.UTC().Format(timeLayout), p.UpdatedAt.UTC().Format(timeLayout),
		boolArg(p.IsDeleted))
	if err != nil {
		return domain.Page{}, writeError(fmt.Sprintf("storing page %q", p.Path), err)
	}

	// Read the row back rather than returning the value that went in. On a
	// conflict the stored id and created_at are the existing ones, and
	// returning anything else would be a lie with a plausible-looking
	// timestamp on it.
	return s.pageAt(ctx, p.CampaignID, p.Path)
}

// pageAt returns the row at a campaign and path, archived rows included. It
// reads through the write connection so it sees the row the statement above
// just wrote, on the same connection.
func (s *Store) pageAt(ctx context.Context, campaignID, path string) (domain.Page, error) {
	const query = `SELECT ` + pageColumns + ` FROM pages WHERE campaign_id = ? AND path = ?`

	p, err := scanPage(s.write.QueryRowContext(ctx, query, campaignID, path))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Page{}, notFound("page", campaignID+"/"+path)
	}
	if err != nil {
		return domain.Page{}, fmt.Errorf("reading page %s: %w", path, err)
	}
	return p, nil
}

// GetPage returns the live page at a campaign and path. An archived page is
// not found: every read path filters it, so a caller that wants the row behind
// an archive asks the sync engine, which knows the file is still on disk.
func (s *Store) GetPage(ctx context.Context, campaignID, path string) (domain.Page, error) {
	const query = `SELECT ` + pageColumns + ` FROM pages
		WHERE campaign_id = ? AND path = ? AND is_deleted = 0`

	p, err := scanPage(s.read.QueryRowContext(ctx, query, campaignID, path))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Page{}, notFound("page", campaignID+"/"+path)
	}
	if err != nil {
		return domain.Page{}, fmt.Errorf("reading page %s: %w", path, err)
	}
	return p, nil
}

// GetPageByID returns the live page with the given id, which is how a link
// resolves to a page without knowing its path.
func (s *Store) GetPageByID(ctx context.Context, id string) (domain.Page, error) {
	const query = `SELECT ` + pageColumns + ` FROM pages WHERE id = ? AND is_deleted = 0`

	p, err := scanPage(s.read.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Page{}, notFound("page", id)
	}
	if err != nil {
		return domain.Page{}, fmt.Errorf("reading page %s: %w", id, err)
	}
	return p, nil
}

// ListPages returns every live page in a campaign, ordered by path.
//
// The order is part of the contract: a page tree, a search result and a
// reindex report all read from this, and a list whose order varies between
// runs cannot be diffed or asserted on.
func (s *Store) ListPages(ctx context.Context, campaignID string) ([]domain.Page, error) {
	const query = `SELECT ` + pageColumns + ` FROM pages
		WHERE campaign_id = ? AND is_deleted = 0 ORDER BY path`

	rows, err := s.read.QueryContext(ctx, query, campaignID)
	if err != nil {
		return nil, fmt.Errorf("listing pages of campaign %s: %w", campaignID, err)
	}
	defer func() { _ = rows.Close() }()

	pages := []domain.Page{}
	for rows.Next() {
		p, err := scanPage(rows)
		if err != nil {
			return nil, fmt.Errorf("listing pages of campaign %s: %w", campaignID, err)
		}
		pages = append(pages, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing pages of campaign %s: %w", campaignID, err)
	}

	return pages, nil
}

// DeletePage archives a page: the row stays, and every read stops returning it.
//
// The row stays because revisions and inbound links reference it, and because
// an archived page is recoverable. Purging it — the row, its revisions and its
// outgoing links — is a separate operation, and one a DM asks for out loud.
func (s *Store) DeletePage(ctx context.Context, id string) error {
	const query = `UPDATE pages SET is_deleted = 1, updated_at = ? WHERE id = ? AND is_deleted = 0`

	result, err := s.write.ExecContext(ctx, query, s.now().Format(timeLayout), id)
	if err != nil {
		return writeError("archiving page "+id, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("archiving page %s: %w", id, err)
	}
	if affected == 0 {
		return notFound("page", id)
	}
	return nil
}

func scanPage(row rowScanner) (domain.Page, error) {
	var (
		p                              domain.Page
		pageType, createdAt, updatedAt string
		deleted                        int
	)

	err := row.Scan(
		&p.ID, &p.CampaignID, &p.Path, &p.Title, &pageType, &p.Frontmatter, &p.Body,
		&p.ContentHash, &p.RendererVersion, &createdAt, &updatedAt, &deleted)
	if err != nil {
		return domain.Page{}, err
	}

	p.Type = domain.PageType(pageType)
	p.IsDeleted = deleted != 0
	if p.CreatedAt, err = requiredTime("pages.created_at", createdAt); err != nil {
		return domain.Page{}, err
	}
	if p.UpdatedAt, err = requiredTime("pages.updated_at", updatedAt); err != nil {
		return domain.Page{}, err
	}

	return p, nil
}
