package auth

import (
	"context"
	"time"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// Backend is the store, as this package needs it.
//
// It is declared here, by the consumer, and it is three methods rather than the
// whole store: an interface here that is a copy of *store.Store would be a second
// declaration of the store's surface to keep in step, and the point of a small
// interface is that it is small enough to be obviously the right shape. Adding a
// method the store does not have is a compile error here, which is the property
// that makes this worth having.
type Backend interface {
	CreatePrincipal(ctx context.Context, p domain.Principal) (domain.Principal, error)
	GetCampaign(ctx context.Context, id string) (domain.Campaign, error)
	PrincipalByTokenHash(ctx context.Context, tokenHash string) (domain.Principal, bool, error)
	PrincipalByID(ctx context.Context, id string) (domain.Principal, bool, error)
	RevokePrincipal(ctx context.Context, id string) error
	CreateSession(ctx context.Context, sess domain.Session) (domain.Session, error)
	SessionByID(ctx context.Context, id string) (domain.Session, bool, error)
	TouchSession(ctx context.Context, id string, expiresAt time.Time) error
	DeleteSession(ctx context.Context, id string) error
	EndSessions(ctx context.Context, principalID string) (int, error)
	TouchPrincipal(ctx context.Context, id string) error
	SetPrincipalRole(ctx context.Context, id string, role domain.Role) error
	ReplacePrincipalCharacters(ctx context.Context, principalID string, pageIDs []string) error
	AppendAudit(ctx context.Context, entry domain.AuditEntry) (domain.AuditEntry, error)
}
