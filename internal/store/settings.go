package store

import (
	"context"
	"errors"
	"net/netip"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/settings"
	"linxpbx.com/linx/internal/webhook"
)

var _ settings.Store = (*Store)(nil)

const settingsColumns = `country, extension_digits, extension_ranges, site_kind, simple_mode,
	admin_network_restricted, admin_networks, default_call_permission_level_id, setup_step, setup_completed_at,
	company_sign_in_required`

func scanSettings(row pgx.Row) (settings.Settings, error) {
	var out settings.Settings
	err := row.Scan(&out.Country, &out.ExtensionDigits, &out.ExtensionRanges, &out.SiteKind, &out.SimpleMode,
		&out.AdminNetworkRestricted, &out.AdminNetworks, &out.DefaultCallPermissionLevelID, &out.SetupStep, &out.SetupCompletedAt,
		&out.CompanySignInRequired)
	return out, err
}

func (s *Store) Settings(ctx context.Context) (settings.Settings, error) {
	return scanSettings(s.pool.QueryRow(ctx, `SELECT `+settingsColumns+` FROM pbx_setting`))
}

func (s *Store) UpdateSettings(ctx context.Context, in settings.Settings, audit auth.AuditEntry) (settings.Settings, error) {
	var out settings.Settings
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSettings(tx.QueryRow(ctx, `UPDATE pbx_setting SET
				country = $1, extension_digits = $2, extension_ranges = $3, site_kind = $4, simple_mode = $5,
				admin_network_restricted = $6, admin_networks = $7, company_sign_in_required = $8
			RETURNING `+settingsColumns,
			in.Country, in.ExtensionDigits, in.ExtensionRanges, in.SiteKind, in.SimpleMode,
			in.AdminNetworkRestricted, orEmpty(in.AdminNetworks), in.CompanySignInRequired))
		if err != nil {
			return err
		}
		if audit.TenantID != nil {
			ev, err := webhook.NewEvent(*audit.TenantID, "settings.updated", map[string]any{
				"country": out.Country, "extension_digits": out.ExtensionDigits, "site_kind": out.SiteKind,
				"simple_mode": out.SimpleMode, "admin_network_restricted": out.AdminNetworkRestricted,
				"company_sign_in_required": out.CompanySignInRequired,
			}, time.Now())
			if err != nil {
				return err
			}
			if err := insertEvent(ctx, tx, ev, nil); err != nil {
				return err
			}
		}
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

func (s *Store) SetDefaultCallPermissionLevel(ctx context.Context, id uuid.UUID, audit auth.AuditEntry) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pbx_setting SET default_call_permission_level_id = $1`, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, audit)
	})
}

// SetSetupStep records the wizard's resume point. completedAt, once set, is
// kept (COALESCE): a later step can't un-complete the wizard.
func (s *Store) SetSetupStep(ctx context.Context, step int, completedAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE pbx_setting SET setup_step = $1, setup_completed_at = COALESCE(setup_completed_at, $2)`,
		step, completedAt)
	return err
}

// NextFreeExtensionNumber returns the lowest number, digits long, in
// [from, to] that no extension (including deleted ones don't count: their
// number is already free) currently holds, or "" if the range is full.
func (s *Store) NextFreeExtensionNumber(ctx context.Context, tenant uuid.UUID, digits, from, to int) (string, error) {
	var n string
	err := s.pool.QueryRow(ctx, `
		SELECT t.n FROM (SELECT lpad(gs::text, $2, '0') AS n FROM generate_series($3, $4) AS gs) t
		WHERE NOT EXISTS (SELECT 1 FROM extension e WHERE e.tenant_id = $1 AND e.deleted_at IS NULL AND e.number = t.n)
		ORDER BY t.n LIMIT 1`, tenant, digits, from, to).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return n, err
}

// RangeReservedNumber reuses migration 0016's numbering_extension_clash (the
// same check an extension's own number gets) over every number in the
// range, so a saved range can never disagree with what a real extension
// number is refused for.
func (s *Store) RangeReservedNumber(ctx context.Context, country string, digits, from, to int) (string, string, error) {
	var number, reason string
	err := s.pool.QueryRow(ctx, `
		SELECT t.n, numbering_extension_clash($1, t.n) FROM (SELECT lpad(gs::text, $2, '0') AS n FROM generate_series($3, $4) AS gs) t
		WHERE numbering_extension_clash($1, t.n) IS NOT NULL
		ORDER BY t.n LIMIT 1`, country, digits, from, to).Scan(&number, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return number, reason, err
}

func (s *Store) ExtensionsWithOtherLength(ctx context.Context, tenant uuid.UUID, digits int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT number FROM extension
		WHERE tenant_id = $1 AND deleted_at IS NULL AND length(number) <> $2 ORDER BY number LIMIT 20`, tenant, digits)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectNumbers(rows)
}

func (s *Store) ExtensionsOutsideRanges(ctx context.Context, tenant uuid.UUID, ranges []settings.Range) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT number FROM extension WHERE tenant_id = $1 AND deleted_at IS NULL ORDER BY number`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		v, err := strconv.Atoi(n)
		if err != nil {
			continue
		}
		inside := false
		for _, r := range ranges {
			if v >= r.From && v <= r.To {
				inside = true
				break
			}
		}
		if !inside {
			out = append(out, n)
			if len(out) >= 20 {
				break
			}
		}
	}
	return out, rows.Err()
}

func collectNumbers(rows pgx.Rows) ([]string, error) {
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DefaultCallPermissionLevelID is the level new extensions get when none is
// named (docs/ADMIN.md §4); nil until the setup wizard (or an admin) sets
// one, so 1D's fail-closed default (no level = emergency only) is
// unchanged until then.
func (s *Store) DefaultCallPermissionLevelID(ctx context.Context) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := s.pool.QueryRow(ctx, `SELECT default_call_permission_level_id FROM pbx_setting`).Scan(&id)
	return id, err
}

// orEmpty is admin_networks for the database: the column is NOT NULL, and
// pgx sends a nil slice as NULL.
func orEmpty(p []netip.Prefix) []netip.Prefix {
	if p == nil {
		return []netip.Prefix{}
	}
	return p
}
