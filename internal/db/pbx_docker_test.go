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

	for _, table := range []string{"tenant", "extension", "device", "api_key", "webhook_endpoint", "alert_channel"} {
		if _, err := astPool.Exec(ctx, "SELECT 1 FROM "+table+" LIMIT 1"); err == nil {
			t.Errorf("linx_asterisk could read table %s directly; want permission denied", table)
		}
	}
}
