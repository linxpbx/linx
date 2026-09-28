package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/numbering"
)

// TestNumberRangesDocker runs the numbering plan's range queries on real
// Postgres: saving the extension ranges failed on a real server with
// "generate_series(unknown, unknown) is not unique" (found in the install
// demo), which nothing ran against a database before.
func TestNumberRangesDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-ranges-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	data, err := numbering.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncNumbering(ctx, data, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if n, err := s.NextFreeExtensionNumber(ctx, uuid.New(), "AE", 3, 100, 199); err != nil || n != "100" {
		t.Errorf("next free: %q %v", n, err)
	}
	// 112 is the UAE's emergency number: never handed out.
	if n, err := s.NextFreeExtensionNumber(ctx, uuid.New(), "AE", 3, 112, 199); err != nil || n != "113" {
		t.Errorf("next free after 112: %q %v", n, err)
	}
	// The wizard's default 3-digit people range holds 112: still fine.
	if n, reason, err := s.RangeReservedNumber(ctx, "AE", 3, 100, 599); err != nil || n != "" {
		t.Errorf("default people range: %q %q %v", n, reason, err)
	}
	// Numbers starting with the national prefix (0) are a whole block.
	if n, reason, err := s.RangeReservedNumber(ctx, "AE", 3, 0, 99); err != nil || n != "000" || reason == "" {
		t.Errorf("range over the national prefix: %q %q %v", n, reason, err)
	}
}
