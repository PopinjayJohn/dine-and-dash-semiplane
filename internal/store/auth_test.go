package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// A share link's whole safety rests on two things about this table: the plaintext
// token is not in it, and the hash in it is unique. The first is a property of
// what the method takes — it takes a hash, so there is nothing else to store — and
// the second is a constraint, and a constraint nobody tests is a constraint that
// gets dropped in a migration.
func TestCreatePrincipalKeepsTwoLinksApart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	first, err := s.CreatePrincipal(ctx, principal(c.ID, "Alice (Ranger)", "hash-of-alice"))
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	// Two principals, one hash: the second is refused rather than replacing the
	// first, because a DM with two links in their list and one that does nothing is
	// worse than a DM with an error they can see.
	dup := principal(c.ID, "Mallory", "hash-of-alice")
	if _, dupErr := s.CreatePrincipal(ctx, dup); !errors.Is(dupErr, store.ErrConflict) {
		t.Errorf("a second principal with the same token hash returned %v, want an error matching ErrConflict", err)
	}

	// And the first is intact rather than relabelled.
	got, ok, err := s.PrincipalByTokenHash(ctx, "hash-of-alice")
	if err != nil || !ok {
		t.Fatalf("PrincipalByTokenHash = %v, %t, %v; want the principal, true, nil", got, ok, err)
	}
	if got.ID != first.ID || got.Label != "Alice (Ranger)" {
		t.Errorf("the first principal is %+v, want it untouched by the refused second one", got)
	}

	// A hash in another campaign is also a conflict, and for a better reason: the
	// same token would then be one credential for two campaigns, and a DM with
	// two campaigns should not be able to make one of their players a principal of
	// both by accident.
	other := otherCampaign(t, s, "thornford")
	if _, err := s.CreatePrincipal(ctx, principal(other.ID, "Alice (other)", "hash-of-alice")); !errors.Is(err, store.ErrConflict) {
		t.Errorf("the same hash in another campaign returned %v, want an error matching ErrConflict", err)
	}
}

// The redemption lookup is the only way in, and it takes a hash. A plaintext token
// that is not the hash of anything is not found, which is the property that makes
// "the token is not in the database" a fact about the schema rather than a
// promise about discipline.
func TestPrincipalByTokenHash(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	created, err := s.CreatePrincipal(ctx, principal(c.ID, "Alice (Ranger)", "hash-of-alice"))
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}

	found, ok, err := s.PrincipalByTokenHash(ctx, "hash-of-alice")
	if err != nil || !ok {
		t.Fatalf("PrincipalByTokenHash = %v, %t, %v; want the principal, true, nil", found, ok, err)
	}
	if found.ID != created.ID {
		t.Errorf("PrincipalByTokenHash found %q, want %q", found.ID, created.ID)
	}

	// A hash nothing was ever issued for is simply not found, and not an error:
	// "this token is not one of ours" is the answer, and it is an answer a
	// redemption gives on every request somebody types a wrong link.
	for _, hash := range []string{"", "not-a-hash", "hash-of-alice-x", "HASH-OF-ALICE"} {
		_, ok, err := s.PrincipalByTokenHash(ctx, hash)
		if err != nil {
			t.Errorf("PrincipalByTokenHash(%q): %v", hash, err)
		}
		if ok {
			t.Errorf("PrincipalByTokenHash(%q) found a principal", hash)
		}
	}
}

// A revoked principal is still *found*, because whether a revoked link
// authenticates is a question about revocation and time and belongs in one place.
// Returning notFound here would make "revoked" and "never existed" the same
// answer, and those are two different things a DM is looking at.
func TestARevokedPrincipalIsStillFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	created, err := s.CreatePrincipal(ctx, principal(c.ID, "Alice (Ranger)", "hash-of-alice"))
	if err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	if revokeErr := s.RevokePrincipal(ctx, created.ID); revokeErr != nil {
		t.Fatalf("RevokePrincipal: %v", revokeErr)
	}

	got, ok, err := s.PrincipalByTokenHash(ctx, "hash-of-alice")
	if err != nil || !ok {
		t.Fatalf("a revoked principal is not findable: %v, %t, %v", got, ok, err)
	}
	if !got.Revoked() {
		t.Error("the principal does not report itself as revoked")
	}
	if got.Label != "Alice (Ranger)" {
		t.Errorf("the label is %q after revocation, want it kept: the DM's list should still say who this was",
			got.Label)
	}
	if got.TokenHint != created.TokenHint {
		t.Errorf("the hint is %q after revocation, want %q: a revoked link still has to be recognisable in a list",
			got.TokenHint, created.TokenHint)
	}
}

// Revocation has three answers and a DM clicking a button needs to be able to
// tell them apart.
func TestRevokePrincipal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)

	t.Run("a principal that is not there is not found", func(t *testing.T) {
		if err := s.RevokePrincipal(ctx, "no-such-principal"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("RevokePrincipal of nobody returned %v, want an error matching ErrNotFound", err)
		}
	})

	t.Run("a live principal is revoked once and only once", func(t *testing.T) {
		created, err := s.CreatePrincipal(ctx, principal(c.ID, "Bob", "hash-of-bob"))
		if err != nil {
			t.Fatalf("CreatePrincipal: %v", err)
		}

		if err := s.RevokePrincipal(ctx, created.ID); err != nil {
			t.Fatalf("the first RevokePrincipal: %v", err)
		}
		first := revokedAt(t, s, created.ID)

		// Revoking twice is not an error. A DM who clicks twice, or a UI that
		// sends the request twice, must not be told something is wrong.
		if err := s.RevokePrincipal(ctx, created.ID); err != nil {
			t.Errorf("the second RevokePrincipal: %v", err)
		}
		if second := revokedAt(t, s, created.ID); !second.Equal(first) {
			t.Errorf("the second revocation moved revoked_at from %v to %v; a revocation is a fact, not a counter",
				first, second)
		}
	})

	t.Run("a leftover session is cleared even by a second revoke", func(t *testing.T) {
		// The state a failed revoke leaves: the principal is flagged but a
		// session outlived it. A second revoke is the repair, and it has to be.
		created, err := s.CreatePrincipal(ctx, principal(c.ID, "Carol", "hash-of-carol"))
		if err != nil {
			t.Fatalf("CreatePrincipal: %v", err)
		}
		if revokeErr := s.RevokePrincipal(ctx, created.ID); revokeErr != nil {
			t.Fatalf("RevokePrincipal: %v", revokeErr)
		}
		if _, sessionErr := s.CreateSession(ctx, liveSession(created.ID)); sessionErr != nil {
			t.Fatalf("CreateSession: %v", sessionErr)
		}

		if revokeErr := s.RevokePrincipal(ctx, created.ID); revokeErr != nil {
			t.Errorf("the second RevokePrincipal: %v", revokeErr)
		}
		if live := liveSessions(t, s, created.ID); live != 0 {
			t.Errorf("a principal with a revoked_at and %d live sessions: a revocation that leaves a session is not a revocation", live)
		}
	})
}

// "Everyone sign in again" is a real button, and it has to count honestly so the
// DM can say what happened.
func TestRevokeAllPrincipals(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	other := otherCampaign(t, s, "thornford")

	for _, label := range []string{"Alice", "Bob", "Carol"} {
		created, err := s.CreatePrincipal(ctx, principal(c.ID, label, "hash-of-"+label))
		if err != nil {
			t.Fatalf("CreatePrincipal(%q): %v", label, err)
		}
		if _, err := s.CreateSession(ctx, liveSession(created.ID)); err != nil {
			t.Fatalf("CreateSession(%q): %v", label, err)
		}
	}

	// Another campaign's principal, which must survive: "everyone" means everyone
	// in *this* campaign, and a DM with two campaigns should not be able to log
	// out the players of the other one by accident.
	untouched, err := s.CreatePrincipal(ctx, principal(other.ID, "Somebody else", "hash-of-somebody-else"))
	if err != nil {
		t.Fatalf("CreatePrincipal for the other campaign: %v", err)
	}
	if _, sessionErr := s.CreateSession(ctx, liveSession(untouched.ID)); sessionErr != nil {
		t.Fatalf("CreateSession for the other campaign: %v", sessionErr)
	}

	revoked, err := s.RevokeAllPrincipals(ctx, c.ID)
	if err != nil {
		t.Fatalf("RevokeAllPrincipals: %v", err)
	}
	if revoked != 3 {
		t.Errorf("RevokeAllPrincipals reported %d revocations, want 3", revoked)
	}

	// And a second call reports none, because there were none left to revoke.
	again, err := s.RevokeAllPrincipals(ctx, c.ID)
	if err != nil {
		t.Fatalf("the second RevokeAllPrincipals: %v", err)
	}
	if again != 0 {
		t.Errorf("a second RevokeAllPrincipals reported %d revocations, want 0", again)
	}

	survivor, ok, err := s.PrincipalByID(ctx, untouched.ID)
	if err != nil || !ok {
		t.Fatalf("the other campaign's principal is gone: %v, %t, %v", survivor, ok, err)
	}
	if survivor.Revoked() {
		t.Error("revoking one campaign's principals revoked another's")
	}
	if live := liveSessions(t, s, untouched.ID); live != 1 {
		t.Errorf("the other campaign's principal has %d sessions, want 1", live)
	}
}

// A session is a row, and a row with a required expiry is the difference between
// a cookie that lapses and a cookie that does not.
func TestCreateSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	owner := mustCreatePrincipal(t, s, c.ID, "Alice (Ranger)", "hash-of-alice")

	t.Run("a session with no expiry is refused", func(t *testing.T) {
		// domain.Session.Expired treats a zero expiry as "never", so that a
		// zero-valued struct is safe to ask. That is a reason the *value* is safe,
		// not a reason to store one: a stored session with no expiry is a
		// credential that outlives the reason it was issued, and the reason is
		// usually one evening at one table.
		_, err := s.CreateSession(ctx, domain.Session{
			PrincipalID: owner.ID,
			ExpiresAt:   time.Time{},
		})
		if err == nil {
			t.Fatal("a session with no expiry was stored")
		}
		if !strings.Contains(err.Error(), "expiry") {
			t.Errorf("the error does not mention the expiry: %v", err)
		}
	})

	t.Run("an id and a creation time are filled in", func(t *testing.T) {
		created, err := s.CreateSession(ctx, liveSession(owner.ID))
		if err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if created.ID == "" {
			t.Error("the session was stored with no id")
		}
		if created.CreatedAt.IsZero() {
			t.Error("the session was stored with no creation time")
		}
	})

	t.Run("a session of a principal that is not there is refused", func(t *testing.T) {
		_, err := s.CreateSession(ctx, liveSession("no-such-principal"))
		if err == nil {
			t.Fatal("a session of a principal that does not exist was stored")
		}
	})
}

// The reaper is housekeeping, and its arithmetic is the only arithmetic in this
// table: an expired session that stays is untidy, one that goes while live is a
// logged-out player.
func TestPurgeExpiredSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	owner := mustCreatePrincipal(t, s, c.ID, "Alice (Ranger)", "hash-of-alice")

	now := time.Date(2026, 2, 14, 19, 3, 0, 0, time.UTC)

	for _, expiry := range []time.Time{now.Add(-time.Hour), now, now.Add(time.Hour)} {
		if _, err := s.CreateSession(ctx, liveSession(owner.ID, expiry)); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
	}

	purged, err := s.PurgeExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("PurgeExpiredSessions: %v", err)
	}

	// Two, and not one: a session whose expiry is exactly now is expired, which is
	// the same rule Session.Expired applies and the same direction — being wrong
	// by the smallest possible amount is still being wrong, in the safe direction.
	if purged != 2 {
		t.Errorf("PurgeExpiredSessions removed %d sessions, want 2", purged)
	}

	live, err := s.SessionsForPrincipal(ctx, owner.ID, now)
	if err != nil {
		t.Fatalf("SessionsForPrincipal: %v", err)
	}
	if len(live) != 1 {
		t.Errorf("%d sessions are live, want 1", len(live))
	}
}

// A binding is a statement about what a player owns *now*, and the test is that
// "now" is taken seriously: replacing is not adding, and a duplicate is not two
// bindings.
func TestPrincipalCharacters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	owner := mustCreatePrincipal(t, s, c.ID, "Alice (Ranger)", "hash-of-alice")
	page := mustCreatePage(t, s, c.ID)

	t.Run("a binding is a set, not a list", func(t *testing.T) {
		if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{page.ID, page.ID, page.ID}); err != nil {
			t.Fatalf("ReplacePrincipalCharacters: %v", err)
		}
		owned, err := s.PrincipalCharacters(ctx, owner.ID)
		if err != nil {
			t.Fatalf("PrincipalCharacters: %v", err)
		}
		if len(owned) != 1 || owned[0] != page.ID {
			t.Errorf("the principal owns %v, want exactly [%s]: a duplicate is not two bindings", owned, page.ID)
		}
	})

	t.Run("replacing removes what was there", func(t *testing.T) {
		second := anotherPage(t, s, c.ID, "characters/aria")

		if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{page.ID, second.ID}); err != nil {
			t.Fatalf("ReplacePrincipalCharacters: %v", err)
		}
		if owned := ownedPages(t, s, owner.ID); len(owned) != 2 {
			t.Errorf("the principal owns %v, want two pages", owned)
		}

		// A player who is given a new character and loses the old one has to stop
		// reading the old one's pages on their next request. An add-only table
		// cannot do this, which is the whole reason this is a replace.
		if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{second.ID}); err != nil {
			t.Fatalf("ReplacePrincipalCharacters: %v", err)
		}
		owned, err := s.PrincipalCharacters(ctx, owner.ID)
		if err != nil {
			t.Fatalf("PrincipalCharacters: %v", err)
		}
		if len(owned) != 1 || owned[0] != second.ID {
			t.Errorf("after replacing, the principal owns %v, want only [%s]", owned, second.ID)
		}
		if has, err := s.OwnerExists(ctx, owner.ID, page.ID); err != nil || has {
			t.Errorf("the principal still owns the page it lost: %t, %v", has, err)
		}
	})

	t.Run("the other direction is answerable", func(t *testing.T) {
		// Bound here rather than relying on a sibling subtest, because a test
		// that depends on the order its siblings run in is a test that stops
		// meaning anything the moment someone reorders them.
		if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{page.ID}); err != nil {
			t.Fatalf("ReplacePrincipalCharacters: %v", err)
		}

		owners, err := s.CharacterOwners(ctx, page.ID)
		if err != nil {
			t.Fatalf("CharacterOwners: %v", err)
		}
		if len(owners) != 1 || owners[0] != owner.ID {
			t.Errorf("the page's owners are %v, want [%s]", owners, owner.ID)
		}
	})

	t.Run("an empty binding is a valid thing to ask for", func(t *testing.T) {
		if err := s.ReplacePrincipalCharacters(ctx, owner.ID, nil); err != nil {
			t.Fatalf("ReplacePrincipalCharacters with nothing: %v", err)
		}
		if owned := ownedPages(t, s, owner.ID); len(owned) != 0 {
			t.Errorf("the principal owns %v after being unbound, want nothing", owned)
		}
	})
}

// The ACL asks "does this principal own this page" once per row, and the two ways
// to be wrong are both worth a test: saying yes to a page nobody owns, and
// refusing to answer for a request that failed to identify its caller.
func TestOwnerExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	owner := mustCreatePrincipal(t, s, c.ID, "Alice (Ranger)", "hash-of-alice")
	other := mustCreatePrincipal(t, s, c.ID, "Bob", "hash-of-bob")
	page := anotherPage(t, s, c.ID, "characters/aria")
	stranger := anotherPage(t, s, c.ID, "characters/bob")

	if err := s.ReplacePrincipalCharacters(ctx, owner.ID, []string{page.ID}); err != nil {
		t.Fatalf("ReplacePrincipalCharacters: %v", err)
	}

	tests := map[string]struct {
		principal string
		page      string
		want      bool
	}{
		"the owner, on the owned page":    {principal: owner.ID, page: page.ID, want: true},
		"the owner, on somebody else's":   {principal: other.ID, page: page.ID},
		"somebody else, on the page":      {principal: other.ID, page: stranger.ID},
		"a principal that does not exist": {principal: "no-such-principal", page: page.ID},
		// A request that failed to identify its caller. There is no principal, so
		// nobody owns the page, so the `dm-and-owner` branch is empty. An error
		// here would turn "not logged in" into a 500 for the one caller who must
		// not see the page.
		"no principal at all": {principal: "", page: page.ID},
		"no page at all":      {principal: owner.ID, page: ""},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := s.OwnerExists(ctx, tt.principal, tt.page)
			if err != nil {
				t.Fatalf("OwnerExists: %v", err)
			}
			if got != tt.want {
				t.Errorf("OwnerExists(%q, %q) = %t, want %t", tt.principal, tt.page, got, tt.want)
			}
		})
	}
}

// The audit log is the answer to "was this link ever used", and an entry that
// cannot be appended is an incident that cannot be investigated.
func TestAppendAudit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := newStore(t)
	c := mustCreateCampaign(t, s)
	owner := mustCreatePrincipal(t, s, c.ID, "Alice (Ranger)", "hash-of-alice")

	t.Run("an entry with no action is refused", func(t *testing.T) {
		if _, err := s.AppendAudit(ctx, domain.AuditEntry{CampaignID: c.ID}); err == nil {
			t.Error("an audit entry with no action was appended")
		}
	})

	t.Run("an entry from a plugin is fine", func(t *testing.T) {
		// The action column is deliberately open: ADR 0010 gives plugins no raw
		// database access, so an action nobody here emits is a plugin recording
		// something of its own, and refusing it would be the audit log deciding
		// what a plugin is allowed to say.
		if _, err := s.AppendAudit(ctx, domain.AuditEntry{
			CampaignID: c.ID,
			Action:     domain.AuditAction("plugin_spoiler_revealed"),
		}); err != nil {
			t.Errorf("an action this build does not know was refused: %v", err)
		}
	})

	t.Run("entries come back newest first, with their ids", func(t *testing.T) {
		first, err := s.AppendAudit(ctx, domain.AuditEntry{
			CampaignID:  c.ID,
			PrincipalID: owner.ID,
			Action:      domain.AuditPrincipalIssued,
			Detail:      "Alice (Ranger)",
		})
		if err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
		if first.ID == 0 {
			t.Error("the entry was appended with no id: the rowid is the store's to assign")
		}

		// The fixture clock advances a minute on every read, so a second append is
		// stamped a minute later: "newest first" is a real ordering here and not a
		// tie broken by the id.
		second, err := s.AppendAudit(ctx, domain.AuditEntry{
			CampaignID:  c.ID,
			PrincipalID: owner.ID,
			Action:      domain.AuditShareLinkUsed,
		})
		if err != nil {
			t.Fatalf("the second AppendAudit: %v", err)
		}

		entries, err := s.ListAudit(ctx, c.ID, 0)
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		if len(entries) < 2 {
			t.Fatalf("ListAudit returned %d entries, want at least 2", len(entries))
		}
		if entries[0].ID != second.ID {
			t.Errorf("the first entry is %d, want the newest (%d): the log is read as a story", entries[0].ID, second.ID)
		}
		if entries[0].Action != domain.AuditShareLinkUsed {
			t.Errorf("the first entry is %q, want %q", entries[0].Action, domain.AuditShareLinkUsed)
		}
		if entries[0].At.IsZero() || entries[0].At.Equal(entries[1].At) {
			t.Errorf("the two entries are stamped %v and %v, want distinct times", entries[0].At, entries[1].At)
		}
	})

	t.Run("an empty string is NULL, so there is one way to say nothing", func(t *testing.T) {
		entry, err := s.AppendAudit(ctx, domain.AuditEntry{
			CampaignID:  c.ID,
			PrincipalID: owner.ID,
			Action:      domain.AuditPrincipalRevoked,
			PageID:      "",
			Detail:      "",
		})
		if err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}

		entries, err := s.ListAudit(ctx, c.ID, 0)
		if err != nil {
			t.Fatalf("ListAudit: %v", err)
		}
		for _, got := range entries {
			if got.ID != entry.ID {
				continue
			}
			if got.PageID != "" || got.Detail != "" {
				t.Errorf("the entry came back as %+v, want empty strings for what was written as NULL", got)
			}
			return
		}
		t.Fatalf("the entry with id %d is not in the log", entry.ID)
	})
}

// Fixtures and helpers for this file.

// The shared fixtures build one campaign and one page, which is enough for a test
// about one of each and not enough for a test about a second campaign or a second
// page. These are those, and they are local rather than additions to the shared
// file because a shared "another page" helper with a default nobody overrides is
// a way for a test to pass without meaning anything.

// otherCampaign stores a second campaign, for the tests that need one and must be
// sure it is not the first.
func otherCampaign(t *testing.T, s *store.Store, slug string) domain.Campaign {
	t.Helper()

	other := campaign()
	other.Slug = domain.Slug(slug)
	other.Name = "Somewhere else"
	other.VaultDir = "vault/" + slug

	created, err := s.CreateCampaign(context.Background(), other)
	if err != nil {
		t.Fatalf("CreateCampaign(%q): %v", slug, err)
	}
	return created
}

// anotherPage stores a page at a path of the test's choosing.
func anotherPage(t *testing.T, s *store.Store, campaignID, path string) domain.Page {
	t.Helper()

	p := page(campaignID)
	p.Path = path
	p.Title = strings.ToUpper(path[:1]) + path[1:]

	created, err := s.UpsertPage(context.Background(), p)
	if err != nil {
		t.Fatalf("UpsertPage(%q): %v", path, err)
	}
	return created
}

// principal is a share link's row, with the token already hashed.
func principal(campaignID, label, tokenHash string) domain.Principal {
	return domain.Principal{
		CampaignID: campaignID,
		Label:      label,
		Role:       domain.RolePlayer,
		TokenHash:  tokenHash,
		TokenHint:  "a1b2",
	}
}

// mustCreatePrincipal stores a player and fails the test if it cannot.
func mustCreatePrincipal(t *testing.T, s *store.Store, campaignID, label, tokenHash string) domain.Principal {
	t.Helper()

	created, err := s.CreatePrincipal(context.Background(), principal(campaignID, label, tokenHash))
	if err != nil {
		t.Fatalf("CreatePrincipal(%q): %v", label, err)
	}
	return created
}

// liveSession is a session that authenticates, optionally at a given expiry.
//
// No id, because the store mints one and a test that supplied it would be testing
// that the store keeps what it is given rather than that a session works.
func liveSession(principalID string, expiry ...time.Time) domain.Session {
	until := testTime.Add(8 * time.Hour)
	if len(expiry) > 0 {
		until = expiry[0]
	}
	return domain.Session{PrincipalID: principalID, ExpiresAt: until}
}

// revokedAt reads when a principal was revoked, failing if it was not.
func revokedAt(t *testing.T, s *store.Store, id string) time.Time {
	t.Helper()

	got, ok, err := s.PrincipalByID(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("PrincipalByID(%q) = %t, %v", id, ok, err)
	}
	if !got.Revoked() {
		t.Fatalf("the principal %q does not report itself as revoked", id)
	}
	return got.RevokedAt
}

// liveSessions counts a principal's live sessions, which is the question both
// revocation tests are asking.
func liveSessions(t *testing.T, s *store.Store, id string) int {
	t.Helper()

	sessions, err := s.SessionsForPrincipal(context.Background(), id, testTime)
	if err != nil {
		t.Fatalf("SessionsForPrincipal(%q): %v", id, err)
	}
	return len(sessions)
}

// ownedPages is a principal's bindings as a set, for the tests that only care how
// many there are.
func ownedPages(t *testing.T, s *store.Store, id string) []string {
	t.Helper()

	owned, err := s.PrincipalCharacters(context.Background(), id)
	if err != nil {
		t.Fatalf("PrincipalCharacters(%q): %v", id, err)
	}
	return owned
}
