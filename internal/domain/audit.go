package domain

import "time"

// AuditAction names something worth recording. It is a string and not a closed
// enum, because a plugin may have events of its own and the audit log is
// exactly the place they belong; the constants below are the ones core emits.
type AuditAction string

// The actions core records. Anything else in the column came from a plugin.
const (
	// AuditPageSaved is a page saved through the application, by a DM or by
	// a player in their own character subtree.
	AuditPageSaved AuditAction = "page_saved"

	// AuditPageDeleted is a page archived, which hides it without destroying
	// its revisions.
	AuditPageDeleted AuditAction = "page_deleted"

	// AuditPageViewed is a page read by a principal. The spec's named test
	// TestSecretStrippedFromAllSurfaces depends on views being traceable: a
	// leak is both a bad response and a row nobody looked at.
	AuditPageViewed AuditAction = "page_viewed"

	// AuditRevisionRestored is a revision put back in place, which is an
	// ordinary save with an unusual source and deserves its own row.
	AuditRevisionRestored AuditAction = "revision_restored"

	// AuditShareLinkUsed is a share link redeemed for a session cookie. It is
	// the row that answers "was my link pasted somewhere it should not have
	// been", with the time and the principal.
	AuditShareLinkUsed AuditAction = "share_link_used"

	// AuditPrincipalIssued is a new share link created.
	AuditPrincipalIssued AuditAction = "principal_issued"

	// AuditPrincipalRevoked is a share link revoked, which ends every session
	// that link created.
	AuditPrincipalRevoked AuditAction = "principal_revoked"
)

// String returns the action as it appears in the audit log.
func (a AuditAction) String() string {
	return string(a)
}

// AuditEntry is one line of the audit log: who did what, to which page, and
// when.
//
// The log is append-only and is never read by a request path. It exists so
// that the questions a DM asks after an incident — was this link used from
// somewhere unexpected, who changed this page, when — have an answer, which is
// the difference between a host on their own laptop and a service.
//
// Detail is a short human-readable note, not a payload: enough to identify an
// event, never a copy of the content it happened to. A page revision belongs
// in page_revisions, where it is already stored, and duplicating it here would
// double the blast radius of a mistake.
type AuditEntry struct {
	// ID is the autoincrementing row number. It is zero before the insert;
	// the store fills it in and returns the row as stored.
	ID int64

	// CampaignID, PrincipalID and PageID are all optional: a boot that
	// failed, or a link redeemed before its campaign row existed, is still
	// worth a line.
	CampaignID  string
	PrincipalID string
	PageID      string

	Action AuditAction
	At     time.Time
	Detail string
}

// Validate reports whether e is an audit entry that may be persisted.
func (e AuditEntry) Validate() error {
	if e.Action == "" {
		return required("audit action")
	}
	return nil
}
