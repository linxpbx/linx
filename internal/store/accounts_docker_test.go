package store

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
)

// TestAccountsDocker runs the people-account and session queries —
// including optimistic concurrency, the disable/password-change session
// cascade and the lockout/rolling-window arithmetic — against real
// Postgres. It needs Docker: make test-docker.
func TestAccountsDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-accounts-store-test")
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

	newUser := func(email, role string) auth.User {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		u := auth.User{ID: id, TenantID: tenant, Email: email, Name: "Test Person", Role: role,
			PasswordHash: "placeholder", PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateUser(ctx, u, audit("user.create")); err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		return u
	}

	t.Run("create and duplicate email", func(t *testing.T) {
		u := newUser("person@example.com", auth.RoleUser)
		got, err := s.User(ctx, tenant, u.ID)
		if err != nil || got.Email != u.Email {
			t.Fatalf("User: %+v, %v", got, err)
		}
		if _, err := s.UserByEmail(ctx, tenant, "PERSON@EXAMPLE.COM"); err != nil {
			t.Fatalf("UserByEmail should be case-insensitive: %v", err)
		}
		dup := u
		dup.ID = uuid.Must(uuid.NewV7())
		if err := s.CreateUser(ctx, dup, audit("user.create")); err != auth.ErrDuplicate {
			t.Fatalf("duplicate email: err = %v, want ErrDuplicate", err)
		}
	})

	t.Run("update optimistic concurrency", func(t *testing.T) {
		u := newUser("update@example.com", auth.RoleUser)
		u.Name = "Renamed"
		out, err := s.UpdateUser(ctx, u, audit("user.update"))
		if err != nil || out.Name != "Renamed" || out.Version != 2 {
			t.Fatalf("UpdateUser: %+v, %v", out, err)
		}
		if _, err := s.UpdateUser(ctx, u, audit("user.update")); err != auth.ErrVersionChanged {
			t.Fatalf("stale version: err = %v, want ErrVersionChanged", err)
		}
	})

	t.Run("disable cascades to sessions", func(t *testing.T) {
		u := newUser("disable@example.com", auth.RoleUser)
		sess := auth.UserSession{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: u.ID, Role: u.Role,
			TokenHash: auth.HashSecret("tok1"), CSRFHash: auth.HashSecret("csrf1"), MFAVerified: true,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastSeenAt: now}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatalf("CreateSession: %v", err)
		}
		if _, err := s.DisableUser(ctx, tenant, u.ID, now, audit("user.disable")); err != nil {
			t.Fatalf("DisableUser: %v", err)
		}
		got, err := s.SessionByTokenHash(ctx, auth.HashSecret("tok1"))
		if err != nil || got.RevokedAt == nil {
			t.Fatalf("session should be revoked: %+v, %v", got, err)
		}
		// Disabling again is a no-op, not an error.
		if _, err := s.DisableUser(ctx, tenant, u.ID, now.Add(time.Minute), audit("user.disable")); err != nil {
			t.Fatalf("disabling twice: %v", err)
		}
	})

	t.Run("set password revokes sessions", func(t *testing.T) {
		u := newUser("password@example.com", auth.RoleUser)
		sess := auth.UserSession{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: u.ID, Role: u.Role,
			TokenHash: auth.HashSecret("tok2"), CSRFHash: auth.HashSecret("csrf2"), MFAVerified: true,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastSeenAt: now}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		if err := s.SetPassword(ctx, tenant, u.ID, "new-hash", now, true, audit("user.password_changed")); err != nil {
			t.Fatalf("SetPassword: %v", err)
		}
		got, _ := s.User(ctx, tenant, u.ID)
		if got.PasswordHash != "new-hash" {
			t.Fatalf("password not updated: %+v", got)
		}
		revoked, err := s.SessionByTokenHash(ctx, auth.HashSecret("tok2"))
		if err != nil || revoked.RevokedAt == nil {
			t.Fatalf("session should be revoked by a password change: %+v, %v", revoked, err)
		}
	})

	t.Run("MFA enrollment and recovery codes", func(t *testing.T) {
		u := newUser("mfa@example.com", auth.RoleAdmin)
		if err := s.SetMFASecret(ctx, tenant, u.ID, []byte("sealed-secret"), now); err != nil {
			t.Fatalf("SetMFASecret: %v", err)
		}
		mid, _ := s.User(ctx, tenant, u.ID)
		if mid.MFAEnabled || len(mid.MFAPendingSecretEnc) == 0 || len(mid.MFASecretEnc) != 0 {
			t.Fatalf("expected a pending secret, not yet enabled: %+v", mid)
		}
		hashes := [][]byte{auth.HashSecret("code-one"), auth.HashSecret("code-two")}
		if err := s.ConfirmMFA(ctx, tenant, u.ID, hashes, now, audit("user.mfa_enabled")); err != nil {
			t.Fatalf("ConfirmMFA: %v", err)
		}
		confirmed, _ := s.User(ctx, tenant, u.ID)
		if !confirmed.MFAEnabled || len(confirmed.RecoveryCodeHashes) != 2 {
			t.Fatalf("expected MFA enabled with 2 recovery codes: %+v", confirmed)
		}
		if err := s.ConsumeRecoveryCode(ctx, tenant, u.ID, hashes[0]); err != nil {
			t.Fatalf("ConsumeRecoveryCode: %v", err)
		}
		after, _ := s.User(ctx, tenant, u.ID)
		if len(after.RecoveryCodeHashes) != 1 {
			t.Fatalf("expected 1 recovery code left, got %d", len(after.RecoveryCodeHashes))
		}

		// Restarting enrollment must not disturb the confirmed secret: it
		// stays active (and MFA stays enabled) until a code from the *new*
		// pending secret is confirmed in turn.
		if err := s.SetMFASecret(ctx, tenant, u.ID, []byte("second-sealed-secret"), now); err != nil {
			t.Fatalf("SetMFASecret (restart): %v", err)
		}
		restarted, _ := s.User(ctx, tenant, u.ID)
		if !restarted.MFAEnabled || string(restarted.MFASecretEnc) != "sealed-secret" {
			t.Fatalf("restarting enrollment must not disable or replace the active secret: %+v", restarted)
		}
		if string(restarted.MFAPendingSecretEnc) != "second-sealed-secret" {
			t.Fatalf("expected the new secret to be pending, got %q", restarted.MFAPendingSecretEnc)
		}
	})

	t.Run("setup links are single use", func(t *testing.T) {
		u := newUser("setup@example.com", auth.RoleUser)
		link := auth.SetupLink{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: u.ID,
			TokenHash: auth.HashSecret("link-token"), CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
		if err := s.CreateSetupLink(ctx, link); err != nil {
			t.Fatalf("CreateSetupLink: %v", err)
		}
		got, err := s.SetupLinkByTokenHash(ctx, auth.HashSecret("link-token"))
		if err != nil || got.UsedAt != nil {
			t.Fatalf("fresh link: %+v, %v", got, err)
		}
		if err := s.ConsumeSetupLink(ctx, link.ID, now); err != nil {
			t.Fatalf("ConsumeSetupLink: %v", err)
		}
		if err := s.ConsumeSetupLink(ctx, link.ID, now); !errors.Is(err, auth.ErrNotFound) {
			t.Fatalf("a used link was used again: %v", err)
		}
		used, err := s.SetupLinkByTokenHash(ctx, auth.HashSecret("link-token"))
		if err != nil || used.UsedAt == nil {
			t.Fatalf("consumed link should have UsedAt set: %+v, %v", used, err)
		}
	})

	t.Run("session promotion", func(t *testing.T) {
		u := newUser("pending@example.com", auth.RoleAdmin)
		sess := auth.UserSession{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, UserID: u.ID, Role: u.Role,
			TokenHash: auth.HashSecret("tok3"), CSRFHash: auth.HashSecret("csrf3"), MFAVerified: false,
			CreatedAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastSeenAt: now}
		if err := s.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
		if err := s.PromoteSession(ctx, sess.ID); err != nil {
			t.Fatalf("PromoteSession: %v", err)
		}
		got, err := s.SessionByTokenHash(ctx, auth.HashSecret("tok3"))
		if err != nil || !got.MFAVerified {
			t.Fatalf("session should be promoted: %+v, %v", got, err)
		}
		if err := s.TouchSession(ctx, sess.ID, now.Add(time.Minute), now.Add(time.Hour+time.Minute), netip.MustParseAddr("203.0.113.5")); err != nil {
			t.Fatalf("TouchSession: %v", err)
		}
		if err := s.RevokeSession(ctx, sess.ID, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("RevokeSession: %v", err)
		}
		if _, err := s.SessionByTokenHash(ctx, auth.HashSecret("tok3")); err != nil {
			t.Fatalf("a revoked session should still be found (callers check RevokedAt): %v", err)
		}
	})

	t.Run("login lockout and rolling window", func(t *testing.T) {
		u := newUser("lockout@example.com", auth.RoleUser)
		base := now
		for i := 1; i <= 4; i++ {
			lockedUntil, alert, err := s.RecordLoginFailure(ctx, tenant, u.ID, base.Add(time.Duration(i)*time.Second))
			if err != nil {
				t.Fatalf("RecordLoginFailure %d: %v", i, err)
			}
			if lockedUntil != nil {
				t.Fatalf("failure %d shouldn't lock yet", i)
			}
			if alert {
				t.Fatalf("failure %d shouldn't hit the alert threshold", i)
			}
		}
		// 5th failure: locked for 1 minute (2^0).
		lockedUntil, _, err := s.RecordLoginFailure(ctx, tenant, u.ID, base.Add(5*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if lockedUntil == nil {
			t.Fatal("the 5th failure should lock the account")
		}
		wantUntil := base.Add(5 * time.Second).Add(time.Minute)
		if diff := lockedUntil.Sub(wantUntil); diff < -time.Second || diff > time.Second {
			t.Fatalf("locked_until = %v, want close to %v", lockedUntil, wantUntil)
		}
		// A successful login clears it.
		if err := s.RecordLoginSuccess(ctx, tenant, u.ID, base.Add(10*time.Second)); err != nil {
			t.Fatal(err)
		}
		cleared, _ := s.User(ctx, tenant, u.ID)
		if cleared.FailedAttempts != 0 || cleared.LockedUntil != nil || cleared.FailureWindowCount != 0 {
			t.Fatalf("login success should clear lockout state: %+v", cleared)
		}

		// The rolling window fires the alert on the 20th failure within an hour.
		u2 := newUser("guessing@example.com", auth.RoleUser)
		var lastAlert bool
		for i := 1; i <= 20; i++ {
			_, alert, err := s.RecordLoginFailure(ctx, tenant, u2.ID, base.Add(time.Duration(i)*time.Second))
			if err != nil {
				t.Fatalf("RecordLoginFailure %d: %v", i, err)
			}
			lastAlert = alert
		}
		if !lastAlert {
			t.Fatal("the 20th failure in an hour should cross the alert threshold")
		}

		// Tries during the lockout wait count toward the alert without
		// lengthening the wait.
		u3 := newUser("waiting@example.com", auth.RoleUser)
		for i := 1; i <= 5; i++ {
			if _, _, err := s.RecordLoginFailure(ctx, tenant, u3.ID, base.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		lastAlert = false
		for i := 6; i <= 20; i++ {
			alert, err := s.RecordLockedAttempt(ctx, tenant, u3.ID, base.Add(time.Duration(i)*time.Second))
			if err != nil {
				t.Fatalf("RecordLockedAttempt %d: %v", i, err)
			}
			lastAlert = alert
		}
		after, _ := s.User(ctx, tenant, u3.ID)
		if !lastAlert || after.FailedAttempts != 5 || after.FailureWindowCount != 20 {
			t.Fatalf("locked tries: alert %v, %+v", lastAlert, after)
		}
	})
}
