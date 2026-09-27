package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// The two search indexes, and what it takes to keep them in step with `pages`.
//
// The shape of the whole thing is decided by ADR 0009 and it is worth stating
// here because this is the file that carries it:
//
//   - A page is findable through `pages_fts`, which holds only text that no
//     principal is barred from seeing. That index is safe to read for anybody,
//     and it is fed from `pages.body_public`.
//   - A page's `[!SECRET]` text lives in `pages_secrets_fts` and nowhere else.
//     It is reachable exclusively through the read predicate in acl.go, and a
//     snippet for a hit in it is built from it, so a secret excerpt can only ever
//     be produced for a principal the predicate admitted.
//
// The consequence that shapes every signature below: **the store cannot derive
// the split.** Which text a principal may be shown depends on the page's
// visibility and on who owns it, neither of which the store is allowed to decide,
// so the caller hands over both halves and this file records them. That is why
// there is an IndexEntry rather than a method taking a domain.Page: a store that
// worked out the split itself would be an access-control decision hiding in the
// projection, and the one thing this project cannot bend is that the ACL is
// written once, in SQL, and nowhere else.

// IndexEntry is everything a search needs to know about one page, as the caller
// read it from the file.
//
// The two bodies are the whole point. BodyPublic is what goes into the index
// anybody may search, and SecretText is what goes into the index only the
// privileged path reads; an entry with both empty is a page a search can find by
// its title and nothing else, which is exactly what a page looks like before the
// redaction milestone.
type IndexEntry struct {
	// PageID is the page this entry describes. It is not checked against the
	// page's campaign here: the entry is written against a row the caller has
	// just written, and a foreign key to a page that is not there is a
	// constraint this schema does not carry.
	PageID string

	// Title, Aliases, Tags and Kind are what a public search can match on
	// outside the body. A title is indexed because a DM looking for a page
	// usually knows what it is called; aliases and tags are indexed because both
	// are names the DM would otherwise have typed instead.
	Title   string
	Aliases []string
	Tags    []string
	Kind    string

	// BodyPublic is the body with every secret block replaced by a marker. It is
	// empty today and the redaction milestone fills it; see the migration for why
	// the only safe value before then is the empty string.
	BodyPublic string

	// SecretText is the text of the page's unrevealed `[!SECRET]` callouts,
	// joined. A revealed block is not here: revealed means the DM chose to show
	// it, and it belongs in the public half.
	SecretText string
}

// validate reports whether an entry may be written.
//
// PageID is the only required field, and that looseness is deliberate: an empty
// title, no tags and an empty body is a page a DM has not finished, and refusing
// to index it would mean a page is unsearchable because it is new.
func (e IndexEntry) validate() error {
	if e.PageID == "" {
		return errors.New("store: an index entry needs the page it describes")
	}
	return nil
}

// ReplacePageIndex makes a page's two index rows be exactly this entry, and
// records the public body on the page row.
//
// It is a replace, in one transaction, and only the sync engine calls it, having
// just parsed the file and knowing every field in it. Appending would leave a
// term findable that the DM deleted from their notes, which is how a search index
// starts answering questions about text that is not there.
//
// The public body is written to `pages.body_public` as well as into the index, and
// the two are written together on purpose: the column is the record of what the
// public index was built from, and a row whose column disagrees with it is a row
// nobody can audit. The column is not derived here either — the caller supplies
// the text, exactly as it supplies the index rows.
func (s *Store) ReplacePageIndex(ctx context.Context, entry IndexEntry) error {
	if err := entry.validate(); err != nil {
		return err
	}

	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE pages SET body_public = ? WHERE id = ?`, entry.BodyPublic, entry.PageID); err != nil {
			return writeError("recording the public body of page "+entry.PageID, err)
		}

		if err := s.replaceIndexRow(ctx, tx, publicIndex, entry.publicRow()); err != nil {
			return err
		}
		return s.replaceIndexRow(ctx, tx, secretIndex, entry.secretRow())
	})
}

// PageIndexMatches reports whether both index rows for a page are exactly the
// rows this entry would write.
//
// It is a question rather than a getter, and that is the design. An FTS5 row
// cannot be read back: `page_id` is UNINDEXED, the indexed columns are stored
// as a bag of tokens rather than as the text that was written, and a list is
// joined with a space on the way in. So an entry cannot be reconstructed from
// what came out, and a getter here would hand back something that looks like the
// entry and is not.
//
// Comparing instead answers the question the sync engine actually has, which is
// "would writing this change what a search returns?". Where the two forms differ
// — one tag with a space in it against three tags without — the rows are equal
// and the difference is not one a search could tell: which is the whole point of
// settling on what the index holds rather than on what the frontmatter said.
func (s *Store) PageIndexMatches(ctx context.Context, entry IndexEntry) (bool, error) {
	if err := entry.validate(); err != nil {
		return false, err
	}

	for _, want := range []struct {
		table string
		row   []any
	}{
		{publicIndex, entry.publicRow()},
		{secretIndex, entry.secretRow()},
	} {
		got, found, err := readIndexRow(ctx, s.read, want.table, entry.PageID)
		if err != nil {
			return false, err
		}
		if !found || !equalRow(got, want.row) {
			return false, nil
		}
	}

	return true, nil
}

// DeletePageIndex takes a page out of both indexes.
//
// It is called when a page is archived, and it is separate from PurgePage because
// an archive is recoverable: a page that comes back is re-indexed by the sync
// that put it back, and a stale index row would let a search find a page no read
// can open.
func (s *Store) DeletePageIndex(ctx context.Context, pageID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := dropIndexRow(ctx, tx, publicIndex, pageID); err != nil {
			return err
		}
		return dropIndexRow(ctx, tx, secretIndex, pageID)
	})
}

// publicRow is the entry's row in the public index: the unindexed key, then
// one value per indexed column.
func (e IndexEntry) publicRow() []any {
	return []any{
		e.PageID,
		e.Title,
		strings.Join(e.Aliases, " "),
		e.BodyPublic,
		strings.Join(e.Tags, " "),
		e.Kind,
	}
}

// secretRow is the entry's row in the private index, which has one indexed
// column and nothing else.
func (e IndexEntry) secretRow() []any {
	return []any{e.PageID, e.SecretText}
}

// The two index names.
//
// They are constants because `snippet()` and `bm25()` take the table name as a
// SQL *literal* — FTS5 has no way to ask for the excerpt of "the table named by
// this parameter" — so a query naming one of these has it spliced in. A constant
// declared here is what makes that splice something a reader can check.
const (
	publicIndex = "pages_fts"
	secretIndex = "pages_secrets_fts"
)

// indexColumns is the indexed columns of each index, in the order the rows above
// are written and read back.
var indexColumns = map[string][]string{
	publicIndex: {"title", "aliases", "body", "tags", "kind"},
	secretIndex: {"secret_text"},
}

// replaceIndexRow deletes a page's row in one index and writes the new one.
//
// Delete and insert rather than an UPDATE, because an FTS5 table has no primary
// key to update: `page_id` is UNINDEXED, which means it is stored and not
// searchable, not that it is unique, and an UPDATE there would leave the old row
// in the index alongside the new one.
func (s *Store) replaceIndexRow(ctx context.Context, tx *sql.Tx, table string, row []any) error {
	pageID, _ := row[0].(string)

	if err := dropIndexRow(ctx, tx, table, pageID); err != nil {
		return err
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(row)), ", ")

	// The table name is one of the two constants above and never a caller's
	// string, which is what makes this concatenation safe; the next reader has
	// to be told, because the shape of the line is otherwise a finding. An FTS5
	// table name cannot be a bound parameter either — the table is part of the
	// statement, not a value in it.
	query := `INSERT INTO ` + table + ` VALUES (` + placeholders + `)` //nolint:gosec // table is one of two constants
	if _, err := tx.ExecContext(ctx, query, row...); err != nil {
		return writeError("writing the "+table+" row for page "+pageID, err)
	}
	return nil
}

// dropIndexRow removes a page's row from one index.
func dropIndexRow(ctx context.Context, tx *sql.Tx, table, pageID string) error {
	query := `DELETE FROM ` + table + ` WHERE page_id = ?` //nolint:gosec // table is one of two constants
	if _, err := tx.ExecContext(ctx, query, pageID); err != nil {
		return writeError("clearing the "+table+" row for page "+pageID, err)
	}
	return nil
}

// querier is the one method readIndexRow needs, so the same read works against
// the read pool and against a transaction.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// readIndexRow reads one page's row from one index, and reports whether there
// was one.
//
// The row's own key is read as well as its columns, so that what comes back is
// the whole row `replaceIndexRow` wrote and not a suffix of it — a comparison
// against a suffix is a comparison that silently ignores whichever column is at
// the front.
//
// "No row" and "a row with nothing in it" are different answers, which is why
// this returns a bool rather than an error: a page with no secret is present in
// the private index with a blank in it, and a page the index has never heard of
// is not present at all.
func readIndexRow(ctx context.Context, db querier, table, pageID string) ([]any, bool, error) {
	columns := indexColumns[table]

	scanned := make([]string, len(columns)+1)
	destinations := make([]any, len(scanned))
	for i := range scanned {
		destinations[i] = &scanned[i]
	}

	query := `SELECT page_id, ` + strings.Join(columns, ", ") + ` FROM ` + table + ` WHERE page_id = ?`
	if err := db.QueryRowContext(ctx, query, pageID).Scan(destinations...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reading the %s row for page %s: %w", table, pageID, err)
	}

	row := make([]any, len(scanned))
	for i, value := range scanned {
		row[i] = value
	}
	return row, true, nil
}

// equalRow compares two index rows.
//
// A nil and an empty string are the same value here, because a column added to an
// FTS5 table by a later migration reads as NULL in a row written before it and as
// "" in a row written after, and telling those two apart would make every page
// look unsettled after a migration for ever.
func equalRow(got, want []any) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if textOf(got[i]) != textOf(want[i]) {
			return false
		}
	}
	return true
}

// textOf reads one column value as the string it was written as.
func textOf(value any) string {
	text, _ := value.(string)
	return text
}
