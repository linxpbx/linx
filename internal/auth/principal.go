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
	// Pending is true for a signed-in person's session still waiting on its
	// authenticator code or its first-run MFA enrollment (docs/WEB.md §4).
	// A pending principal holds no scopes, so it can only reach operations
	// that need no scope; those handlers decide what it may actually do.
	Pending bool
	// DeviceID is set when the caller is a phone using its device token
	// (docs/PHASE2.md §4): the principal is still the person whose phone it
	// is, with the ordinary person's role and scopes whatever their own
	// role is, so an app can never reach the admin area. Audit entries name
	// the device.
	DeviceID *uuid.UUID
	// AdminNetworkRestricted is true when this session's role was limited to
	// RoleUser's scopes because "only from my home/office network" is on and
	// the request didn't come from one of the allowed networks
	// (docs/ADMIN.md §3); /me uses it to explain why the admin area is
	// missing. Never set for API keys or OAuth clients (they have their own
	// AllowedIPs).
	AdminNetworkRestricted bool
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
