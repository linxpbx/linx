package settings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/numbering"
)

// Service is what the API's /settings, /setup and /numbering/next endpoints
// do.
type Service struct {
	Store Store
	Now   func() time.Time
	// PhoneNetworks are the phone networks from setup (LINX_SIP_NETWORKS):
	// always allowed for admin sign-in, on top of admin_networks
	// (docs/ADMIN.md §3: "the phone networks from setup, plus addresses the
	// admin adds").
	PhoneNetworks []netip.Prefix
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

func audit(ctx context.Context, action string) (auth.Principal, auth.AuditEntry, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.AuditEntry{}, errNoPrincipal
	}
	return p, auth.AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: action, Target: "settings", Result: auth.ResultOK,
	}, nil
}

// AdminAccess is auth.AdminNetworkChecker's requirement (docs/ADMIN.md §3):
// wiring *Service into Authenticator.Networks needs no adapter type, since
// Go interfaces are structural. Reads the store directly (not Get, which
// needs a principal): this runs in the authentication middleware, before
// one exists.
func (s *Service) AdminAccess(ctx context.Context) (bool, []netip.Prefix, error) {
	cur, err := s.Store.Settings(ctx)
	if err != nil {
		return false, nil, err
	}
	if !cur.AdminNetworkRestricted {
		return false, nil, nil
	}
	networks := make([]netip.Prefix, 0, len(cur.AdminNetworks)+len(s.PhoneNetworks))
	networks = append(networks, cur.AdminNetworks...)
	networks = append(networks, s.PhoneNetworks...)
	return true, networks, nil
}

// Get returns the tenant's settings.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	if _, ok := auth.PrincipalFromContext(ctx); !ok {
		return Settings{}, errNoPrincipal
	}
	return s.Store.Settings(ctx)
}

// Patch is a JSON Merge Patch of the settings; nil fields stay as they are.
type Patch struct {
	Country                *string
	ExtensionDigits        *int
	ExtensionRanges        *[]Range
	SiteKind               *string
	SimpleMode             *bool
	AdminNetworkRestricted *bool
	// AdminNetworks are parsed here (not by the caller), like a credential's
	// AllowedIPs (internal/auth.ParseAllowedIP).
	AdminNetworks *[]string
	// CompanySignInRequired needs sso:write as well as settings:write.
	CompanySignInRequired *bool
}

// Update applies patch, validating the numbering plan against country
// (docs/ADMIN.md §4): a digit-count change is refused while an extension of
// another length exists, and every range must fit, not overlap, and avoid
// the country's reserved numbers.
func (s *Service) Update(ctx context.Context, patch Patch) (Settings, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Settings{}, errNoPrincipal
	}
	cur, err := s.Store.Settings(ctx)
	if err != nil {
		return Settings{}, err
	}
	// "Where admins may sign in from" and "people must use company sign-in"
	// need a fresh confirmation even in an
	// already-signed-in session (docs/ADMIN.md §7).
	if patch.CompanySignInRequired != nil && !p.Has("sso:write") {
		return Settings{}, &apihttp.Error{Status: http.StatusForbidden, Code: "scope_missing",
			Detail: "Changing \"people must use company sign-in\" also needs the sso:write scope."}
	}
	if patch.AdminNetworkRestricted != nil || patch.AdminNetworks != nil || patch.CompanySignInRequired != nil {
		if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
			return Settings{}, err
		}
	}
	changes := map[string]any{}

	country := cur.Country
	if patch.Country != nil {
		if _, ok := numbering.Countries[*patch.Country]; !ok {
			return Settings{}, invalid("country_invalid", fmt.Sprintf("%q isn't a country Linx can be set up in.", *patch.Country))
		}
		country = *patch.Country
	}

	digits := cur.ExtensionDigits
	if patch.ExtensionDigits != nil {
		digits = *patch.ExtensionDigits
		if digits < 2 || digits > 6 {
			return Settings{}, invalid("extension_digits_invalid", "Extension numbers are 2 to 6 digits.")
		}
		if digits != cur.ExtensionDigits {
			offenders, err := s.Store.ExtensionsWithOtherLength(ctx, p.TenantID, digits)
			if err != nil {
				return Settings{}, err
			}
			if len(offenders) > 0 {
				return Settings{}, invalid("extension_digits_in_use",
					fmt.Sprintf("Renumber these extensions to %d digits first: %s.", digits, joinNumbers(offenders)))
			}
		}
	}

	ranges := cur.ExtensionRanges
	if patch.ExtensionRanges != nil {
		ranges = *patch.ExtensionRanges
	}
	if patch.ExtensionRanges != nil || patch.ExtensionDigits != nil || patch.Country != nil {
		if err := s.validateRanges(ctx, country, digits, ranges); err != nil {
			return Settings{}, err
		}
	}

	if patch.Country != nil {
		cur.Country = country
		changes["country"] = country
	}
	if patch.ExtensionDigits != nil {
		cur.ExtensionDigits = digits
		changes["extension_digits"] = digits
	}
	if patch.ExtensionRanges != nil {
		cur.ExtensionRanges = ranges
		changes["extension_ranges"] = ranges
	}
	if patch.SiteKind != nil {
		if *patch.SiteKind != "" && *patch.SiteKind != "home" && *patch.SiteKind != "business" {
			return Settings{}, invalid("site_kind_invalid", `site_kind must be "home" or "business".`)
		}
		cur.SiteKind = *patch.SiteKind
		changes["site_kind"] = cur.SiteKind
	}
	if patch.SimpleMode != nil {
		cur.SimpleMode = *patch.SimpleMode
		changes["simple_mode"] = cur.SimpleMode
	}
	if patch.AdminNetworkRestricted != nil {
		cur.AdminNetworkRestricted = *patch.AdminNetworkRestricted
		changes["admin_network_restricted"] = cur.AdminNetworkRestricted
	}
	if patch.AdminNetworks != nil {
		if len(*patch.AdminNetworks) > 50 {
			return Settings{}, invalid("admin_networks_invalid", "List at most 50 addresses or networks.")
		}
		parsed := make([]netip.Prefix, 0, len(*patch.AdminNetworks))
		for _, raw := range *patch.AdminNetworks {
			p, err := auth.ParseAllowedIP(raw)
			if err != nil {
				return Settings{}, invalid("admin_networks_invalid", fmt.Sprintf("%q isn't an address or network.", raw))
			}
			parsed = append(parsed, p)
		}
		cur.AdminNetworks = parsed
		changes["admin_networks"] = "changed"
	}
	if patch.CompanySignInRequired != nil {
		cur.CompanySignInRequired = *patch.CompanySignInRequired
		changes["company_sign_in_required"] = cur.CompanySignInRequired
	}
	if cur.AdminNetworkRestricted && len(cur.AdminNetworks) == 0 {
		return Settings{}, invalid("admin_networks_required",
			"Add at least one address or network before turning on \"only from my home/office network\" (otherwise no admin could sign in as one).")
	}

	_, a, err := audit(ctx, "settings.update")
	if err != nil {
		return Settings{}, err
	}
	a.Detail = changes
	out, err := s.Store.UpdateSettings(ctx, cur, a)
	if err != nil {
		return Settings{}, err
	}
	return out, nil
}

// validateRanges checks ranges fit digits digits, don't overlap, and avoid
// country's reserved numbers (docs/ADMIN.md §4: "the API refuses overlaps,
// numbers of another length and ranges containing the country's
// emergency/short codes").
func (s *Service) validateRanges(ctx context.Context, country string, digits int, ranges []Range) error {
	if len(ranges) == 0 {
		return invalid("range_invalid", "Give at least one range.")
	}
	max := pow10(digits) - 1
	sorted := append([]Range(nil), ranges...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].From < sorted[j].From })
	for i, r := range sorted {
		if r.Kind == "" {
			return invalid("range_invalid", "Every range needs a kind.")
		}
		if r.From < 0 || r.To > max || r.From > r.To {
			return invalid("range_invalid", fmt.Sprintf("The %s range must fit in %d digits (0 to %d).", r.Kind, digits, max))
		}
		if i > 0 && r.From <= sorted[i-1].To {
			return invalid("range_invalid", fmt.Sprintf("The %s and %s ranges overlap.", sorted[i-1].Kind, r.Kind))
		}
		number, reason, err := s.Store.RangeReservedNumber(ctx, country, digits, r.From, r.To)
		if err != nil {
			return err
		}
		if number != "" {
			return invalid("range_invalid", fmt.Sprintf("The %s range includes %s, which is reserved (%s).", r.Kind, number, reason))
		}
	}
	return nil
}

func pow10(n int) int {
	v := 1
	for range n {
		v *= 10
	}
	return v
}

func joinNumbers(nums []string) string {
	out := ""
	for i, n := range nums {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}

// NextNumber returns the next free number in the people range.
func (s *Service) NextNumber(ctx context.Context) (string, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return "", errNoPrincipal
	}
	cur, err := s.Store.Settings(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range cur.ExtensionRanges {
		if r.Kind == RangePeople {
			n, err := s.Store.NextFreeExtensionNumber(ctx, p.TenantID, cur.Country, cur.ExtensionDigits, r.From, r.To)
			if err != nil {
				return "", err
			}
			if n == "" {
				return "", invalid("no_numbers_free", "Every number in the people range is used. Widen it in Settings first.")
			}
			return n, nil
		}
	}
	return "", invalid("range_missing", "There is no people range in the numbering plan.")
}

// SetDefaultCallPermissionLevel points every new extension without an
// explicit level at id (docs/ADMIN.md §4: the setup wizard's "Everyone"
// level).
func (s *Service) SetDefaultCallPermissionLevel(ctx context.Context, id uuid.UUID) error {
	_, a, err := audit(ctx, "settings.default_call_permission_level")
	if err != nil {
		return err
	}
	a.Detail = map[string]any{"call_permission_level_id": id}
	return s.Store.SetDefaultCallPermissionLevel(ctx, id, a)
}

// SetSetupStep advances the wizard's resume point; completedAt, once set by
// a caller finishing the wizard, is never cleared again by a later step.
func (s *Service) SetSetupStep(ctx context.Context, step int, completedAt *time.Time) error {
	if step < 1 || step > 7 {
		return invalid("step_invalid", "The setup wizard has steps 1 to 7.")
	}
	return s.Store.SetSetupStep(ctx, step, completedAt)
}
