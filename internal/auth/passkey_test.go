package auth

import (
	"context"
	"encoding/json"
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/webauthntest"
)

const testOrigin = "https://meet.linx.test"

func newPasskeyAccounts(t *testing.T) (*Accounts, *fakeAccountStore, *fakeAlerts) {
	t.Helper()
	a, st, alerts := newTestAccounts(t)
	wa, err := NewWebAuthn("linx.test")
	if err != nil {
		t.Fatal(err)
	}
	a.WebAuthn, a.Passkeys = wa, st
	return a, st, alerts
}

// sessCtx is a request carrying sess as the session middleware would set
// it, read fresh from the store (promotions and confirmations land there).
func sessCtx(st *fakeAccountStore, id uuid.UUID) context.Context {
	s := st.sessions[id]
	ctx := WithSession(WithPrincipal(context.Background(), s.Principal()), s)
	return WithClientIP(ctx, netip.MustParseAddr("203.0.113.40"))
}

func optionsJSON(t *testing.T, c PasskeyCeremony) []byte {
	t.Helper()
	b, err := json.Marshal(c.Options)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var passkeyIP = netip.MustParseAddr("203.0.113.40")

// passkeyOnlyUser sets role's new account up through its setup link with a
// passkey on dev, as the "Passkey" choice does.
func passkeyOnlyUser(t *testing.T, a *Accounts, tenant uuid.UUID, email, role string, dev *webauthntest.Device) (User, SessionOutcome) {
	t.Helper()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	u, link, err := a.CreateUser(adminCtx, UserInput{Email: email, Name: "Passkey Person", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	cer, err := a.BeginSetupLinkPasskey(context.Background(), link, passkeyIP)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := dev.Create(optionsJSON(t, cer))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.FinishSetupLinkPasskey(context.Background(), link, cer.Token, "Test phone", cred, passkeyIP, "ua")
	if err != nil {
		t.Fatalf("FinishSetupLinkPasskey: %v", err)
	}
	return u, out
}

func signInWithPasskey(t *testing.T, a *Accounts, tenant uuid.UUID, dev *webauthntest.Device) (SessionOutcome, error) {
	t.Helper()
	cer, err := a.BeginPasskeySignIn(context.Background(), tenant, passkeyIP)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := dev.Get(optionsJSON(t, cer))
	if err != nil {
		t.Fatal(err)
	}
	return a.FinishPasskeySignIn(context.Background(), tenant, cer.Token, cred, passkeyIP, "ua")
}

// TestPasskeySetupLinkAndSignIn: the "Passkey" choice on a first-admin link
// signs in at once (a passkey is both steps, admins too), shows recovery
// codes, leaves no password, and the passkey then signs in with no email.
func TestPasskeySetupLinkAndSignIn(t *testing.T) {
	a, st, _ := newPasskeyAccounts(t)
	tenant := uuid.New()
	dev := webauthntest.New(testOrigin)
	u, out := passkeyOnlyUser(t, a, tenant, "owner@example.com", RoleSystemAdmin, dev)
	if out.Status != "signed_in" || !out.Session.MFAVerified || !out.Session.Confirmed(time.Now()) {
		t.Fatalf("setup outcome = %+v", out)
	}
	if len(out.RecoveryCodes) != 10 {
		t.Errorf("recovery codes = %d, want 10", len(out.RecoveryCodes))
	}
	got := st.users[u.ID]
	if got.HasPassword() || got.PasskeyCount != 1 || !got.HasSecondStep() {
		t.Fatalf("after setup: password %v, passkeys %d", got.HasPassword(), got.PasskeyCount)
	}

	in, err := signInWithPasskey(t, a, tenant, dev)
	if err != nil {
		t.Fatalf("passkey sign-in: %v", err)
	}
	if in.Status != "signed_in" || !in.Session.MFAVerified || in.Session.UserID != u.ID {
		t.Fatalf("sign-in outcome = %+v", in)
	}
	last := st.audits[len(st.audits)-1]
	if last.Action != "user.sign_in" || last.Detail["method"] != "passkey" || last.Result != ResultOK {
		t.Errorf("audit = %+v", last)
	}
	// No password to guess: any password is refused.
	_, err = a.SignIn(context.Background(), tenant, "owner@example.com", "", passkeyIP, "ua")
	wantCode(t, err, "sign_in_invalid")
}

func TestPasskeyChallengeIsSingleUseAndChecked(t *testing.T) {
	a, _, _ := newPasskeyAccounts(t)
	tenant := uuid.New()
	dev := webauthntest.New(testOrigin)
	passkeyOnlyUser(t, a, tenant, "one@example.com", RoleUser, dev)

	cer, err := a.BeginPasskeySignIn(context.Background(), tenant, passkeyIP)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := dev.Get(optionsJSON(t, cer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.FinishPasskeySignIn(context.Background(), tenant, cer.Token, cred, passkeyIP, "ua"); err != nil {
		t.Fatal(err)
	}
	_, err = a.FinishPasskeySignIn(context.Background(), tenant, cer.Token, cred, passkeyIP, "ua")
	wantCode(t, err, "passkey_expired")
	_, err = a.FinishPasskeySignIn(context.Background(), tenant, "", cred, passkeyIP, "ua")
	wantCode(t, err, "passkey_expired")

	// Another site's page can't use the answer: the origin is checked.
	dev.Origin = "https://meet.evil.test"
	_, err = signInWithPasskey(t, a, tenant, dev)
	wantCode(t, err, "passkey_refused")

	// A passkey this server never registered.
	stranger := webauthntest.New(testOrigin)
	a2, _, _ := newPasskeyAccounts(t)
	passkeyOnlyUser(t, a2, tenant, "elsewhere@example.com", RoleUser, stranger)
	_, err = signInWithPasskey(t, a, tenant, stranger)
	wantCode(t, err, "passkey_invalid")
}

// TestPasskeyCounterGoingBackIsRefused: a counter that doesn't go up means
// the key may have been copied: refused, and the admins are told.
func TestPasskeyCounterGoingBackIsRefused(t *testing.T) {
	a, _, alerts := newPasskeyAccounts(t)
	tenant := uuid.New()
	dev := webauthntest.New(testOrigin)
	dev.SignCount = 5
	passkeyOnlyUser(t, a, tenant, "copied@example.com", RoleUser, dev)
	if _, err := signInWithPasskey(t, a, tenant, dev); err != nil {
		t.Fatal(err)
	}
	dev.SignCount = 2
	_, err := signInWithPasskey(t, a, tenant, dev)
	wantCode(t, err, "passkey_refused")
	if len(alerts.fired) != 1 || alerts.fired[0][:len("passkey_cloned:")] != "passkey_cloned:" {
		t.Errorf("alerts fired = %v", alerts.fired)
	}
}

// TestPasskeyAsSecondStep: an admin with a password sets up a passkey as
// their second step from the pending session, then a password sign-in
// waits for the passkey (or a recovery code).
func TestPasskeyAsSecondStep(t *testing.T) {
	a, st, _ := newPasskeyAccounts(t)
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	u, link, err := a.CreateUser(adminCtx, UserInput{Email: "admin@example.com", Name: "Admin", Role: RoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(context.Background(), link, "a fine long passphrase 9", false, passkeyIP, "ua")
	if err != nil || out.Status != "mfa_setup_required" {
		t.Fatalf("CompleteSetup = %+v, %v", out, err)
	}
	ctx := sessCtx(st, out.Session.ID)
	dev := webauthntest.New(testOrigin)
	cer, err := a.BeginPasskeyRegistration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := dev.Create(optionsJSON(t, cer))
	if err != nil {
		t.Fatal(err)
	}
	p, codes, err := a.FinishPasskeyRegistration(ctx, cer.Token, "  Laptop  ", cred)
	if err != nil {
		t.Fatalf("FinishPasskeyRegistration: %v", err)
	}
	if p.Name != "Laptop" || len(codes) != 10 || !st.sessions[out.Session.ID].MFAVerified {
		t.Fatalf("passkey %+v, %d codes, session verified %v", p, len(codes), st.sessions[out.Session.ID].MFAVerified)
	}

	in, err := a.SignIn(context.Background(), tenant, u.Email, "a fine long passphrase 9", passkeyIP, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != "mfa_verify_required" || !slices.Equal(in.Methods, []string{"passkey", "recovery_code"}) {
		t.Fatalf("sign-in = %q %v", in.Status, in.Methods)
	}
	pending := sessCtx(st, in.Session.ID)
	// Knowing the password isn't enough to add another passkey.
	_, err = a.BeginPasskeyRegistration(pending)
	wantCode(t, err, "sign_in_unfinished")
	chk, err := a.BeginPasskeyCheck(pending)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := dev.Get(optionsJSON(t, chk))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.FinishPasskeyCheck(pending, chk.Token, answer); err != nil {
		t.Fatalf("FinishPasskeyCheck: %v", err)
	}
	if !st.sessions[in.Session.ID].MFAVerified {
		t.Fatal("the passkey should finish the sign-in")
	}

	// A recovery code stands in for a lost passkey after the password.
	in2, err := a.SignIn(context.Background(), tenant, u.Email, "a fine long passphrase 9", passkeyIP, "ua")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.VerifyMFA(sessCtx(st, in2.Session.ID), codes[0]); err != nil {
		t.Fatalf("recovery code: %v", err)
	}
}

// TestPasskeyRegistrationNeedsConfirm: in a whole session, adding a passkey
// is a new way in, so it needs a fresh "confirm it's you".
func TestPasskeyRegistrationNeedsConfirm(t *testing.T) {
	a, st, _ := newPasskeyAccounts(t)
	tenant := uuid.New()
	dev := webauthntest.New(testOrigin)
	_, out := passkeyOnlyUser(t, a, tenant, "later@example.com", RoleUser, dev)
	old := time.Now().Add(-time.Hour)
	s := st.sessions[out.Session.ID]
	s.ConfirmedAt = &old
	st.sessions[out.Session.ID] = s
	_, err := a.BeginPasskeyRegistration(sessCtx(st, out.Session.ID))
	wantCode(t, err, "confirm_required")

	// Confirming with the passkey itself.
	ctx := sessCtx(st, out.Session.ID)
	chk, err := a.BeginPasskeyCheck(ctx)
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := dev.Get(optionsJSON(t, chk))
	if err := a.FinishPasskeyCheck(ctx, chk.Token, answer); err != nil {
		t.Fatal(err)
	}
	if _, err := a.BeginPasskeyRegistration(sessCtx(st, out.Session.ID)); err != nil {
		t.Fatalf("after confirming: %v", err)
	}
}

// TestPasswordOnlyAdmin: an admin may choose a password only, on the link
// or while a sign-in waits for a second step's setup; never to skip one
// they already have.
func TestPasswordOnlyAdmin(t *testing.T) {
	a, st, _ := newPasskeyAccounts(t)
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))

	u, link, _ := a.CreateUser(adminCtx, UserInput{Email: "po@example.com", Name: "PO", Role: RoleAdmin})
	out, err := a.CompleteSetup(context.Background(), link, "a fine long passphrase 7", true, passkeyIP, "ua")
	if err != nil || out.Status != "signed_in" {
		t.Fatalf("password-only setup = %+v, %v", out, err)
	}
	if st.users[u.ID].PasswordOnlyAcceptedAt == nil {
		t.Fatal("the choice should be recorded")
	}
	if in, err := a.SignIn(context.Background(), tenant, u.Email, "a fine long passphrase 7", passkeyIP, "ua"); err != nil || in.Status != "signed_in" {
		t.Fatalf("password-only sign-in = %+v, %v", in, err)
	}

	v, link2, _ := a.CreateUser(adminCtx, UserInput{Email: "later@example.com", Name: "Later", Role: RoleAdmin})
	out2, _ := a.CompleteSetup(context.Background(), link2, "a fine long passphrase 8", false, passkeyIP, "ua")
	if out2.Status != "mfa_setup_required" {
		t.Fatalf("status = %q", out2.Status)
	}
	if err := a.AcceptPasswordOnly(sessCtx(st, out2.Session.ID)); err != nil {
		t.Fatal(err)
	}
	if !st.sessions[out2.Session.ID].MFAVerified || st.users[v.ID].PasswordOnlyAcceptedAt == nil {
		t.Fatal("accepting should finish the sign-in and be recorded")
	}

	w, _ := enrolledUser(t, a, adminCtx, "hasapp@example.com", RoleAdmin, "a fine long passphrase 6")
	link3, err := a.CreateSetupLink(adminCtx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.CompleteSetup(context.Background(), link3, "a fine long passphrase 5", true, passkeyIP, "ua")
	wantCode(t, err, "second_step_exists")
	_, err = a.BeginSetupLinkPasskey(context.Background(), link3, passkeyIP)
	wantCode(t, err, "second_step_exists")
}

func TestRemovePasskeyRules(t *testing.T) {
	a, st, _ := newPasskeyAccounts(t)
	tenant := uuid.New()
	dev := webauthntest.New(testOrigin)
	u, out := passkeyOnlyUser(t, a, tenant, "rm@example.com", RoleAdmin, dev)
	ctx := sessCtx(st, out.Session.ID)
	keys, _ := a.ListPasskeys(ctx)
	if len(keys) != 1 {
		t.Fatalf("keys = %d", len(keys))
	}
	// No password: the passkey is the only way in.
	wantCode(t, a.RemovePasskey(ctx, keys[0].ID, true), "last_sign_in_method")

	if err := a.ChangePassword(ctx, "", "a fine long passphrase 4"); err != nil {
		t.Fatalf("adding a password: %v", err)
	}
	if !st.users[u.ID].HasPassword() || st.sessions[out.Session.ID].RevokedAt != nil {
		t.Fatal("adding a password should set it and keep the session")
	}
	// An admin's last second step: the warning must be accepted.
	wantCode(t, a.RemovePasskey(ctx, keys[0].ID, false), "password_only_warning")
	if err := a.RemovePasskey(ctx, keys[0].ID, true); err != nil {
		t.Fatal(err)
	}
	got := st.users[u.ID]
	if got.PasskeyCount != 0 || got.PasswordOnlyAcceptedAt == nil || len(got.RecoveryCodeHashes) != 0 {
		t.Fatalf("after removing: %d passkeys, accepted %v, %d codes", got.PasskeyCount, got.PasswordOnlyAcceptedAt, len(got.RecoveryCodeHashes))
	}

	// Stale confirmation.
	old := time.Now().Add(-time.Hour)
	s := st.sessions[out.Session.ID]
	s.ConfirmedAt = &old
	st.sessions[out.Session.ID] = s
	wantCode(t, a.RemovePasskey(sessCtx(st, out.Session.ID), uuid.New(), true), "confirm_required")
}

func TestPasskeyLimitAndNames(t *testing.T) {
	a, st, _ := newPasskeyAccounts(t)
	tenant := uuid.New()
	_, out := passkeyOnlyUser(t, a, tenant, "many@example.com", RoleUser, webauthntest.New(testOrigin))
	for i := 1; i < MaxPasskeys; i++ {
		ctx := sessCtx(st, out.Session.ID)
		cer, err := a.BeginPasskeyRegistration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cred, _ := webauthntest.New(testOrigin).Create(optionsJSON(t, cer))
		if _, codes, err := a.FinishPasskeyRegistration(ctx, cer.Token, "Key", cred); err != nil || codes != nil {
			t.Fatalf("passkey %d: codes %v, %v", i+1, codes, err)
		}
	}
	_, err := a.BeginPasskeyRegistration(sessCtx(st, out.Session.ID))
	wantCode(t, err, "passkey_limit")

	for _, bad := range []string{"", "   ", "tab\there", string(make([]rune, 61))} {
		if _, err := PasskeyName(bad); err == nil {
			t.Errorf("name %q should be refused", bad)
		}
	}
}
