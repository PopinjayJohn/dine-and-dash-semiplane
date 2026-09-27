package auth

import (
	"context"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The two changes that make every existing session of a principal wrong, and the
// rotation both of them get.
//
// ADR 0003 lists them: "session rotation whenever role or character binding
// changes". Both are rare and both are the DM pressing a button, which is why they
// share a function rather than getting one each — and why it is here rather than in
// a handler: the two changes and the rotation are one decision, and a handler that
// did the first and forgot the second would leave a browser holding a session whose
// authority no longer matches what the DM believes they granted.
//
// What rotation means here is that the old cookies stop working and the player
// clicks their link again. It is not a silent re-issue: a player who has been
// demoted from DM to player should find out, and the way they find out is by being
// asked for their link rather than by being quietly corrected.

// RotationReason is why a principal's sessions were ended, and it is an audit
// action rather than a field on the rotation: the two rotations are different
// events and "the DM changed Alice's character" is not "the DM changed Bob's role".
type RotationReason string

const (
	// RotatedForRoleChange is a principal whose role changed.
	RotatedForRoleChange RotationReason = "role"

	// RotatedForBindingChange is a principal whose character bindings changed.
	RotatedForBindingChange RotationReason = "binding"
)

// SetRole changes a principal's role and ends every session it had.
//
// The order is the whole point: the role is written first and the sessions second,
// so a failure between them leaves a principal with the *new* role and stale
// cookies, which the predicate resolves in the safe direction — a demoted DM's
// stale cookie is a player, not a DM. The other order would leave a stale cookie
// still holding DM.
func (r Redeemer) SetRole(ctx context.Context, principal domain.Principal, role domain.Role) error {
	if !role.Valid() {
		return fmt.Errorf("auth: %q is not a role, so it was not set", role)
	}
	if principal.Role == role {
		// Nothing changed, so nothing is rotated. A DM who opens a page and saves it
		// without touching the role should not log their players out.
		return nil
	}

	if err := r.Backend.SetPrincipalRole(ctx, principal.ID, role); err != nil {
		return fmt.Errorf("setting the role of %q: %w", principal.Label, err)
	}
	if _, err := r.Rotate(ctx, principal, RotatedForRoleChange); err != nil {
		return err
	}

	_, err := r.Backend.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  principal.CampaignID,
		PrincipalID: principal.ID,
		Action:      domain.AuditRoleChanged,
		At:          r.Config.now(),
		Detail:      string(RotatedForRoleChange),
	})
	if err != nil {
		return fmt.Errorf("recording a role change for %q: %w", principal.Label, err)
	}
	return nil
}

// SetCharacters replaces a principal's character bindings and ends every session
// they had.
//
// Rotation on a binding change is not obvious, and it is the more important of the
// two. A session is a row, and a row is not told that what it may read has changed;
// so a player who was just unbound from a character page would keep reading it for
// as long as their cookie lived, which is a fortnight. Unbinding somebody and
// having it take effect on the next request is the whole point of sessions being
// rows rather than signed blobs — and it only works if the unbinding says so.
func (r Redeemer) SetCharacters(ctx context.Context, principal domain.Principal, pageIDs []string) error {
	if err := r.Backend.ReplacePrincipalCharacters(ctx, principal.ID, pageIDs); err != nil {
		return fmt.Errorf("binding characters to %q: %w", principal.Label, err)
	}
	if _, err := r.Rotate(ctx, principal, RotatedForBindingChange); err != nil {
		return err
	}

	// The detail is the count and not the page ids: a log that listed a player's
	// characters is a log that says who plays what, and this is the log somebody
	// pastes into a bug report.
	_, err := r.Backend.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  principal.CampaignID,
		PrincipalID: principal.ID,
		Action:      domain.AuditBindingChanged,
		At:          r.Config.now(),
		Detail:      fmt.Sprintf("%d characters", len(pageIDs)),
	})
	if err != nil {
		return fmt.Errorf("recording a binding change for %q: %w", principal.Label, err)
	}
	return nil
}

// Rotate ends every session of a principal and records that it happened.
//
// It takes the whole principal rather than an id, because the audit row has to name
// the campaign as well as the principal: the log is read one campaign at a time, so
// a row with no campaign on it is in no campaign's log, and a rotation that is in
// no log is a rotation nobody can find.
//
// The row is written whether or not any session existed, because "nobody was logged
// in" and "we did not check" are different answers and this is the place that tells
// them apart. The count is not in the detail either: a rotation is not a statement
// about how many browsers were open, and a DM reading the log later does not need
// to know.
func (r Redeemer) Rotate(ctx context.Context, principal domain.Principal, reason RotationReason) (int, error) {
	ended, err := r.Backend.EndSessions(ctx, principal.ID)
	if err != nil {
		return 0, fmt.Errorf("ending the sessions of %s: %w", principal.ID, err)
	}

	if _, err := r.Backend.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  principal.CampaignID,
		PrincipalID: principal.ID,
		Action:      domain.AuditSessionRotated,
		At:          r.Config.now(),
		Detail:      string(reason),
	}); err != nil {
		return ended, fmt.Errorf("recording a session rotation for %s: %w", principal.ID, err)
	}
	return ended, nil
}
