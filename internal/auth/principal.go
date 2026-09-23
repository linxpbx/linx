package auth

import (
	"context"
	"net/netip"
	"slices"

	"github.com/google/uuid"
)

// Principal types.
const (
	TypeAPIKey      = "api_key"
	TypeOAuthClient = "oauth_client"
	TypeUser        = "user"
	TypeSystem      = "system"
)

// Principal is an authenticated caller: tenant, role ceiling and effective
// scopes (docs/API.md §3). Every kind of credential ends up as one.
type Principal struct {
	Type string
	// ID is the key's, client's or user's id; "cli" for the server-side CLI.
	ID       string
	TenantID uuid.UUID
	Role     string
	// Scopes are already limited to Role's ceiling.
	Scopes []string
}

// Actor is how the principal appears in audit_log.actor.
func (p Principal) Actor() string { return p.Type + ":" + p.ID }

// Has reports whether the principal holds scope.
func (p Principal) Has(scope string) bool { return slices.Contains(p.Scopes, scope) }

// SystemPrincipal is the server-side CLI (`linx api-key ...`), run by
// someone with root on the server. It can grant any role and scope.
func SystemPrincipal(tenant uuid.UUID) Principal {
	return Principal{Type: TypeSystem, ID: "cli", TenantID: tenant, Role: RoleSystemAdmin, Scopes: Scopes}
}

type principalKey struct{}
type clientIPKey struct{}

// WithPrincipal returns ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFromContext returns the authenticated caller, if any.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// WithClientIP returns ctx carrying the caller's address (see ClientIPResolver).
func WithClientIP(ctx context.Context, ip netip.Addr) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

// ClientIPFromContext returns the caller's address; invalid if unknown.
func ClientIPFromContext(ctx context.Context) netip.Addr {
	ip, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return ip
}
