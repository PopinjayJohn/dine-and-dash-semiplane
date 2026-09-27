package auth

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// Redeemer turns a token into a session, and a session into an identity.
type Redeemer struct {
	// Backend is the store.
	Backend Backend

	// Config is how this deployment is set up. See Config.
	Config Config
}

var (
	// ErrNoToken says a redemption arrived without a token, which is what a
	// request to `/c/<slug>/` without one is.
	ErrNoToken = errors.New("auth: no share-link token in the request")

	// ErrBadToken says the token is not shaped like a token, or is not one of
	// ours. One error for both, on purpose: a caller who could tell "that is not
	// a token" from "that is a token" from "that is not a token for here" can use
	// the difference to learn which campaigns a DM has links for.
	ErrBadToken = errors.New("auth: that share link is not valid for this campaign")

	// ErrRevoked says the link was revoked. Separate from ErrBadToken because
	// "this link was revoked" is something a *player* needs to be told: it is not a
	// secret, it is the answer to why their browser stopped working, and a message
	// that says only "not valid" sends them to the DM to ask.
	ErrRevoked = errors.New("auth: that share link has been revoked")

	// ErrLinkExpired says the link has an expiry and it has passed. Also separate,
	// and for the same reason: a player whose link expired needs to know that,
	// because the fix is a new link and not a revoked account.
	ErrLinkExpired = errors.New("auth: that share link has expired")

	// ErrWrongCampaign says the slug in the path is not the campaign the link was
	// minted for. It is a distinct error because it is a *caller* mistake — the DM
	// pasted a link into the wrong place — and it is worth saying so.
	ErrWrongCampaign = errors.New("auth: that share link belongs to a different campaign")

	// ErrNoSession says a cookie named a session that is not there, or is no
	// longer live. Revocation is a row delete, so this is the answer a revoked
	// player's browser gets on its next request.
	ErrNoSession = errors.New("auth: that session has ended")
)

// Redeemed is the result of exchanging a token for a session.
//
// Every field is something a router has to put in a response, and none of them is
// a decision. That is deliberate: this package decides *whether*, and returns
// *what*, and M8's router does the rest. The alternative is a decision and its
// consequences in the same function, where the consequence is a header nobody
// reads twice.
type Redeemed struct {
	// Principal is who the token belongs to, with the role and campaign resolved.
	Principal domain.Principal

	// SessionID is the cookie's value.
	SessionID string

	// ExpiresAt is when the session stops authenticating. A router that sets a
	// cookie without an expiry gets a session cookie, and a session cookie dies
	// when the browser closes; a player who reopened their laptop on Sunday to
	// re-read the session notes would be logged out.
	ExpiresAt time.Time

	// RedirectTo is where the browser goes next, and it does not contain the token.
	// That is the whole point of this package.
	RedirectTo string
}

// Redeem exchanges a token for a session.
//
// The five steps are ADR 0003's, in order, and each is a place a naive
// implementation gets one of them wrong:
//
//  1. hash the token and look the *hash* up;
//  2. refuse if it is not ours, not for this campaign, revoked, or expired;
//  3. create the session and stamp the principal's last use;
//  4. hand back a redirect that does not contain the token;
//  5. record the redemption in the audit log.
//
// # On constant-time comparison
//
// The specification's hardening list asks for a constant-time comparison of the
// token, and there is none here, because there is no comparison. A redemption
// hashes what it was given and looks the hash up in a UNIQUE column: nothing is
// ever compared to a stored value, so there is no comparison whose duration could
// depend on where the first differing character is. The lookup is a b-tree search
// on an index, not a scan, so it does not leak the hash either.
//
// The property that is asserted instead is in the tests: a token that is wrong,
// truncated, or from another campaign is refused, and a *correct* token of the
// right length is not accepted for a different one — so there is no path where a
// partial match is a match. A constant-time comparison added to this path would be
// theatre that implies a threat which is not here.
func (r Redeemer) Redeem(ctx context.Context, campaignSlug domain.Slug, token string) (Redeemed, error) {
	if token == "" {
		return Redeemed{}, ErrNoToken
	}

	parsed, err := ParseToken(token)
	if err != nil {
		return Redeemed{}, ErrBadToken
	}

	now := r.Config.now()

	// The hash is what is looked up, and the plaintext is never passed to the
	// store. The database cannot confirm a token it has never held.
	principal, found, err := r.Backend.PrincipalByTokenHash(ctx, parsed.Hash())
	if err != nil {
		return Redeemed{}, fmt.Errorf("looking the share link up: %w", err)
	}
	if !found {
		return Redeemed{}, ErrBadToken
	}

	// The campaign check. A principal cannot exist without its campaign — the
	// column is a cascade — so a lookup failure here is a failure, not a
	// not-found, and there is no sentinel to compare against.
	campaign, err := r.Backend.GetCampaign(ctx, principal.CampaignID)
	if err != nil {
		return Redeemed{}, fmt.Errorf("looking up the share link's campaign: %w", err)
	}
	if campaign.Slug != campaignSlug {
		return Redeemed{}, ErrWrongCampaign
	}

	// Revocation and expiry, after the lookup and before anything is written. The
	// store returns revoked rows on purpose, so that there is one place saying what
	// a revoked link does rather than two.
	if principal.Revoked() {
		return Redeemed{}, ErrRevoked
	}
	if !principal.ExpiresAt.IsZero() && !now.Before(principal.ExpiresAt) {
		return Redeemed{}, ErrLinkExpired
	}

	session, err := r.Backend.CreateSession(ctx, domain.Session{
		PrincipalID: principal.ID,
		CreatedAt:   now,
		ExpiresAt:   now.Add(r.Config.sessionLifetime()),
	})
	if err != nil {
		return Redeemed{}, fmt.Errorf("creating a session for %q: %w", principal.Label, err)
	}

	// The last use, for the DM's active-now view. Not best-effort-and-swallowed:
	// it is one update, and failing to record who is online is not a reason to
	// refuse a player a session they are entitled to.
	if err := r.Backend.TouchPrincipal(ctx, principal.ID); err != nil {
		return Redeemed{}, fmt.Errorf("recording the use of %q's link: %w", principal.Label, err)
	}

	// And the audit row, which is what answers "was this link used from somewhere
	// it should not have been". It is written before the redirect is handed back,
	// because a redirect that has been sent cannot be taken back and an unrecorded
	// redemption is a redemption nobody can trace.
	if _, err := r.Backend.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  principal.CampaignID,
		PrincipalID: principal.ID,
		Action:      domain.AuditShareLinkUsed,
		At:          now,
	}); err != nil {
		return Redeemed{}, fmt.Errorf("recording the use of %q's link: %w", principal.Label, err)
	}

	return Redeemed{
		Principal:  principal,
		SessionID:  session.ID,
		ExpiresAt:  session.ExpiresAt,
		RedirectTo: redirectTo(campaignSlug),
	}, nil
}

// Authenticate is the other half: a cookie's value, and who it belongs to.
//
// It is a function of its own so the difference between *redeeming a link* and
// *using a cookie* is visible at the call site, and so a router cannot reach the
// store's session lookup directly and skip the expiry check. The check is here
// rather than in the router for the same reason the predicate is in one place in
// the store: it is a rule about time, and two copies of a rule about time is a
// rule that will be copied with one of the two bugs.
//
// # The slide
//
// A successful authentication moves the session's expiry out to now plus the
// session lifetime, so somebody who plays every week never sees their link again.
// Three things about that are worth stating because each of them is a way to get
// it wrong:
//
//   - **Only a success slides.** A wrong cookie must not extend anything, or a
//     script guessing ids would keep sessions alive by trying them.
//   - **The expiry never moves backwards.** `now` is the injected clock and a
//     machine's clock is not a fact, so a session that worked an hour ago still
//     works. The store enforces that too, because the rule belongs next to the
//     column rather than only in the caller.
//   - **It is one UPDATE on a row that has just been read.** A write on the
//     request path is a cost, and the alternative — refreshing only when the
//     remaining life drops below half — bounds the writes at the price of an
//     unpredictable effective session length. At this scale, on one machine, one
//     UPDATE is cheaper than a rule nobody can state in one sentence.
func (r Redeemer) Authenticate(ctx context.Context, sessionID string) (domain.Principal, error) {
	if sessionID == "" {
		return domain.Principal{}, ErrNoSession
	}

	session, found, err := r.Backend.SessionByID(ctx, sessionID)
	if err != nil {
		return domain.Principal{}, fmt.Errorf("looking a session up: %w", err)
	}
	if !found {
		return domain.Principal{}, ErrNoSession
	}

	now := r.Config.now()
	if session.Expired(now) {
		// A session that has lapsed is ended rather than left to be found and
		// rejected on every request for the rest of its life. Revocation is a
		// delete; expiry is the same idea for the clock's version of it.
		if deleteErr := r.Backend.DeleteSession(ctx, sessionID); deleteErr != nil {
			return domain.Principal{}, fmt.Errorf("ending a lapsed session: %w", deleteErr)
		}
		return domain.Principal{}, ErrNoSession
	}

	principal, found, err := r.Backend.PrincipalByID(ctx, session.PrincipalID)
	if err != nil {
		return domain.Principal{}, fmt.Errorf("looking up a session's principal: %w", err)
	}
	if !found {
		// The session outlived its principal. The cascade should have taken it, so
		// this is a bug rather than a case; treating it as "no session" is the
		// answer that does not care whether it is.
		return domain.Principal{}, ErrNoSession
	}
	if principal.Revoked() {
		// A revocation that somehow left a session behind. RevokePrincipal ends
		// them in the same transaction, so reaching here means the two paths
		// disagree — and a disagreement about whether a revoked player is logged
		// in resolves against the player being logged in.
		return domain.Principal{}, ErrRevoked
	}

	// The slide, last, so that it happens only for a session that has just been
	// proved live and belongs to a principal that has just been proved un-revoked.
	// A failure is not fatal: the session works, and an expiry that did not move
	// means the player is asked for their link again in thirty days rather than
	// being logged out of a game they are in the middle of.
	if err := r.slide(ctx, session, now); err != nil {
		return principal, nil //nolint:nilerr // a session that works beats a session that slides
	}

	return principal, nil
}

// slide moves a session's expiry out, if that is an extension.
//
// A session whose remaining life is already longer than the new expiry — a
// machine whose clock went backwards — is left alone, and the store's own
// "never move it backwards" clause means the two agree even if one of them is
// wrong.
func (r Redeemer) slide(ctx context.Context, session domain.Session, now time.Time) error {
	until := now.Add(r.Config.sessionLifetime())
	if !until.After(session.ExpiresAt) {
		return nil
	}
	return r.Backend.TouchSession(ctx, session.ID, until)
}

// EndSession is a logout. A session that is not there is not an error: a browser
// may send the request twice, and a back button is not a problem to report.
func (r Redeemer) EndSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if err := r.Backend.DeleteSession(ctx, sessionID); err != nil {
		return fmt.Errorf("ending a session: %w", err)
	}
	return nil
}

// redirectTo is where the browser goes once the token has been exchanged.
//
// It is the campaign's root, and it is *built* rather than taken from the request:
// a redirect target that came from the incoming URL is a target an attacker
// chose, and this is the one response in the whole program whose entire job is to
// send the browser somewhere.
func redirectTo(slug domain.Slug) string {
	return "/c/" + url.PathEscape(string(slug)) + "/"
}
