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

// TestWebPhoneDocker runs the browser phone line's queries against real
// Postgres (docs/WEB.md §5, migration 0013): one line per session, a new
// password on every issue, and revocation once the session ends. It needs
// Docker: make test-docker.
func TestWebPhoneDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-webphone-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "device.web_phone", Result: auth.ResultOK}
	newExtension := func(number string) pbx.Extension {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: "Test", Enabled: true,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		return e
	}
	ext1, ext2 := newExtension("201"), newExtension("202")
	u := auth.User{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Email: "web@example.com", Name: "Web", Role: auth.RoleUser,
		ExtensionID: &ext1.ID, PasswordHash: "x", PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateUser(ctx, u, audit); err != nil {
		t.Fatal(err)
	}
	newSession := func() auth.UserSession {
		sess := auth.UserSession{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: u.ID, Role: u.Role,
			TokenHash: auth.HashSecret(auth.NewSecret()), CSRFHash: auth.HashSecret(auth.NewSecret()), MFAVerified: true,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastSeenAt: now}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		return sess
	}
	issue := func(sess auth.UserSession, ext uuid.UUID, password string) pbx.Device {
		t.Helper()
		id := sess.ID
		d := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: ext, Name: "Web browser", Kind: pbx.KindWeb,
			SIPUsername: pbx.NewSIPUsername(), Enabled: true, UserSessionID: &id, Version: 1, CreatedAt: now, UpdatedAt: now}
		out, err := s.IssueWebDevice(ctx, d, func(user string) string { return pbx.DigestHash(user, password) }, audit)
		if err != nil {
			t.Fatalf("IssueWebDevice: %v", err)
		}
		return out
	}
	events := func(typ string) int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE type = $1", typ).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	a := newSession()
	first := issue(a, ext1.ID, "pw-one")
	if first.DigestHash != pbx.DigestHash(first.SIPUsername, "pw-one") || first.UserSessionID == nil || *first.UserSessionID != a.ID {
		t.Fatalf("first: %+v", first)
	}
	again := issue(a, ext1.ID, "pw-two")
	if again.ID != first.ID || again.SIPUsername != first.SIPUsername || again.DigestHash != pbx.DigestHash(first.SIPUsername, "pw-two") ||
		again.Version != first.Version+1 {
		t.Errorf("reissue: %+v", again)
	}
	if got, err := s.WebDeviceForSession(ctx, a.ID); err != nil || got.ID != first.ID {
		t.Errorf("WebDeviceForSession: %+v %v", got, err)
	}
	if n := events("device.created"); n != 1 {
		t.Errorf("%d device.created events, want 1 (a new password isn't a new device)", n)
	}

	// A line for another extension replaces the old one for good.
	moved := issue(a, ext2.ID, "pw-three")
	if moved.ID == first.ID {
		t.Fatal("reused the line for another extension")
	}
	if old, _ := s.Device(ctx, tenant, first.ID); old.RevokedAt == nil || old.Enabled {
		t.Errorf("old line not revoked: %+v", old)
	}

	// Nothing is dead yet (u's extension is ext1, so the ext2 line is:
	// u doesn't have that extension).
	dead, err := s.RevokeDeadWebDevices(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 || dead[0].ID != moved.ID {
		t.Fatalf("dead: %+v", dead)
	}
	b := newSession()
	line := issue(b, ext1.ID, "pw")
	if _, err := pool.Exec(ctx, `UPDATE device SET online = true WHERE id = $1`, line.ID); err != nil {
		t.Fatal(err)
	}
	if dead, _ := s.RevokeDeadWebDevices(ctx, now); len(dead) != 0 {
		t.Errorf("a live session's line was revoked: %+v", dead)
	}
	if err := s.RevokeSession(ctx, b.ID, now); err != nil {
		t.Fatal(err)
	}
	dead, err = s.RevokeDeadWebDevices(ctx, now)
	if err != nil || len(dead) != 1 || dead[0].ID != line.ID || dead[0].RevokedAt == nil || dead[0].Online {
		t.Fatalf("after sign-out (want revoked and offline): %+v %v", dead, err)
	}
	if _, err := s.WebDeviceForSession(ctx, b.ID); err != pbx.ErrNotFound {
		t.Errorf("signed-out session still has a line: %v", err)
	}
	if n := events("device.revoked"); n != 3 {
		t.Errorf("%d device.revoked events, want 3", n)
	}
}
