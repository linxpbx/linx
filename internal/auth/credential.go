package auth

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/google/uuid"
)

// Credential is a stored API key (Kind TypeAPIKey) or OAuth client (Kind
// TypeOAuthClient). Only a hash of its secret is kept.
type Credential struct {
	Kind       string
	ID         uuid.UUID
	PublicID   string
	TenantID   uuid.UUID
	Name       string
	SecretHash []byte
	Role       string
	Scopes     []string
	// AllowedIPs limits where it can be used from; empty allows anywhere.
	AllowedIPs []netip.Prefix
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastUsedAt *time.Time
	LastUsedIP *netip.Addr
	RevokedAt  *time.Time
}

// Principal is the caller this credential authenticates as.
func (c Credential) Principal() Principal {
	return Principal{
		Type:     c.Kind,
		ID:       c.ID.String(),
		TenantID: c.TenantID,
		Role:     c.Role,
		Scopes:   Effective(c.Scopes, c.Role),
	}
}

// ErrNotFound is returned by Store lookups that find nothing.
var ErrNotFound = errors.New("not found")

// AuditEntry is one audit_log row (docs/API.md §2).
type AuditEntry struct {
	TenantID *uuid.UUID
	Actor    string
	IP       netip.Addr
	Action   string
	Target   string
	Result   string
	Detail   map[string]any
}

// Audit results.
const (
	ResultOK     = "ok"
	ResultDenied = "denied"
	ResultFailed = "failed"
)

// Store is the database access authentication needs.
type Store interface {
	CredentialByPublicID(ctx context.Context, kind, publicID string) (Credential, error)
	CredentialByID(ctx context.Context, kind string, id uuid.UUID) (Credential, error)
	// TouchCredential records the last use of a credential.
	TouchCredential(ctx context.Context, kind string, id uuid.UUID, ip netip.Addr, at time.Time) error
	TokenRevoked(ctx context.Context, jti string) (bool, error)
	Audit(ctx context.Context, e AuditEntry) error
}
