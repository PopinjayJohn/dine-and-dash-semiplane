package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// linkColumns is the column list every link query selects, in the order
// scanLink expects.
const linkColumns = `src_page_id, dst_path, dst_page_id, kind`

// ReplaceLinks makes a page's outgoing links be exactly links.
//
// It is a replace rather than an append because the caller is the sync engine,
// which has just parsed a file and knows every link in it. Appending would
// leave links to pages the DM deleted from the text, and a link graph that
// remembers removed links is a graph nobody can trust. Running it with an empty
// slice therefore clears the page's links, which is the correct result for a
// page that no longer mentions anything.
//
// The whole replacement is one transaction: a half-replaced graph has fewer
// links than either the file or the index, and the next reindex would find a
// difference that is not there.
func (s *Store) ReplaceLinks(ctx context.Context, srcPageID string, links []domain.PageLink) error {
	checked := make([]domain.PageLink, 0, len(links))
	for _, l := range links {
		if l.SrcPageID != "" && l.SrcPageID != srcPageID {
			return fmt.Errorf("link to %q claims to come from page %s, not %s", l.DstPath, l.SrcPageID, srcPageID)
		}

		l.SrcPageID = srcPageID
		if l.Kind == "" {
			l.Kind = domain.LinkKindLink
		}
		if err := l.Validate(); err != nil {
			return err
		}
		checked = append(checked, l)
	}

	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM page_links WHERE src_page_id = ?`, srcPageID); err != nil {
			return fmt.Errorf("clearing the links of page %s: %w", srcPageID, err)
		}

		const query = `INSERT INTO page_links (` + linkColumns + `) VALUES (?, ?, ?, ?)`
		for _, l := range checked {
			_, err := tx.ExecContext(ctx, query, srcPageID, l.DstPath, nullableString(l.DstPageID), l.Kind.String())
			if err != nil {
				return writeError(fmt.Sprintf("recording the link to %q on page %s", l.DstPath, srcPageID), err)
			}
		}

		return nil
	})
}

// LinksFrom returns a page's outgoing links, ordered by destination path.
func (s *Store) LinksFrom(ctx context.Context, pageID string) ([]domain.PageLink, error) {
	const query = `SELECT ` + linkColumns + ` FROM page_links WHERE src_page_id = ? ORDER BY dst_path`

	links, err := s.readLinks(ctx, query, "reading the links of page "+pageID, pageID)
	if err != nil {
		return nil, err
	}
	return links, nil
}

// Backlinks returns the links that point at a page, ordered by source page.
// They are ordered by id because the graph stores ids; the page tree and the
// backlink list sort by title for display, which is the view's decision rather
// than the store's.
func (s *Store) Backlinks(ctx context.Context, pageID string) ([]domain.PageLink, error) {
	const query = `SELECT ` + linkColumns + ` FROM page_links WHERE dst_page_id = ? ORDER BY src_page_id, dst_path`

	links, err := s.readLinks(ctx, query, "reading the backlinks of page "+pageID, pageID)
	if err != nil {
		return nil, err
	}
	return links, nil
}

// LinksToPath returns the links that name a path, whether or not that path
// exists. It is how a page the DM has not written yet knows it is mentioned,
// and how the sync engine finds the links to re-resolve when it appears.
func (s *Store) LinksToPath(ctx context.Context, path string) ([]domain.PageLink, error) {
	const query = `SELECT ` + linkColumns + ` FROM page_links WHERE dst_path = ? ORDER BY src_page_id`

	links, err := s.readLinks(ctx, query, "reading the links to "+path, path)
	if err != nil {
		return nil, err
	}
	return links, nil
}

func (s *Store) readLinks(ctx context.Context, query, what string, arg any) ([]domain.PageLink, error) {
	rows, err := s.read.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = rows.Close() }()

	links := []domain.PageLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", what, err)
		}
		links = append(links, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	return links, nil
}

func scanLink(row rowScanner) (domain.PageLink, error) {
	var (
		l    domain.PageLink
		dst  sql.NullString
		kind string
	)

	if err := row.Scan(&l.SrcPageID, &l.DstPath, &dst, &kind); err != nil {
		return domain.PageLink{}, err
	}

	l.DstPageID = dst.String

	parsed, err := domain.ParseLinkKind(kind)
	if err != nil {
		return domain.PageLink{}, err
	}
	l.Kind = parsed

	return l, nil
}
