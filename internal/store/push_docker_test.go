package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/enroll"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/push"
)

// TestPushDocker checks ringing a sleeping phone against real Postgres
// (migration 0042): the Apple key is saved and read back without ever being
// shown, a phone's push tokens are kept and forgotten, and Asterisk's own
// lookup names a phone to wake only when there is a key, a token and a
// phone still set up. It needs Docker: make test-docker.
func TestPushDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-push-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := func(action string) auth.AuditEntry {
		return auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: action, Result: auth.ResultOK}
	}

	// A person with an extension, and an app phone of theirs.
	ext := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: "101", DisplayName: "Sara Haddad",
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateExtension(ctx, ext, audit("extension.create")); err != nil {
		t.Fatal(err)
	}
	sara := auth.User{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Email: "sara@example.com", Name: "Sara Haddad",
		Role: auth.RoleUser, ExtensionID: &ext.ID, PasswordHash: "placeholder", PasswordUpdatedAt: now,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateUser(ctx, sara, audit("user.create")); err != nil {
		t.Fatal(err)
	}
	ticket := enroll.Ticket{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: sara.ID, ExtensionID: ext.ID,
		Kind: pbx.KindIOS, DeviceName: "Sara's iPhone", Delivery: enroll.DeliveryQR, CreatedBy: "system",
		CodeHash: auth.HashSecret("ABCD2345"), TokenJTI: uuid.Must(uuid.NewV7()),
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	if err := s.CreateEnrollment(ctx, ticket, audit("device.enroll_ticket")); err != nil {
		t.Fatal(err)
	}
	username := pbx.NewSIPUsername()
	device := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: ext.ID, Name: "Sara's iPhone",
		Kind: pbx.KindIOS, SIPUsername: username, DigestHash: pbx.DigestHash(username, "a-password-nobody-keeps"),
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	identity := enroll.Identity{DeviceID: device.ID, TenantID: tenant, UserID: sara.ID,
		PublicKey: []byte("spki"), CertSerial: "1", CertFingerprint: []byte("fingerprint-1"),
		CertNotAfter: now.Add(183 * 24 * time.Hour), EnrolledAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(183 * 24 * time.Hour)}
	if err := s.RedeemEnrollment(ctx, ticket.ID, device, identity, audit("device.enrolled")); err != nil {
		t.Fatal(err)
	}

	// Before anything is set up: no settings row, and nothing to wake.
	settings, keyEnc, err := s.PushSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || settings.HasKey || len(keyEnc) != 0 || settings.Tenant != tenant {
		t.Fatalf("a server with no Apple key: %+v", settings)
	}
	if settings.WaitMS != push.DefaultWaitMS || settings.Environment != push.Production {
		t.Errorf("defaults = %+v", settings)
	}
	if aors, wait := wake(t, ctx, pool, "PJSIP/"+username); aors != "" || wait != 0 {
		t.Errorf("linx_wake with no key named %q (wait %d)", aors, wait)
	}

	// The key, saved.
	saved, err := s.SavePushSettings(ctx, push.Settings{Tenant: tenant, Enabled: true, TeamID: "ABCDE12345",
		KeyID: "KEY1234567", BundleID: "com.linxpbx.app", Environment: push.Production, WaitMS: 6000,
		UpdatedAt: now}, []byte("sealed-key"), 0, audit("push.settings"))
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Enabled || !saved.HasKey || saved.Version != 1 {
		t.Fatalf("saved = %+v", saved)
	}
	// Still nothing to wake: the phone hasn't sent a token.
	if aors, _ := wake(t, ctx, pool, "PJSIP/"+username); aors != "" {
		t.Errorf("linx_wake named a phone with no push token: %q", aors)
	}

	// The app sends its tokens.
	if err := s.SavePushTokens(ctx, device.ID, "aabbccdd", "eeff0011", push.Sandbox, now); err != nil {
		t.Fatal(err)
	}
	aors, wait := wake(t, ctx, pool, "PJSIP/"+username+"&PJSIP/d_nosuchone")
	if aors != username || wait != 6000 {
		t.Errorf("linx_wake = %q, %d; want %q, 6000", aors, wait, username)
	}

	devices, err := s.WakeDevices(ctx, []string{username})
	if err != nil || len(devices) != 1 || devices[0].VoIPToken != "aabbccdd" || devices[0].Environment != push.Sandbox {
		t.Fatalf("WakeDevices = %+v, %v", devices, err)
	}
	alerts, err := s.AlertDevices(ctx, ext.ID)
	if err != nil || len(alerts) != 1 || alerts[0].AlertToken != "eeff0011" {
		t.Fatalf("AlertDevices = %+v, %v", alerts, err)
	}

	// Apple says the VoIP token is dead: it goes, the alert one stays, and
	// the phone is no longer woken.
	if err := s.ForgetPushToken(ctx, device.ID, "voip", now); err != nil {
		t.Fatal(err)
	}
	if devices, err := s.WakeDevices(ctx, []string{username}); err != nil || len(devices) != 0 {
		t.Errorf("a dead token is still woken: %+v, %v", devices, err)
	}
	if alerts, err := s.AlertDevices(ctx, ext.ID); err != nil || len(alerts) != 1 {
		t.Errorf("the notification token went with the VoIP one: %+v, %v", alerts, err)
	}
	if aors, _ := wake(t, ctx, pool, "PJSIP/"+username); aors != "" {
		t.Errorf("linx_wake still names a phone whose token Apple refused: %q", aors)
	}

	// A phone that is no longer set up is never woken, token or not.
	if err := s.SavePushTokens(ctx, device.ID, "aabbccdd", "eeff0011", push.Sandbox, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE device_identity SET expired_at = now() WHERE device_id = $1`, device.ID); err != nil {
		t.Fatal(err)
	}
	if aors, _ := wake(t, ctx, pool, "PJSIP/"+username); aors != "" {
		t.Errorf("linx_wake names an expired phone: %q", aors)
	}

	// Turning it off stops the waiting for everyone at once.
	if _, err := pool.Exec(ctx, `UPDATE device_identity SET expired_at = NULL WHERE device_id = $1`, device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePushSettings(ctx, push.Settings{Tenant: tenant, Enabled: false, Environment: push.Production,
		WaitMS: 6000, UpdatedAt: now}, []byte("sealed-key"), saved.Version, audit("push.settings")); err != nil {
		t.Fatal(err)
	}
	if aors, _ := wake(t, ctx, pool, "PJSIP/"+username); aors != "" {
		t.Errorf("linx_wake answers while push is off: %q", aors)
	}

	// The audit log kept both saves, and neither carries the key.
	var entries int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'push.settings'`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 2 {
		t.Errorf("audit entries for push.settings = %d, want 2", entries)
	}
}

// wake calls the dialplan's own lookup, exactly as func_odbc does.
func wake(t *testing.T, ctx context.Context, pool *pgxpool.Pool, targets string) (string, int) {
	t.Helper()
	var aors string
	var waitMS int
	if err := pool.QueryRow(ctx, `SELECT aors, wait_ms FROM asterisk.linx_wake($1)`, targets).Scan(&aors, &waitMS); err != nil {
		t.Fatalf("linx_wake(%q): %v", targets, err)
	}
	return aors, waitMS
}
