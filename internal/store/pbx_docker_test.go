package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
)

// TestPbxDocker runs the extension and device queries — including the
// extension-delete transaction and optimistic concurrency — against real
// Postgres. It needs Docker: make test-docker.
func TestPbxDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-pbx-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	audit := func(action string) auth.AuditEntry {
		return auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: action, Result: auth.ResultOK}
	}
	eventCount := func(t *testing.T, eventType string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE tenant_id = $1 AND type = $2", tenant, eventType).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	newExtension := func(number string) pbx.Extension {
		id := uuid.Must(uuid.NewV7())
		e := pbx.Extension{ID: id, TenantID: tenant, Number: number, DisplayName: "Test", Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit("extension.create")); err != nil {
			t.Fatalf("CreateExtension: %v", err)
		}
		return e
	}
	newDevice := func(ext uuid.UUID) pbx.Device {
		id := uuid.Must(uuid.NewV7())
		username := pbx.NewSIPUsername()
		hash := pbx.DigestHash(username, "irrelevant-for-this-test")
		d := pbx.Device{ID: id, TenantID: tenant, ExtensionID: ext, Name: "Phone", Kind: pbx.KindSoftphone,
			SIPUsername: username, DigestHash: hash, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateDevice(ctx, d, audit("device.create")); err != nil {
			t.Fatalf("CreateDevice: %v", err)
		}
		return d
	}

	t.Run("extension CRUD and events", func(t *testing.T) {
		e := newExtension("301")
		got, err := s.Extension(ctx, tenant, e.ID)
		if err != nil || got.Number != "301" || got.Version != 1 {
			t.Fatalf("Extension() = %+v, %v", got, err)
		}
		if eventCount(t, "extension.created") != 1 {
			t.Error("no extension.created event")
		}

		got.DisplayName = "Renamed"
		updated, err := s.UpdateExtension(ctx, got, audit("extension.update"))
		if err != nil || updated.DisplayName != "Renamed" || updated.Version != 2 {
			t.Fatalf("UpdateExtension() = %+v, %v", updated, err)
		}
		if eventCount(t, "extension.updated") != 1 {
			t.Error("no extension.updated event")
		}

		// Stale version.
		got.DisplayName = "Stale"
		if _, err := s.UpdateExtension(ctx, got, audit("extension.update")); err != pbx.ErrVersionChanged {
			t.Fatalf("UpdateExtension with stale version = %v, want ErrVersionChanged", err)
		}

		// Duplicate number.
		other := newExtension("302")
		other.Number = "301"
		other.Version = 1
		if _, err := s.UpdateExtension(ctx, other, audit("extension.update")); err != pbx.ErrDuplicate {
			t.Fatalf("UpdateExtension with duplicate number = %v, want ErrDuplicate", err)
		}
		if err := s.CreateExtension(ctx, pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: "301",
			DisplayName: "Dup", Version: 1, CreatedAt: now, UpdatedAt: now}, audit("extension.create")); err != pbx.ErrDuplicate {
			t.Fatalf("CreateExtension duplicate = %v, want ErrDuplicate", err)
		}

		list, err := s.ListExtensions(ctx, tenant, nil, 50)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, x := range list {
			found = found || x.ID == e.ID
		}
		if !found {
			t.Error("extension missing from ListExtensions")
		}
	})

	t.Run("device CRUD, revoke and concurrency", func(t *testing.T) {
		ext := newExtension("310")
		d := newDevice(ext.ID)
		if eventCount(t, "device.created") == 0 {
			t.Error("no device.created event")
		}

		got, err := s.Device(ctx, tenant, d.ID)
		if err != nil || !got.Enabled || got.DigestHash != d.DigestHash {
			t.Fatalf("Device() = %+v, %v", got, err)
		}

		got.Name = "Renamed phone"
		updated, err := s.UpdateDevice(ctx, got, audit("device.update"))
		if err != nil || updated.Name != "Renamed phone" || updated.Version != 2 {
			t.Fatalf("UpdateDevice() = %+v, %v", updated, err)
		}

		got.Name = "Stale"
		if _, err := s.UpdateDevice(ctx, got, audit("device.update")); err != pbx.ErrVersionChanged {
			t.Fatalf("UpdateDevice with stale version = %v, want ErrVersionChanged", err)
		}

		devices, err := s.ListDevicesByExtension(ctx, tenant, ext.ID, nil, 50)
		if err != nil || len(devices) != 1 || devices[0].ID != d.ID {
			t.Fatalf("ListDevicesByExtension() = %+v, %v", devices, err)
		}

		revoked, err := s.RevokeDevice(ctx, tenant, d.ID, time.Now(), audit("device.revoke"))
		if err != nil || revoked.Enabled || revoked.Version != 3 {
			t.Fatalf("RevokeDevice() = %+v, %v", revoked, err)
		}
		if eventCount(t, "device.revoked") != 1 {
			t.Error("no device.revoked event")
		}
		// Idempotent: revoking again changes nothing (no version bump, no second event).
		revokedAgain, err := s.RevokeDevice(ctx, tenant, d.ID, time.Now(), audit("device.revoke"))
		if err != nil || revokedAgain.Version != 3 {
			t.Fatalf("second RevokeDevice() = %+v, %v", revokedAgain, err)
		}
		if eventCount(t, "device.revoked") != 1 {
			t.Error("revoking an already-revoked device fired a second event")
		}

		// Revoked is for good: no update (turning it back on, a new
		// password) goes through, and the database itself refuses one.
		revokedAgain.Enabled = true
		if _, err := s.UpdateDevice(ctx, revokedAgain, audit("device.update")); err != pbx.ErrRevoked {
			t.Fatalf("UpdateDevice on a revoked device = %v, want ErrRevoked", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE device SET enabled = true WHERE id = $1`, d.ID); err == nil {
			t.Fatal("the database let a revoked device be turned back on")
		}
	})

	t.Run("revoking a turned-off device", func(t *testing.T) {
		d := newDevice(newExtension("311").ID)
		d.Enabled = false
		off, err := s.UpdateDevice(ctx, d, audit("device.update"))
		if err != nil {
			t.Fatal(err)
		}
		revoked, err := s.RevokeDevice(ctx, tenant, off.ID, time.Now(), audit("device.revoke"))
		if err != nil || revoked.RevokedAt == nil {
			t.Fatalf("RevokeDevice() = %+v, %v", revoked, err)
		}
	})

	t.Run("deleting an extension revokes its devices and frees the number", func(t *testing.T) {
		ext := newExtension("320")
		d1 := newDevice(ext.ID)
		d2 := newDevice(ext.ID)

		if err := s.DeleteExtension(ctx, tenant, ext.ID, time.Now(), audit("extension.delete")); err != nil {
			t.Fatalf("DeleteExtension: %v", err)
		}
		if _, err := s.Extension(ctx, tenant, ext.ID); err != pbx.ErrNotFound {
			t.Fatalf("Extension() after delete = %v, want ErrNotFound", err)
		}
		if eventCount(t, "extension.deleted") != 1 {
			t.Error("no extension.deleted event")
		}
		for _, id := range []uuid.UUID{d1.ID, d2.ID} {
			got, err := s.Device(ctx, tenant, id)
			if err != nil || got.Enabled || got.RevokedAt == nil {
				t.Fatalf("device %s after extension delete: %+v, %v", id, got, err)
			}
		}
		if n := eventCount(t, "device.revoked"); n < 2 {
			t.Errorf("device.revoked events = %d, want at least 2", n)
		}

		// The number is free for reuse.
		reused := newExtension("320")
		if reused.Number != "320" {
			t.Fatal("couldn't reuse the freed number")
		}

		// Deleting again (already gone) is a not-found, not a silent no-op.
		if err := s.DeleteExtension(ctx, tenant, ext.ID, time.Now(), audit("extension.delete")); err != pbx.ErrNotFound {
			t.Fatalf("second DeleteExtension = %v, want ErrNotFound", err)
		}
	})
}
