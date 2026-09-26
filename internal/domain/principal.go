package domain

import "time"

// Principal is one person holding a link to one campaign: the DM, or a
// player. Sharing a link to the wrong person is a real incident, so a
// principal is a row that can be revoked, expires and be looked at, not just
// a signed cookie.
//
// The token is stored as the hash of the plaintext and the plaintext is shown
// exactly once, at issuance; nothing here ever holds it.
type Principal struct {
	// ID is the primary key. A zero ID means "mint one", which the store
	// does; see store.Options.IDGen.
	ID string

	CampaignID string

	// Label is what the DM calls this person in the "new player link" dialog
	// and in the active-now view: "Alice (Ranger)".
	Label string

	Role Role

	// TokenHash is the SHA-256 of the share-link token, and TokenHint is the
	// last four characters, which is enough to tell two links apart in a
	// list and useless to anyone who finds one.
	TokenHash string
	TokenHint string

	CreatedAt time.Time

	// ExpiresAt is when the link stops working. A zero time means it never
	// expires, which is the default because most DMs would rather revoke by
	// hand than remember to set an expiry.
	ExpiresAt time.Time

	// RevokedAt is when the link was revoked. Revocation is a fact about the
	// row rather than a delete, so the audit log can say when it happened.
	RevokedAt time.Time

	// LastUsedAt is when the link was last redeemed.
	LastUsedAt time.Time
}

// Validate reports whether p is a principal that may be persisted.
func (p Principal) Validate() error {
	switch {
	case p.ID == "":
		return required("principal ID")
	case p.CampaignID == "":
		return required("principal campaign ID")
	case p.Label == "":
		return required("principal label")
	case p.TokenHash == "":
		return required("principal token hash")
	}

	if !p.Role.Valid() {
		return oneOf("role", p.Role.String(), roleNames(roles)...)
	}
	return nil
}

// Revoked reports whether the principal's link has been revoked. A zero
// RevokedAt means it has not, so a fresh principal is not revoked.
func (p Principal) Revoked() bool {
	return !p.RevokedAt.IsZero()
}
