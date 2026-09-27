package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// Defaults for the two windows, and the reasoning for each.
//
// They are constants rather than flags because they are not the DM's decision:
// a DM who set a session lifetime to a month would not understand what they had
// done, and the value that is right for a wiki played at a table is the value
// nobody has to think about.
const (
	// DefaultSessionLifetime is how long a redeemed link is good for.
	//
	// A fortnight, which is long enough that nobody at a table has been logged
	// out mid-session and short enough that a link pasted somewhere public is
	// stale on its own even if the DM never gets round to revoking it. A player
	// who has not played in a month clicks their link again and it works, which
	// is the whole point of a capability link rather than a password.
	DefaultSessionLifetime = 14 * 24 * time.Hour

	// DefaultLinkExpiry is when a *newly minted* link stops being redeemable.
	//
	// Zero, which means never, and that is deliberate: most DMs would rather
	// revoke by hand than remember to set a date, and a link that quietly expired
	// would arrive as "my player's link stopped working" with no cause. A DM who
	// wants an expiry passes one.
	DefaultLinkExpiry time.Duration = 0
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

// Minter issues share links for one campaign.
type Minter struct {
	// Backend is the store.
	Backend Backend

	// Now is the clock. Injected for the same reason as everything else in this
	// project: golden files and deterministic tests.
	Now func() time.Time

	// BaseURL is the origin links are built against: scheme and host, and
	// nothing else.
	//
	// A caller that got this from an untrusted header could mint links pointing
	// anywhere, and a DM who followed one would hand their player's token to
	// whoever chose the host. It comes from configuration, and M8 is where that
	// is read.
	BaseURL string

	// SessionLifetime and LinkExpiry override the two windows above, for a test
	// and for a DM who asked. Zero means the default, which is what "not set" has
	// to mean.
	//
	// SessionLifetime is read by redemption, not by minting, which is why it lives
	// on a minter: one value on one struct for the campaign, rather than two
	// configurations that could disagree about how long a session lasts.
	SessionLifetime time.Duration
	LinkExpiry      time.Duration
}

var (
	// ErrNoBaseURL says the minter was built without somewhere to point links.
	// It is a startup error rather than a runtime one, and it is a named error so
	// that whoever wires this up fails at boot with a message rather than on the
	// DM's first click.
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

// Issue mints one share link for a player.
//
// The label is what the DM calls this person -- "Alice (Ranger)" -- and it is the
// only thing that tells two links apart in the list, because the token is not
// recoverable and the hint is sixteen bits. It is stored, never displayed to
// anybody else, and never used for authorisation.
func (m Minter) Issue(ctx context.Context, campaign domain.Campaign, role domain.Role, label string) (Issued, error) {
	if m.BaseURL == "" {
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

	now := m.now()
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
	if m.LinkExpiry > 0 {
		principal.ExpiresAt = now.Add(m.LinkExpiry)
	}

	stored, err := m.Backend.CreatePrincipal(ctx, principal)
	if err != nil {
		return Issued{}, fmt.Errorf("storing the share link for %q: %w", label, err)
	}

	// The audit row exists before the DM is shown the link, so that a link which
	// was issued and is about to be leaked is a link the log knows about. The
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
// against the slug the token was minted for.
func (m Minter) linkURL(campaign domain.Campaign, token Token) string {
	return fmt.Sprintf("%s/c/%s/?k=%s", m.BaseURL, campaign.Slug, token.Hex())
}

func (m Minter) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}
