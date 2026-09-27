// Package sso is company sign-in's provider side (ADR-052, docs/ADMIN.md
// §6): the OpenID Connect providers an admin sets up (Google, Microsoft,
// Authentik, Keycloak, any other), and the client that sends people to them
// and checks what comes back. Linking provider accounts to people and
// signing them in is internal/auth's (company.go), which uses Client
// through auth.CompanyProviders.
package sso

import (
	"context"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Provider kinds: the templates of docs/ADMIN.md §6. The kind decides the
// default name and issuer and how a verified email is read from the ID
// token (Microsoft's differs from the standard claim).
const (
	KindGoogle    = "google"
	KindMicrosoft = "microsoft"
	KindAuthentik = "authentik"
	KindKeycloak  = "keycloak"
	KindOIDC      = "oidc"
)

// GoogleIssuer is Google's one issuer, for every Gmail and Workspace
// account.
const GoogleIssuer = "https://accounts.google.com"

// CallbackPath is where every provider sends the browser back: one address
// for all of them, registered at each provider as the redirect URI.
const CallbackPath = "/api/v1/sso/callback"

// RedirectURI is the address to register at the provider for domain.
func RedirectURI(domain string) string { return "https://meet." + domain + CallbackPath }

// Provider is one company sign-in provider. ClientSecretEnc is sealed with
// ADR-030's key, row id SealID(ID); nil when the provider has no secret (a
// public client relying on PKCE alone).
type Provider struct {
	ID, TenantID    uuid.UUID
	Kind, Name      string
	Issuer          string
	ClientID        string
	ClientSecretEnc []byte
	// Enabled: usable at all. Shown: its button is on the sign-in page.
	Enabled, Shown       bool
	Position             int
	Version              int
	CreatedAt, UpdatedAt time.Time
}

// SealID is the client secret's row id for dbsecret.
func SealID(id uuid.UUID) string { return "sso_provider:" + id.String() }

// Store is the database access providers need (internal/store implements
// it). Lookups return auth.ErrNotFound; a clashing name auth.ErrDuplicate;
// an update of a stale version auth.ErrVersionChanged.
type Store interface {
	CreateSSOProvider(ctx context.Context, p Provider, audit auth.AuditEntry) error
	SSOProvider(ctx context.Context, tenant, id uuid.UUID) (Provider, error)
	// ListSSOProviders lists a tenant's providers in button order.
	ListSSOProviders(ctx context.Context, tenant uuid.UUID) ([]Provider, error)
	UpdateSSOProvider(ctx context.Context, p Provider, audit auth.AuditEntry) (Provider, error)
	// DeleteSSOProvider removes a provider and every link to it.
	DeleteSSOProvider(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error
}
