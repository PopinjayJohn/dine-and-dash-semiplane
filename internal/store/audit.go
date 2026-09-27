package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// auditColumns is the column list every audit query selects, in the order
// scanAuditEntry expects.
const auditColumns = `id, campaign_id, principal_id, action, page_id, at, detail`

// AppendAudit writes one line to the log and returns it with the id the database
// gave it.
//
// The id is the rowid, so it is assigned here rather than minted, and a zero ID
// on the way in means "the next one". It is the only table in this schema whose id
// comes from SQLite rather than from the injected IDGen, and that is because it
// is the only table nobody refers to: an audit row is read by a person reading a
// story, not by a foreign key.
//
// The ids are reused after a delete, which is a property of INTEGER PRIMARY KEY
// and not a bug today. It becomes one on the day something purges the tail, and
// the note in the migration says so.
func (s *Store) AppendAudit(ctx context.Context, e domain.AuditEntry) (domain.AuditEntry, error) {
	if e.At.IsZero() {
		e.At = s.now()
	}

	if err := e.Validate(); err != nil {
		return domain.AuditEntry{}, err
	}

	// `id` is deliberately absent from the column list. An INTEGER PRIMARY KEY is
	// the rowid, and naming the column without binding it is a statement the
	// driver counts as an argument that is not there.
	const query = `INSERT INTO audit_log (campaign_id, principal_id, action, page_id, at, detail)
		VALUES (?, ?, ?, ?, ?, ?)`

	result, err := s.write.ExecContext(ctx, query,
		nullableString(e.CampaignID), nullableString(e.PrincipalID), e.Action.String(),
		nullableString(e.PageID), e.At.UTC().Format(timeLayout), nullableString(e.Detail))
	if err != nil {
		return domain.AuditEntry{}, writeError("appending the audit entry "+e.Action.String(), err)
	}

	if e.ID == 0 {
		id, idErr := result.LastInsertId()
		if idErr != nil {
			return domain.AuditEntry{}, fmt.Errorf("reading the id of the audit entry: %w", idErr)
		}
		e.ID = id
	}

	return e, nil
}

// ListAudit returns a campaign's log newest first, at most limit entries.
//
// Newest first because the question this log answers is "what happened", asked
// after something happened, and the answer is the last thing that happened. A
// limit is required rather than defaulted, because this table has no upper bound
// and a request that reads all of it is a request that eventually does not come
// back.
//
// A limit of zero or less means the default, for the same reason the search
// limit does: a caller's bug should not become "return nothing", which looks like
// an empty campaign rather than a mistake.
func (s *Store) ListAudit(ctx context.Context, campaignID string, limit int) ([]domain.AuditEntry, error) {
	if limit <= 0 {
		limit = DefaultAuditLimit
	}

	const query = `SELECT ` + auditColumns + ` FROM audit_log
		WHERE campaign_id = ? ORDER BY at DESC, id DESC LIMIT ?`

	rows, err := s.read.QueryContext(ctx, query, campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing the audit log of campaign %s: %w", campaignID, err)
	}
	defer func() { _ = rows.Close() }()

	entries := []domain.AuditEntry{}
	for rows.Next() {
		entry, scanErr := scanAuditEntry(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("listing the audit log of campaign %s: %w", campaignID, scanErr)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the audit log of campaign %s: %w", campaignID, err)
	}
	return entries, nil
}

// DefaultAuditLimit is how many entries ListAudit returns when the caller does not
// say. A hundred is a screen or two of "what happened recently", and it is the
// size of the question a DM asks after noticing something.
const DefaultAuditLimit = 100

// scanAuditEntry reads one row of auditColumns.
func scanAuditEntry(row rowScanner) (domain.AuditEntry, error) {
	// Every column but the action and the timestamp is nullable, because a boot
	// that failed, or a link redeemed before its campaign row existed, is still
	// worth a line. Only the action is a bare string: an entry with no action is
	// not an entry.
	var (
		e                                       domain.AuditEntry
		action, at                              string
		campaignID, principalID, pageID, detail sql.NullString
	)

	if err := row.Scan(&e.ID, &campaignID, &principalID, &action, &pageID, &at, &detail); err != nil {
		return domain.AuditEntry{}, err
	}

	parsed, err := requiredTime("at", at)
	if err != nil {
		return domain.AuditEntry{}, fmt.Errorf("audit entry %d: at: %w", e.ID, err)
	}

	e.CampaignID = campaignID.String
	e.PrincipalID = principalID.String
	e.Action = domain.AuditAction(action)
	e.PageID = pageID.String
	e.At = parsed
	e.Detail = detail.String

	return e, nil
}
