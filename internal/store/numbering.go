package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/numbering"
)

// SyncNumbering writes libphonenumber's data into the numbering tables
// (migration 0016) if what's stored is from another version of it. It
// reports whether anything changed. Calls being decided meanwhile see the
// old data until the new one is committed.
func (s *Store) SyncNumbering(ctx context.Context, d *numbering.Data, now time.Time) (bool, error) {
	changed := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Two control planes starting at once write one after the other.
		if _, err := tx.Exec(ctx, "LOCK TABLE numbering_data IN EXCLUSIVE MODE"); err != nil {
			return err
		}
		var version string
		err := tx.QueryRow(ctx, "SELECT version FROM numbering_data").Scan(&version)
		if err == nil && version == d.Version {
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		changed = true
		for _, t := range []string{"numbering_desc", "numbering_region", "numbering_short", "numbering_always"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+t); err != nil {
				return fmt.Errorf("clearing %s: %w", t, err)
			}
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"numbering_region"},
			[]string{"calling_code", "region", "position", "international_prefix", "national_prefix",
				"national_prefix_for_parsing", "transform_rule", "leading_digits", "same_mobile_and_fixed",
				"general_pattern", "general_lengths", "general_local_lengths"},
			pgx.CopyFromSlice(len(d.Regions), func(i int) ([]any, error) {
				r := d.Regions[i]
				return []any{r.CallingCode, r.Region, r.Position, r.InternationalPrefix, r.NationalPrefix,
					r.NationalPrefixForParsing, r.TransformRule, r.LeadingDigits, r.SameMobileAndFixed,
					r.GeneralPattern, r.GeneralLengths, r.GeneralLocalLengths}, nil
			})); err != nil {
			return fmt.Errorf("writing numbering_region: %w", err)
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"numbering_desc"},
			[]string{"calling_code", "region", "number_type", "check_order", "pattern", "lengths"},
			pgx.CopyFromSlice(len(d.Descs), func(i int) ([]any, error) {
				r := d.Descs[i]
				return []any{r.CallingCode, r.Region, r.NumberType, r.CheckOrder, r.Pattern, r.Lengths}, nil
			})); err != nil {
			return fmt.Errorf("writing numbering_desc: %w", err)
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"numbering_short"},
			[]string{"region", "general_pattern", "general_lengths", "short_code_pattern", "short_code_lengths", "emergency_pattern"},
			pgx.CopyFromSlice(len(d.Short), func(i int) ([]any, error) {
				r := d.Short[i]
				return []any{r.Region, r.GeneralPattern, r.GeneralLengths, r.ShortCodePattern, r.ShortCodeLengths, r.EmergencyPattern}, nil
			})); err != nil {
			return fmt.Errorf("writing numbering_short: %w", err)
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"numbering_always"},
			[]string{"region", "number", "label"},
			pgx.CopyFromSlice(len(d.Always), func(i int) ([]any, error) {
				r := d.Always[i]
				return []any{r.Region, r.Number, r.Label}, nil
			})); err != nil {
			return fmt.Errorf("writing numbering_always: %w", err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO numbering_data (version, updated_at) VALUES ($1, $2)
			ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version, updated_at = EXCLUDED.updated_at`, d.Version, now)
		return err
	})
	return changed, err
}

// Country is the region code of the country Linx is set up in.
func (s *Store) Country(ctx context.Context) (string, error) {
	var c string
	err := s.pool.QueryRow(ctx, "SELECT country FROM pbx_setting").Scan(&c)
	return c, err
}

// Classify is the database's decision on what dialled is, for a caller in
// home (numbering_classify).
func (s *Store) Classify(ctx context.Context, home, dialled string) (numbering.Result, error) {
	var r numbering.Result
	var cat string
	var typ, region, e164, dial, label *string
	err := s.pool.QueryRow(ctx, `SELECT category, number_type, region, e164, dial, label FROM numbering_classify($1, $2)`,
		home, dialled).Scan(&cat, &typ, &region, &e164, &dial, &label)
	r.Category = numbering.Category(cat)
	r.NumberType, r.Region, r.E164, r.Dial, r.Label = deref(typ), deref(region), deref(e164), deref(dial), deref(label)
	return r, err
}

// Route is the outgoing-call decision for a call from extension to
// dialled: the same function Asterisk's asterisk.linx_route_outbound uses
// (numbering_route), so `linx route test` can't disagree with a real call.
func (s *Store) Route(ctx context.Context, extension uuid.UUID, dialled string) (numbering.Route, error) {
	var r numbering.Route
	var cat string
	var typ, region, e164, dial, label *string
	err := s.pool.QueryRow(ctx, `SELECT category, number_type, region, e164, dial, label, allowed, reason
		FROM numbering_route($1, $2)`, extension, dialled).
		Scan(&cat, &typ, &region, &e164, &dial, &label, &r.Allowed, &r.Reason)
	r.Category = numbering.Category(cat)
	r.NumberType, r.Region, r.E164, r.Dial, r.Label = deref(typ), deref(region), deref(e164), deref(dial), deref(label)
	return r, err
}

// ExtensionClashes lists the extensions whose numbers can't be used in
// country (numbering_extension_clash), e.g. when the admin chooses another
// country or doctor checks.
func (s *Store) ExtensionClashes(ctx context.Context, tenant uuid.UUID, country string) ([]numbering.Clash, error) {
	rows, err := s.pool.Query(ctx, `SELECT number, reason FROM (
			SELECT number, numbering_extension_clash($2, number) AS reason
			FROM extension WHERE tenant_id = $1 AND deleted_at IS NULL) c
		WHERE reason IS NOT NULL ORDER BY number`, tenant, country)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (numbering.Clash, error) {
		var c numbering.Clash
		err := row.Scan(&c.Number, &c.Reason)
		return c, err
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
