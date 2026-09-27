package testsuite

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// The auth half of the contract. These are the four tables ADR 0003's decision
// rests on, and the properties below are the ones where a quiet difference
// between two store implementations becomes an incident rather than an
// inconsistency:
//
//   - a link is found by the *hash* of its token, and never by the token;
//   - a revocation ends every session, in the same breath as the flag;
//   - a stored session always has an expiry;
//   - a character binding is a replace, not an add.
//
// None of them is about the signatures. They are about the answers.

// A link is found by its hash and its hash alone, and the two links of a campaign
// are the two links.
func principalIsFoundByItsTokenHash(t *testing.T, factory Factory) {
	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")

	alice := createPrincipal(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")

	found, ok, err := s.PrincipalByTokenHash(ctx, "hash-of-alice")
	if err != nil || !ok {
		t.Fatalf("PrincipalByTokenHash = %+v, %t, %v; want Alice, true, nil", found, ok, err)
	}
	if found.ID != alice.ID {
		t.Errorf("PrincipalByTokenHash found %q, want %q", found.ID, alice.ID)
	}

	// A hash nothing was issued for is not found, and not an error. "This token is
	// not one of ours" is the answer, and it is the answer every redemption of
	// every mistyped link has to be able to give.
	for _, hash := range []string{"", "hash-of-carol", "HASH-OF-ALICE"} {
		if _, foundIt, hashErr := s.PrincipalByTokenHash(ctx, hash); hashErr != nil || foundIt {
			t.Errorf("PrincipalByTokenHash(%q) = %t, %v; want not found and no error", hash, foundIt, hashErr)
		}
	}

	// Two principals may not share a hash: that is two accounts for one
	// credential, and a DM with two links in their list and one that does nothing.
	dup := domain.Principal{
		CampaignID: campaign.ID,
		Label:      "Mallory",
		Role:       domain.RolePlayer,
		TokenHash:  "hash-of-alice",
		TokenHint:  "ffff",
	}
	if _, dupErr := s.CreatePrincipal(ctx, dup); !errors.Is(dupErr, Conflict) {
		t.Errorf("a second principal with the same token hash returned %v, want an error matching Conflict", err)
	}

	// And the first is intact rather than relabelled.
	again, ok, err := s.PrincipalByTokenHash(ctx, "hash-of-alice")
	if err != nil || !ok {
		t.Fatalf("the first principal is gone: %+v, %t, %v", again, ok, err)
	}
	if again.Label != "Alice (Ranger)" {
		t.Errorf("the first principal is labelled %q, want it untouched by the refused second one", again.Label)
	}
}

// A revocation has three answers, and a DM clicking a button needs to tell them
// apart: not found, already revoked, and revoked now.
func revokingAPrincipalEndsItsSessions(t *testing.T, factory Factory) {
	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	alice := createPrincipal(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")

	if _, err := s.CreateSession(ctx, liveSessionFor(alice.ID)); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, secondErr := s.CreateSession(ctx, liveSessionFor(alice.ID)); secondErr != nil {
		t.Fatalf("the second CreateSession: %v", secondErr)
	}

	// Nobody is not revoked.
	if err := s.RevokePrincipal(ctx, "no-such-principal"); !errors.Is(err, NotFound) {
		t.Errorf("revoking nobody returned %v, want an error matching NotFound", err)
	}

	if revokeErr := s.RevokePrincipal(ctx, alice.ID); revokeErr != nil {
		t.Fatalf("RevokePrincipal: %v", revokeErr)
	}

	// The flag and the sessions, or neither. A revoke that set the flag and left a
	// session leaves a browser working with a link the DM believes is dead.
	revoked, ok, err := s.PrincipalByID(ctx, alice.ID)
	if err != nil || !ok {
		t.Fatalf("PrincipalByID after revoking = %+v, %t, %v", revoked, ok, err)
	}
	if !revoked.Revoked() {
		t.Error("the principal does not report itself as revoked")
	}
	if live, liveErr := s.SessionsForPrincipal(ctx, alice.ID, contractNow); liveErr != nil {
		t.Fatalf("SessionsForPrincipal: %v", liveErr)
	} else if len(live) != 0 {
		t.Errorf("a revoked principal has %d live sessions; a revocation that leaves one is not a revocation", len(live))
	}

	// The row is flagged, not deleted, because the audit log's question is "was
	// this link ever used" and a deleted principal cannot answer it.
	if revoked.Label != "Alice (Ranger)" || revoked.TokenHint == "" {
		t.Errorf("the revoked principal kept neither its label nor its hint: %+v", revoked)
	}

	// Revoking twice is not an error, and does not move the moment of revocation.
	if revokeErr := s.RevokePrincipal(ctx, alice.ID); revokeErr != nil {
		t.Errorf("the second RevokePrincipal: %v", revokeErr)
	}
	twice, _, err := s.PrincipalByID(ctx, alice.ID)
	if err != nil {
		t.Fatalf("PrincipalByID after the second revoke: %v", err)
	}
	if !twice.RevokedAt.Equal(revoked.RevokedAt) {
		t.Errorf("the second revocation moved revoked_at from %v to %v; a revocation is a fact, not a counter",
			revoked.RevokedAt, twice.RevokedAt)
	}
}

// "Everyone sign in again" is scoped to one campaign. A DM with two campaigns
// should not be able to log out the players of the other one by accident, and
// that is a property of the query rather than of the caller's care.
func revokingEveryPrincipalIsScopedToOneCampaign(t *testing.T, factory Factory) {
	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	other := createCampaignWithSlug(t, s, "thornford")

	for _, label := range []string{"Alice", "Bob"} {
		owner := createPrincipal(t, s, campaign.ID, label, "hash-of-"+label)
		if _, err := s.CreateSession(ctx, liveSessionFor(owner.ID)); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}
	untouched := createPrincipal(t, s, other.ID, "Somebody else", "hash-of-somebody-else")
	if _, err := s.CreateSession(ctx, liveSessionFor(untouched.ID)); err != nil {
		t.Fatalf("CreateSession for the other campaign: %v", err)
	}

	revoked, err := s.RevokeAllPrincipals(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("RevokeAllPrincipals: %v", err)
	}
	if revoked != 2 {
		t.Errorf("RevokeAllPrincipals reported %d, want 2", revoked)
	}
	if again, againErr := s.RevokeAllPrincipals(ctx, campaign.ID); againErr != nil {
		t.Fatalf("the second RevokeAllPrincipals: %v", againErr)
	} else if again != 0 {
		t.Errorf("a second RevokeAllPrincipals reported %d, want 0: there were none left to revoke", again)
	}

	survivor, ok, err := s.PrincipalByID(ctx, untouched.ID)
	if err != nil || !ok {
		t.Fatalf("the other campaign's principal is gone: %+v, %t, %v", survivor, ok, err)
	}
	if survivor.Revoked() {
		t.Error("revoking one campaign's principals revoked another's")
	}
	if live, liveErr := s.SessionsForPrincipal(ctx, untouched.ID, contractNow); liveErr != nil {
		t.Fatalf("SessionsForPrincipal: %v", liveErr)
	} else if len(live) != 1 {
		t.Errorf("the other campaign's principal has %d sessions, want 1", len(live))
	}
}

// A stored session always has an expiry. domain.Session.Expired treats a zero
// expiry as "never" so that a zero-valued struct is safe to ask; that is a
// reason the value is safe, not a reason to store one.
func aStoredSessionAlwaysExpires(t *testing.T, factory Factory) {
	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	alice := createPrincipal(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")

	never := domain.Session{PrincipalID: alice.ID}
	if _, err := s.CreateSession(ctx, never); err == nil {
		t.Error("a session with no expiry was stored: that is a credential outliving the reason it was issued")
	}

	// An id and a creation time are the store's to fill in.
	created, err := s.CreateSession(ctx, liveSessionFor(alice.ID))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if created.ID == "" || created.CreatedAt.IsZero() {
		t.Errorf("the session came back as %+v, want an id and a creation time filled in", created)
	}

	// A session round-trips, which is what the per-request lookup depends on.
	found, ok, err := s.SessionByID(ctx, created.ID)
	if err != nil || !ok {
		t.Fatalf("SessionByID = %+v, %t, %v; want the session, true, nil", found, ok, err)
	}
	if found.PrincipalID != alice.ID || !found.ExpiresAt.Equal(created.ExpiresAt) {
		t.Errorf("the session came back as %+v, want the principal and expiry it was stored with", found)
	}

	// An expired session is still *found*; whether it authenticates is a question
	// about time, and asking it in two places is how two answers happen.
	past := liveSessionFor(alice.ID)
	past.ExpiresAt = contractNow.Add(-time.Hour)
	expired, err := s.CreateSession(ctx, past)
	if err != nil {
		t.Fatalf("CreateSession for an expired one: %v", err)
	}
	if _, expiredOK, expiredErr := s.SessionByID(ctx, expired.ID); expiredErr != nil || !expiredOK {
		t.Errorf("an expired session is not findable: %t, %v; it should be found and then judged expired", expiredOK, expiredErr)
	}
	if live, liveErr := s.SessionsForPrincipal(ctx, alice.ID, contractNow); liveErr != nil {
		t.Fatalf("SessionsForPrincipal: %v", liveErr)
	} else if len(live) != 1 {
		t.Errorf("%d live sessions, want 1: an expired row is not a live one", len(live))
	}

	// The reaper's arithmetic, and the direction of the boundary: a session whose
	// expiry is exactly now is expired. Three go, and the count is stated rather
	// than left to the reader: the two from the loop, plus the one made expired
	// above, because a test that says "2" without saying which two is a test
	// that is wrong for the right reason.
	for _, expiry := range []time.Time{contractNow.Add(-time.Hour), contractNow, contractNow.Add(time.Hour)} {
		one := liveSessionFor(alice.ID)
		one.ExpiresAt = expiry
		if _, oneErr := s.CreateSession(ctx, one); oneErr != nil {
			t.Fatalf("CreateSession: %v", oneErr)
		}
	}
	purged, err := s.PurgeExpiredSessions(ctx, contractNow)
	if err != nil {
		t.Fatalf("PurgeExpiredSessions: %v", err)
	}
	if purged != 3 {
		t.Errorf("PurgeExpiredSessions removed %d, want 3", purged)
	}

	if live, err := s.SessionsForPrincipal(ctx, alice.ID, contractNow); err != nil {
		t.Fatalf("SessionsForPrincipal: %v", err)
	} else if len(live) != 2 {
		t.Errorf("%d sessions are live, want 2: the one expiring in an hour and the one expiring in eight", len(live))
	}
}

// The slide, and the rule about it that is not obvious: an expiry never moves
// backwards. A clock that goes backwards is a machine whose battery died, and the
// failure is a player logged out mid-session with no cause — so the rule lives next
// to the column rather than only in the caller that has to remember it.
func touchSessionOnlyExtends(t *testing.T, factory Factory) {
	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	alice := createPrincipal(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")

	created, err := s.CreateSession(ctx, liveSessionFor(alice.ID))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	later := created.ExpiresAt.Add(30 * 24 * time.Hour)
	if slideErr := s.TouchSession(ctx, created.ID, later); slideErr != nil {
		t.Fatalf("TouchSession: %v", slideErr)
	}
	after, found, err := s.SessionByID(ctx, created.ID)
	if err != nil || !found {
		t.Fatalf("SessionByID = %+v, %t, %v", after, found, err)
	}
	if !after.ExpiresAt.Equal(later) {
		t.Errorf("after the slide the expiry is %v, want %v", after.ExpiresAt, later)
	}

	backwards := created.ExpiresAt.Add(-24 * time.Hour)
	if backErr := s.TouchSession(ctx, created.ID, backwards); backErr != nil {
		t.Fatalf("TouchSession backwards: %v", backErr)
	}
	after, _, err = s.SessionByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("SessionByID: %v", err)
	}
	if !after.ExpiresAt.Equal(later) {
		t.Errorf("a backwards touch moved the expiry from %v to %v", later, after.ExpiresAt)
	}

	// A session that is not there is not an error: the caller has just read it,
	// and one revoked between the read and the write is a session the caller was
	// never entitled to keep. Returning nil is what makes sliding safe on a request
	// that has already been authenticated.
	if err := s.TouchSession(ctx, "no-such-session", later); err != nil {
		t.Errorf("TouchSession of a session that is not there: %v", err)
	}
}

// A binding is a statement about what a player owns now, and "now" is the whole
// point: an add-only table is a table where a player who loses a character keeps
// reading its pages.
func aCharacterBindingIsAReplace(t *testing.T, factory Factory) {
	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	alice := createPrincipal(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")
	bob := createPrincipal(t, s, campaign.ID, "Bob", "hash-of-bob")

	aria := createPage(t, s, campaign.ID, func(p *domain.Page) { p.Path = "characters/aria" })
	bram := createPage(t, s, campaign.ID, func(p *domain.Page) { p.Path = "characters/bram" })

	// A duplicate is one binding, not two, and not an error: the caller is usually
	// a sync, and a sync should not fail a campaign over a page bound twice.
	if err := s.ReplacePrincipalCharacters(ctx, alice.ID, []string{aria.ID, aria.ID, aria.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters with a duplicate: %v", err)
	}
	if owned, err := s.PrincipalCharacters(ctx, alice.ID); err != nil {
		t.Fatalf("PrincipalCharacters: %v", err)
	} else if len(owned) != 1 {
		t.Errorf("the principal owns %v, want exactly one page: a duplicate is not two bindings", owned)
	}

	if err := s.ReplacePrincipalCharacters(ctx, alice.ID, []string{aria.ID, bram.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}
	if owned, err := s.PrincipalCharacters(ctx, alice.ID); err != nil {
		t.Fatalf("PrincipalCharacters: %v", err)
	} else if len(owned) != 2 {
		t.Errorf("the principal owns %v, want two pages", owned)
	}

	if err := s.ReplacePrincipalCharacters(ctx, alice.ID, []string{bram.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}
	if owns, ownsErr := s.OwnerExists(ctx, alice.ID, aria.ID); ownsErr != nil || owns {
		t.Errorf("the principal still owns the page it lost: %t, %v", owns, ownsErr)
	}
	if owns, ownsErr := s.OwnerExists(ctx, alice.ID, bram.ID); ownsErr != nil || !owns {
		t.Errorf("the principal does not own the page it kept: %t, %v", owns, ownsErr)
	}

	// The other direction, because "why can Alice not see her own character page"
	// is answered by there being no row here.
	owners, err := s.CharacterOwners(ctx, bram.ID)
	if err != nil {
		t.Fatalf("CharacterOwners: %v", err)
	}
	if !slices.Equal(owners, []string{alice.ID}) {
		t.Errorf("the page's owners are %v, want [%s]", owners, alice.ID)
	}

	// Somebody else's page is somebody else's.
	if owns, ownsErr := s.OwnerExists(ctx, bob.ID, bram.ID); ownsErr != nil || owns {
		t.Errorf("Bob owns Alice's page: %t, %v", owns, ownsErr)
	}

	// A request that failed to identify its caller has no principal, so nobody
	// owns the page, so the dm-and-owner branch of the predicate is empty. An error
	// here would turn "not logged in" into a 500 for the one caller who must not
	// see the page.
	if owns, ownsErr := s.OwnerExists(ctx, "", bram.ID); ownsErr != nil || owns {
		t.Errorf("a request with no principal owns a page: %t, %v", owns, ownsErr)
	}

	// And unbinding is a thing you can ask for.
	if err := s.ReplacePrincipalCharacters(ctx, alice.ID, nil); err != nil {
		t.Fatalf("ReplacePrincipalCharacters with nothing: %v", err)
	}
	if owned, err := s.PrincipalCharacters(ctx, alice.ID); err != nil {
		t.Fatalf("PrincipalCharacters: %v", err)
	} else if len(owned) != 0 {
		t.Errorf("the principal owns %v after being unbound, want nothing", owned)
	}
}

// The audit log is the answer to "was this link ever used", and the two properties
// it needs are that the rowid is the store's to assign and that the read is
// newest-first, because it is read as a story.
func theAuditLogIsAppendedAndReadNewestFirst(t *testing.T, factory Factory) {
	ctx := context.Background()
	s := factory(t)
	campaign := createCampaignWithSlug(t, s, "blackwater")
	alice := createPrincipal(t, s, campaign.ID, "Alice (Ranger)", "hash-of-alice")

	// An entry with no action is not an entry.
	if _, err := s.AppendAudit(ctx, domain.AuditEntry{CampaignID: campaign.ID}); err == nil {
		t.Error("an audit entry with no action was appended")
	}

	// An action nobody in this build emits is a plugin recording something of its
	// own, and refusing it would be the audit log deciding what a plugin may say.
	if _, pluginErr := s.AppendAudit(ctx, domain.AuditEntry{
		CampaignID: campaign.ID,
		Action:     domain.AuditAction("plugin_spoiler_revealed"),
	}); pluginErr != nil {
		t.Errorf("an action this build does not know was refused: %v", pluginErr)
	}

	issued, err := s.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  campaign.ID,
		PrincipalID: alice.ID,
		Action:      domain.AuditPrincipalIssued,
		Detail:      "Alice (Ranger)",
	})
	if err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if issued.ID == 0 {
		t.Error("the entry came back with no id: the rowid is the store's to assign")
	}
	if issued.At.IsZero() {
		t.Error("the entry came back with no timestamp: the store stamps it")
	}

	used, err := s.AppendAudit(ctx, domain.AuditEntry{
		CampaignID:  campaign.ID,
		PrincipalID: alice.ID,
		Action:      domain.AuditShareLinkUsed,
	})
	if err != nil {
		t.Fatalf("the second AppendAudit: %v", err)
	}

	entries, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) < 2 {
		t.Fatalf("ListAudit returned %d entries, want at least 2", len(entries))
	}
	if entries[0].ID != used.ID {
		t.Errorf("the first entry is %d, want the newest (%d)", entries[0].ID, used.ID)
	}
	if entries[0].Action != domain.AuditShareLinkUsed {
		t.Errorf("the first entry is %q, want %q", entries[0].Action, domain.AuditShareLinkUsed)
	}
	if entries[0].At.IsZero() {
		t.Error("an entry read back has no timestamp")
	}

	// The limit is a limit: this table has no upper bound, so a request that read
	// all of it is a request that eventually does not come back.
	if few, fewErr := s.ListAudit(ctx, campaign.ID, 1); fewErr != nil {
		t.Fatalf("ListAudit with a limit: %v", fewErr)
	} else if len(few) != 1 {
		t.Errorf("ListAudit with a limit of 1 returned %d entries", len(few))
	}

	// Another campaign's log is another campaign's log.
	other := createCampaignWithSlug(t, s, "thornford")
	if _, otherErr := s.AppendAudit(ctx, domain.AuditEntry{
		CampaignID: other.ID,
		Action:     domain.AuditPrincipalRevoked,
	}); otherErr != nil {
		t.Fatalf("AppendAudit for the other campaign: %v", otherErr)
	}
	mine, err := s.ListAudit(ctx, campaign.ID, 0)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, entry := range mine {
		if entry.CampaignID != campaign.ID {
			t.Errorf("the log for %s contains an entry for %s", campaign.ID, entry.CampaignID)
		}
	}
}

// Fixtures for this file.

// contractNow is the instant the time-sensitive cases are written against, and it
// is a constant rather than a read of the store's clock so that "an hour ago" and
// "in an hour" mean the same thing in every subtest.
var contractNow = time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

// createPrincipal stores a player and fails the test if it cannot.
func createPrincipal(t *testing.T, s API, campaignID, label, tokenHash string) domain.Principal {
	t.Helper()

	created, err := s.CreatePrincipal(context.Background(), domain.Principal{
		CampaignID: campaignID,
		Label:      label,
		Role:       domain.RolePlayer,
		TokenHash:  tokenHash,
		TokenHint:  "a1b2",
	})
	if err != nil {
		t.Fatalf("CreatePrincipal(%q): %v", label, err)
	}
	return created
}

// liveSessionFor is a session that authenticates at contractNow.
func liveSessionFor(principalID string) domain.Session {
	return domain.Session{
		PrincipalID: principalID,
		CreatedAt:   contractNow,
		ExpiresAt:   contractNow.Add(8 * time.Hour),
	}
}
