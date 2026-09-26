package db_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/store"
)

// TestAsteriskRealtimeDocker runs the real migrations, then checks that the
// linx_asterisk role (ADR-032, docs/PBX.md §3) can read a device through the
// realtime views — and only through them: everything else in the database
// must come back "permission denied". It needs Docker: make test-docker.
func TestAsteriskRealtimeDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-pbx-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	const password = "test-asterisk-role-password"
	pwFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pwFile, []byte(password), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureAsteriskRole(ctx, pool, pwFile); err != nil {
		t.Fatalf("EnsureAsteriskRole: %v", err)
	}
	// Idempotent: rerunning at every startup must not fail.
	if err := db.EnsureAsteriskRole(ctx, pool, pwFile); err != nil {
		t.Fatalf("EnsureAsteriskRole (second run): %v", err)
	}

	tenant, err := store.New(pool).DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	extID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO extension (id, tenant_id, number, display_name, created_at, updated_at)
		VALUES ($1, $2, '101', 'Reception', $3, $3)`, extID, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device (id, tenant_id, extension_id, name, sip_username, digest_hash, created_at, updated_at)
		VALUES ($1, $2, $3, 'Front desk phone', 'd_1a2b3c4d', '0123456789abcdef0123456789abcdef', $4, $4)`,
		uuid.New(), tenant, extID, now); err != nil {
		t.Fatal(err)
	}
	// A second, disabled device: it must never appear in the realtime views.
	if _, err := pool.Exec(ctx, `INSERT INTO device (id, tenant_id, extension_id, name, sip_username, digest_hash, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, 'Revoked phone', 'd_9z8y7x6w', 'fedcba9876543210fedcba9876543210', false, $4, $4)`,
		uuid.New(), tenant, extID, now); err != nil {
		t.Fatal(err)
	}

	connCfg := pool.Config().ConnConfig
	dsn := fmt.Sprintf("postgres://linx_asterisk:%s@%s:%d/%s?sslmode=disable",
		url.QueryEscape(password), connCfg.Host, connCfg.Port, connCfg.Database)
	astPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting as linx_asterisk: %v", err)
	}
	defer astPool.Close()
	if err := astPool.Ping(ctx); err != nil {
		t.Fatalf("linx_asterisk can't log in: %v", err)
	}

	var transport, auth, mediaEnc string
	if err := astPool.QueryRow(ctx, "SELECT transport, auth, media_encryption FROM asterisk.ps_endpoints WHERE id = 'd_1a2b3c4d'").
		Scan(&transport, &auth, &mediaEnc); err != nil {
		t.Fatalf("reading the enabled device's endpoint: %v", err)
	}
	if transport != "transport-tls" || auth != "d_1a2b3c4d" || mediaEnc != "sdes" {
		t.Errorf("ps_endpoints row = (%q, %q, %q)", transport, auth, mediaEnc)
	}

	// A browser's device (Phase 1C, migration 0011): the websocket and WebRTC
	// media; the phone above keeps TLS and SDES. Opus first for both. It
	// belongs to a signed-in person's session (migration 0013).
	webExt, webUser, webSession := uuid.New(), uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO extension (id, tenant_id, number, display_name, created_at, updated_at)
		VALUES ($1, $2, '102', 'Dana', $3, $3)`, webExt, tenant, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app_user (id, tenant_id, email, name, role, extension_id, password_hash,
		password_updated_at, created_at, updated_at) VALUES ($1, $2, 'dana@example.com', 'Dana', 'user', $3, 'x', $4, $4, $4)`,
		webUser, tenant, webExt, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_session (id, tenant_id, user_id, role, token_hash, csrf_hash, mfa_verified,
		created_at, expires_at, idle_expires_at, last_seen_at, user_agent) VALUES ($1, $2, $3, 'user', $6, $7, true, $4, $5, $5, $4, '')`,
		webSession, tenant, webUser, now, now.Add(time.Hour), make([]byte, 32), make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device (id, tenant_id, extension_id, name, kind, sip_username, digest_hash, user_session_id, created_at, updated_at)
		VALUES ($1, $2, $3, 'Browser', 'web', 'd_w3bw3bw3', '0123456789abcdef0123456789abcdef', $4, $5, $5)`,
		uuid.New(), tenant, webExt, webSession, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device (id, tenant_id, extension_id, name, kind, sip_username, digest_hash, created_at, updated_at)
		VALUES ($1, $2, $3, 'Browser', 'web', 'd_n0s3ss10', '0123456789abcdef0123456789abcdef', $4, $4)`,
		uuid.New(), tenant, webExt, now); err == nil {
		t.Error("a web device without a session was accepted")
	}
	endpoint := func(id string) string {
		var row string
		if err := astPool.QueryRow(ctx, `SELECT concat_ws(' ', transport, allow, media_encryption, ice_support, use_avpf, rtcp_mux,
			coalesce(dtls_verify, '-'), coalesce(dtls_setup, '-'), coalesce(dtls_auto_generate_cert, '-'), media_use_received_transport,
			coalesce(incoming_offer_codec_prefs, '-')) FROM asterisk.ps_endpoints WHERE id = $1`, id).Scan(&row); err != nil {
			t.Fatalf("reading endpoint %s: %v", id, err)
		}
		return row
	}
	if got, want := endpoint("d_w3bw3bw3"), "transport-wss opus,g722,ulaw dtls yes yes yes fingerprint actpass yes yes -"; got != want {
		t.Errorf("web endpoint = %q, want %q", got, want)
	}
	if got, want := endpoint("d_1a2b3c4d"), "transport-tls opus,g722,ulaw sdes no no no - - - no -"; got != want {
		t.Errorf("phone endpoint = %q, want %q", got, want)
	}

	var credHash string
	if err := astPool.QueryRow(ctx, "SELECT md5_cred FROM asterisk.ps_auths WHERE id = 'd_1a2b3c4d'").Scan(&credHash); err != nil {
		t.Fatalf("reading the enabled device's auth: %v", err)
	}
	if credHash != "0123456789abcdef0123456789abcdef" {
		t.Errorf("md5_cred = %q", credHash)
	}

	var ringAOR string
	if err := astPool.QueryRow(ctx, "SELECT aor FROM asterisk.linx_ring_targets WHERE number = '101'").Scan(&ringAOR); err != nil {
		t.Fatalf("reading the ring targets: %v", err)
	}
	if ringAOR != "d_1a2b3c4d" {
		t.Errorf("linx_ring_targets aor = %q, want d_1a2b3c4d", ringAOR)
	}

	var revokedCount int
	if err := astPool.QueryRow(ctx, "SELECT count(*) FROM asterisk.ps_endpoints WHERE id = 'd_9z8y7x6w'").Scan(&revokedCount); err != nil {
		t.Fatalf("checking the disabled device is absent: %v", err)
	}
	if revokedCount != 0 {
		t.Error("a disabled device's endpoint is still readable")
	}

	// The web device is there only while its session is live, its person
	// is enabled and still has that extension (migration 0013): each of
	// these makes every view drop it, and undoing it brings it back.
	visible := func() string {
		var e, a, o, r int
		if err := astPool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM asterisk.ps_endpoints WHERE id = 'd_w3bw3bw3'),
			(SELECT count(*) FROM asterisk.ps_auths WHERE id = 'd_w3bw3bw3'),
			(SELECT count(*) FROM asterisk.ps_aors WHERE id = 'd_w3bw3bw3'),
			(SELECT count(*) FROM asterisk.linx_ring_targets WHERE aor = 'd_w3bw3bw3')`).Scan(&e, &a, &o, &r); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(e, a, o, r)
	}
	if got := visible(); got != "1 1 1 1" {
		t.Fatalf("live web device: %s", got)
	}
	otherExt := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO extension (id, tenant_id, number, display_name, created_at, updated_at)
		VALUES ($1, $2, '103', 'Other', $3, $3)`, otherExt, tenant, now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ what, change, undo string }{
		{"session signed out", `UPDATE user_session SET revoked_at = now() WHERE id = $1`, `UPDATE user_session SET revoked_at = NULL WHERE id = $1`},
		{"session expired", `UPDATE user_session SET expires_at = now() - interval '1 s' WHERE id = $1`, `UPDATE user_session SET expires_at = now() + interval '1 h' WHERE id = $1`},
		{"session idle", `UPDATE user_session SET idle_expires_at = now() - interval '1 s' WHERE id = $1`, `UPDATE user_session SET idle_expires_at = now() + interval '1 h' WHERE id = $1`},
		{"session pending MFA", `UPDATE user_session SET mfa_verified = false WHERE id = $1`, `UPDATE user_session SET mfa_verified = true WHERE id = $1`},
		{"person disabled", `UPDATE app_user SET disabled_at = now() WHERE id = (SELECT user_id FROM user_session WHERE id = $1)`,
			`UPDATE app_user SET disabled_at = NULL WHERE id = (SELECT user_id FROM user_session WHERE id = $1)`},
		{"person moved to another extension", `UPDATE app_user SET extension_id = '` + otherExt.String() + `' WHERE id = (SELECT user_id FROM user_session WHERE id = $1)`,
			`UPDATE app_user SET extension_id = '` + webExt.String() + `' WHERE id = (SELECT user_id FROM user_session WHERE id = $1)`},
	} {
		if _, err := pool.Exec(ctx, c.change, webSession); err != nil {
			t.Fatal(err)
		}
		if got := visible(); got != "0 0 0 0" {
			t.Errorf("%s: web device still visible (%s)", c.what, got)
		}
		if _, err := pool.Exec(ctx, c.undo, webSession); err != nil {
			t.Fatal(err)
		}
		if got := visible(); got != "1 1 1 1" {
			t.Fatalf("after undoing %s: %s", c.what, got)
		}
	}

	// Outgoing calls (migration 0016): Asterisk asks one function, which
	// reads the tables behind it with its owner's rights. The caller is a
	// live device's SIP username; anything else is an unknown caller.
	data, err := numbering.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.New(pool).SyncNumbering(ctx, data, now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ endpoint, dialled, want string }{
		{"d_1a2b3c4d", "0501234567", "mobile +971501234567 f no_lines"},
		{"d_1a2b3c4d", "999", "emergency 999 t emergency"},
		{"d_9z8y7x6w", "999", "emergency 999 f unknown_caller"},
		{"101", "999", "emergency 999 f unknown_caller"},
	} {
		var got string
		if err := astPool.QueryRow(ctx, `SELECT concat_ws(' ', category, dial, allowed, reason)
			FROM asterisk.linx_route_outbound($1, $2)`, c.endpoint, c.dialled).Scan(&got); err != nil {
			t.Fatalf("linx_route_outbound(%s, %s): %v", c.endpoint, c.dialled, err)
		}
		if got != c.want {
			t.Errorf("linx_route_outbound(%s, %s) = %q, want %q", c.endpoint, c.dialled, got, c.want)
		}
	}
	if _, err := astPool.Exec(ctx, "SELECT numbering_classify('AE', '0501234567')"); err == nil {
		t.Error("linx_asterisk could run numbering_classify on the tables directly; want permission denied")
	}

	for _, table := range []string{"tenant", "extension", "device", "api_key", "webhook_endpoint", "alert_channel", "user_session", "app_user",
		"pbx_setting", "numbering_region", "numbering_desc", "numbering_short", "numbering_always", "numbering_data"} {
		if _, err := astPool.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 1"); err == nil {
			t.Errorf("linx_asterisk could read table %s directly; want permission denied", table)
		}
	}
}
