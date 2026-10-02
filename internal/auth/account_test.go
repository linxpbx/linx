package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/dbsecret"
)

// fakeAccountStore is an in-memory UserStore for testing Accounts without a
// database (internal/store is tested against a real one, in Docker tests).
type fakeAccountStore struct {
	audits   []AuditEntry
	users    map[uuid.UUID]User
	links    map[string]SetupLink // by token hash, hex-ish (string of bytes)
	sessions map[uuid.UUID]UserSession
	passkeys map[uuid.UUID]Passkey
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
	for _, o := range f.users {
		if o.ID != u.ID && o.TenantID == u.TenantID && o.Email == u.Email {
			return User{}, ErrDuplicate
		}
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

func (f *fakeAccountStore) ConfirmMFA(_ context.Context, _, user uuid.UUID, hashes [][]byte, step int64, _ time.Time, _ AuditEntry) error {
	u, ok := f.users[user]
	if !ok {
		return ErrNotFound
	}
	u.MFASecretEnc, u.MFAPendingSecretEnc = u.MFAPendingSecretEnc, nil
	u.MFAEnabled = true
	u.RecoveryCodeHashes = hashes
	u.MFALastStep = &step
	f.users[user] = u
	return nil
}

func (f *fakeAccountStore) ConsumeRecoveryCode(_ context.Context, _, user uuid.UUID, hash []byte) (bool, error) {
	u, ok := f.users[user]
	if !ok {
		return false, ErrNotFound
	}
	out := make([][]byte, 0, len(u.RecoveryCodeHashes))
	for _, h := range u.RecoveryCodeHashes {
		if string(h) != string(hash) {
			out = append(out, h)
		}
	}
	found := len(out) < len(u.RecoveryCodeHashes)
	u.RecoveryCodeHashes = out
	f.users[user] = u
	return found, nil
}

func (f *fakeAccountStore) UseTOTPStep(_ context.Context, _, user uuid.UUID, step int64) (bool, error) {
	u, ok := f.users[user]
	if !ok {
		return false, ErrNotFound
	}
	if u.MFALastStep != nil && *u.MFALastStep >= step {
		return false, nil
	}
	u.MFALastStep = &step
	f.users[user] = u
	return true, nil
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
			if l.UsedAt != nil || !at.Before(l.ExpiresAt) {
				break
			}
			l.UsedAt = &at
			f.links[k] = l
			return nil
		}
	}
	return ErrNotFound
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

func (f *fakeAccountStore) ConfirmSession(_ context.Context, id uuid.UUID, at time.Time) error {
	s, ok := f.sessions[id]
	if !ok {
		return ErrNotFound
	}
	s.ConfirmedAt = &at
	f.sessions[id] = s
	return nil
}

func (f *fakeAccountStore) ResetMFA(_ context.Context, tenant, user uuid.UUID, at time.Time, _ AuditEntry) (User, error) {
	u, ok := f.users[user]
	if !ok || u.TenantID != tenant {
		return User{}, ErrNotFound
	}
	u.MFASecretEnc, u.MFAPendingSecretEnc, u.MFAEnabled, u.RecoveryCodeHashes, u.MFALastStep = nil, nil, false, nil, nil
	for id, p := range f.passkeys {
		if p.UserID == user {
			delete(f.passkeys, id)
		}
	}
	u.PasskeyCount, u.PasswordOnlyAcceptedAt = 0, nil
	u.Version++
	u.UpdatedAt = at
	f.users[user] = u
	return u, nil
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
func (f *fakeAccountStore) Audit(_ context.Context, e AuditEntry) error {
	f.audits = append(f.audits, e)
	return nil
}

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
	fired, resolved, announced []string
	messages                   []string
}

func (f *fakeAlerts) Announce(_ context.Context, _ uuid.UUID, key, _, _, message, _ string) error {
	f.announced = append(f.announced, key)
	f.messages = append(f.messages, message)
	return nil
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

	out, err := a.CompleteSetup(ctx, token, "a fine long passphrase 1", false, ip, "test-agent")
	if err != nil {
		t.Fatalf("CompleteSetup: %v", err)
	}
	if out.Status != "mfa_setup_required" {
		t.Fatalf("status = %q, want mfa_setup_required", out.Status)
	}
	if out.Session.MFAVerified {
		t.Fatal("an admin's session shouldn't be verified before MFA is even enrolled")
	}
	if _, err := a.CompleteSetup(ctx, token, "another passphrase entirely", false, ip, "test-agent"); err == nil {
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
	// The next step's code (within the ±1 step allowed): the enrollment
	// code's own step is used up.
	code2 := totpCode(rawSecret, uint64(a.Now().Unix())/30+1)
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
	out, err := a.CompleteSetup(adminCtx, token, "a perfectly fine passphrase", false, ip, "ua")
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
	if _, err := a.CompleteSetup(adminCtx, token, "the correct passphrase here", false, ip, "ua"); err != nil {
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
	out, err := a.CompleteSetup(adminCtx, token, "a fine long passphrase 2", false, ip, "ua")
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

// TestMFAEnrollmentConfirmLockout guards against guessing the enrollment
// code itself: unlike every other guessable secret (password, sign-in MFA
// code, confirm-it's-you), ConfirmMFAEnrollment used to have no throttling
// at all, so a pending session (reachable with just the password step
// done) could brute-force a fresh 6-digit code with no limit.
func TestMFAEnrollmentConfirmLockout(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.22")

	_, token, err := a.CreateUser(adminCtx, UserInput{Email: "enrolllock@example.com", Name: "Person", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(adminCtx, token, "a fine long passphrase 3", false, ip, "ua")
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
	right := totpCode(rawSecret, uint64(a.Now().Unix())/30)
	wrong := "000000"
	if wrong == right {
		wrong = "111111"
	}
	for i := 0; i < 5; i++ {
		if _, err := a.ConfirmMFAEnrollment(sessionCtx, wrong); err == nil {
			t.Fatal("a wrong enrollment code should never succeed")
		}
	}
	// Even the right code should now be refused: repeated wrong enrollment
	// codes lock the account the same way repeated wrong passwords do.
	if _, err := a.ConfirmMFAEnrollment(sessionCtx, right); err == nil {
		t.Fatal("the account should be locked out after repeated wrong enrollment codes")
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
	out, err := a.CompleteSetup(adminCtx, token, "a fine long passphrase 3", false, ip, "ua")
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
	// confirming it, from the now fully signed-in session.
	promoted, err := a.Store.SessionByTokenHash(ctx, HashSecret(out.Token))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.BeginMFAEnrollment(WithSession(WithPrincipal(ctx, promoted.Principal()), promoted)); err != nil {
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
	if err := a.VerifyMFA(verifyCtx, totpCode(rawSecret1, uint64(a.Now().Unix())/30+1)); err != nil {
		t.Fatalf("the original confirmed secret should still verify: %v", err)
	}
}

// enrolledUser creates a person with role, sets their password and turns on
// MFA, returning them and their authenticator secret.
func enrolledUser(t *testing.T, a *Accounts, adminCtx context.Context, email, role, password string) (User, []byte) {
	t.Helper()
	ip := netip.MustParseAddr("203.0.113.30")
	u, token, err := a.CreateUser(adminCtx, UserInput{Email: email, Name: "Person", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(adminCtx, token, password, false, ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	sessionCtx := WithSession(WithPrincipal(context.Background(), out.Session.Principal()), out.Session)
	secret, _, err := a.BeginMFAEnrollment(sessionCtx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := totpSecretEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.ConfirmMFAEnrollment(sessionCtx, totpCode(raw, uint64(a.Now().Unix())/30)); err != nil {
		t.Fatal(err)
	}
	return u, raw
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *apihttp.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

// TestPendingSessionCantReplaceMFA: someone who knows only the password
// gets a session waiting on the authenticator code. It must not be able to
// enroll a new authenticator (which would promote it past the code), nor
// change the password.
func TestPendingSessionCantReplaceMFA(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	for _, role := range []string{RoleUser, RoleAdmin} {
		t.Run(role, func(t *testing.T) {
			email := role + "-pending@example.com"
			enrolledUser(t, a, adminCtx, email, role, "the owner's own passphrase")
			signIn, err := a.SignIn(ctx, tenant, email, "the owner's own passphrase", netip.MustParseAddr("198.51.100.7"), "ua")
			if err != nil || signIn.Status != "mfa_verify_required" {
				t.Fatalf("%+v %v", signIn, err)
			}
			pending := WithSession(WithPrincipal(ctx, signIn.Session.Principal()), signIn.Session)
			_, _, err = a.BeginMFAEnrollment(pending)
			wantCode(t, err, "sign_in_unfinished")
			_, err = a.ConfirmMFAEnrollment(pending, "123456")
			wantCode(t, err, "sign_in_unfinished")
			wantCode(t, a.ChangePassword(pending, "the owner's own passphrase", "a brand new passphrase"), "sign_in_unfinished")
			if s, _ := a.Store.SessionByTokenHash(ctx, HashSecret(signIn.Token)); s.MFAVerified {
				t.Fatal("the pending session was promoted")
			}
		})
	}
}

// TestSetupLinkKeepsMFA: a set-password link (an admin resetting a lost
// password) ends the person's other sessions and still asks for their
// authenticator code.
func TestSetupLinkKeepsMFA(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.31")
	u, _ := enrolledUser(t, a, adminCtx, "reset@example.com", RoleUser, "the first passphrase here")
	old, err := a.SignIn(ctx, tenant, u.Email, "the first passphrase here", ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.CreateSetupLink(adminCtx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(ctx, token, "the second passphrase here", false, ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "mfa_verify_required" || out.Session.MFAVerified {
		t.Fatalf("a setup link skipped the authenticator code: %+v", out)
	}
	if s, _ := st.SessionByTokenHash(ctx, HashSecret(old.Token)); s.RevokedAt == nil {
		t.Fatal("the person's earlier session should have ended")
	}
}

// TestAdminCantManageSystemAdmin: an admin can't reset, change or disable
// someone with a role above their own.
func TestAdminCantManageSystemAdmin(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	sys := WithPrincipal(ctx, adminPrincipal(tenant))
	target, _, err := a.CreateUser(sys, UserInput{Email: "root@example.com", Name: "Root", Role: RoleSystemAdmin})
	if err != nil {
		t.Fatal(err)
	}
	peer, _, err := a.CreateUser(sys, UserInput{Email: "peer@example.com", Name: "Peer", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	admin := WithPrincipal(ctx, Principal{Type: TypeUser, ID: uuid.NewString(), TenantID: tenant, Role: RoleAdmin, Scopes: Effective(Scopes, RoleAdmin)})
	_, err = a.CreateSetupLink(admin, target.ID)
	wantCode(t, err, "role_exceeds_caller")
	user := RoleUser
	_, err = a.UpdateUser(admin, target.ID, UserPatch{Role: &user}, "")
	wantCode(t, err, "role_exceeds_caller")
	_, err = a.DisableUser(admin, target.ID)
	wantCode(t, err, "role_exceeds_caller")
	// Their own level is fine.
	if _, err := a.CreateSetupLink(admin, peer.ID); err != nil {
		t.Fatal(err)
	}
}

// TestPendingSessionExpires: a session still waiting on its authenticator
// code lasts PendingSessionTTL, not the full session lifetime.
func TestPendingSessionExpires(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	u, _ := enrolledUser(t, a, adminCtx, "slow@example.com", RoleUser, "a slow typist's passphrase")
	signIn, err := a.SignIn(ctx, tenant, u.Email, "a slow typist's passphrase", netip.MustParseAddr("203.0.113.40"), "ua")
	if err != nil {
		t.Fatal(err)
	}
	authn := NewAuthenticator(st, nil, mustResolver(t), slog.New(slog.DiscardHandler))
	authn.Sessions = st
	status := func(at time.Time) int {
		authn.Now = func() time.Time { return at }
		req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: signIn.Token})
		rr := httptest.NewRecorder()
		authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(rr, req)
		return rr.Code
	}
	created := signIn.Session.CreatedAt
	if got := status(created.Add(PendingSessionTTL - time.Minute)); got != http.StatusNoContent {
		t.Fatalf("within the wait: %d", got)
	}
	if got := status(created.Add(PendingSessionTTL)); got != http.StatusUnauthorized {
		t.Fatalf("after the wait: %d", got)
	}
}

func (f *fakeAccountStore) RecordLockedAttempt(_ context.Context, _, user uuid.UUID, at time.Time) (bool, error) {
	u, ok := f.users[user]
	if !ok {
		return false, ErrNotFound
	}
	if u.FailureWindowStart == nil || at.Sub(*u.FailureWindowStart) > time.Hour {
		u.FailureWindowStart = &at
		u.FailureWindowCount = 1
	} else {
		u.FailureWindowCount++
	}
	f.users[user] = u
	return u.FailureWindowCount == 20, nil
}

// TestGuessingAlertFires: the lockout waits allow only about 10 counted
// password checks an hour, so tries during the wait must count toward the
// "someone is guessing a password" alert, or it could never fire.
func TestGuessingAlertFires(t *testing.T) {
	a, st, alerts := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	u, token, err := a.CreateUser(adminCtx, UserInput{Email: "target@example.com", Name: "Target", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompleteSetup(adminCtx, token, "the real passphrase here", false, netip.MustParseAddr("203.0.113.50"), "ua"); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		// A new address each time: the per-address limit isn't what's tested.
		ip := netip.AddrFrom4([4]byte{198, 51, 100, byte(i + 1)})
		if _, err := a.SignIn(ctx, tenant, u.Email, "a wrong guess entirely", ip, "ua"); err == nil {
			t.Fatal("a wrong password signed in")
		}
	}
	if len(alerts.fired) != 1 || alerts.fired[0] != loginGuessKey(u.ID) {
		t.Fatalf("alerts fired: %v", alerts.fired)
	}
	if got := st.users[u.ID].FailedAttempts; got != 5 {
		t.Errorf("tries during the wait lengthened it: %d counted failures", got)
	}
}

// TestAuthenticatorCodeWorksOnce: an authenticator code (including the one
// typed to turn MFA on) and a recovery code each sign in once.
func TestAuthenticatorCodeWorksOnce(t *testing.T) {
	a, _, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("198.51.100.20")
	const email, password = "once@example.com", "a passphrase for single use"
	_, secret := enrolledUser(t, a, adminCtx, email, RoleAdmin, password)
	// enrolledUser confirmed with this step's code.
	step := uint64(a.Now().Unix()) / 30

	verify := func(code string) error {
		t.Helper()
		out, err := a.SignIn(ctx, tenant, email, password, ip, "ua")
		if err != nil {
			t.Fatal(err)
		}
		return a.VerifyMFA(WithSession(ctx, out.Session), code)
	}
	wantCode(t, verify(totpCode(secret, step)), "mfa_code_used")
	next := totpCode(secret, step+1)
	if err := verify(next); err != nil {
		t.Fatalf("a fresh code: %v", err)
	}
	wantCode(t, verify(next), "mfa_code_used")
	// An older step than the last one accepted is refused too.
	wantCode(t, verify(totpCode(secret, step)), "mfa_code_used")
}

func TestRecoveryCodeWorksOnce(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("198.51.100.21")
	const email, password = "recovery@example.com", "a passphrase for recovery"
	u, _ := enrolledUser(t, a, adminCtx, email, RoleUser, password)
	codes, hashes, err := NewRecoveryCodes()
	if err != nil {
		t.Fatal(err)
	}
	stored := st.users[u.ID]
	stored.RecoveryCodeHashes = hashes
	st.users[u.ID] = stored

	for i, want := range []string{"", "mfa_code_invalid"} {
		out, err := a.SignIn(ctx, tenant, email, password, ip, "ua")
		if err != nil {
			t.Fatal(err)
		}
		err = a.VerifyMFA(WithSession(ctx, out.Session), codes[0])
		if want == "" {
			if err != nil {
				t.Fatalf("try %d: %v", i, err)
			}
			continue
		}
		wantCode(t, err, want)
	}
}

func TestSignInAudited(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("198.51.100.22")
	const email, password = "audited@example.com", "the audited passphrase"
	u, secret := enrolledUser(t, a, adminCtx, email, RoleUser, password)
	st.audits = nil

	_, _ = a.SignIn(ctx, tenant, "nobody@example.com", "typed into the wrong box", ip, "ua")
	_, _ = a.SignIn(ctx, tenant, email, "not the right passphrase", ip, "ua")
	out, err := a.SignIn(ctx, tenant, email, password, ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	sessCtx := WithClientIP(WithSession(ctx, out.Session), ip)
	_ = a.VerifyMFA(sessCtx, "000000x")
	if err := a.VerifyMFA(sessCtx, totpCode(secret, uint64(a.Now().Unix())/30+1)); err != nil {
		t.Fatal(err)
	}

	target := "user:" + u.ID.String()
	want := []struct{ action, actor, target, result, reason, method string }{
		{"user.sign_in", "anonymous", "", ResultDenied, "unknown_email", ""},
		{"user.sign_in", "anonymous", target, ResultDenied, "wrong_password", ""},
		{"user.sign_in", "anonymous", target, ResultOK, "", "password"},
		{"user.sign_in_code", target, target, ResultDenied, "code_invalid", ""},
		{"user.sign_in_code", target, target, ResultOK, "", "authenticator"},
	}
	if len(st.audits) != len(want) {
		t.Fatalf("got %d audit entries, want %d: %+v", len(st.audits), len(want), st.audits)
	}
	for i, w := range want {
		e := st.audits[i]
		if e.Action != w.action || e.Actor != w.actor || e.Target != w.target || e.Result != w.result ||
			e.IP != ip || e.TenantID == nil || *e.TenantID != tenant {
			t.Errorf("entry %d = %+v, want %+v", i, e, w)
		}
		if got, _ := e.Detail["reason"].(string); got != w.reason {
			t.Errorf("entry %d reason = %q, want %q", i, got, w.reason)
		}
		if got, _ := e.Detail["method"].(string); got != w.method {
			t.Errorf("entry %d method = %q, want %q", i, got, w.method)
		}
		for _, v := range e.Detail {
			if s, ok := v.(string); ok && (strings.Contains(s, "passphrase") || strings.Contains(s, "wrong box") || strings.Contains(s, "nobody@")) {
				t.Errorf("entry %d records what was typed: %+v", i, e.Detail)
			}
		}
	}
}

func TestUnlockUser(t *testing.T) {
	a, st, alerts := newTestAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	u, token, err := a.CreateUser(adminCtx, UserInput{Email: "locked@example.com", Name: "Locked", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CompleteSetup(adminCtx, token, "the locked person's passphrase", false, netip.MustParseAddr("203.0.113.60"), "ua"); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		ip := netip.AddrFrom4([4]byte{198, 51, 100, byte(100 + i)})
		_, _ = a.SignIn(ctx, tenant, u.Email, "a wrong guess entirely", ip, "ua")
	}
	_, err = a.SignIn(ctx, tenant, u.Email, "the locked person's passphrase", netip.MustParseAddr("203.0.113.61"), "ua")
	wantCode(t, err, "account_locked")

	// An admin can't unlock a system admin (like every other change).
	sa, _, err := a.CreateUser(adminCtx, UserInput{Email: "sa@example.com", Name: "SA", Role: RoleSystemAdmin})
	if err != nil {
		t.Fatal(err)
	}
	admin := Principal{Type: TypeUser, ID: uuid.NewString(), TenantID: tenant, Role: RoleAdmin, Scopes: Scopes}
	_, err = a.UnlockUser(WithPrincipal(ctx, admin), sa.ID)
	wantCode(t, err, "role_exceeds_caller")

	st.audits = nil
	got, err := a.UnlockUser(adminCtx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LockedUntil != nil || st.users[u.ID].LockedUntil != nil || st.users[u.ID].FailedAttempts != 0 {
		t.Fatalf("still locked: %+v", st.users[u.ID])
	}
	if len(st.audits) != 1 || st.audits[0].Action != "user.unlock" || st.audits[0].Detail["locked_until"] == nil {
		t.Fatalf("audit: %+v", st.audits)
	}
	if !slices.Contains(alerts.resolved, loginGuessKey(u.ID)) {
		t.Errorf("the guessing alert wasn't resolved: %v", alerts.resolved)
	}
	if _, err := a.SignIn(ctx, tenant, u.Email, "the locked person's passphrase", netip.MustParseAddr("203.0.113.61"), "ua"); err != nil {
		t.Fatalf("sign-in after unlock: %v", err)
	}
	_, err = a.UnlockUser(adminCtx, uuid.New())
	wantCode(t, err, "not_found")
}

func (f *fakeAccountStore) AcceptPasswordOnly(_ context.Context, _, user uuid.UUID, at time.Time, _ AuditEntry) error {
	u, ok := f.users[user]
	if !ok {
		return ErrNotFound
	}
	u.PasswordOnlyAcceptedAt = &at
	f.users[user] = u
	return nil
}

// Passkeys: the fake keeps User.PasskeyCount in step, as the real store's
// count does.
func (f *fakeAccountStore) Passkeys(_ context.Context, tenant, user uuid.UUID) ([]Passkey, error) {
	out := []Passkey{}
	for _, p := range f.passkeys {
		if p.TenantID == tenant && p.UserID == user {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Passkey) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out, nil
}

func (f *fakeAccountStore) PasskeyByCredentialID(_ context.Context, id []byte) (Passkey, error) {
	for _, p := range f.passkeys {
		if string(p.CredentialID) == string(id) {
			return p, nil
		}
	}
	return Passkey{}, ErrNotFound
}

func (f *fakeAccountStore) AddPasskey(_ context.Context, p Passkey, recovery [][]byte, _ AuditEntry) error {
	u := f.users[p.UserID]
	if u.PasskeyCount >= MaxPasskeys {
		return ErrLimit
	}
	for _, q := range f.passkeys {
		if string(q.CredentialID) == string(p.CredentialID) {
			return ErrDuplicate
		}
	}
	if f.passkeys == nil {
		f.passkeys = map[uuid.UUID]Passkey{}
	}
	f.passkeys[p.ID] = p
	u.PasskeyCount++
	u.PasswordOnlyAcceptedAt = nil
	if recovery != nil {
		u.RecoveryCodeHashes = recovery
	}
	f.users[p.UserID] = u
	return nil
}

func (f *fakeAccountStore) UsePasskey(_ context.Context, id uuid.UUID, count uint32, backup bool, at time.Time) error {
	p := f.passkeys[id]
	p.SignCount, p.BackupState, p.LastUsedAt = count, backup, &at
	f.passkeys[id] = p
	return nil
}

func (f *fakeAccountStore) RenamePasskey(_ context.Context, tenant, user, id uuid.UUID, name string, _ AuditEntry) (Passkey, error) {
	p, ok := f.passkeys[id]
	if !ok || p.TenantID != tenant || p.UserID != user {
		return Passkey{}, ErrNotFound
	}
	p.Name = name
	f.passkeys[id] = p
	return p, nil
}

func (f *fakeAccountStore) DeletePasskey(_ context.Context, tenant, user, id uuid.UUID, passwordOnly bool, at time.Time, _ AuditEntry) error {
	p, ok := f.passkeys[id]
	if !ok || p.TenantID != tenant || p.UserID != user {
		return ErrNotFound
	}
	delete(f.passkeys, id)
	u := f.users[user]
	u.PasskeyCount--
	if !u.MFAEnabled && u.PasskeyCount == 0 {
		u.RecoveryCodeHashes = nil
	}
	if passwordOnly {
		u.PasswordOnlyAcceptedAt = &at
	}
	f.users[user] = u
	return nil
}

func (f *fakeAccountStore) LiveUserSessions(_ context.Context, user uuid.UUID, now time.Time) ([]UserSession, error) {
	var out []UserSession
	for _, s := range f.sessions {
		if s.UserID == user && s.RevokedAt == nil && s.ExpiresAt.After(now) && s.IdleExpiresAt.After(now) {
			out = append(out, s)
		}
	}
	return out, nil
}

// My account → Signed-in browsers: my live sessions only; signing out one
// or all the others ends them (and closes their phone lines), never
// someone else's.
func TestMySessions(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	var ended []uuid.UUID
	a.SessionsEnded = func(_ context.Context, _ uuid.UUID, s *uuid.UUID) { ended = append(ended, *s) }
	now := time.Now()
	tenant, me, other := uuid.New(), uuid.New(), uuid.New()
	mk := func(user uuid.UUID, expired bool) UserSession {
		s := UserSession{ID: uuid.New(), TenantID: tenant, UserID: user, Role: RoleUser, MFAVerified: true,
			CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour)}
		if expired {
			s.IdleExpiresAt = now.Add(-time.Minute)
		}
		st.sessions[s.ID] = s
		return s
	}
	here, phone, laptop, stale, theirs := mk(me, false), mk(me, false), mk(me, false), mk(me, true), mk(other, false)
	ctx := WithSession(WithPrincipal(context.Background(), here.Principal()), here)

	list, err := a.MySessions(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("MySessions = %d sessions, %v; want 3 (not the expired one, not someone else's)", len(list), err)
	}
	if err := a.SignOutMySession(ctx, theirs.ID); apiErrCode(err) != "not_found" {
		t.Errorf("signing out someone else's session: %v", err)
	}
	if err := a.SignOutMySession(ctx, stale.ID); apiErrCode(err) != "not_found" {
		t.Errorf("signing out an expired session: %v", err)
	}
	if err := a.SignOutMySession(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	if st.sessions[phone.ID].RevokedAt == nil || len(ended) != 1 || ended[0] != phone.ID {
		t.Errorf("phone not ended: %v", ended)
	}
	pending := here
	pending.MFAVerified = false
	if _, err := a.MySessions(WithSession(WithPrincipal(context.Background(), pending.Principal()), pending)); err == nil {
		t.Error("a sign-in still waiting on its second step listed sessions")
	}
	if err := a.SignOutOtherSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if st.sessions[laptop.ID].RevokedAt == nil || st.sessions[here.ID].RevokedAt != nil || st.sessions[theirs.ID].RevokedAt != nil {
		t.Error("sign out everywhere else ended the wrong sessions")
	}
}

func apiErrCode(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Change password asks for the current one first: right → nothing changes,
// wrong → counted like a wrong sign-in.
func TestCheckCurrentPassword(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	tenant := uuid.New()
	hash, err := HashPassword("the current passphrase")
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: uuid.New(), TenantID: tenant, Email: "p@example.com", Name: "P", Role: RoleUser, PasswordHash: hash}
	st.users[u.ID] = u
	sess := UserSession{ID: uuid.New(), TenantID: tenant, UserID: u.ID, Role: RoleUser, MFAVerified: true}
	ctx := WithSession(WithPrincipal(context.Background(), sess.Principal()), sess)
	if err := a.CheckCurrentPassword(ctx, "the current passphrase"); err != nil {
		t.Fatalf("right password: %v", err)
	}
	if err := a.CheckCurrentPassword(ctx, "not it at all, sorry"); apiErrCode(err) != "password_invalid" {
		t.Fatalf("wrong password: %v", err)
	}
	for i := 0; i < FailedAuthPerMinute+1; i++ {
		_ = a.CheckCurrentPassword(ctx, "guess number something")
	}
	if err := a.CheckCurrentPassword(ctx, "the current passphrase"); err == nil {
		t.Error("guessing wasn't limited: even the right password should wait now")
	}
	if st.users[u.ID].PasswordHash != hash {
		t.Error("checking changed the password")
	}
}

// An email is who signs in: changing one needs "confirm it's you", is
// lower-cased, and can't take someone else's.
func TestChangeEmail(t *testing.T) {
	a, st, _ := newTestAccounts(t)
	tenant := uuid.New()
	me := User{ID: uuid.New(), TenantID: tenant, Email: "me@example.com", Name: "Me", Role: RoleUser}
	other := User{ID: uuid.New(), TenantID: tenant, Email: "taken@example.com", Name: "O", Role: RoleUser}
	st.users[me.ID], st.users[other.ID] = me, other
	sess := UserSession{ID: uuid.New(), TenantID: tenant, UserID: me.ID, Role: RoleUser, MFAVerified: true}
	ctx := func(s UserSession) context.Context {
		return WithSession(WithPrincipal(context.Background(), s.Principal()), s)
	}
	if _, err := a.ChangeMyEmail(ctx(sess), "new@example.com"); apiErrCode(err) != errConfirmRequired.Code {
		t.Fatalf("without confirming: %v", err)
	}
	if _, err := a.ChangeMyEmail(ctx(sess), "ME@example.com"); err != nil {
		t.Fatalf("same email, other case, needs no confirm: %v", err)
	}
	at := time.Now()
	sess.ConfirmedAt = &at
	if _, err := a.ChangeMyEmail(ctx(sess), "not an email"); apiErrCode(err) != "email_invalid" {
		t.Errorf("bad email: %v", err)
	}
	if _, err := a.ChangeMyEmail(ctx(sess), "Taken@Example.com"); apiErrCode(err) != "email_taken" {
		t.Errorf("someone else's email: %v", err)
	}
	u, err := a.ChangeMyEmail(ctx(sess), " New@Example.com ")
	if err != nil || u.Email != "new@example.com" {
		t.Fatalf("change: %q, %v", u.Email, err)
	}
	pending := sess
	pending.MFAVerified = false
	if _, err := a.ChangeMyEmail(ctx(pending), "x@example.com"); err == nil {
		t.Error("a half-finished sign-in changed the email")
	}
}
