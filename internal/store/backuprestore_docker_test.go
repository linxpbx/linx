package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
)

// TestBackupRestoreDocker runs the restore request's queries against real
// Postgres (docs/BACKUP.md §4). make test-docker.
func TestBackupRestoreDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-backup-restore-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "user:x", Action: "backup.restore_requested", Result: auth.ResultOK}

	if done, err := s.SetupCompleted(ctx); err != nil || done {
		t.Fatalf("SetupCompleted() on a fresh install = %v, %v", done, err)
	}
	if _, found, err := s.Restore(ctx, tenant); err != nil || found {
		t.Fatalf("Restore() before any request = %v, %v", found, err)
	}
	if _, _, found, err := s.TakeRestore(ctx, time.Now()); err != nil || found {
		t.Fatalf("TakeRestore() with nothing waiting = %v, %v", found, err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	put := func() backupschedule.Restore {
		t.Helper()
		r := backupschedule.Restore{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Source: "folder", Location: "/var/backups/linx",
			Snapshot: "latest", Status: backupschedule.RestorePending, RequestedBy: "user:x", RequestedAt: now, UpdatedAt: now}
		if err := s.PutRestore(ctx, r, []byte("sealed-password"), audit); err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := put()

	got, sealed, found, err := s.TakeRestore(ctx, now.Add(time.Minute))
	if err != nil || !found || string(sealed) != "sealed-password" || got.ID != first.ID || got.Status != backupschedule.RestoreRunning {
		t.Fatalf("TakeRestore() = %+v, %q, %v, %v", got, sealed, found, err)
	}
	var left []byte
	if err := pool.QueryRow(ctx, `SELECT password_enc FROM backup_restore_request`).Scan(&left); err != nil || left != nil {
		t.Fatalf("password still stored after taking it: %q, %v", left, err)
	}
	if _, _, found, _ := s.TakeRestore(ctx, now); found {
		t.Fatal("taken twice")
	}

	if err := s.FailRestore(ctx, first.ID, "wrong password", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, _, _ := s.Restore(ctx, tenant)
	if r.Status != backupschedule.RestoreFailed || r.Error != "wrong password" {
		t.Fatalf("after FailRestore: %+v", r)
	}
	// Another request replaces it (one per tenant) with no error left over.
	second := put()
	r, _, _ = s.Restore(ctx, tenant)
	if r.ID != second.ID || r.Status != backupschedule.RestorePending || r.Error != "" {
		t.Fatalf("second request = %+v", r)
	}
	// Failing a request that isn't there any more is a no-op.
	if err := s.FailRestore(ctx, first.ID, "late", now); err != nil {
		t.Fatal(err)
	}
	if r, _, _ = s.Restore(ctx, tenant); r.Error != "" {
		t.Fatalf("an old request's failure landed on the new one: %+v", r)
	}

	if err := s.RecordRestored(ctx, auth.AuditEntry{TenantID: &tenant, Actor: "system:linx-backup-agent",
		Action: "backup.restored", Result: auth.ResultOK}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action IN ('backup.restore_requested', 'backup.restored')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("audit entries = %d, %v; want 3", n, err)
	}
}
