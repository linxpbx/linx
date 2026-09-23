package auth

import (
	"fmt"
	"slices"
	"strings"
)

// Scopes is every scope a key or client can hold, `resource:read` /
// `resource:write` (docs/API.md §3). A scope used in api/openapi.yaml must be
// listed here (tested).
var Scopes = []string{
	"alerts:read", "alerts:write",
	"api_keys:read", "api_keys:write",
	"audit:read",
	"calls:control",
	"extensions:read", "extensions:write",
	"oauth_clients:read", "oauth_clients:write",
	"outbound_allowlist:read", "outbound_allowlist:write",
	"recordings:read",
	"transcripts:read",
	"webhooks:read", "webhooks:write",
}

// sensitiveScopes are never included in "all"; they must be named. Creating
// keys or clients is as powerful as holding every scope, so both are here.
// outbound_allowlist:write opens the server's LAN to outbound requests.
var sensitiveScopes = []string{
	"api_keys:write", "calls:control", "oauth_clients:write", "outbound_allowlist:write", "recordings:read", "transcripts:read",
}

// ScopeAll asks for every non-sensitive scope the role allows.
const ScopeAll = "all"

// Role names (docs/API.md §3).
const (
	RoleSystemAdmin = "system_admin"
	RoleAdmin       = "admin"
	RoleUser        = "user"
	RoleReporter    = "reporter"
)

// Roles lists every role.
var Roles = []string{RoleSystemAdmin, RoleAdmin, RoleUser, RoleReporter}

// roleCeilings maps each role to the scopes it may ever hold. A key's
// effective scopes are its own scopes limited to this ceiling, so narrowing
// a role here takes effect on existing keys too.
var roleCeilings = map[string][]string{
	RoleSystemAdmin: Scopes,
	RoleAdmin:       Scopes,
	RoleReporter:    {"alerts:read", "extensions:read", "webhooks:read"},
	RoleUser:        {"extensions:read"},
}

// grantableRoles is which roles each role may give a new key or client:
// its own, or one below it.
var grantableRoles = map[string][]string{
	RoleSystemAdmin: Roles,
	RoleAdmin:       {RoleAdmin, RoleUser, RoleReporter},
	RoleReporter:    {RoleReporter},
	RoleUser:        {RoleUser},
}

// ValidRole reports whether role is a known role.
func ValidRole(role string) bool { return slices.Contains(Roles, role) }

// ValidScope reports whether s is a known scope.
func ValidScope(s string) bool { return slices.Contains(Scopes, s) }

// Sensitive reports whether s must always be granted by name.
func Sensitive(s string) bool { return slices.Contains(sensitiveScopes, s) }

// CanGrantRole reports whether a caller with role caller may create a key
// or client with role target.
func CanGrantRole(caller, target string) bool {
	return slices.Contains(grantableRoles[caller], target)
}

// Effective limits scopes to the role's ceiling, dropping unknown scopes.
// The result is sorted and has no duplicates.
func Effective(scopes []string, role string) []string {
	ceiling := roleCeilings[role]
	out := []string{}
	for _, s := range scopes {
		if slices.Contains(ceiling, s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

// ScopeError explains why a requested scope list was refused. Code is the
// stable API error code.
type ScopeError struct {
	Code, Detail string
}

func (e *ScopeError) Error() string { return e.Detail }

// GrantScopes resolves the scopes requested for a new key or client with
// role, created by a caller holding callerScopes. "all" expands to every
// non-sensitive scope that both the role and the caller have; any scope named
// explicitly must be known, within the role's ceiling and held by the caller.
// A new credential can never exceed the one that created it.
func GrantScopes(requested []string, role string, callerScopes []string) ([]string, error) {
	if len(requested) == 0 {
		return nil, &ScopeError{"scopes_required", `Choose at least one scope, or "all".`}
	}
	ceiling := roleCeilings[role]
	var out []string
	for _, s := range requested {
		switch {
		case s == ScopeAll:
			for _, c := range ceiling {
				if !Sensitive(c) && slices.Contains(callerScopes, c) {
					out = append(out, c)
				}
			}
		case !ValidScope(s):
			return nil, &ScopeError{"scope_unknown", fmt.Sprintf("%q is not a scope. Valid scopes: %s.", s, strings.Join(Scopes, ", "))}
		case !slices.Contains(ceiling, s):
			return nil, &ScopeError{"scope_exceeds_role", fmt.Sprintf("The %s role can't hold the %s scope.", role, s)}
		case !slices.Contains(callerScopes, s):
			return nil, &ScopeError{"scope_exceeds_caller", fmt.Sprintf("You can't give the %s scope because you don't hold it yourself.", s)}
		default:
			out = append(out, s)
		}
	}
	out = Effective(out, role)
	if len(out) == 0 {
		return nil, &ScopeError{"scopes_required", "That leaves no scopes. Name at least one scope you hold."}
	}
	return out, nil
}
