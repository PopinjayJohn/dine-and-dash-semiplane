package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// principalsColumns is the column list every principal query selects, in the
// order scanPrincipal expects.
const principalsColumns = `id, campaign_id, label, role, token_hash, token_hint,
	created_at, expires_at, revoked_at, last_used_at`

// CreatePrincipal stores a new share link's principal and returns it as stored.
//
// A blank ID is minted and a blank creation time is stamped, which is the store's
// usual "the caller filled in what it knows" convention. What the caller must
// supply is the token *hash*, never the token: this package has no opinion on how
// a token is generated, and a method taking a plaintext would be a method whose
// every caller has a plaintext in hand.
//
// The token hash is UNIQUE, and that is load-bearing rather than tidy. Two
// principals sharing one hash would be two accounts for one credential, and a
// second one silently replacing the first would be worse: the DM would have two
// links in their list and one of them would do nothing.
func (s *Store) CreatePrincipal(ctx context.Context, p domain.Principal) (domain.Principal, error) {
	if p.ID == "" {
		p.ID = s.mint()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = s.now()
	}

	if err := p.Validate(); err != nil {
		return domain.Principal{}, err
	}

	const query = `INSERT INTO principals (` + principalsColumns + `)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	_, err := s.write.ExecContext(ctx, query,
		p.ID, p.CampaignID, p.Label, p.Role.String(), p.TokenHash, p.TokenHint,
		p.CreatedAt.UTC().Format(timeLayout), nullableTime(p.ExpiresAt),
		nullableTime(p.RevokedAt), nullableTime(p.LastUsedAt))
	if err != nil {
		return domain.Principal{}, writeError("creating the principal "+p.Label, err)
	}

	return p, nil
}

// PrincipalByTokenHash is the redemption lookup: the SHA-256 of a token, and the
// principal it belongs to.
//
// The hash is the only thing this can be asked with, and that is the point: the
// plaintext token is not in the database, so this method cannot be used to
// recover one. A caller holding a token hashes it first, which is also where the
// constant-time comparison lives — see internal/auth.
//
// A revoked principal is returned rather than hidden. Whether a revoked link
// authenticates is a question about revocation and time, and answering it here
// would put a second rule next to the one in internal/auth.
func (s *Store) PrincipalByTokenHash(ctx context.Context, tokenHash string) (domain.Principal, bool, error) {
	const query = `SELECT ` + principalsColumns + ` FROM principals WHERE token_hash = ?`

	p, err := scanPrincipal(s.read.QueryRowContext(ctx, query, tokenHash))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Principal{}, false, nil
		}
		return domain.Principal{}, false, fmt.Errorf("looking a principal up by token hash: %w", err)
	}
	return p, true, nil
}

// PrincipalByID is one principal by its id.
func (s *Store) PrincipalByID(ctx context.Context, id string) (domain.Principal, bool, error) {
	const query = `SELECT ` + principalsColumns + ` FROM principals WHERE id = ?`

	p, err := scanPrincipal(s.read.QueryRowContext(ctx, query, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Principal{}, false, nil
		}
		return domain.Principal{}, false, fmt.Errorf("looking up the principal %s: %w", id, err)
	}
	return p, true, nil
}

// ListPrincipals returns a campaign's principals oldest first.
//
// Oldest first rather than newest, because this list is "the links I have handed
// out" and it is read against the frontmatter of who is playing, where the order
// people were added is the order you want to read them in. The audit log is the
// other way round, because it is read as a story.
func (s *Store) ListPrincipals(ctx context.Context, campaignID string) ([]domain.Principal, error) {
	const query = `SELECT ` + principalsColumns + ` FROM principals WHERE campaign_id = ? ORDER BY created_at, id`

	rows, err := s.read.QueryContext(ctx, query, campaignID)
	if err != nil {
		return nil, fmt.Errorf("listing the principals of campaign %s: %w", campaignID, err)
	}
	defer func() { _ = rows.Close() }()

	principals := []domain.Principal{}
	for rows.Next() {
		p, scanErr := scanPrincipal(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("listing the principals of campaign %s: %w", campaignID, scanErr)
		}
		principals = append(principals, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the principals of campaign %s: %w", campaignID, err)
	}
	return principals, nil
}

// RevokePrincipal stops a share link working, and ends every session it made.
//
// Three answers, and which one you get matters to a DM clicking a button:
//
//   - a principal that does not exist is notFound, because "revoked" would be a lie;
//   - a principal that is already revoked succeeds and does nothing, because
//     revoking twice is not an error and reporting one teaches a DM to ignore the
//     button;
//   - a live principal is flagged and its sessions are deleted, in one
//     transaction.
//
// The transaction is the feature. A player whose link was pasted into a Discord
// channel has to be logged out *now*, and a revoke that set the flag and then
// failed to delete the sessions would leave a browser working with a link the DM
// believes is dead. Either both happened or neither did.
//
// The row is flagged rather than deleted, because the audit log's question is "was
// this link ever used" and a deleted principal cannot answer it. The label and
// the hint stay for the same reason: the DM's list should still say "Alice
// (Ranger)" beside the link they revoked.
//
// The sessions are deleted even for an already-revoked principal, which repairs
// the state a failed revoke would have left rather than treating it as done.
func (s *Store) RevokePrincipal(ctx context.Context, id string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		const read = `SELECT revoked_at FROM principals WHERE id = ?`

		var revokedAt sql.NullString
		switch err := tx.QueryRowContext(ctx, read, id).Scan(&revokedAt); {
		case errors.Is(err, sql.ErrNoRows):
			return notFound("principal", id)
		case err != nil:
			return fmt.Errorf("looking up the principal %s to revoke it: %w", id, err)
		}

		if !revokedAt.Valid {
			const revoke = `UPDATE principals SET revoked_at = ? WHERE id = ?`
			if _, err := tx.ExecContext(ctx, revoke, s.now().UTC().Format(timeLayout), id); err != nil {
				return writeError("revoking the principal "+id, err)
			}
		}

		const endSessions = `DELETE FROM sessions WHERE principal_id = ?`
		if _, err := tx.ExecContext(ctx, endSessions, id); err != nil {
			return writeError("ending the sessions of the principal "+id, err)
		}
		return nil
	})
}

// RevokeAllPrincipals ends every share link in a campaign, which is what a DM
// means by "everyone sign in again". It returns how many links were revoked.
//
// It is not one transaction. Every principal is being revoked, so there is no
// consistent state to protect: a failure halfway leaves some revoked and some
// not, which is a state the DM asked for either way and which they can ask for
// again. The sessions are cleared afterwards rather than with each revocation,
// because a principal that was *already* revoked should have had its sessions
// gone, and a leftover session that still works is exactly the bug the per-link
// revoke is careful about.
func (s *Store) RevokeAllPrincipals(ctx context.Context, campaignID string) (int, error) {
	const query = `UPDATE principals SET revoked_at = ? WHERE campaign_id = ? AND revoked_at IS NULL`

	result, err := s.write.ExecContext(ctx, query, s.now().UTC().Format(timeLayout), campaignID)
	if err != nil {
		return 0, writeError("revoking every principal of campaign "+campaignID, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("revoking every principal of campaign %s: %w", campaignID, err)
	}

	const endAll = `DELETE FROM sessions WHERE principal_id IN
		(SELECT id FROM principals WHERE campaign_id = ?)`
	if _, err := s.write.ExecContext(ctx, endAll, campaignID); err != nil {
		return int(affected), writeError("ending every session of campaign "+campaignID, err)
	}

	return int(affected), nil
}

// TouchPrincipal stamps last_used_at, which is how the active-now view tells a
// link that is in use from one that was merely issued.
func (s *Store) TouchPrincipal(ctx context.Context, id string) error {
	const query = `UPDATE principals SET last_used_at = ? WHERE id = ?`

	result, err := s.write.ExecContext(ctx, query, s.now().UTC().Format(timeLayout), id)
	if err != nil {
		return writeError("recording the last use of the principal "+id, err)
	}

	affected, err := result.RowsAffected()
	switch {
	case err != nil:
		return fmt.Errorf("recording the last use of the principal %s: %w", id, err)
	case affected == 0:
		return notFound("principal", id)
	}
	return nil
}

// SetPrincipalRole changes a principal's role.
//
// The sessions are deliberately not touched. A role change is one of the two
// changes that makes every existing session of that principal wrong, and the
// rotation belongs to whoever asked for the change, in the same breath — see
// internal/auth. A store method that quietly logged a DM out on a role change
// would be a revocation nobody asked for, and it would be the *player* side that
// noticed.
func (s *Store) SetPrincipalRole(ctx context.Context, id string, role domain.Role) error {
	if !role.Valid() {
		return fmt.Errorf("store: %q is not a role", role)
	}

	const query = `UPDATE principals SET role = ? WHERE id = ?`

	result, err := s.write.ExecContext(ctx, query, role.String(), id)
	if err != nil {
		return writeError("setting the role of the principal "+id, err)
	}

	affected, err := result.RowsAffected()
	switch {
	case err != nil:
		return fmt.Errorf("setting the role of the principal %s: %w", id, err)
	case affected == 0:
		return notFound("principal", id)
	}
	return nil
}

// scanPrincipal reads one row of principalsColumns.
func scanPrincipal(row rowScanner) (domain.Principal, error) {
	var (
		p                                domain.Principal
		role, createdAt                  string
		expiresAt, revokedAt, lastUsedAt sql.NullString
	)

	if err := row.Scan(&p.ID, &p.CampaignID, &p.Label, &role, &p.TokenHash, &p.TokenHint,
		&createdAt, &expiresAt, &revokedAt, &lastUsedAt); err != nil {
		return domain.Principal{}, err
	}

	created, err := requiredTime("created_at", createdAt)
	if err != nil {
		return domain.Principal{}, fmt.Errorf("principal %s: created_at: %w", p.ID, err)
	}

	p.CreatedAt = created
	p.Role = domain.Role(role)

	for _, column := range []struct {
		name  string
		value sql.NullString
		into  *time.Time
	}{
		{"expires_at", expiresAt, &p.ExpiresAt},
		{"revoked_at", revokedAt, &p.RevokedAt},
		{"last_used_at", lastUsedAt, &p.LastUsedAt},
	} {
		if !column.value.Valid {
			continue
		}
		parsed, parseErr := requiredTime(column.name, column.value.String)
		if parseErr != nil {
			return domain.Principal{}, fmt.Errorf("principal %s: %s: %w", p.ID, column.name, parseErr)
		}
		*column.into = parsed
	}

	return p, nil
}

// nullableTime stores a zero time as NULL.
//
// A zero time means "this never happened" for RevokedAt and LastUsedAt, and "this
// never" for a share link's ExpiresAt — the default, because most DMs would
// rather revoke by hand than remember to set a date. Writing a zero as
// 0001-01-01 instead would make "no expiry" and "expired at the beginning of
// time" one column value, and only one of those is a link a DM can use.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(timeLayout)
}
