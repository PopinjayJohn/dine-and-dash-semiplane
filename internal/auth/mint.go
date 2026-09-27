package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// Minter issues share links for one campaign.
type Minter struct {
	// Backend is the store.
	Backend Backend

	// Config is how this deployment is set up. See Config.
	Config Config
}

var (
	// ErrNoBaseURL says the minter was built without somewhere to point links. It
	// is a startup error rather than a runtime one, and a named error so that
	// whoever wires this up fails at boot with a message rather than on the DM's
	// first click.
	ErrNoBaseURL = errors.New("auth: no base URL, so a share link could not be built")

	// ErrNoCampaign says a link was asked for without a campaign to scope it to.
	// A share link with no campaign is a credential for the whole data directory,
	// which is not a thing this application issues.
	ErrNoCampaign = errors.New("auth: a share link needs a campaign to belong to")

	// ErrNoLabel says a link was asked for with nothing to call it.
	//
	// The label is the only thing that tells two links apart in the DM's list: the
	// token is unrecoverable and the hint is sixteen bits, so a list of six links
	// with no labels is a list of six sixteen-bit numbers. Refusing to mint one is
	// better than minting a link the DM will not be able to find again.
	ErrNoLabel = errors.New("auth: a share link needs a label, so the DM can find it again")
)

// Issued is a newly minted share link.
//
// The Token is here, and here only. It is shown to the DM once and then it is
// gone: not stored, not logged, not recoverable. Everything downstream — the URL,
// the hint, the hash — is derived from it and is safe to keep.
type Issued struct {
	// Principal is the row as stored, including the hash and the hint.
	Principal domain.Principal

	// Token is the plaintext. It must not be logged, and Token.String() does not
	// print it.
	Token Token

	// URL is the link to hand over. The token is in it, and the token is in
	// exactly this string for exactly as long as the DM holds it.
	URL string
}

// Issue mints one share link.
//
// The label is what the DM calls this person — "Alice (Ranger)" — and it is the
// only thing that tells two links apart in the list. It is stored, never shown to
// anybody else, and never used for authorisation.
func (m Minter) Issue(ctx context.Context, campaign domain.Campaign, role domain.Role, label string) (Issued, error) {
	if m.Config.BaseURL == "" {
		return Issued{}, ErrNoBaseURL
	}
	if campaign.ID == "" {
		return Issued{}, ErrNoCampaign
	}
	if !role.Valid() {
		return Issued{}, fmt.Errorf("auth: %q is not a role, so no share link was issued for it", role)
	}
	if strings.TrimSpace(label) == "" {
		return Issued{}, ErrNoLabel
	}

	token, err := NewToken()
	if err != nil {
		return Issued{}, err
	}

	now := m.Config.now()
	principal := domain.Principal{
		CampaignID: campaign.ID,
		Label:      label,
		Role:       role,
		// The hash, and the hint. The plaintext goes no further than the URL in
		// the return value and the caller that shows it once.
		TokenHash: token.Hash(),
		TokenHint: token.Hint(),
		CreatedAt: now,
	}
	if expiry := m.Config.linkExpiry(); expiry > 0 {
		principal.ExpiresAt = now.Add(expiry)
	}

	stored, err := m.Backend.CreatePrincipal(ctx, principal)
	if err != nil {
		return Issued{}, fmt.Errorf("storing the share link for %q: %w", label, err)
	}

	// The audit row exists before the DM is shown the link, so a link that was
	// issued and is about to be leaked is a link the log knows about. The
	// principal is named and not the token, because the log is the thing somebody
	// pastes into a bug report.
	if _, err := m.Backend.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  stored.CampaignID,
		PrincipalID: stored.ID,
		Action:      domain.AuditPrincipalIssued,
		At:          now,
		Detail:      stored.Label,
	}); err != nil {
		return Issued{}, fmt.Errorf("recording the issue of a share link for %q: %w", label, err)
	}

	return Issued{
		Principal: stored,
		Token:     token,
		URL:       m.linkURL(campaign, token),
	}, nil
}

// linkURL is the capability URL, exactly as ADR 0003 writes it.
//
//	https://host/c/<campaign-slug>/?k=<token>
//
// The campaign's slug, not the principal's id, because the link names the campaign
// a player is being invited to. That is also what makes a link pasted into the
// wrong campaign fail in a way a DM can see: the slug in the path is checked
// against the campaign the token was minted for.
func (m Minter) linkURL(campaign domain.Campaign, token Token) string {
	return fmt.Sprintf("%s/c/%s/?k=%s", m.Config.BaseURL, campaign.Slug, token.Hex())
}
