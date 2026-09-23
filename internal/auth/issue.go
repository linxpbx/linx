package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

// Credential lifetimes (docs/API.md §3).
const (
	DefaultLifetime = 365 * 24 * time.Hour
	MaxLifetime     = 2 * 365 * 24 * time.Hour
	maxAllowedIPs   = 20
	maxNameLen      = 100
)

// CredentialRequest asks for a new API key or OAuth client.
type CredentialRequest struct {
	Kind string
	Name string
	// Role defaults to the caller's role.
	Role string
	// Scopes may include "all" (every non-sensitive scope the role and the
	// caller have).
	Scopes []string
	// ExpiresAt defaults to DefaultLifetime from now.
	ExpiresAt  *time.Time
	AllowedIPs []string
}

// NewCredential checks a request against the caller's own role and scopes
// and builds the credential to store. secret is shown to the caller once
// (the full API key, or the client secret) and never stored.
func NewCredential(caller Principal, req CredentialRequest, now time.Time) (cred Credential, secret string, err error) {
	now = now.UTC().Truncate(time.Microsecond)
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > maxNameLen || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Credential{}, "", badRequest("name_invalid", fmt.Sprintf("Give it a name of 1 to %d characters, without line breaks.", maxNameLen))
	}

	role := req.Role
	if role == "" {
		role = caller.Role
	}
	if !ValidRole(role) {
		return Credential{}, "", badRequest("role_invalid", fmt.Sprintf("%q is not a role. Roles: %s.", role, strings.Join(Roles, ", ")))
	}
	if !CanGrantRole(caller.Role, role) {
		return Credential{}, "", &apihttp.Error{Status: http.StatusForbidden, Code: "role_exceeds_caller",
			Detail: fmt.Sprintf("You can't create a credential with the %s role.", role)}
	}

	scopes, err := GrantScopes(req.Scopes, role, caller.Scopes)
	if err != nil {
		var se *ScopeError
		if errors.As(err, &se) {
			status := http.StatusBadRequest
			if se.Code == "scope_exceeds_caller" {
				status = http.StatusForbidden
			}
			return Credential{}, "", &apihttp.Error{Status: status, Code: se.Code, Detail: se.Detail}
		}
		return Credential{}, "", err
	}

	expires := now.Add(DefaultLifetime)
	if req.ExpiresAt != nil {
		expires = req.ExpiresAt.UTC().Truncate(time.Microsecond)
		if !expires.After(now) {
			return Credential{}, "", badRequest("expiry_invalid", "The expiry date must be in the future.")
		}
		if expires.Sub(now) > MaxLifetime {
			return Credential{}, "", badRequest("expiry_invalid", "A credential can last at most 2 years.")
		}
	}

	if len(req.AllowedIPs) > maxAllowedIPs {
		return Credential{}, "", badRequest("allowed_ips_invalid", fmt.Sprintf("List at most %d addresses or ranges.", maxAllowedIPs))
	}
	var allowed []netip.Prefix
	for _, s := range req.AllowedIPs {
		p, err := ParseAllowedIP(strings.TrimSpace(s))
		if err != nil {
			return Credential{}, "", badRequest("allowed_ips_invalid", fmt.Sprintf("%q is not an IP address or range (like 203.0.113.7 or 203.0.113.0/24).", s))
		}
		allowed = append(allowed, p)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Credential{}, "", err
	}
	cred = Credential{
		Kind:       req.Kind,
		ID:         id,
		TenantID:   caller.TenantID,
		Name:       name,
		Role:       role,
		Scopes:     scopes,
		AllowedIPs: allowed,
		CreatedBy:  caller.Actor(),
		CreatedAt:  now,
		ExpiresAt:  expires,
	}
	switch req.Kind {
	case TypeAPIKey:
		var raw string
		cred.PublicID, raw, secret = NewAPIKey()
		cred.SecretHash = HashSecret(raw)
	case TypeOAuthClient:
		var raw string
		cred.PublicID = NewPublicID()
		raw, secret = NewClientSecret()
		cred.SecretHash = HashSecret(raw)
	default:
		return Credential{}, "", fmt.Errorf("unknown credential kind %q", req.Kind)
	}
	return cred, secret, nil
}

func badRequest(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusBadRequest, Code: code, Detail: detail}
}
