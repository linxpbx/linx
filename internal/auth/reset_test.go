package auth

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeMailer records what the reset flow emails.
type fakeMailer struct {
	off     bool
	links   map[string]string // email → token
	changed []string
}

func (m *fakeMailer) On(context.Context, uuid.UUID) (bool, error) { return !m.off, nil }

func (m *fakeMailer) ResetLink(_ context.Context, u User, token string) error {
	if m.links == nil {
		m.links = map[string]string{}
	}
	m.links[u.Email] = token
	return nil
}

func (m *fakeMailer) PasswordChanged(_ context.Context, u User, _ time.Time, _ netip.Addr, _ string) error {
	m.changed = append(m.changed, u.Email)
	return nil
}

func newResetAccounts(t *testing.T) (*Accounts, *fakeAccountStore, *fakeAlerts, *fakeMailer) {
	t.Helper()
	a, st, alerts := newTestAccounts(t)
	m := &fakeMailer{}
	a.Mailer = m
	a.Later = func(run func()) { run() }
	return a, st, alerts, m
}

func resetUser(t *testing.T, st *fakeAccountStore, tenant uuid.UUID, email, role, password string) User {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: uuid.New(), TenantID: tenant, Email: email, Name: "Sara", Role: role, PasswordHash: hash}
	st.users[u.ID] = u
	return u
}

// An unknown email and a known one get the same answer; only the known one
// gets a link, which is a reset link (not a set-password one) for 30
// minutes.
func TestRequestPasswordResetSameAnswer(t *testing.T) {
	a, st, _, m := newResetAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	ip := netip.MustParseAddr("203.0.113.40")
	resetUser(t, st, tenant, "sara@example.com", RoleUser, "the first passphrase here")
	if err := a.RequestPasswordReset(ctx, tenant, "nobody@example.com", ip); err != nil {
		t.Fatalf("unknown email: %v", err)
	}
	if err := a.RequestPasswordReset(ctx, tenant, " Sara@Example.com ", ip); err != nil {
		t.Fatalf("known email: %v", err)
	}
	if len(m.links) != 1 || m.links["sara@example.com"] == "" {
		t.Fatalf("links emailed: %v", m.links)
	}
	link, err := st.SetupLinkByTokenHash(ctx, HashSecret(m.links["sara@example.com"]))
	if err != nil {
		t.Fatal(err)
	}
	if link.Purpose != LinkReset || link.ExpiresAt.Sub(link.CreatedAt) != ResetLinkTTL {
		t.Fatalf("link: %+v", link)
	}
	// A reset link is not a set-password link.
	if _, err := a.CheckSetupLink(ctx, m.links["sara@example.com"], ip); apiErrCode(err) != "setup_link_invalid" {
		t.Fatalf("reset link used as a set-password link: %v", err)
	}
	for _, e := range st.audits {
		if e.Action == "user.password_reset_requested" && e.Target == "" {
			for _, v := range e.Detail {
				if s, ok := v.(string); ok && strings.Contains(s, "nobody@") {
					t.Fatal("an email that matched nobody was written to the audit log")
				}
			}
		}
	}

	m.off = true
	if err := a.RequestPasswordReset(ctx, tenant, "sara@example.com", ip); apiErrCode(err) != "password_reset_off" {
		t.Fatalf("email off: %v", err)
	}
}

// No link for a disabled account, nor one that must use company sign-in;
// the answer is still the same.
func TestRequestPasswordResetNotForEveryone(t *testing.T) {
	a, st, _, m := newResetAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	ip := netip.MustParseAddr("203.0.113.41")
	u := resetUser(t, st, tenant, "gone@example.com", RoleUser, "the first passphrase here")
	now := time.Now()
	u.DisabledAt = &now
	st.users[u.ID] = u
	if err := a.RequestPasswordReset(ctx, tenant, u.Email, ip); err != nil {
		t.Fatal(err)
	}
	if len(m.links) != 0 {
		t.Fatalf("a disabled account got a link: %v", m.links)
	}
}

// 3 an hour per email and 10 per address; beyond, nothing is sent, the
// answer doesn't change, and too many from one address announces one
// alert.
func TestRequestPasswordResetLimits(t *testing.T) {
	a, st, alerts, m := newResetAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	resetUser(t, st, tenant, "sara@example.com", RoleUser, "the first passphrase here")
	sent := 0
	for i := 0; i < 5; i++ {
		m.links = nil
		if err := a.RequestPasswordReset(ctx, tenant, "sara@example.com", netip.MustParseAddr("203.0.113.50")); err != nil {
			t.Fatal(err)
		}
		if m.links != nil {
			sent++
		}
	}
	if sent != ResetsPerEmail {
		t.Fatalf("sent %d links to one email, want %d", sent, ResetsPerEmail)
	}
	ip := netip.MustParseAddr("198.51.100.60")
	for i := 0; i < ResetsPerAddress+5; i++ {
		if err := a.RequestPasswordReset(ctx, tenant, "x"+string(rune('a'+i))+"@example.com", ip); err != nil {
			t.Fatal(err)
		}
	}
	if len(alerts.announced) != 1 || !strings.HasPrefix(alerts.announced[0], resetManyPrefix) {
		t.Fatalf("alerts announced: %v", alerts.announced)
	}
}

// With an authenticator, the new password changes nothing until the code
// is right; then the link is used up, other sessions end, and the person
// is signed in and told by email.
func TestCompleteResetNeedsSecondStep(t *testing.T) {
	a, st, alerts, m := newResetAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.42")
	u, secret := enrolledUser(t, a, adminCtx, "sara@example.com", RoleUser, "the first passphrase here")
	old, err := a.SignIn(ctx, tenant, u.Email, "the first passphrase here", ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RequestPasswordReset(ctx, tenant, u.Email, ip); err != nil {
		t.Fatal(err)
	}
	token := m.links[u.Email]
	info, err := a.CheckResetLink(ctx, token, ip)
	if err != nil || info.Email != u.Email {
		t.Fatalf("check: %+v %v", info, err)
	}
	before := st.users[u.ID].PasswordHash

	_, err = a.CompleteReset(ctx, token, "the second passphrase here", ResetProof{}, ip, "ua")
	wantCode(t, err, "second_step_required")
	_, err = a.CompleteReset(ctx, token, "the second passphrase here", ResetProof{Code: "000000"}, ip, "ua")
	wantCode(t, err, "mfa_code_invalid")
	if st.users[u.ID].PasswordHash != before {
		t.Fatal("the password changed before the second step passed")
	}
	if s, _ := st.SessionByTokenHash(ctx, HashSecret(old.Token)); s.RevokedAt != nil {
		t.Fatal("sessions ended before the second step passed")
	}

	out, err := a.CompleteReset(ctx, token, "the second passphrase here",
		ResetProof{Code: totpCode(secret, uint64(a.Now().Unix())/30+1)}, ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "signed_in" || !out.Session.MFAVerified {
		t.Fatalf("outcome: %+v", out)
	}
	if !VerifyPassword(st.users[u.ID].PasswordHash, "the second passphrase here") {
		t.Fatal("the new password wasn't set")
	}
	if s, _ := st.SessionByTokenHash(ctx, HashSecret(old.Token)); s.RevokedAt == nil {
		t.Fatal("the earlier session should have ended")
	}
	if len(m.changed) != 1 {
		t.Fatalf("password-changed emails: %v", m.changed)
	}
	if len(alerts.announced) != 0 {
		t.Fatalf("a person with a second step shouldn't alert: %v", alerts.announced)
	}
	_, err = a.CompleteReset(ctx, token, "a third passphrase here", ResetProof{Code: "123456"}, ip, "ua")
	wantCode(t, err, "reset_link_invalid")
}

// A password-only admin gets in on the link alone, and the other admins
// are told.
func TestCompleteResetPasswordOnlyAdminAlerts(t *testing.T) {
	a, st, alerts, m := newResetAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	ip := netip.MustParseAddr("203.0.113.43")
	u := resetUser(t, st, tenant, "aisha@example.com", RoleAdmin, "the first passphrase here")
	now := time.Now()
	u.PasswordOnlyAcceptedAt = &now
	st.users[u.ID] = u
	if err := a.RequestPasswordReset(ctx, tenant, u.Email, ip); err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteReset(ctx, m.links[u.Email], "the second passphrase here", ResetProof{}, ip, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "signed_in" {
		t.Fatalf("outcome: %+v", out)
	}
	if len(alerts.announced) != 1 || !strings.HasPrefix(alerts.announced[0], resetAdminPrefix) ||
		!strings.Contains(alerts.messages[0], "aisha@example.com") {
		t.Fatalf("alerts: %v %v", alerts.announced, alerts.messages)
	}
}

// A set-password link isn't a reset link either.
func TestSetupLinkIsNotAResetLink(t *testing.T) {
	a, _, _, _ := newResetAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	_, token, err := a.CreateUser(adminCtx, UserInput{Email: "new@example.com", Name: "New", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.CheckResetLink(ctx, token, netip.MustParseAddr("203.0.113.44"))
	wantCode(t, err, "reset_link_invalid")
}

// Hourly limiters keep their count for the whole hour, not just the usual
// ten idle minutes.
func TestLimitersPerKeepTheHour(t *testing.T) {
	l := NewLimitersPer(1, time.Hour)
	now := time.Now()
	if !l.Allow("k", now) {
		t.Fatal("first")
	}
	// Another key's use 20 minutes later sweeps idle buckets.
	l.Allow("other", now.Add(20*time.Minute))
	if l.Allow("k", now.Add(21*time.Minute)) {
		t.Fatal("a second within the hour was allowed")
	}
	if !l.Allow("k", now.Add(61*time.Minute)) {
		t.Fatal("not allowed again after the hour")
	}
}

// "Ask my admin to reset it": only from a working reset link, for an
// account with a second step; the admins hear once an hour; nothing about
// the account changes and the link still works.
func TestAskSecondStepReset(t *testing.T) {
	a, st, alerts, m := newResetAccounts(t)
	ctx := context.Background()
	tenant := uuid.New()
	adminCtx := WithPrincipal(ctx, adminPrincipal(tenant))
	ip := netip.MustParseAddr("203.0.113.45")
	u, _ := enrolledUser(t, a, adminCtx, "sara@example.com", RoleUser, "the first passphrase here")
	wantCode(t, a.AskSecondStepReset(ctx, "not-a-link", ip), "reset_link_invalid")
	if err := a.RequestPasswordReset(ctx, tenant, u.Email, ip); err != nil {
		t.Fatal(err)
	}
	token := m.links[u.Email]
	before := st.users[u.ID]
	for i := 0; i < 3; i++ {
		if err := a.AskSecondStepReset(ctx, token, ip); err != nil {
			t.Fatal(err)
		}
	}
	if len(alerts.announced) != 1 || !strings.HasPrefix(alerts.announced[0], askAdminPrefix) || !strings.Contains(alerts.messages[0], u.Email) {
		t.Fatalf("alerts: %v %v", alerts.announced, alerts.messages)
	}
	after := st.users[u.ID]
	if !after.MFAEnabled || after.PasswordHash != before.PasswordHash {
		t.Fatal("asking changed the account")
	}
	if _, err := a.CheckResetLink(ctx, token, ip); err != nil {
		t.Fatalf("the link should still work: %v", err)
	}

	plain := resetUser(t, st, tenant, "yusuf@example.com", RoleUser, "the first passphrase here")
	if err := a.RequestPasswordReset(ctx, tenant, plain.Email, ip); err != nil {
		t.Fatal(err)
	}
	wantCode(t, a.AskSecondStepReset(ctx, m.links[plain.Email], ip), "no_second_step")
}
