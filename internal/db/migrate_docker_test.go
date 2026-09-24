package db_test

import (
	"context"
	"testing"
	"time"

	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
)

// TestMigrateDocker runs Migrate against a real Postgres and checks the
// tables it creates. It needs Docker: make test-docker.
func TestMigrateDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool := dbtest.Start(t, ctx, "linx-db-migrate-test")

	version, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if version != 5 {
		t.Errorf("Migrate() version = %d, want 5", version)
	}

	for _, table := range []string{"tenant", "audit_log", "schema_migrations", "api_key", "oauth_client", "token_revocation",
		"event_outbox", "webhook_endpoint", "webhook_delivery", "webhook_attempt", "outbound_allowlist",
		"alert_channel", "alert", "alert_delivery", "alert_attempt", "extension", "device"} {
		var exists bool
		if err := pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)", table,
		).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %s not created", table)
		}
	}

	// Running again is a no-op and returns the same version.
	version2, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if version2 != 5 {
		t.Errorf("second Migrate() version = %d, want 5", version2)
	}
}
