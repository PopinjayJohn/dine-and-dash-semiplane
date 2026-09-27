package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// sessionsColumns is the column list every session query selects, in the order
// scanSession expects.
const sessionsColumns = `id, principal_id, created_at, expires_at, user_agent`

// CreateSession stores a redeemed link and returns it as stored.
//
// A blank id is minted and a blank creation time is stamped. The id is the value
// of the cookie, so it is a credential, and it comes from the store's IDGen like
// every other id here — a UUID rather than 32 bytes of crypto/rand, and that is a
// deliberate difference from a share-link token. A session id is looked up in a
// table on every request, so it needs to be unique and unguessable but it never
// travels anywhere a scanner will read it: it goes in a cookie that the browser
// stores and sends back. A share-link token is the opposite case, and gets the
// stronger generator for that reason.
//
// An expiry is required, and the column is NOT NULL, which is where the two
// halves of that meet: `domain.Session.Expired` treats a zero expiry as never
// expiring so that a zero-valued struct is safe to ask, and this method refuses
// to *store* one. A stored session with no expiry is a credential that outlives
// the reason it was issued, and the reason is usually "this browser, on this
// evening, at this table".
func (s *Store) CreateSession(ctx context.Context, sess domain.Session) (domain.Session, error) {
	if sess.ID == "" {
		sess.ID = s.mint()
	}
	if sess.CreatedAt.IsZero() {
		sess.CreatedAt = s.now()
	}
	if sess.ExpiresAt.IsZero() {
		return domain.Session{}, errors.New(
			"store: a session must have an expiry; a link with none is a link that never ends, and that is decided by revoking the principal")
	}

	if err := sess.Validate(); err != nil {
		return domain.Session{}, err
	}

	const query = `INSERT INTO sessions (` + sessionsColumns + `) VALUES (?, ?, ?, ?, ?)`

	_, err := s.write.ExecContext(ctx, query,
		sess.ID, sess.PrincipalID,
		sess.CreatedAt.UTC().Format(timeLayout), sess.ExpiresAt.UTC().Format(timeLayout),
		nullableString(sess.UserAgent))
	if err != nil {
		return domain.Session{}, writeError("creating a session for the principal "+sess.PrincipalID, err)
	}

	return sess, nil
}

// SessionByID is the per-request lookup: a cookie's value, and the session it
// names.
//
// An expired session is returned, and whether it authenticates is Expired's
// question rather than this method's. That keeps one rule about time in one place:
// a caller that wanted "the live session" has to say so, and saying so is what
// makes the difference between a session and a cookie visible in the code.
func (s *Store) SessionByID(ctx context.Context, id string) (domain.Session, bool, error) {
	const query = `SELECT ` + sessionsColumns + ` FROM sessions WHERE id = ?`

	sess, err := scanSession(s.read.QueryRowContext(ctx, query, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.Session{}, false, nil
		}
		return domain.Session{}, false, fmt.Errorf("looking up session %s: %w", id, err)
	}
	return sess, true, nil
}

// SessionsForPrincipal returns a principal's live sessions, newest first, for the
// DM's active-now view.
//
// Expired sessions are skipped rather than returned with a flag, because this list
// is read by a person deciding whether to revoke something, and rows of sessions
// that stopped working an hour ago are noise in that decision. The reaper is what
// removes them from the table.
func (s *Store) SessionsForPrincipal(ctx context.Context, principalID string, now time.Time) ([]domain.Session, error) {
	const query = `SELECT ` + sessionsColumns + `
		FROM sessions WHERE principal_id = ? AND expires_at > ?
		ORDER BY created_at DESC, id`

	rows, err := s.read.QueryContext(ctx, query, principalID, now.UTC().Format(timeLayout))
	if err != nil {
		return nil, fmt.Errorf("listing the sessions of the principal %s: %w", principalID, err)
	}
	defer func() { _ = rows.Close() }()

	sessions := []domain.Session{}
	for rows.Next() {
		sess, scanErr := scanSession(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("listing the sessions of the principal %s: %w", principalID, scanErr)
		}
		sessions = append(sessions, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing the sessions of the principal %s: %w", principalID, err)
	}
	return sessions, nil
}

// DeleteSession ends one session, which is a logout.
//
// A session that is not there is not an error. Logout is the one operation a
// browser may repeat, and a second logout arriving from the back button is not a
// problem to be told about.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	const query = `DELETE FROM sessions WHERE id = ?`

	if _, err := s.write.ExecContext(ctx, query, id); err != nil {
		return writeError("ending session "+id, err)
	}
	return nil
}

// PurgeExpiredSessions deletes the sessions that no longer authenticate and
// returns how many went.
//
// The reaper is housekeeping, not security: an expired session already fails
// Expired, so leaving the row costs a little space and nothing else. It exists
// because the sessions table is the one table in this schema that grows without
// anybody asking, and a table nobody prunes is a table somebody prunes by
// deleting the data directory.
func (s *Store) PurgeExpiredSessions(ctx context.Context, now time.Time) (int, error) {
	const query = `DELETE FROM sessions WHERE expires_at <= ?`

	result, err := s.write.ExecContext(ctx, query, now.UTC().Format(timeLayout))
	if err != nil {
		return 0, writeError("purging the expired sessions", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purging the expired sessions: %w", err)
	}
	return int(affected), nil
}

// scanSession reads one row of sessionsColumns.
func scanSession(row rowScanner) (domain.Session, error) {
	var (
		sess                 domain.Session
		createdAt, expiresAt string
		userAgent            sql.NullString
	)

	if err := row.Scan(&sess.ID, &sess.PrincipalID, &createdAt, &expiresAt, &userAgent); err != nil {
		return domain.Session{}, err
	}

	created, err := requiredTime("created_at", createdAt)
	if err != nil {
		return domain.Session{}, fmt.Errorf("session %s: created_at: %w", sess.ID, err)
	}
	expires, err := requiredTime("expires_at", expiresAt)
	if err != nil {
		return domain.Session{}, fmt.Errorf("session %s: expires_at: %w", sess.ID, err)
	}

	sess.CreatedAt = created
	sess.ExpiresAt = expires
	sess.UserAgent = userAgent.String

	return sess, nil
}
