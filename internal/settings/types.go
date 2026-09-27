// Package settings is the admin portal's tenant-wide configuration
// (docs/ADMIN.md §4): the numbering plan (extension digits and ranges),
// site kind, Simple mode, and where admins may sign in from. Backed by
// pbx_setting, the single-row table 1D added country to. Caller mistakes
// come back as *apihttp.Error.
package settings

import (
	"context"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Range kinds (docs/ADMIN.md §4). The plan may add more later (1F's ring
// groups already have a place: "groups").
const (
	RangePeople   = "people"
	RangeGroups   = "groups"
	RangeReserved = "reserved"
)

// Range is one span of the numbering plan, digits long, inclusive.
type Range struct {
	Kind string `json:"kind"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

// Settings is the tenant's admin-portal configuration.
type Settings struct {
	Country                      string
	ExtensionDigits              int
	ExtensionRanges              []Range
	SiteKind                     string
	SimpleMode                   bool
	AdminNetworkRestricted       bool
	AdminNetworks                []netip.Prefix
	DefaultCallPermissionLevelID *uuid.UUID
	SetupStep                    int
	SetupCompletedAt             *time.Time
	// CompanySignInRequired is "people must use company sign-in"
	// (docs/ADMIN.md §6): passwords stop working for everyone but system
	// admins.
	CompanySignInRequired bool
}

// Store is the database access the settings service needs (internal/store
// implements it).
type Store interface {
	Settings(ctx context.Context) (Settings, error)
	UpdateSettings(ctx context.Context, s Settings, audit auth.AuditEntry) (Settings, error)
	// SetDefaultCallPermissionLevel points every new extension without an
	// explicit level at id (docs/ADMIN.md §4).
	SetDefaultCallPermissionLevel(ctx context.Context, id uuid.UUID, audit auth.AuditEntry) error
	// SetSetupStep records the wizard's resume point; completedAt, once set,
	// stays set.
	SetSetupStep(ctx context.Context, step int, completedAt *time.Time) error

	// NextFreeExtensionNumber returns the lowest unused number, digits long,
	// in [from, to], or "" if the range is full.
	NextFreeExtensionNumber(ctx context.Context, tenant uuid.UUID, digits, from, to int) (string, error)
	// RangeReservedNumber returns the first number in [from, to] (digits
	// long) that's reserved in country (an emergency/short number, or one
	// that looks like a prefixed outside number), and why; "", "" if none
	// is.
	RangeReservedNumber(ctx context.Context, country string, digits, from, to int) (number, reason string, err error)
	// ExtensionsWithOtherLength lists (up to a handful of) extension numbers
	// that aren't digits digits long: what a digit-count change would have
	// to renumber first.
	ExtensionsWithOtherLength(ctx context.Context, tenant uuid.UUID, digits int) ([]string, error)
	// ExtensionsOutsideRanges lists (up to a handful of) extension numbers
	// that fall outside every range: allowed, but worth a warning
	// (docs/ADMIN.md §4).
	ExtensionsOutsideRanges(ctx context.Context, tenant uuid.UUID, ranges []Range) ([]string, error)
}
