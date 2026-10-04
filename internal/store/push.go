package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/push"
)

var _ push.Store = (*Store)(nil)

// PushSettings is the Apple key this server rings app phones with, and the
// sealed key itself (migration 0042). A server that has never been given
// one gets the empty settings, not an error: push simply isn't on.
func (s *Store) PushSettings(ctx context.Context) (push.Settings, []byte, error) {
	var out push.Settings
	var keyEnc []byte
	err := s.pool.QueryRow(ctx, `SELECT tenant_id, enabled, team_id, key_id, bundle_id, environment,
			wait_ms, key_enc, version, updated_at
		FROM push_settings ORDER BY tenant_id LIMIT 1`).
		Scan(&out.Tenant, &out.Enabled, &out.TeamID, &out.KeyID, &out.BundleID, &out.Environment,
			&out.WaitMS, &keyEnc, &out.Version, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		tenant, err := s.DefaultTenant(ctx)
		if err != nil {
			return out, nil, err
		}
		return push.Settings{Tenant: tenant, Environment: push.Production, WaitMS: push.DefaultWaitMS}, nil, nil
	}
	if err != nil {
		return out, nil, err
	}
	out.HasKey = len(keyEnc) > 0
	return out, keyEnc, nil
}

// SavePushSettings writes the card, audited. ifVersion 0 is a first save.
func (s *Store) SavePushSettings(ctx context.Context, in push.Settings, keyEnc []byte, ifVersion int, audit auth.AuditEntry) (push.Settings, error) {
	var out push.Settings
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var keptEnc []byte
		err := tx.QueryRow(ctx, `INSERT INTO push_settings (tenant_id, enabled, team_id, key_id, bundle_id,
				environment, wait_ms, key_enc, version, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9)
			ON CONFLICT (tenant_id) DO UPDATE SET enabled = $2, team_id = $3, key_id = $4, bundle_id = $5,
				environment = $6, wait_ms = $7, key_enc = $8, version = push_settings.version + 1, updated_at = $9
			WHERE push_settings.version = $10 OR $10 = 0
			RETURNING tenant_id, enabled, team_id, key_id, bundle_id, environment, wait_ms, key_enc, version, updated_at`,
			in.Tenant, in.Enabled, in.TeamID, in.KeyID, in.BundleID, in.Environment, in.WaitMS, keyEnc,
			in.UpdatedAt, ifVersion).
			Scan(&out.Tenant, &out.Enabled, &out.TeamID, &out.KeyID, &out.BundleID, &out.Environment,
				&out.WaitMS, &keptEnc, &out.Version, &out.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.ErrVersionChanged
		}
		if err != nil {
			return fmt.Errorf("saving how app phones are rung: %w", err)
		}
		out.HasKey = len(keptEnc) > 0
		return insertAudit(ctx, tx, audit)
	})
	return out, err
}

// WakeDevices returns the app phones among these SIP usernames that can be
// woken: still set up, still their person's, and with a token the app sent
// — a VoIP token, or a notification token where CallKit may not be used
// (ADR-078). It matches asterisk.linx_wake, which is what decided to wait.
func (s *Store) WakeDevices(ctx context.Context, sipUsernames []string) ([]push.Device, error) {
	if len(sipUsernames) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT i.device_id, i.tenant_id, i.user_id, l.sip_username,
			i.voip_token, i.alert_token, i.push_environment, i.call_alerts
		FROM device_live l JOIN device_identity i ON i.device_id = l.id
		WHERE l.kind = 'ios' AND l.sip_username = ANY ($1)
		  AND (i.voip_token <> '' OR (i.call_alerts AND i.alert_token <> ''))`, sipUsernames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectDevices(rows)
}

// AlertDevices returns the app phones of whoever has this extension that
// may show a quiet notification: the app asked them and they said yes,
// which is the only way an alert token exists at all.
func (s *Store) AlertDevices(ctx context.Context, extension uuid.UUID) ([]push.Device, error) {
	rows, err := s.pool.Query(ctx, `SELECT i.device_id, i.tenant_id, i.user_id, l.sip_username,
			i.voip_token, i.alert_token, i.push_environment, i.call_alerts
		FROM device_live l
		JOIN device_identity i ON i.device_id = l.id
		JOIN app_user u ON u.id = i.user_id
		WHERE l.kind = 'ios' AND u.extension_id = $1 AND i.alert_token <> ''`, extension)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectDevices(rows)
}

func collectDevices(rows pgx.Rows) ([]push.Device, error) {
	out := []push.Device{}
	for rows.Next() {
		var d push.Device
		if err := rows.Scan(&d.DeviceID, &d.Tenant, &d.UserID, &d.SIPUsername,
			&d.VoIPToken, &d.AlertToken, &d.Environment, &d.CallAlerts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ForgetPushToken clears a token Apple called dead. The phone itself is
// untouched: it keeps working, and sends a new token next time it runs.
func (s *Store) ForgetPushToken(ctx context.Context, device uuid.UUID, kind string, at time.Time) error {
	column := "alert_token"
	if kind == "voip" {
		column = "voip_token"
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE device_identity SET `+column+` = '', push_dead_at = $2 WHERE device_id = $1`, device, at)
	return err
}

// SavePushTokens is the app telling Linx where Apple can reach it.
func (s *Store) SavePushTokens(ctx context.Context, device uuid.UUID, t push.Tokens, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE device_identity
		SET voip_token = $2, alert_token = $3, push_environment = $4, call_alerts = $5,
			push_updated_at = $6, push_dead_at = NULL
		WHERE device_id = $1`, device, t.VoIP, t.Alert, t.Environment, t.CallAlerts, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrNotFound
	}
	return nil
}
