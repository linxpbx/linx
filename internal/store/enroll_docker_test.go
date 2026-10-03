package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/enroll"
	"linxpbx.com/linx/internal/pbx"
)

// TestEnrollDocker checks setting up an iPhone against real Postgres
// (migration 0041): a ticket is made and redeemed exactly once, the phone
// and its identity appear with device.created and device.enrolled events, a
// proof can be used once, an expired, revoked or disabled phone stops being
// a live phone at once, and Asterisk's own view agrees. It needs Docker:
// make test-docker.
func TestEnrollDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-enroll-store-test")
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

	// Who a ticket may be made for.
	target, err := s.EnrollmentTarget(ctx, tenant, sara.ID)
	if err != nil || target.ExtensionID != ext.ID || target.PersonName != "Sara Haddad" || target.Number != "101" {
		t.Fatalf("EnrollmentTarget: %+v, %v", target, err)
	}
	if _, err := s.EnrollmentTarget(ctx, tenant, uuid.New()); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("a ticket for nobody: %v", err)
	}

	ticket := enroll.Ticket{
		ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: sara.ID, ExtensionID: ext.ID,
		Kind: pbx.KindIOS, DeviceName: "Sara's iPhone", Delivery: enroll.DeliveryQR, CreatedBy: "user:" + sara.ID.String(),
		CodeHash: auth.HashSecret("ABCD2345"), TokenJTI: uuid.Must(uuid.NewV7()),
		CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute),
	}
	if err := s.CreateEnrollment(ctx, ticket, audit("device.enroll_ticket")); err != nil {
		t.Fatal(err)
	}
	open, err := s.Enrollments(ctx, tenant, nil)
	if err != nil || len(open) != 1 || open[0].ID != ticket.ID || open[0].PersonName != "Sara Haddad" || open[0].Number != "101" {
		t.Fatalf("open tickets: %+v, %v", open, err)
	}
	byCode, err := s.EnrollmentByCodeHash(ctx, auth.HashSecret("ABCD2345"))
	if err != nil || byCode.ID != ticket.ID {
		t.Fatalf("by code: %+v, %v", byCode, err)
	}
	if _, err := s.EnrollmentByCodeHash(ctx, auth.HashSecret("WRONG123")); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("a code nobody has: %v", err)
	}
	if err := s.FailEnrollmentAttempt(ctx, ticket.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Enrollment(ctx, ticket.ID); err != nil || got.Attempts != 1 {
		t.Fatalf("after a wrong code: %+v, %v", got, err)
	}

	// Redeeming it makes the phone, its identity, and the two events.
	username := pbx.NewSIPUsername()
	device := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: ext.ID, Name: "Sara's iPhone",
		Kind: pbx.KindIOS, SIPUsername: username, DigestHash: pbx.DigestHash(username, "a-password-nobody-keeps"), Enabled: true,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	identity := enroll.Identity{DeviceID: device.ID, TenantID: tenant, UserID: sara.ID,
		PublicKey: []byte("spki"), CertSerial: "1", CertFingerprint: []byte("fingerprint-1"),
		CertNotAfter: now.Add(183 * 24 * time.Hour), EnrolledAt: now, LastSeenAt: now,
		ExpiresAt: now.Add(183 * 24 * time.Hour), AppVersion: "0.1.0", OSVersion: "26.0"}
	if err := s.RedeemEnrollment(ctx, ticket.ID, device, identity, audit("device.enrolled")); err != nil {
		t.Fatal(err)
	}
	// The same ticket can never make a second phone.
	second := device
	second.ID = uuid.Must(uuid.NewV7())
	secondIdentity := identity
	secondIdentity.DeviceID, secondIdentity.CertFingerprint = second.ID, []byte("fingerprint-2")
	if err := s.RedeemEnrollment(ctx, ticket.ID, second, secondIdentity, audit("device.enrolled")); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("redeeming a ticket twice: %v", err)
	}
	if open, err := s.Enrollments(ctx, tenant, nil); err != nil || len(open) != 0 {
		t.Errorf("a used ticket is still open: %+v, %v", open, err)
	}

	var created, enrolled int
	rows, err := pool.Query(ctx, `SELECT type FROM event_outbox WHERE encode(body, 'escape') LIKE '%' || $1 || '%'`, device.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		switch typ {
		case "device.created":
			created++
		case "device.enrolled":
			enrolled++
		}
	}
	rows.Close()
	if created != 1 || enrolled != 1 {
		t.Errorf("events: %d device.created, %d device.enrolled", created, enrolled)
	}

	// The phone is found by its certificate, and is a live phone.
	got, gotDevice, err := s.IdentityByFingerprint(ctx, []byte("fingerprint-1"))
	if err != nil || got.DeviceID != device.ID || gotDevice.Kind != pbx.KindIOS || got.OSVersion != "26.0" {
		t.Fatalf("by fingerprint: %+v, %+v, %v", got, gotDevice, err)
	}
	if _, _, err := s.IdentityByFingerprint(ctx, []byte("someone else")); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("an unknown certificate: %v", err)
	}
	if gotTenant, gotUser, err := s.DevicePrincipalFor(ctx, device.ID, now); err != nil || gotTenant != tenant || gotUser != sara.ID {
		t.Fatalf("whose phone: %v, %v, %v", gotTenant, gotUser, err)
	}
	live := func() bool {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_live WHERE id = $1`, device.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	if !live() {
		t.Error("Asterisk can't see a phone that was just set up")
	}

	// The app asks for its phone line: the SIP username stays, the password
	// is new, and Asterisk has the new digest the moment it's issued.
	line, lineExt, err := s.IssuePhoneLine(ctx, tenant, device.ID,
		func(u string) string { return pbx.DigestHash(u, "line-password-1") }, now, audit("device.phone_line"))
	if err != nil || line.SIPUsername != username || line.Version != 2 || lineExt.Number != "101" {
		t.Fatalf("phone line: %+v, %+v, %v", line, lineExt, err)
	}
	var digest string
	if err := pool.QueryRow(ctx, `SELECT digest_hash FROM device_live WHERE id = $1`, device.ID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != pbx.DigestHash(username, "line-password-1") {
		t.Error("Asterisk hasn't got the password the app was just given")
	}
	if _, _, err := s.IssuePhoneLine(ctx, tenant, uuid.New(),
		func(string) string { return "x" }, now, audit("device.phone_line")); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("a phone line for a phone that isn't there: %v", err)
	}

	// A proof can be used once.
	jti := uuid.Must(uuid.NewV7())
	if err := s.UseProof(ctx, jti, device.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UseProof(ctx, jti, device.ID, now); !errors.Is(err, pbx.ErrDuplicate) {
		t.Errorf("replaying a proof: %v", err)
	}
	if err := s.DeleteUsedProofs(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.UseProof(ctx, jti, device.ID, now); err != nil {
		t.Errorf("after the proof was swept: %v", err)
	}

	// Renewing keeps the same phone with a new certificate.
	later := now.Add(2 * time.Hour)
	if err := s.TouchIdentity(ctx, device.ID, &enroll.CertUpdate{Serial: "2", Fingerprint: []byte("fingerprint-3"),
		NotAfter: later.Add(183 * 24 * time.Hour)}, "0.2.0", "26.1", later, later.Add(183*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.IdentityByFingerprint(ctx, []byte("fingerprint-1")); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("the old certificate still works: %v", err)
	}
	renewed, _, err := s.IdentityByFingerprint(ctx, []byte("fingerprint-3"))
	if err != nil || renewed.CertSerial != "2" || renewed.AppVersion != "0.2.0" {
		t.Fatalf("after renewal: %+v, %v", renewed, err)
	}

	// Six months with no contact: expired, with its event, and gone from
	// Asterisk's view.
	expired, err := s.ExpireIdentities(ctx, later.Add(184*24*time.Hour))
	if err != nil || len(expired) != 1 || expired[0].ID != device.ID {
		t.Fatalf("expiring: %+v, %v", expired, err)
	}
	var expiredEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE type = 'device.expired'
		AND encode(body, 'escape') LIKE '%' || $1 || '%'`, device.ID.String()).Scan(&expiredEvents); err != nil {
		t.Fatal(err)
	}
	if expiredEvents != 1 {
		t.Errorf("device.expired events: %d", expiredEvents)
	}
	if live() {
		t.Error("an expired phone is still live for Asterisk")
	}
	if _, _, err := s.DevicePrincipalFor(ctx, device.ID, later); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("an expired phone still has a token: %v", err)
	}
	if again, err := s.ExpireIdentities(ctx, later.Add(185*24*time.Hour)); err != nil || len(again) != 0 {
		t.Errorf("expiring twice: %+v, %v", again, err)
	}

	// Being in touch again brings it back.
	back := later.Add(185 * 24 * time.Hour)
	if err := s.TouchIdentity(ctx, device.ID, &enroll.CertUpdate{Serial: "3", Fingerprint: []byte("fingerprint-4"),
		NotAfter: back.Add(183 * 24 * time.Hour)}, "0.2.0", "26.1", back, back.Add(183*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !live() {
		t.Error("a phone that came back isn't live")
	}
	// Changing the password asks for the phone to be set up again (owner,
	// 2026-10-03), with its own device.expired event.
	changed := back.Add(time.Minute)
	if err := s.SetPassword(ctx, tenant, sara.ID, "a-new-hash", changed, true, audit("user.password_change")); err != nil {
		t.Fatal(err)
	}
	if live() {
		t.Error("a phone still works after its person changed their password")
	}
	if _, _, err := s.DevicePrincipalFor(ctx, device.ID, changed); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("a phone still has a token after a password change: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE type = 'device.expired'
		AND encode(body, 'escape') LIKE '%' || $1 || '%'`, device.ID.String()).Scan(&expiredEvents); err != nil {
		t.Fatal(err)
	}
	if expiredEvents != 2 {
		t.Errorf("device.expired events after the password change: %d, want 2", expiredEvents)
	}
	// Adding a password to a passkey-only account doesn't end sessions, and
	// doesn't touch the phones either.
	if err := s.SetPassword(ctx, tenant, sara.ID, "another-hash", changed.Add(time.Minute), false, audit("user.password_added")); err != nil {
		t.Fatal(err)
	}

	// Set up again after that, and then disabling the person takes it away
	// too, as it already does to their browser line.
	again := changed.Add(2 * time.Minute)
	if err := s.TouchIdentity(ctx, device.ID, &enroll.CertUpdate{Serial: "4", Fingerprint: []byte("fingerprint-5"),
		NotAfter: again.Add(183 * 24 * time.Hour)}, "0.2.0", "26.1", again, again.Add(183*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !live() {
		t.Fatal("a phone set up again isn't live")
	}
	if _, err := s.DisableUser(ctx, tenant, sara.ID, again, audit("user.disable")); err != nil {
		t.Fatal(err)
	}
	if live() {
		t.Error("a disabled person's phone is still live")
	}
	if _, _, err := s.DevicePrincipalFor(ctx, device.ID, again); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("a disabled person's phone still has a token: %v", err)
	}

	// Revoked for good: no phone line either, however valid the token in
	// the app's memory still is.
	if _, err := s.RevokeDevice(ctx, tenant, device.ID, again, audit("device.revoke")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.IssuePhoneLine(ctx, tenant, device.ID,
		func(string) string { return "x" }, again, audit("device.phone_line")); !errors.Is(err, pbx.ErrNotFound) {
		t.Errorf("a revoked phone got a phone line: %v", err)
	}
}
