package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The two lookups below are Obsidian's fallback positions for a wiki link,
// after the exact path: an alias, then a case-insensitive filename. They exist
// because that is the order a DM's links resolve in, and a link that resolves in
// Obsidian and not here is a broken link in a wiki that is otherwise
// Obsidian-openable.
//
// Neither reads `frontmatter`. The aliases are in there and they are YAML, and
// SQL cannot read YAML: the projection carries them in `page_targets` instead,
// and the sync engine fills that in from the files. The rule that follows from
// it is worth stating once, because it is the kind of thing that gets
// reimplemented by accident in three places and drifts:
//
//   - the `pages` primary key is the exact path;
//   - `page_targets` rows of kind `alias` hold the alias as written and are
//     matched exactly;
//   - `page_targets` rows of kind `name` hold the lower-cased file stem and are
//     matched case-insensitively.

// Target kinds, as the `page_targets.kind` column spells them.
const (
	targetAlias = "alias"
	targetName  = "name"
)

// ReplacePageAliases makes a page answer to exactly the aliases given, and
// returns the previous set so a caller can see what changed.
//
// It is a replace rather than an append for the same reason `ReplaceLinks` is:
// the caller is the sync engine, which has just read the file and knows every
// alias in it. An append would leave the aliases of a name the DM removed from
// their frontmatter reachable for ever, which is how a link outlives the thing it
// pointed at.
//
// It does not touch the `name` target, because a page's own name is a property
// of its path and `UpsertPage` writes that one itself. A caller that supplied it
// could get it wrong, and a page that exists but answers to no name is a page a
// `[[rivergate]]` link cannot reach.
func (s *Store) ReplacePageAliases(ctx context.Context, pageID string, aliases []string) error {
	normalised := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" {
			continue
		}
		if seen := contains(normalised, trimmed); seen {
			continue
		}
		normalised = append(normalised, trimmed)
	}

	return s.inTx(ctx, func(tx *sql.Tx) error {
		// Scoped to the alias kind. An unscoped delete would take the page's own
		// name target with it, and the page would stop answering to the name of
		// its own file -- which is exactly what a caller replacing one alias in
		// a frontmatter block would not expect to happen.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM page_targets WHERE page_id = ? AND kind = ?`, pageID, targetAlias); err != nil {
			return fmt.Errorf("clearing the aliases of page %s: %w", pageID, err)
		}

		const query = `INSERT OR IGNORE INTO page_targets (campaign_id, kind, target, page_id)
			SELECT campaign_id, ?, ?, id FROM pages WHERE id = ?`

		for _, alias := range normalised {
			if _, err := tx.ExecContext(ctx, query, targetAlias, alias, pageID); err != nil {
				return fmt.Errorf("recording the alias %q for page %s: %w", alias, pageID, err)
			}
		}

		return nil
	})
}

// PageTargets returns the targets a page answers to: its aliases as written and
// the lower-cased stem of its file name. It is what a caller reads to find out
// what a page is reachable as, and what a test reads to check that the index
// agrees with the file.
func (s *Store) PageTargets(ctx context.Context, pageID string) (map[string][]string, error) {
	const query = `SELECT kind, target FROM page_targets WHERE page_id = ? ORDER BY kind, target`

	rows, err := s.read.QueryContext(ctx, query, pageID)
	if err != nil {
		return nil, fmt.Errorf("reading the targets of page %s: %w", pageID, err)
	}
	defer func() { _ = rows.Close() }()

	targets := map[string][]string{targetAlias: {}, targetName: {}}
	for rows.Next() {
		var kind, target string
		if err := rows.Scan(&kind, &target); err != nil {
			return nil, fmt.Errorf("reading the targets of page %s: %w", pageID, err)
		}
		targets[kind] = append(targets[kind], target)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading the targets of page %s: %w", pageID, err)
	}

	return targets, nil
}

// FindPageByAlias returns the page a `[[alias]]` names, and false when no page
// in the campaign declares that alias.
//
// The match is exact and case-sensitive, because the alternative makes two
// pages differing only in case into one reachable page. The ambiguity that does
// exist -- two pages declaring the same alias -- is real and lives in the
// vault; the tie is broken by path, so the same link always resolves to the
// same page.
func (s *Store) FindPageByAlias(ctx context.Context, campaignID, alias string, as domain.Principal) (domain.Page, bool, error) {
	if strings.TrimSpace(alias) == "" {
		return domain.Page{}, false, nil
	}

	sc := readable(campaignID, as)
	query := targetLookupQuery(sc)

	page, found, err := scanPageOptional(s.read.QueryRowContext(ctx, query,
		sc.argsAfter(campaignID, targetAlias, strings.TrimSpace(alias))...))
	if err != nil {
		return domain.Page{}, false, fmt.Errorf("looking up the alias %q in campaign %s: %w", alias, campaignID, err)
	}
	return page, found, nil
}

// FindPageByName returns the page whose file name matches, ignoring case and
// ignoring the directory the file is in. It is the last fallback a wiki link
// tries, and it is a wide one: two pages called `notes.md` in different folders,
// and the one that answers is the one that sorts first by path. That ambiguity
// is Obsidian's ambiguity — a vault with two `notes.md` resolves the same way
// there — and ordering by path is here so the answer is at least the *same*
// answer every time.
func (s *Store) FindPageByName(ctx context.Context, campaignID, name string, as domain.Principal) (domain.Page, bool, error) {
	stem := nameStem(name)
	if stem == "" {
		return domain.Page{}, false, nil
	}

	// The folding happens here, in Go, because strings.ToLower gets non-ASCII
	// letters right and SQLite's LOWER() only folds ASCII -- so `Ölbach` would
	// be indexed one way and searched for another.
	sc := readable(campaignID, as)
	query := targetLookupQuery(sc)

	page, found, err := scanPageOptional(s.read.QueryRowContext(ctx, query,
		sc.argsAfter(campaignID, targetName, strings.ToLower(stem))...))
	if err != nil {
		return domain.Page{}, false, fmt.Errorf("looking up the file name %q in campaign %s: %w", name, campaignID, err)
	}
	return page, found, nil
}

// nameStem is a page path's or file name's last segment without its extension,
// which is what a `[[rivergate]]` link is matched against.
func nameStem(name string) string {
	stem := strings.TrimSpace(name)
	if index := strings.LastIndex(stem, "/"); index >= 0 {
		stem = stem[index+1:]
	}
	return strings.TrimSuffix(stem, pageExtension)
}

// pageExtension is what a page's file is called. It is written out here rather
// than imported from internal/vault: this is a store, and a store that knew how
// to name files would be the coupling M4 exists to keep in the sync engine.
const pageExtension = ".md"

// scanPageOptional reads a page that may not be there, which is a different
// answer from a read that failed.
func scanPageOptional(row *sql.Row) (domain.Page, bool, error) {
	page, err := scanPage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Page{}, false, nil
	}
	if err != nil {
		return domain.Page{}, false, err
	}
	return page, true, nil
}

func contains(haystack []string, needle string) bool {
	for _, candidate := range haystack {
		if candidate == needle {
			return true
		}
	}
	return false
}

// targetLookupQuery is the one query both fallbacks use: a page that answers to
// a target, in a campaign, with the tie broken by path so the same link always
// resolves to the same page.
//
// It is a function and not a constant because the column list is computed, and
// the two lookups differ only in the kind and the target they pass.
//
// The pages table is aliased `p` because the read predicate is written against
// that alias, and the alias is not cosmetic: it is what stops this query from
// being the one page query in the package that filters nothing.
func targetLookupQuery(sc scope) string {
	return `SELECT ` + pageColumnsQualified() + `
		FROM pages p JOIN page_targets ON page_targets.page_id = p.id
		WHERE page_targets.campaign_id = ? AND page_targets.kind = ? AND page_targets.target = ?
		  AND (` + sc.where + `)
		ORDER BY p.path
		LIMIT 1`
}

// replaceNameTarget makes a page answer to one name target, replacing whatever
// it answered to before. It is called from UpsertPage, inside that write's
// transaction, and takes the transaction rather than opening one of its own.
func (s *Store) replaceNameTarget(ctx context.Context, tx *sql.Tx, campaignID, pageID, stem string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM page_targets WHERE page_id = ? AND kind = ?`, pageID, targetName); err != nil {
		return fmt.Errorf("clearing the name target of page %s: %w", pageID, err)
	}

	if stem == "" {
		return nil
	}

	const query = `INSERT INTO page_targets (campaign_id, kind, target, page_id) VALUES (?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, query, campaignID, targetName, strings.ToLower(stem), pageID); err != nil {
		return fmt.Errorf("recording the name %q for page %s: %w", stem, pageID, err)
	}

	return nil
}

// pageColumnsQualified is `pageColumns` with every column named by its table.
//
// The lookups join `pages` to `page_targets`, and both have `id` and
// `campaign_id`, so an unqualified column list is an error SQLite reports at run
// time: "ambiguous column name". Qualifying is only needed where the two are
// joined, which is why the plain column list still exists.
// pageColumnsQualified is the page column list, qualified.
//
// The alias is `p` and not a parameter, because the read predicate is written
// against `p` and a query that joined pages under some other name would be the
// one page query in this package that could not be filtered. That is the whole
// reason this function stopped taking a table name: the constraint that the
// alias is `p` is a property of every statement in the package, and a parameter
// would let the next caller quietly break it.
func pageColumnsQualified() string {
	columns := strings.Split(pageColumns, ",")
	for i, column := range columns {
		columns[i] = "p." + strings.TrimSpace(column)
	}
	return strings.Join(columns, ", ")
}
