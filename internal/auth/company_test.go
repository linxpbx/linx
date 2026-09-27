package auth

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeProvider is an OpenID Connect provider: each code signs in as the
// identity registered for it, once.
type fakeProvider struct {
	id      uuid.UUID
	codes   map[string]CompanyIdentity
	nonce   map[string]string // state → nonce seen at Start
	lastURL string
	fail    error
}

func (p *fakeProvider) Start(_ context.Context, _, provider uuid.UUID, state, nonce, verifier string) (string, CompanyProvider, error) {
	if provider != p.id {
		return "", CompanyProvider{}, ErrNotFound
	}
	if verifier == "" || nonce == "" {
		return "", CompanyProvider{}, errors.New("no PKCE verifier or nonce")
	}
	p.nonce[state] = nonce
	p.lastURL = "https://idp.example/auth?state=" + url.QueryEscape(state)
	return p.lastURL, CompanyProvider{ID: p.id, Name: "Google"}, nil
}

func (p *fakeProvider) Finish(_ context.Context, _, provider uuid.UUID, code, verifier, nonce string) (CompanyProvider, CompanyIdentity, error) {
	if p.fail != nil {
		return CompanyProvider{}, CompanyIdentity{}, p.fail
	}
	id, ok := p.codes[code]
	if !ok || verifier == "" || nonce == "" {
		return CompanyProvider{}, CompanyIdentity{}, errors.New("bad code")
	}
	delete(p.codes, code)
	return CompanyProvider{ID: p.id, Name: "Google"}, id, nil
}

type fakeCompanyStore struct {
	accounts *fakeAccountStore
	links    []CompanyLinkInfo
	required bool
}

func (f *fakeCompanyStore) CompanyLinkBySubject(_ context.Context, tenant, provider uuid.UUID, subject string) (CompanyLinkInfo, error) {
	for _, l := range f.links {
		if l.TenantID == tenant && l.ProviderID == provider && l.Subject == subject {
			return l, nil
		}
	}
	return CompanyLinkInfo{}, ErrNotFound
}

func (f *fakeCompanyStore) CompanyLinks(_ context.Context, tenant, user uuid.UUID) ([]CompanyLinkInfo, error) {
	var out []CompanyLinkInfo
	for _, l := range f.links {
		if l.TenantID == tenant && l.UserID == user {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeCompanyStore) AddCompanyLink(_ context.Context, l CompanyLinkInfo, _ AuditEntry) error {
	for _, e := range f.links {
		if (e.ProviderID == l.ProviderID && e.Subject == l.Subject) || (e.ProviderID == l.ProviderID && e.UserID == l.UserID) {
			return ErrDuplicate
		}
	}
	l.ProviderName = "Google"
	f.links = append(f.links, l)
	u := f.accounts.users[l.UserID]
	u.CompanyLogins = append(u.CompanyLogins, "Google")
	f.accounts.users[l.UserID] = u
	return nil
}

func (f *fakeCompanyStore) UseCompanyLink(context.Context, uuid.UUID, time.Time) error { return nil }

func (f *fakeCompanyStore) RemoveCompanyLink(_ context.Context, _, user, id uuid.UUID, _ AuditEntry) error {
	for i, l := range f.links {
		if l.ID == id && l.UserID == user {
			f.links = slices.Delete(f.links, i, i+1)
			u := f.accounts.users[user]
			u.CompanyLogins = u.CompanyLogins[1:]
			f.accounts.users[user] = u
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeCompanyStore) CompanySignInRequired(context.Context, uuid.UUID) (bool, error) {
	return f.required, nil
}

var companyIP = netip.MustParseAddr("203.0.113.50")

func newCompanyAccounts(t *testing.T) (*Accounts, *fakeAccountStore, *fakeCompanyStore, *fakeProvider) {
	t.Helper()
	a, st, _ := newTestAccounts(t)
	prov := &fakeProvider{id: uuid.New(), codes: map[string]CompanyIdentity{}, nonce: map[string]string{}}
	cs := &fakeCompanyStore{accounts: st}
	a.CompanyProviders, a.Company = prov, cs
	return a, st, cs, prov
}

// companyRound runs one flow: start (in ctx), then the provider answers
// with code for identity.
func companyRound(t *testing.T, a *Accounts, prov *fakeProvider, ctx context.Context, tenant uuid.UUID, purpose string, id CompanyIdentity) (string, CompanyResult, error) {
	t.Helper()
	st, err := a.BeginCompany(ctx, tenant, prov.id, purpose, companyIP)
	if err != nil {
		t.Fatalf("BeginCompany: %v", err)
	}
	u, _ := url.Parse(st.URL)
	code := uuid.NewString()
	prov.codes[code] = id
	return a.FinishCompany(context.Background(), u.Query().Get("state"), st.Cookie, code, "", companyIP, "ua")
}

func TestCompanySignInLinksByVerifiedEmailOnce(t *testing.T) {
	a, st, cs, prov := newCompanyAccounts(t)
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	u, _, err := a.CreateUser(adminCtx, UserInput{Email: "sara@example.com", Name: "Sara", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}

	// Nobody new gets in, and an unverified email links nobody.
	_, _, err = companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-1", Email: "stranger@example.com", EmailVerified: true})
	wantCode(t, err, "no_account")
	_, _, err = companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-2", Email: "sara@example.com", EmailVerified: false})
	wantCode(t, err, "email_unverified")
	if len(cs.links) != 0 {
		t.Fatalf("links = %v, want none", cs.links)
	}

	// First use: linked by the verified email, whatever its case.
	purpose, res, err := companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-sara", Email: "Sara@Example.com", EmailVerified: true})
	if err != nil || purpose != CompanySignIn || res.Status != "signed_in" || res.Session == nil {
		t.Fatalf("first sign-in = %q %+v %v", purpose, res, err)
	}
	if len(cs.links) != 1 || cs.links[0].UserID != u.ID || cs.links[0].Subject != "g-sara" {
		t.Fatalf("links = %+v", cs.links)
	}
	if !st.sessions[res.Session.Session.ID].MFAVerified {
		t.Fatal("a person with no second step should be fully signed in")
	}

	// After that only the subject counts: a changed email still signs in,
	// and another provider account with Sara's email doesn't.
	_, res, err = companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-sara", Email: "sara.new@example.com", EmailVerified: false})
	if err != nil || res.Status != "signed_in" {
		t.Fatalf("by subject = %+v %v", res, err)
	}
	_, _, err = companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-impostor", Email: "sara@example.com", EmailVerified: true})
	wantCode(t, err, "not_linked")

	// Sign-ins are audited with the provider, never the token.
	var methods []any
	for _, e := range st.audits {
		if e.Action == "user.sign_in" {
			methods = append(methods, e.Detail["method"])
		}
	}
	if !slices.Contains(methods, any("company:Google")) {
		t.Fatalf("sign-in audit methods = %v", methods)
	}

	// A disabled person can't.
	if _, err := a.DisableUser(adminCtx, u.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true})
	wantCode(t, err, "account_disabled")
}

func TestCompanySignInKeepsTheSecondStep(t *testing.T) {
	a, _, _, prov := newCompanyAccounts(t)
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	enrolledUser(t, a, adminCtx, "omar@example.com", RoleAdmin, "omar's own long passphrase")
	_, res, err := companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-omar", Email: "omar@example.com", EmailVerified: true})
	if err != nil || res.Status != "mfa_verify_required" || res.Session.Session.MFAVerified {
		t.Fatalf("admin with an authenticator = %+v %v, want mfa_verify_required", res, err)
	}

	// An admin with no second step must set one up (or accept password
	// only), exactly as after a password.
	if _, _, err := a.CreateUser(adminCtx, UserInput{Email: "noor@example.com", Name: "Noor", Role: RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	_, res, err = companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-noor", Email: "noor@example.com", EmailVerified: true})
	if err != nil || res.Status != "mfa_setup_required" {
		t.Fatalf("admin without a second step = %+v %v", res, err)
	}
}

func TestCompanyFlowIsBoundToItsBrowserAndUsedOnce(t *testing.T) {
	a, _, _, prov := newCompanyAccounts(t)
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	if _, _, err := a.CreateUser(adminCtx, UserInput{Email: "sara@example.com", Name: "Sara", Role: RoleUser}); err != nil {
		t.Fatal(err)
	}
	st, err := a.BeginCompany(context.Background(), tenant, prov.id, CompanySignIn, companyIP)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	state := u.Query().Get("state")
	prov.codes["c1"] = CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true}

	// Another browser (no cookie, or someone else's) can't finish it; the
	// attempt uses the flow up.
	if _, _, err := a.FinishCompany(context.Background(), state, NewSecret(), "c1", "", companyIP, "ua"); err == nil {
		t.Fatal("finished with the wrong cookie")
	}
	if _, _, err := a.FinishCompany(context.Background(), state, st.Cookie, "c1", "", companyIP, "ua"); err == nil {
		t.Fatal("a flow was used twice")
	}

	// Cancelling at the provider.
	st, _ = a.BeginCompany(context.Background(), tenant, prov.id, CompanySignIn, companyIP)
	u, _ = url.Parse(st.URL)
	_, _, err = a.FinishCompany(context.Background(), u.Query().Get("state"), st.Cookie, "", "access_denied", companyIP, "ua")
	wantCode(t, err, "company_cancelled")

	// A provider that fails the exchange or the ID token check.
	prov.fail = errors.New("id token signature invalid")
	st, _ = a.BeginCompany(context.Background(), tenant, prov.id, CompanySignIn, companyIP)
	u, _ = url.Parse(st.URL)
	_, _, err = a.FinishCompany(context.Background(), u.Query().Get("state"), st.Cookie, "c1", "", companyIP, "ua")
	wantCode(t, err, "company_failed")

	// Unknown provider.
	if _, err := a.BeginCompany(context.Background(), tenant, uuid.New(), CompanySignIn, companyIP); err == nil {
		t.Fatal("started with an unknown provider")
	}
}

func TestCompanyRequiredTurnsPasswordsOff(t *testing.T) {
	a, _, cs, _ := newCompanyAccounts(t)
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	const pw = "a long enough passphrase"
	for _, p := range []struct{ email, role string }{{"user@example.com", RoleUser}, {"owner@example.com", RoleSystemAdmin}} {
		_, link, err := a.CreateUser(adminCtx, UserInput{Email: p.email, Name: "P", Role: p.role})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.CompleteSetup(context.Background(), link, pw, p.role == RoleSystemAdmin, companyIP, "ua"); err != nil {
			t.Fatal(err)
		}
	}
	cs.required = true
	// Only once the password is right does the answer say why.
	_, err := a.SignIn(context.Background(), tenant, "user@example.com", "wrong password entirely", companyIP, "ua")
	wantCode(t, err, "sign_in_invalid")
	_, err = a.SignIn(context.Background(), tenant, "user@example.com", pw, companyIP, "ua")
	wantCode(t, err, "company_sign_in_required")
	// System admins always can: a broken provider mustn't lock the owner out.
	if out, err := a.SignIn(context.Background(), tenant, "owner@example.com", pw, companyIP, "ua"); err != nil || out.Status != "signed_in" {
		t.Fatalf("system admin = %+v %v", out, err)
	}
}

func TestCompanyLinkAndConfirm(t *testing.T) {
	a, st, cs, prov := newCompanyAccounts(t)
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	u, link, err := a.CreateUser(adminCtx, UserInput{Email: "sara@example.com", Name: "Sara", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.CompleteSetup(context.Background(), link, "sara's long passphrase", false, companyIP, "ua")
	if err != nil {
		t.Fatal(err)
	}
	ctx := sessCtx(st, out.Session.ID)

	// Linking needs the provider to vouch for Sara's own email.
	_, _, err = companyRound(t, a, prov, ctx, tenant, CompanyLink,
		CompanyIdentity{Subject: "g-other", Email: "other@example.com", EmailVerified: true})
	wantCode(t, err, "email_mismatch")
	purpose, res, err := companyRound(t, a, prov, ctx, tenant, CompanyLink,
		CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true})
	if err != nil || purpose != CompanyLink || res.Status != "linked" {
		t.Fatalf("link = %q %+v %v", purpose, res, err)
	}

	// A company account already linked to someone else can't be taken.
	other, _, err := a.CreateUser(adminCtx, UserInput{Email: "omar@example.com", Name: "Omar", Role: RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	cs.links = append(cs.links, CompanyLinkInfo{ID: uuid.New(), TenantID: tenant, UserID: other.ID, ProviderID: prov.id, Subject: "g-omar"})
	_, _, err = companyRound(t, a, prov, ctx, tenant, CompanyLink,
		CompanyIdentity{Subject: "g-omar", Email: "omar@example.com", EmailVerified: true})
	wantCode(t, err, "linked_elsewhere")

	// "Confirm it's you" with the linked account (no second step: done).
	st.sessions[out.Session.ID] = func(s UserSession) UserSession { s.ConfirmedAt = nil; return s }(st.sessions[out.Session.ID])
	_, res, err = companyRound(t, a, prov, ctx, tenant, CompanyConfirm,
		CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true})
	if err != nil || res.Status != "confirmed" || st.sessions[out.Session.ID].ConfirmedAt == nil {
		t.Fatalf("confirm = %+v %v", res, err)
	}
	// Someone else's company account doesn't confirm Sara.
	_, _, err = companyRound(t, a, prov, ctx, tenant, CompanyConfirm,
		CompanyIdentity{Subject: "g-omar", Email: "omar@example.com", EmailVerified: true})
	wantCode(t, err, "not_linked")

	// Unlinking: refused while it's the only way in (no password, no
	// passkey), allowed otherwise.
	links, _ := a.MyCompanyLinks(sessCtx(st, out.Session.ID))
	if len(links) != 1 {
		t.Fatalf("my links = %+v", links)
	}
	withoutPassword := st.users[u.ID]
	withoutPassword.PasswordHash = ""
	st.users[u.ID] = withoutPassword
	wantCode(t, a.UnlinkMyCompany(sessCtx(st, out.Session.ID), links[0].ID), "last_sign_in_method")
	withoutPassword.PasswordHash = st.users[other.ID].PasswordHash + "x"
	st.users[u.ID] = withoutPassword
	if err := a.UnlinkMyCompany(sessCtx(st, out.Session.ID), links[0].ID); err != nil {
		t.Fatalf("unlink: %v", err)
	}
}

func TestCompanyConfirmStillAsksForTheCode(t *testing.T) {
	a, st, _, prov := newCompanyAccounts(t)
	clock := time.Now()
	a.Now = func() time.Time { return clock }
	// code is the authenticator's code for the current step; each step's
	// code works once, so the clock moves on after every use.
	code := func(secret []byte) string {
		clock = clock.Add(30 * time.Second)
		return totpCode(secret, uint64(clock.Unix())/30)
	}
	tenant := uuid.New()
	adminCtx := WithPrincipal(context.Background(), adminPrincipal(tenant))
	_, secret := enrolledUser(t, a, adminCtx, "omar@example.com", RoleAdmin, "omar's own long passphrase")
	_, res, err := companyRound(t, a, prov, context.Background(), tenant, CompanySignIn,
		CompanyIdentity{Subject: "g-omar", Email: "omar@example.com", EmailVerified: true})
	if err != nil {
		t.Fatal(err)
	}
	sess := res.Session.Session
	if err := a.VerifyMFA(sessCtx(st, sess.ID), code(secret)); err != nil {
		t.Fatal(err)
	}
	s := st.sessions[sess.ID]
	s.ConfirmedAt = nil
	st.sessions[sess.ID] = s
	ctx := sessCtx(st, sess.ID)

	// Without coming back from the provider, a code alone isn't enough.
	wantCode(t, a.Confirm(ctx, "", code(secret)), "password_invalid")

	_, res, err = companyRound(t, a, prov, ctx, tenant, CompanyConfirm,
		CompanyIdentity{Subject: "g-omar", Email: "omar@example.com", EmailVerified: true})
	if err != nil || res.Status != "code_required" || st.sessions[sess.ID].ConfirmedAt != nil {
		t.Fatalf("confirm = %+v %v, want code_required and not yet confirmed", res, err)
	}
	if err := a.Confirm(ctx, "", code(secret)); err != nil {
		t.Fatalf("code after company: %v", err)
	}
	if st.sessions[sess.ID].ConfirmedAt == nil {
		t.Fatal("not confirmed")
	}
	// The proof is used up.
	wantCode(t, a.Confirm(ctx, "", code(secret)), "password_invalid")
}
