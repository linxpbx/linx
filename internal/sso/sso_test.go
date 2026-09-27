package sso

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
)

type fakeStore struct {
	providers map[uuid.UUID]Provider
	audits    []auth.AuditEntry
}

func newFakeStore() *fakeStore { return &fakeStore{providers: map[uuid.UUID]Provider{}} }

func (f *fakeStore) CreateSSOProvider(_ context.Context, p Provider, a auth.AuditEntry) error {
	for _, e := range f.providers {
		if e.TenantID == p.TenantID && e.Name == p.Name {
			return auth.ErrDuplicate
		}
	}
	f.providers[p.ID] = p
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeStore) SSOProvider(_ context.Context, tenant, id uuid.UUID) (Provider, error) {
	p, ok := f.providers[id]
	if !ok || p.TenantID != tenant {
		return Provider{}, auth.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) ListSSOProviders(_ context.Context, tenant uuid.UUID) ([]Provider, error) {
	var out []Provider
	for _, p := range f.providers {
		if p.TenantID == tenant {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateSSOProvider(_ context.Context, p Provider, a auth.AuditEntry) (Provider, error) {
	cur, ok := f.providers[p.ID]
	if !ok {
		return Provider{}, auth.ErrNotFound
	}
	if cur.Version != p.Version {
		return Provider{}, auth.ErrVersionChanged
	}
	p.Version++
	f.providers[p.ID] = p
	f.audits = append(f.audits, a)
	return p, nil
}

func (f *fakeStore) DeleteSSOProvider(_ context.Context, tenant, id uuid.UUID, a auth.AuditEntry) error {
	if _, ok := f.providers[id]; !ok {
		return auth.ErrNotFound
	}
	delete(f.providers, id)
	f.audits = append(f.audits, a)
	return nil
}

func testSealer() *dbsecret.Sealer {
	var key [dbsecret.KeySize]byte
	copy(key[:], "0123456789abcdef0123456789abcdef")
	return dbsecret.NewSealer(key)
}

// publicResolver answers every name with one public address, so the SSRF
// policy lets it through; the test's HTTP client dials the stand-in
// provider instead.
type publicResolver struct{}

func (publicResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}

// standIn serves a discovery document for https://example.com (the name
// httptest's certificate is for).
func standIn(t *testing.T) *http.Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer": "https://example.com", "authorization_endpoint": "https://example.com/auth",
			"token_endpoint": "https://example.com/token", "jwks_uri": "https://example.com/keys",
		})
	}))
	t.Cleanup(srv.Close)
	c := srv.Client()
	tr := c.Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
	}
	c.Transport = tr
	return c
}

func newTestService(t *testing.T) (*Service, *fakeStore, context.Context) {
	t.Helper()
	st := newFakeStore()
	client := &Client{Store: st, Sealer: testSealer(), HTTP: standIn(t), RedirectURI: RedirectURI("linx.test"), Now: time.Now}
	svc := &Service{Store: st, Sealer: testSealer(), Client: client, Resolver: publicResolver{}, Now: time.Now}
	tenant := uuid.New()
	now := time.Now()
	sess := auth.UserSession{ID: uuid.New(), TenantID: tenant, UserID: uuid.New(), Role: auth.RoleSystemAdmin, MFAVerified: true, ConfirmedAt: &now}
	ctx := auth.WithSession(auth.WithPrincipal(context.Background(), sess.Principal()), sess)
	return svc, st, ctx
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *apihttp.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func TestRedirectURI(t *testing.T) {
	if got := RedirectURI("example.org"); got != "https://meet.example.org/api/v1/sso/callback" {
		t.Fatal(got)
	}
}

func TestIdentityClaims(t *testing.T) {
	cases := []struct {
		name, kind string
		claims     Claims
		email      string
		verified   bool
	}{
		{"google verified", KindGoogle, Claims{Email: "a@example.com", EmailVerified: true}, "a@example.com", true},
		{"google unverified", KindGoogle, Claims{Email: "a@example.com", EmailVerified: false}, "a@example.com", false},
		{"keycloak string true", KindKeycloak, Claims{Email: "a@example.com", EmailVerified: "true"}, "a@example.com", true},
		{"missing claim", KindOIDC, Claims{Email: "a@example.com"}, "a@example.com", false},
		// Microsoft never sends email_verified; its email counts only with
		// xms_edov, and an email_verified claim there means nothing.
		{"microsoft email_verified ignored", KindMicrosoft, Claims{Email: "a@example.com", EmailVerified: true}, "a@example.com", false},
		{"microsoft xms_edov", KindMicrosoft, Claims{Email: "a@example.com", XMSEdov: true}, "a@example.com", true},
		{"microsoft upn", KindMicrosoft, Claims{Email: "personal@gmail.com", UPN: "a@contoso.com"}, "a@contoso.com", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id := Identity(c.kind, "sub", c.claims)
			if id.Subject != "sub" || id.Email != c.email || id.EmailVerified != c.verified {
				t.Fatalf("Identity = %+v, want %s verified=%v", id, c.email, c.verified)
			}
		})
	}
}

func TestCreateProvider(t *testing.T) {
	svc, st, ctx := newTestService(t)

	// Google's issuer is filled in: the test can't reach the real one, so
	// it's refused as unreachable (not as a missing issuer).
	_, err := svc.Create(ctx, ProviderInput{Kind: KindGoogle, ClientID: "id-1", ClientSecret: "s3cret"})
	wantCode(t, err, "issuer_unreachable")

	// Any provider at the stand-in.
	p, err := svc.Create(ctx, ProviderInput{Kind: KindOIDC, Name: "Company", Issuer: "https://example.com", ClientID: "linx", ClientSecret: "s3cret"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !p.Enabled || !p.Shown || len(p.ClientSecretEnc) == 0 || strings.Contains(string(p.ClientSecretEnc), "s3cret") {
		t.Fatalf("provider = %+v", p)
	}
	if got, err := testSealer().Open(SealID(p.ID), p.ClientSecretEnc); err != nil || string(got) != "s3cret" {
		t.Fatalf("sealed secret = %q %v", got, err)
	}
	for _, a := range st.audits {
		if strings.Contains(a.Target+strings.Join(detailStrings(a.Detail), ""), "s3cret") {
			t.Fatal("secret in the audit log")
		}
	}

	_, err = svc.Create(ctx, ProviderInput{Kind: KindOIDC, Name: "Company", Issuer: "https://example.com", ClientID: "x"})
	wantCode(t, err, "name_taken")
	_, err = svc.Create(ctx, ProviderInput{Kind: KindOIDC, Issuer: "https://example.com", ClientID: "x"})
	wantCode(t, err, "name_invalid")
	_, err = svc.Create(ctx, ProviderInput{Kind: KindOIDC, Name: "N", Issuer: "http://example.com", ClientID: "x"})
	wantCode(t, err, "issuer_invalid")
	_, err = svc.Create(ctx, ProviderInput{Kind: KindMicrosoft, Issuer: "https://login.microsoftonline.com/common/v2.0", ClientID: "x"})
	wantCode(t, err, "issuer_invalid")
	_, err = svc.Create(ctx, ProviderInput{Kind: KindGoogle, Issuer: "https://example.com", ClientID: "x"})
	wantCode(t, err, "issuer_invalid")
	_, err = svc.Create(ctx, ProviderInput{Kind: "saml", Name: "N", Issuer: "https://example.com", ClientID: "x"})
	wantCode(t, err, "kind_invalid")
	_, err = svc.Create(ctx, ProviderInput{Kind: KindOIDC, Name: "N2", Issuer: "https://example.com/other", ClientID: "x"})
	wantCode(t, err, "issuer_unreachable")

	// A provider on a private address needs the outbound allowlist.
	svc.Resolver = nil
	_, err = svc.Create(ctx, ProviderInput{Kind: KindAuthentik, Issuer: "https://10.0.0.5/application/o/linx/", ClientID: "x"})
	wantCode(t, err, "issuer_blocked")
	_, err = svc.Create(ctx, ProviderInput{Kind: KindKeycloak, Issuer: "https://127.0.0.1/realms/x", ClientID: "x"})
	wantCode(t, err, "issuer_blocked")
	svc.Policy = safehttp.Policy{Allowlist: func(context.Context) (safehttp.Allowlist, error) {
		return safehttp.Allowlist{Prefixes: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/24")}}, nil
	}}
	_, err = svc.Create(ctx, ProviderInput{Kind: KindAuthentik, Issuer: "https://10.0.0.5/application/o/linx/", ClientID: "x"})
	wantCode(t, err, "issuer_unreachable") // allowed now; the stand-in only serves example.com
}

func detailStrings(d map[string]any) []string {
	var out []string
	for k, v := range d {
		b, _ := json.Marshal(v)
		out = append(out, k+string(b))
	}
	return out
}

func TestProviderChangesNeedConfirmIt(t *testing.T) {
	svc, _, ctx := newTestService(t)
	p, err := svc.Create(ctx, ProviderInput{Kind: KindOIDC, Name: "Company", Issuer: "https://example.com", ClientID: "linx"})
	if err != nil {
		t.Fatal(err)
	}
	sess, _ := auth.SessionFromContext(ctx)
	old := time.Now().Add(-time.Hour)
	sess.ConfirmedAt = &old
	stale := auth.WithSession(auth.WithPrincipal(context.Background(), sess.Principal()), sess)
	_, err = svc.Create(stale, ProviderInput{Kind: KindOIDC, Name: "Other", Issuer: "https://example.com", ClientID: "x"})
	wantCode(t, err, "confirm_required")
	name := "Renamed"
	_, err = svc.Update(stale, p.ID, ProviderPatch{Name: &name}, "")
	wantCode(t, err, "confirm_required")
	wantCode(t, svc.Delete(stale, p.ID), "confirm_required")

	// If-Match, and removing the secret with an empty one.
	_, err = svc.Update(ctx, p.ID, ProviderPatch{Name: &name}, `"99"`)
	wantCode(t, err, "etag_mismatch")
	empty := ""
	up, err := svc.Update(ctx, p.ID, ProviderPatch{Name: &name, ClientSecret: &empty}, auth.ETag(p.Version))
	if err != nil || up.Name != "Renamed" || up.ClientSecretEnc != nil || up.Version != p.Version+1 {
		t.Fatalf("Update = %+v %v", up, err)
	}
	if err := svc.Delete(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	wantCode(t, svc.Delete(ctx, p.ID), "not_found")
}

func TestButtonsAndClientRefuseDisabledProviders(t *testing.T) {
	svc, st, ctx := newTestService(t)
	p, err := svc.Create(ctx, ProviderInput{Kind: KindOIDC, Name: "Company", Issuer: "https://example.com", ClientID: "linx"})
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := svc.Create(ctx, ProviderInput{Kind: KindOIDC, Name: "Hidden", Issuer: "https://example.com", ClientID: "linx",
		Shown: new(bool)})
	if err != nil {
		t.Fatal(err)
	}
	buttons, _ := svc.Buttons(ctx, p.TenantID)
	if len(buttons) != 1 || buttons[0].Name != "Company" {
		t.Fatalf("buttons = %+v", buttons)
	}
	linkable, _ := svc.Linkable(ctx)
	if len(linkable) != 2 {
		t.Fatalf("linkable = %+v", linkable)
	}
	// Start builds a code+PKCE+nonce request against the discovered endpoint.
	url, prov, err := svc.Client.Start(ctx, p.TenantID, hidden.ID, "st", "nn", strings.Repeat("v", 43))
	if err != nil || prov.Name != "Hidden" {
		t.Fatalf("Start = %v %v", prov, err)
	}
	for _, want := range []string{"https://example.com/auth?", "state=st", "nonce=nn", "code_challenge_method=S256", "scope=openid+email+profile",
		"redirect_uri=https%3A%2F%2Fmeet.linx.test%2Fapi%2Fv1%2Fsso%2Fcallback"} {
		if !strings.Contains(url, want) {
			t.Errorf("auth URL %s lacks %s", url, want)
		}
	}
	off := st.providers[hidden.ID]
	off.Enabled = false
	st.providers[hidden.ID] = off
	if _, _, err := svc.Client.Start(ctx, p.TenantID, hidden.ID, "st", "nn", "v"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("disabled provider Start = %v, want ErrNotFound", err)
	}
}
