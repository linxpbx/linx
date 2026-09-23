package store

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
)

// TestStoreDocker runs the credential and audit queries against a real
// Postgres with the real migrations. It needs Docker: make test-docker.
func TestStoreDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)

	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.DefaultTenant(ctx); again != tenant {
		t.Fatalf("DefaultTenant changed: %s then %s", tenant, again)
	}

	caller := auth.SystemPrincipal(tenant)
	now := time.Now()
	var ids []uuid.UUID
	for i := range 3 {
		req := auth.CredentialRequest{Kind: auth.TypeAPIKey, Name: "key", Role: auth.RoleAdmin, Scopes: []string{"all"}}
		if i == 0 {
			req.AllowedIPs = []string{"203.0.113.0/24", "2001:db8::1"}
		}
		c, _, err := auth.NewCredential(caller, req, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateCredential(ctx, c, auth.AuditEntry{TenantID: &tenant, Actor: caller.Actor(), Action: "api_key.create",
			Target: "api_key:" + c.ID.String(), Result: auth.ResultOK, Detail: map[string]any{"scopes": c.Scopes}}); err != nil {
			t.Fatalf("CreateCredential: %v", err)
		}
		ids = append(ids, c.ID)
	}

	first, err := s.CredentialByID(ctx, auth.TypeAPIKey, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(first.AllowedIPs) != 2 || !first.AllowedIPs[1].Contains(netip.MustParseAddr("2001:db8::1")) || len(first.SecretHash) != 32 {
		t.Fatalf("round trip lost data: %+v", first)
	}
	byPublic, err := s.CredentialByPublicID(ctx, auth.TypeAPIKey, first.PublicID)
	if err != nil || byPublic.ID != first.ID {
		t.Fatalf("CredentialByPublicID: %v", err)
	}
	if _, err := s.CredentialByPublicID(ctx, auth.TypeOAuthClient, first.PublicID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("a key was found as an OAuth client: %v", err)
	}
	if _, err := s.TenantCredential(ctx, auth.TypeAPIKey, uuid.Must(uuid.NewV7()), first.ID); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("found in another tenant: %v", err)
	}

	page1, err := s.ListCredentials(ctx, auth.TypeAPIKey, tenant, nil, 2)
	if err != nil || len(page1) != 2 || page1[0].ID != ids[2] {
		t.Fatalf("page 1: %v %+v", err, page1)
	}
	page2, err := s.ListCredentials(ctx, auth.TypeAPIKey, tenant, &page1[1].ID, 2)
	if err != nil || len(page2) != 1 || page2[0].ID != ids[0] {
		t.Fatalf("page 2: %v %+v", err, page2)
	}

	ip := netip.MustParseAddr("198.51.100.7")
	if err := s.TouchCredential(ctx, auth.TypeAPIKey, first.ID, ip, now); err != nil {
		t.Fatal(err)
	}
	touched, _ := s.CredentialByID(ctx, auth.TypeAPIKey, first.ID)
	if touched.LastUsedIP == nil || *touched.LastUsedIP != ip || touched.LastUsedAt == nil {
		t.Fatalf("touch not stored: %+v", touched)
	}

	revokeAudit := auth.AuditEntry{TenantID: &tenant, Actor: caller.Actor(), Action: "api_key.revoke", Result: auth.ResultOK}
	r1, err := s.RevokeCredential(ctx, auth.TypeAPIKey, tenant, first.ID, now, revokeAudit)
	if err != nil || r1.RevokedAt == nil {
		t.Fatalf("revoke: %v %+v", err, r1)
	}
	r2, err := s.RevokeCredential(ctx, auth.TypeAPIKey, tenant, first.ID, now.Add(time.Hour), revokeAudit)
	if err != nil || !r2.RevokedAt.Equal(*r1.RevokedAt) {
		t.Fatalf("second revoke changed the time: %v %v", r1.RevokedAt, r2.RevokedAt)
	}
	if _, err := s.RevokeCredential(ctx, auth.TypeAPIKey, tenant, uuid.Must(uuid.NewV7()), now, revokeAudit); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("revoking an unknown key: %v", err)
	}

	jti := uuid.NewString()
	if revoked, err := s.TokenRevoked(ctx, jti); err != nil || revoked {
		t.Fatalf("TokenRevoked on a fresh jti: %v %v", revoked, err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO token_revocation (jti, expires_at) VALUES ($1, $2)", jti, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if revoked, err := s.TokenRevoked(ctx, jti); err != nil || !revoked {
		t.Fatalf("TokenRevoked after revoking: %v %v", revoked, err)
	}

	if err := s.Audit(ctx, auth.AuditEntry{Actor: "anonymous", IP: ip, Action: "auth.failed", Result: auth.ResultDenied,
		Detail: map[string]any{"reason": "unknown_key"}}); err != nil {
		t.Fatal(err)
	}
	var actions []string
	rows, err := pool.Query(ctx, "SELECT action FROM audit_log ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	want := []string{"api_key.create", "api_key.create", "api_key.create", "api_key.revoke", "api_key.revoke", "auth.failed"}
	if !slices.Equal(actions, want) {
		t.Fatalf("audit actions %v, want %v", actions, want)
	}

	// The database refuses a secret hash of the wrong size, whatever the code does.
	bad := first
	bad.ID, bad.PublicID, bad.SecretHash = uuid.Must(uuid.NewV7()), auth.NewPublicID(), []byte("short")
	if err := s.CreateCredential(ctx, bad, revokeAudit); err == nil {
		t.Fatal("stored a malformed secret hash")
	}
}
