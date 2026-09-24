package auth

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/dbsecret"
)

// fakeAccountStore is an in-memory UserStore for testing Accounts without a
// database (internal/store is tested against a real one, in Docker tests).
type fakeAccountStore struct {
	users    map[uuid.UUID]User
	links    map[string]SetupLink // by token hash, hex-ish (string of bytes)
	sessions map[uuid.UUID]UserSession
}

func newFakeAccountStore() *fakeAccountStore {
	return &fakeAccountStore{users: map[uuid.UUID]User{}, links: map[string]SetupLink{}, sessions: map[uuid.UUID]UserSession{}}
}

func (f *fakeAccountStore) CreateUser(_ context.Context, u User, _ AuditEntry) error {
	for _, existing := range f.users {
		if existing.TenantID == u.TenantID && existing.Email == u.Email {
			return ErrDuplicate
		}
	}
	f.users[u.ID] = u
	return nil
}

func (f *fakeAccountStore) User(_ context.Context, tenant, id uuid.UUID) (User, error) {
	u, ok := f.users[id]
	if !ok || u.TenantID != tenant {
		return User{}, ErrNotFound
	}
	return u, nil
}

func (f *fakeAccountStore) UserByEmail(_ context.Context, tenant uuid.UUID, email string) (User, error) {
	for _, u := range f.users {
		if u.TenantID == tenant && u.Email == email {
			return u, nil
		}
	}
	return User{}, ErrNotFound
}

func (f *fakeAccountStore) ListUsers(_ context.Context, tenant uuid.UUID, _ *uuid.UUID, limit int) ([]User, error) {
	out := []User{}
	for _, u := range f.users {
		if u.TenantID == tenant {
			out = append(out, u)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeAccountStore) UpdateUser(_ context.Context, u User, _ AuditEntry) (User, error) {
	cur, ok := f.users[u.ID]
	if !ok {
		return User{}, ErrNotFound
	}
	if cur.Version != u.Version {
		return User{}, ErrVersionChanged
	}
	u.Version++
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeAccountStore) DisableUser(_ context.Context, tenant, id uuid.UUID, at time.Time, _ AuditEntry) (User, error) {
	u, ok := f.users[id]
	if !ok || u.TenantID != tenant {
		return User{}, ErrNotFound
	}
	if u.DisabledAt == nil {
		u.DisabledAt = &at
		u.Version++
	}
	f.users[id] = u
	for sid, s := range f.sessions {
		if s.UserID == id && s.RevokedAt == nil {
			s.RevokedAt = &at
			f.sessions[sid] = s
		}
	}
	return u, nil
}

func (f *fakeAccountStore) SetPassword(_ context.Context, _, user uuid.UUID, hash string, at time.Time, revoke bool, _ AuditEntry) error {
	u, ok := f.users[user]
	if !ok {
		return ErrNotFound
	}
	u.PasswordHash, u.PasswordUpdatedAt = hash, at
	f.users[user] = u
	if revoke {
		for sid, s := range f.sessions {
			if s.UserID == user && s.RevokedAt == nil {
				s.RevokedAt = &at
				f.sessions[sid] = s
			}
		}
	}
	return nil
}

func (f *fakeAccountStore) SetMFASecret(_ context.Context, _, user uuid.UUID, sealed []byte, _ time.Time) error {
	u, ok := f.users[user]
	if !ok {
		return ErrNotFound
	}
	u.MFAPendingSecretEnc = sealed
	f.users[user] = u
	return nil
}

func (f *fakeAccountStore) ConfirmMFA(_ context.Context, _, user uuid.UUID, hashes [][]byte, _ time.Time, _ AuditEntry) error {
	u, ok := f.users[user]
	if !ok {
		return ErrNotFound
	}
	u.MFASecretEnc, u.MFAPendingSecretEnc = u.MFAPendingSecretEnc, nil
	u.MFAEnabled = true
	u.RecoveryCodeHashes = hashes
	f.users[user] = u
	return nil
}

func (f *fakeAccountStore) ConsumeRecoveryCode(_ context.Context, _, user uuid.UUID, hash []byte) error {
	u, ok := f.users[user]
	if !ok {
		return ErrNotFound
	}
	out := make([][]byte, 0, len(u.RecoveryCodeHashes))
	for _, h := range u.RecoveryCodeHashes {
		if string(h) != string(hash) {
			out = append(out, h)
		}
	}
	u.RecoveryCodeHashes = out
	f.users[user] = u
	return nil
}

func (f *fakeAccountStore) RecordLoginSuccess(_ context.Context, _, user uuid.UUID, _ time.Time) error {
	u, ok := f.users[user]
	if !ok {
		return ErrNotFound
	}
	u.FailedAttempts, u.LockedUntil, u.FailureWindowStart, u.FailureWindowCount = 0, nil, nil, 0
	f.users[user] = u
	return nil
}

func (f *fakeAccountStore) RecordLoginFailure(_ context.Context, _, user uuid.UUID, at time.Time) (*time.Time, bool, error) {
	u, ok := f.users[user]
	if !ok {
		return nil, false, ErrNotFound
	}
	u.FailedAttempts++
	if u.FailureWindowStart == nil || at.Sub(*u.FailureWindowStart) > time.Hour {
		u.FailureWindowStart = &at
		u.FailureWindowCount = 1
	} else {
		u.FailureWindowCount++
	}
	var lockedUntil *time.Time
	if u.FailedAttempts >= 5 {
		wait := time.Minute * time.Duration(1<<uint(u.FailedAttempts-5))
		if wait > time.Hour {
			wait = time.Hour
		}
		until := at.Add(wait)
		lockedUntil = &until
		u.LockedUntil = &until
	}
	alert := u.FailureWindowCount == 20
	f.users[user] = u
	return lockedUntil, alert, nil
}

func (f *fakeAccountStore) CreateSetupLink(_ context.Context, l SetupLink) error {
	f.links[string(l.TokenHash)] = l
	return nil
}

func (f *fakeAccountStore) SetupLinkByTokenHash(_ context.Context, hash []byte) (SetupLink, error) {
	l, ok := f.links[string(hash)]
	if !ok {
		return SetupLink{}, ErrNotFound
	}
	return l, nil
}

func (f *fakeAccountStore) ConsumeSetupLink(_ context.Context, id uuid.UUID, at time.Time) error {
	for k, l := range f.links {
		if l.ID == id {
			l.UsedAt = &at
			f.links[k] = l
		}
	}
	return nil
}

func (f *fakeAccountStore) CreateSession(_ context.Context, s UserSession) error {
	f.sessions[s.ID] = s
	return nil
}

func (f *fakeAccountStore) RevokeSession(_ context.Context, id uuid.UUID, at time.Time) error {
	s, ok := f.sessions[id]
	if ok && s.RevokedAt == nil {
		s.RevokedAt = &at
		f.sessions[id] = s
	}
	return nil
}

func (f *fakeAccountStore) RevokeUserSessions(_ context.Context, user uuid.UUID, at time.Time) error {
	for id, s := range f.sessions {
		if s.UserID == user && s.RevokedAt == nil {
			s.RevokedAt = &at
			f.sessions[id] = s
		}
	}
	return nil
}

func (f *fakeAccountStore) PromoteSession(_ context.Context, id uuid.UUID) error {
	s, ok := f.sessions[id]
	if !ok {
		return ErrNotFound
	}
	s.MFAVerified = true
	f.sessions[id] = s
	return nil
}

func (f *fakeAccountStore) SessionByTokenHash(_ context.Context, hash []byte) (UserSession, error) {
	for _, s := range f.sessions {
		if string(s.TokenHash) == string(hash) {
			return s, nil
		}
	}
	return UserSession{}, ErrNotFound
}

// The methods below satisfy auth.Store (credential authentication), unused
// by any test in this file but required so fakeAccountStore can also back
// an Authenticator for the session-cookie Middleware tests.
func (f *fakeAccountStore) CredentialByPublicID(context.Context, string, string) (Credential, error) {
	return Credential{}, ErrNotFound
}
func (f *fakeAccountStore) CredentialByID(context.Context, string, uuid.UUID) (Credential, error) {
	return Credential{}, ErrNotFound
}
func (f *fakeAccountStore) TouchCredential(context.Context, string, uuid.UUID, netip.Addr, time.Time) error {
	return nil
}
func (f *fakeAccountStore) TokenRevoked(context.Context, string) (bool, error) { return false, nil }
func (f *fakeAccountStore) Audit(context.Context, AuditEntry) error            { return nil }

func (f *fakeAccountStore) TouchSession(_ context.Context, id uuid.UUID, lastSeen, idleExpires time.Time, ip netip.Addr) error {
	s, ok := f.sessions[id]
	if !ok {
		return ErrNotFound
	}
	s.LastSeenAt, s.IdleExpiresAt = lastSeen, idleExpires
	if ip.IsValid() {
		s.LastSeenIP = &ip
	}
	f.sessions[id] = s
	return nil
}

type fakeAlerts struct {
	fired, resolved []string
}

func (f *fakeAlerts) Fire(_ context.Context, _ uuid.UUID, key, _, _, _, _ string) error {
	f.fired = append(f.fired, key)
	return nil
}

func (f *fakeAlerts) Resolve(_ context.Context, _ uuid.UUID, key string) error {
	f.resolved = append(f.resolved, key)
	return nil
}

func newTestAccounts(t *testing.T) (*Accounts, *fakeAccountStore, *fakeAlerts) {
	t.Helper()
	var key [dbsecret.KeySize]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	st := newFakeAccountStore()
	alerts := &fakeAlerts{}
	return &Accounts{
		Store:    st,
		Sealer:   dbsecret.NewSealer(key),
		Alerts:   alerts,
		Failures: NewLimiters(FailedAuthPerMinute, FailedAuthPerMinute),
		Now:      time.Now,
	}, st, alerts
}

func adminPrincipal(tenant uuid.UUID) Principal {
	return Principal{Type: TypeSystem, ID: "cli", TenantID: tenant, Role: RoleSystemAdmin, Scopes: Scopes}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("VerifyPassword rejected the right password")
	}
	if VerifyPassword(hash, "wrong password entirely") {
		t.Fatal("VerifyPassword accepted the wrong password")
	}
}

func TestCheckPasswordPolicy(t *testing.T) {
	if err := CheckPasswordPolicy("short"); err == nil {
		t.Error("a short password should be refused")
	}
	if err := CheckPasswordPolicy("password123456"); err == nil {
		t.Error("a common password should be refused")
	}
	if err := CheckPasswordPolicy("a genuinely unusual passphrase"); err != nil {
		t.Errorf("a reasonable password was refused: %v", err)
	}
}

func TestTOTPValidatesWithSkew(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0)
	code := totpCode(secret, uint64(now.Unix())/30)
	if !ValidTOTPCode(secret, code, now) {
		t.Fatal("a fresh code should validate")
	}
	if !ValidTOTPCode(secret, code, now.Add(29*time.Second)) {
		t.Fatal("a code should still validate one step later")
	}
	if ValidTOTPCode(secret, code, now.Add(90*time.Second)) {
		t.Fatal("a code should not validate three steps later")
	}
	if ValidTOTPCode(secret, "000000", now) && code == "000000" {
		t.Skip("coincidental code collision")
	}
}

func TestRecoveryCodes(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount || len(hashes) != recoveryCodeCount {
		t.Fatalf("got %d codes, %d hashes", len(codes), len(hashes))
	}
	for i, c := range codes {
		if !SecretMatches(hashes[i], normalizeRecoveryCode(c)) {
			t.Fatalf("code %q doesn't match its own hash", c)
		}
		if !SecretMatches(hashes[i], normalizeRecoveryCode(" "+c+" ")) {
			t.Fatalf("code %q with surrounding space should still match", c)
		}
	}
}

// TestFullSignInFlow walks an admin through: create (setup link), complete
// setup (still pending MFA, since admin requires it), enroll MFA, confirm
// it, sign out, then sign back in with the authenticator step.
func TestFullSignInFlow(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	ctx = WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.7")

	u, token, err := a.CreateUser(ctx, UserInput{Email: "New.Admin@Example.com", Name: "New Admin", Role: RoleAdmin})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.Email != "new.admin@example.com" {
		t.Fatalf("email should be lower-cased, got %q", u.Email)
	}

	out, err := a.CompleteSetup(ctx, token, "a fine long passphrase 1", ip, "test-agent")
	if err != nil {
		t.Fatalf("CompleteSetup: %v", err)
	}
	if out.Status != "mfa_setup_required" {
		t.Fatalf("status = %q, want mfa_setup_required", out.Status)
	}
	if out.Session.MFAVerified {
		t.Fatal("an admin's session shouldn't be verified before MFA is even enrolled")
	}
	if _, err := a.CompleteSetup(ctx, token, "another passphrase entirely", ip, "test-agent"); err == nil {
		t.Fatal("a used setup link should be refused a second time")
	}

	sessionCtx := WithSession(WithPrincipal(ctx, out.Session.Principal()), out.Session)
	if out.Session.Principal().Has("extensions:write") {
		t.Fatal("a pending session must hold no scopes")
	}
	secret, uri, err := a.BeginMFAEnrollment(sessionCtx)
	if err != nil {
		t.Fatalf("BeginMFAEnrollment: %v", err)
	}
	if secret == "" || uri == "" {
		t.Fatal("expected a secret and a provisioning URI")
	}
	rawSecret, err := totpSecretEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	code := totpCode(rawSecret, uint64(a.Now().Unix())/30)
	codes, err := a.ConfirmMFAEnrollment(sessionCtx, code)
	if err != nil {
		t.Fatalf("ConfirmMFAEnrollment: %v", err)
	}
	if len(codes) != recoveryCodeCount {
		t.Fatalf("got %d recovery codes", len(codes))
	}

	promoted, err := a.Store.SessionByTokenHash(ctx, HashSecret(out.Token))
	if err != nil {
		t.Fatal(err)
	}
	if !promoted.MFAVerified {
		t.Fatal("the pending session should have been promoted once MFA was confirmed")
	}

	if err := a.SignOut(WithSession(ctx, promoted)); err != nil {
		t.Fatalf("SignOut: %v", err)
	}

	// Sign in again: now MFA is enabled, so the second sign-in step is
	// verifying a code, not enrolling.
	signIn, err := a.SignIn(ctx, tenant, "New.Admin@Example.com", "a fine long passphrase 1", ip, "test-agent")
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if signIn.Status != "mfa_verify_required" {
		t.Fatalf("status = %q, want mfa_verify_required", signIn.Status)
	}
	verifyCtx := WithSession(ctx, signIn.Session)
	code2 := totpCode(rawSecret, uint64(a.Now().Unix())/30)
	if err := a.VerifyMFA(verifyCtx, code2); err != nil {
		t.Fatalf("VerifyMFA: %v", err)
	}
}

func TestMiddlewareSessionCookieAndCSRF(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.11")

	u, token, err := a.CreateUser(adminCtx, UserInput{Email: "browser@example.com", Name: "Browser", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(adminCtx, token, "a perfectly fine passphrase", ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	_ = u

	authn := NewAuthenticator(st, nil, mustResolver(t), slog.New(slog.DiscardHandler))
	authn.Sessions = st
	authn.Now = a.Now

	var gotPrincipal Principal
	var sawPending bool
	final := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFromContext(r.Context())
		gotPrincipal = p
		sawPending = p.Pending
		w.WriteHeader(http.StatusNoContent)
	})
	handler := authn.Middleware(final)

	// GET with a valid session cookie: no CSRF needed.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: out.Token})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("GET with valid cookie: status = %d", rr.Code)
	}
	if gotPrincipal.Type != TypeUser || gotPrincipal.ID != u.ID.String() {
		t.Fatalf("unexpected principal: %+v", gotPrincipal)
	}
	if sawPending {
		t.Fatal("a non-admin account with no MFA required shouldn't be pending")
	}

	// POST without a CSRF header: refused.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/extensions", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: out.Token})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF header: status = %d, want 403", rr.Code)
	}

	// POST with the right CSRF header: allowed through.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/extensions", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: out.Token})
	req.Header.Set(CSRFHeaderName, out.CSRF)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("POST with correct CSRF header: status = %d", rr.Code)
	}

	// A garbage cookie is refused outright, not treated as anonymous.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "not-a-real-token"})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("garbage cookie: status = %d, want 401", rr.Code)
	}
}

func mustResolver(t *testing.T) *ClientIPResolver {
	t.Helper()
	r, err := NewClientIPResolver("")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestSignInLockout(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.9")

	u, token, err := a.CreateUser(adminCtx, UserInput{Email: "person@example.com", Name: "Person", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompleteSetup(adminCtx, token, "the correct passphrase here", ip, "ua"); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		if _, err := a.SignIn(ctx, tenant, u.Email, "wrong passphrase entirely", ip, "ua"); err == nil {
			t.Fatal("a wrong password should never succeed")
		}
	}
	_, err = a.SignIn(ctx, tenant, u.Email, "the correct passphrase here", ip, "ua")
	if err == nil {
		t.Fatal("the right password during the lockout wait should still fail")
	}
	if e, ok := err.(interface{ Error() string }); !ok || e.Error() == "" {
		t.Fatal("expected a locked-out error")
	}
}

func TestVerifyMFALockout(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.21")

	u, token, err := a.CreateUser(adminCtx, UserInput{Email: "mfalock@example.com", Name: "Person", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(adminCtx, token, "a fine long passphrase 2", ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx := WithSession(WithPrincipal(ctx, out.Session.Principal()), out.Session)
	secret, _, err := a.BeginMFAEnrollment(sessionCtx)
	if err != nil {
		t.Fatal(err)
	}
	rawSecret, err := totpSecretEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfirmMFAEnrollment(sessionCtx, totpCode(rawSecret, uint64(a.Now().Unix())/30)); err != nil {
		t.Fatal(err)
	}

	// Sign in again to get a fresh session pending its authenticator code.
	signIn, err := a.SignIn(ctx, tenant, u.Email, "a fine long passphrase 2", ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	verifyCtx := WithSession(ctx, signIn.Session)

	wrong := "000000"
	if wrong == totpCode(rawSecret, uint64(a.Now().Unix())/30) {
		wrong = "111111"
	}
	for i := 0; i < 5; i++ {
		if err := a.VerifyMFA(verifyCtx, wrong); err == nil {
			t.Fatal("a wrong code should never succeed")
		}
	}
	// Even the right code should now be refused: repeated wrong MFA codes
	// lock the account the same way repeated wrong passwords do.
	if err := a.VerifyMFA(verifyCtx, totpCode(rawSecret, uint64(a.Now().Unix())/30)); err == nil {
		t.Fatal("the account should be locked out after repeated wrong MFA codes")
	}
}

// TestMFAReEnrollmentDoesNotDisableExisting guards against starting a new
// (unconfirmed) enrollment silently turning MFA off for an account that
// already has it confirmed and enabled.
func TestMFAReEnrollmentDoesNotDisableExisting(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.22")

	_, token, err := a.CreateUser(adminCtx, UserInput{Email: "reenroll@example.com", Name: "Person", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(adminCtx, token, "a fine long passphrase 3", ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx := WithSession(WithPrincipal(ctx, out.Session.Principal()), out.Session)
	secret1, _, err := a.BeginMFAEnrollment(sessionCtx)
	if err != nil {
		t.Fatal(err)
	}
	rawSecret1, err := totpSecretEncoding.DecodeString(secret1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfirmMFAEnrollment(sessionCtx, totpCode(rawSecret1, uint64(a.Now().Unix())/30)); err != nil {
		t.Fatal(err)
	}

	// Start a second enrollment (e.g. setting up a new phone) without
	// confirming it.
	if _, _, err := a.BeginMFAEnrollment(sessionCtx); err != nil {
		t.Fatal(err)
	}

	// MFA must still be required, and the original confirmed secret must
	// still work: an unconfirmed new enrollment can't weaken the account.
	signIn, err := a.SignIn(ctx, tenant, "reenroll@example.com", "a fine long passphrase 3", ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if signIn.Status != "mfa_verify_required" {
		t.Fatalf("status = %q, want mfa_verify_required (MFA should still be required)", signIn.Status)
	}
	verifyCtx := WithSession(ctx, signIn.Session)
	if err := a.VerifyMFA(verifyCtx, totpCode(rawSecret1, uint64(a.Now().Unix())/30)); err != nil {
		t.Fatalf("the original confirmed secret should still verify: %v", err)
	}
}
