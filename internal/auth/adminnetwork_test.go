package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeAdminNetworks is a fixed answer to "where admins may sign in from"
// (docs/ADMIN.md §3), for testing Authenticator.restrictAdminNetwork.
type fakeAdminNetworks struct {
	restricted bool
	networks   []netip.Prefix
}

func (f fakeAdminNetworks) AdminAccess(context.Context) (bool, []netip.Prefix, error) {
	return f.restricted, f.networks, nil
}

func TestAdminNetworkRestriction(t *testing.T) {
	st := newFakeAccountStore()
	tenant := uuid.New()
	now := time.Now()
	hash, err := HashPassword("a perfectly fine passphrase")
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: uuid.New(), TenantID: tenant, Email: "admin@example.com", Name: "Admin", Role: RoleAdmin,
		PasswordHash: hash, PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateUser(context.Background(), u, AuditEntry{Action: "user.create", Actor: "system:cli", Result: ResultOK}); err != nil {
		t.Fatal(err)
	}
	token := NewSessionToken()
	sess := UserSession{
		ID: uuid.New(), TenantID: tenant, UserID: u.ID, Role: RoleAdmin,
		TokenHash: HashSecret(token), CSRFHash: HashSecret(NewCSRFToken()), MFAVerified: true,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastSeenAt: now,
	}
	if err := st.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}

	authn := NewAuthenticator(st, nil, mustResolver(t), slog.New(slog.DiscardHandler))
	authn.Sessions = st
	authn.Now = func() time.Time { return now }
	authn.Networks = fakeAdminNetworks{restricted: true, networks: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}

	var got Principal
	handler := authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		got = p
		w.WriteHeader(http.StatusNoContent)
	}))

	do := func(remoteAddr string) Principal {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: token})
		req.RemoteAddr = remoteAddr
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rr.Code)
		}
		return got
	}

	t.Run("outside the allowed network", func(t *testing.T) {
		p := do("203.0.113.9:1234")
		if !p.AdminNetworkRestricted {
			t.Error("expected AdminNetworkRestricted")
		}
		if slices.Contains(p.Scopes, "users:write") {
			t.Errorf("an admin scope leaked from outside the allowed network: %v", p.Scopes)
		}
		if p.Role != RoleAdmin {
			t.Errorf("role changed to %q; only scopes should narrow", p.Role)
		}
	})

	t.Run("inside the allowed network", func(t *testing.T) {
		p := do("10.1.2.3:1234")
		if p.AdminNetworkRestricted {
			t.Error("shouldn't be restricted from an allowed network")
		}
		if !slices.Contains(p.Scopes, "users:write") {
			t.Errorf("lost admin scopes from an allowed network: %v", p.Scopes)
		}
	})

	t.Run("restriction off: reaches from anywhere", func(t *testing.T) {
		authn.Networks = fakeAdminNetworks{restricted: false}
		p := do("203.0.113.9:1234")
		if p.AdminNetworkRestricted {
			t.Error("restriction is off; shouldn't be flagged")
		}
		if !slices.Contains(p.Scopes, "users:write") {
			t.Errorf("lost admin scopes although the restriction is off: %v", p.Scopes)
		}
	})
}

func TestRequireConfirmed(t *testing.T) {
	now := time.Now()
	confirmed := now.Add(-5 * time.Minute)
	stale := now.Add(-11 * time.Minute)

	if err := RequireConfirmed(context.Background(), now); err != nil {
		t.Errorf("no session in context should pass through (API keys aren't sessions): %v", err)
	}

	ctx := WithSession(context.Background(), UserSession{ConfirmedAt: &confirmed})
	if err := RequireConfirmed(ctx, now); err != nil {
		t.Errorf("confirmed 5 minutes ago should still count: %v", err)
	}

	ctx = WithSession(context.Background(), UserSession{ConfirmedAt: &stale})
	if err := RequireConfirmed(ctx, now); err == nil {
		t.Error("confirmed 11 minutes ago should need a fresh confirmation")
	}

	ctx = WithSession(context.Background(), UserSession{})
	if err := RequireConfirmed(ctx, now); err == nil {
		t.Error("never confirmed should need one")
	}
}
