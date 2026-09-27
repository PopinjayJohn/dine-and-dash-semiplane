// Package testsuite holds the contract every Store implementation must
// satisfy, as a suite that runs against each one.
//
// It exists because a store is the one place where a quiet difference between
// two implementations turns into a leak. A store that returns an archived page,
// or orders a list differently, or numbers revisions from zero, is not "a bit
// inconsistent": it is a store that disagrees with the vault, and the vault is
// the source of truth (ADR 0001). Writing the expectations once, here, is what
// makes adding a second implementation a matter of pointing this suite at it.
//
// # What the contract is
//
// The API interface below, the two sentinels, and the behaviour asserted by
// Store. The interface is declared here rather than in internal/store because
// the suite is the consumer: an implementation satisfies this contract by
// having these methods, not by being the concrete type the store happens to be
// today. Adding a method to internal/store is therefore a change to this file
// too, which is the point.
package testsuite

import (
	"context"
	"testing"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/store"
)

// API is everything a store must be able to do. A method here that a
// particular implementation cannot honour is a bug in the implementation, not
// an unnecessary method.
type API interface {
	// Campaigns.
	CreateCampaign(ctx context.Context, c domain.Campaign) (domain.Campaign, error)
	GetCampaign(ctx context.Context, id string) (domain.Campaign, error)
	CampaignBySlug(ctx context.Context, slug domain.Slug) (domain.Campaign, error)
	ListCampaigns(ctx context.Context) ([]domain.Campaign, error)
	UpdateCampaign(ctx context.Context, c domain.Campaign) (domain.Campaign, error)

	// Pages.
	UpsertPage(ctx context.Context, p domain.Page) (domain.Page, error)
	GetPage(ctx context.Context, campaignID, path string) (domain.Page, error)
	GetPageByID(ctx context.Context, id string) (domain.Page, error)
	ListPages(ctx context.Context, campaignID string) ([]domain.Page, error)
	DeletePage(ctx context.Context, id string) error

	// Revisions.
	AppendRevision(ctx context.Context, r domain.PageRevision) (domain.PageRevision, error)
	GetRevision(ctx context.Context, pageID string, rev int) (domain.PageRevision, error)
	ListRevisions(ctx context.Context, pageID string) ([]domain.PageRevision, error)

	// The link graph.
	ReplaceLinks(ctx context.Context, srcPageID string, links []domain.PageLink) error
	LinksFrom(ctx context.Context, pageID string) ([]domain.PageLink, error)
	Backlinks(ctx context.Context, pageID string) ([]domain.PageLink, error)
	LinksToPath(ctx context.Context, path string) ([]domain.PageLink, error)

	// The names a page answers to, which is what a wiki link resolves by after
	// the exact path. Added in M4, when the renderer needed a resolver and the
	// projection had to answer one.
	ReplacePageAliases(ctx context.Context, pageID string, aliases []string) error
	PageTargets(ctx context.Context, pageID string) (map[string][]string, error)
	FindPageByAlias(ctx context.Context, campaignID, alias string) (domain.Page, bool, error)
	FindPageByName(ctx context.Context, campaignID, name string) (domain.Page, bool, error)

	// The two search indexes, added in M5. Both halves of a page's text are
	// handed over rather than derived, because working out which half a
	// principal may be shown is an access-control decision and this contract is
	// about a projection. A store that derived the split itself would be hiding
	// the ACL somewhere invariant 3 says it may not be.
	ReplacePageIndex(ctx context.Context, entry store.IndexEntry) error
	PageIndexMatches(ctx context.Context, entry store.IndexEntry) (bool, error)
	DeletePageIndex(ctx context.Context, pageID string) error

	// Share links, sessions, the audit log and character bindings, added in M6.
	//
	// The shape of this group is the contract, not the signatures: a link is
	// found by the *hash* of its token and never by the token, a revocation ends
	// every session in the same breath, and a binding is a replace. A store that
	// found a principal by token would need the plaintext, and a store whose
	// revoke left a session behind would leave a player logged in after the DM
	// revoked them.
	CreatePrincipal(ctx context.Context, p domain.Principal) (domain.Principal, error)
	PrincipalByTokenHash(ctx context.Context, tokenHash string) (domain.Principal, bool, error)
	PrincipalByID(ctx context.Context, id string) (domain.Principal, bool, error)
	ListPrincipals(ctx context.Context, campaignID string) ([]domain.Principal, error)
	RevokePrincipal(ctx context.Context, id string) error
	RevokeAllPrincipals(ctx context.Context, campaignID string) (int, error)
	TouchPrincipal(ctx context.Context, id string) error
	SetPrincipalRole(ctx context.Context, id string, role domain.Role) error

	CreateSession(ctx context.Context, sess domain.Session) (domain.Session, error)
	SessionByID(ctx context.Context, id string) (domain.Session, bool, error)
	SessionsForPrincipal(ctx context.Context, principalID string, now time.Time) ([]domain.Session, error)
	DeleteSession(ctx context.Context, id string) error
	TouchSession(ctx context.Context, id string, expiresAt time.Time) error
	PurgeExpiredSessions(ctx context.Context, now time.Time) (int, error)

	AppendAudit(ctx context.Context, entry domain.AuditEntry) (domain.AuditEntry, error)
	ListAudit(ctx context.Context, campaignID string, limit int) ([]domain.AuditEntry, error)

	ReplacePrincipalCharacters(ctx context.Context, principalID string, pageIDs []string) error
	PrincipalCharacters(ctx context.Context, principalID string) ([]string, error)
	CharacterOwners(ctx context.Context, pageID string) ([]string, error)
	OwnerExists(ctx context.Context, principalID, pageID string) (bool, error)
}

// The two errors a caller must be able to recognise without reading a message.
// An implementation returns these or wraps them; a caller asking with
// errors.Is has to be able to say what happened.
var (
	NotFound = store.ErrNotFound
	Conflict = store.ErrConflict
)

// Factory builds a store for one test, already migrated and ready to use. It
// registers its own cleanup with the *testing.T it is given, so a subtest gets
// its own store and its own cleanup.
type Factory func(t *testing.T) API
