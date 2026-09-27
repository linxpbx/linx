package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/sso"
)

// fakeCompany is company sign-in's provider (every code signs in as the
// identity registered for it) and its provider and link storage.
type fakeCompany struct {
	mu        sync.Mutex
	st        *fakeStore
	provider  sso.Provider
	codes     map[string]auth.CompanyIdentity
	links     []auth.CompanyLinkInfo
	required  bool
	providers map[uuid.UUID]sso.Provider
}

func newFakeCompany(st *fakeStore) *fakeCompany {
	p := sso.Provider{ID: uuid.New(), TenantID: st.tenant, Kind: sso.KindGoogle, Name: "Google", Issuer: sso.GoogleIssuer,
		ClientID: "linx", Enabled: true, Shown: true, Version: 1}
	return &fakeCompany{st: st, provider: p, codes: map[string]auth.CompanyIdentity{}, providers: map[uuid.UUID]sso.Provider{p.ID: p}}
}

func (f *fakeCompany) Start(_ context.Context, _, id uuid.UUID, state, _, _ string) (string, auth.CompanyProvider, error) {
	if id != f.provider.ID {
		return "", auth.CompanyProvider{}, auth.ErrNotFound
	}
	return "https://accounts.google.com/o/oauth2/auth?state=" + url.QueryEscape(state), auth.CompanyProvider{ID: id, Name: "Google"}, nil
}

func (f *fakeCompany) Finish(_ context.Context, _, id uuid.UUID, code, _, _ string) (auth.CompanyProvider, auth.CompanyIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ident, ok := f.codes[code]
	if !ok {
		return auth.CompanyProvider{}, auth.CompanyIdentity{}, errors.New("bad code")
	}
	delete(f.codes, code)
	return auth.CompanyProvider{ID: id, Name: "Google"}, ident, nil
}

func (f *fakeCompany) CompanyLinkBySubject(_ context.Context, tenant, provider uuid.UUID, subject string) (auth.CompanyLinkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links {
		if l.ProviderID == provider && l.Subject == subject {
			return l, nil
		}
	}
	return auth.CompanyLinkInfo{}, auth.ErrNotFound
}

func (f *fakeCompany) CompanyLinks(_ context.Context, _, user uuid.UUID) ([]auth.CompanyLinkInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []auth.CompanyLinkInfo{}
	for _, l := range f.links {
		if l.UserID == user {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeCompany) AddCompanyLink(_ context.Context, l auth.CompanyLinkInfo, _ auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.links {
		if e.ProviderID == l.ProviderID && (e.Subject == l.Subject || e.UserID == l.UserID) {
			return auth.ErrDuplicate
		}
	}
	l.ProviderName = "Google"
	f.links = append(f.links, l)
	return nil
}

func (f *fakeCompany) UseCompanyLink(context.Context, uuid.UUID, time.Time) error { return nil }

func (f *fakeCompany) RemoveCompanyLink(_ context.Context, _, user, id uuid.UUID, _ auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, l := range f.links {
		if l.ID == id && l.UserID == user {
			f.links = append(f.links[:i], f.links[i+1:]...)
			return nil
		}
	}
	return auth.ErrNotFound
}

func (f *fakeCompany) CompanySignInRequired(context.Context, uuid.UUID) (bool, error) {
	return f.required, nil
}

func (f *fakeCompany) CreateSSOProvider(context.Context, sso.Provider, auth.AuditEntry) error {
	return nil
}
func (f *fakeCompany) SSOProvider(_ context.Context, _, id uuid.UUID) (sso.Provider, error) {
	p, ok := f.providers[id]
	if !ok {
		return sso.Provider{}, auth.ErrNotFound
	}
	return p, nil
}
func (f *fakeCompany) ListSSOProviders(context.Context, uuid.UUID) ([]sso.Provider, error) {
	return []sso.Provider{f.provider}, nil
}
func (f *fakeCompany) UpdateSSOProvider(_ context.Context, p sso.Provider, _ auth.AuditEntry) (sso.Provider, error) {
	return p, nil
}
func (f *fakeCompany) DeleteSSOProvider(context.Context, uuid.UUID, uuid.UUID, auth.AuditEntry) error {
	return nil
}

// noRedirects is a browser that stops at each redirect, to look at it.
var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// companyStart posts to a start endpoint and returns the flow cookie and
// the state from the provider URL.
func companyStart(t *testing.T, env *testEnv, path string, cookies []*http.Cookie, csrf string) (*http.Cookie, string) {
	t.Helper()
	resp := postJSON(t, env, path, map[string]any{"provider_id": env.company.provider.ID}, cookies, csrf)
	defer resp.Body.Close()
	var body struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body.URL, "https://accounts.google.com/") {
		t.Fatalf("%s: %d %+v", path, resp.StatusCode, body)
	}
	var flow *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.CompanyCookieName {
			flow = c
		}
	}
	// Lax, unlike every other Linx cookie: the provider's redirect back is
	// a cross-site navigation.
	if flow == nil || !flow.HttpOnly || !flow.Secure || flow.SameSite != http.SameSiteLaxMode || flow.MaxAge != 600 {
		t.Fatalf("flow cookie = %+v", flow)
	}
	u, _ := url.Parse(body.URL)
	return flow, u.Query().Get("state")
}

// companyCallback is the provider sending the browser back.
func companyCallback(t *testing.T, env *testEnv, flow *http.Cookie, state, code string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+sso.CallbackPath+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), nil)
	if flow != nil {
		req.AddCookie(flow)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestCompanySignInEndpoints(t *testing.T) {
	env := newTestEnv(t)
	createTestUser(t, env, "sara@example.com", auth.RoleUser)

	// The sign-in page's options.
	resp, err := http.Get(env.srv.URL + "/api/v1/sign-in-options")
	if err != nil {
		t.Fatal(err)
	}
	var opts struct {
		Company []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
			Kind string    `json:"kind"`
		} `json:"company"`
		Required bool `json:"company_sign_in_required"`
		Passkeys bool `json:"passkeys_available"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&opts)
	resp.Body.Close()
	if len(opts.Company) != 1 || opts.Company[0].Name != "Google" || opts.Company[0].Kind != "google" || opts.Required || !opts.Passkeys {
		t.Fatalf("sign-in options = %+v", opts)
	}

	// Signing in: the callback sets the session cookies and goes home.
	flow, state := companyStart(t, env, "/api/v1/session/company", nil, "")
	env.company.codes["c1"] = auth.CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true}
	cb := companyCallback(t, env, flow, state, "c1")
	if cb.StatusCode != http.StatusSeeOther || cb.Header.Get("Location") != "/" {
		t.Fatalf("callback = %d %s", cb.StatusCode, cb.Header.Get("Location"))
	}
	cookies, csrf := cookiesAndCSRF(cb)
	if csrf == "" {
		t.Fatalf("no session cookies: %+v", cb.Cookies())
	}
	cleared := false
	for _, c := range cb.Cookies() {
		cleared = cleared || (c.Name == auth.CompanyCookieName && c.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("the flow cookie wasn't cleared")
	}
	me := getJSON(t, env, "/api/v1/me", cookies)
	if me["email"] != "sara@example.com" || me["pending"] != false {
		t.Fatalf("/me after company sign-in = %v", me)
	}

	// The same answer again, or without the flow cookie, goes back to the
	// sign-in page with a reason and no session.
	cb = companyCallback(t, env, flow, state, "c1")
	if loc := cb.Header.Get("Location"); loc != "/?company_error=company_expired" || len(cb.Cookies()) != 1 {
		t.Fatalf("replay = %s %+v", loc, cb.Cookies())
	}
	flow2, state2 := companyStart(t, env, "/api/v1/session/company", nil, "")
	_ = flow2
	env.company.codes["c2"] = auth.CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true}
	if loc := companyCallback(t, env, nil, state2, "c2").Header.Get("Location"); loc != "/?company_error=company_expired" {
		t.Fatalf("another browser = %s", loc)
	}
	// Nobody new gets in.
	flow, state = companyStart(t, env, "/api/v1/session/company", nil, "")
	env.company.codes["c3"] = auth.CompanyIdentity{Subject: "g-x", Email: "stranger@example.com", EmailVerified: true}
	if loc := companyCallback(t, env, flow, state, "c3").Header.Get("Location"); loc != "/?company_error=no_account" {
		t.Fatalf("stranger = %s", loc)
	}

	// My company accounts: listed with what can be linked, then unlinked
	// (Sara still has her password).
	mine := getJSON(t, env, "/api/v1/me/sso-links", cookies)
	items, _ := mine["items"].([]any)
	available, _ := mine["available"].([]any)
	if len(items) != 1 || len(available) != 1 {
		t.Fatalf("/me/sso-links = %v", mine)
	}
	linkID := items[0].(map[string]any)["id"].(string)
	req, _ := http.NewRequest(http.MethodDelete, env.srv.URL+"/api/v1/me/sso-links/"+linkID, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req.Header.Set(auth.CSRFHeaderName, csrf)
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("unlink = %d", del.StatusCode)
	}

	// Linking again from My account needs the session (and its CSRF
	// token), and comes back to /account.
	if resp := postJSON(t, env, "/api/v1/me/sso-links", map[string]any{"provider_id": env.company.provider.ID}, cookies, ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("link without CSRF = %d", resp.StatusCode)
	}
	flow, state = companyStart(t, env, "/api/v1/me/sso-links", cookies, csrf)
	env.company.codes["c4"] = auth.CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true}
	if loc := companyCallback(t, env, flow, state, "c4").Header.Get("Location"); loc != "/account?company=linked" {
		t.Fatalf("link = %s", loc)
	}

	// "Confirm it's you" comes back to the small window the dialog opened.
	flow, state = companyStart(t, env, "/api/v1/session/confirm/company", cookies, csrf)
	env.company.codes["c5"] = auth.CompanyIdentity{Subject: "g-sara", Email: "sara@example.com", EmailVerified: true}
	if loc := companyCallback(t, env, flow, state, "c5").Header.Get("Location"); loc != "/company-done?result=confirmed" {
		t.Fatalf("confirm = %s", loc)
	}

	// Passwords off: the right password says why; a wrong one doesn't.
	env.company.required = true
	if r := postJSON(t, env, "/api/v1/session", map[string]any{"email": "sara@example.com", "password": "not it at all"}, nil, ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password with company required = %d", r.StatusCode)
	}
}

func getJSON(t *testing.T, env *testEnv, path string, cookies []*http.Cookie) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %d %v", path, resp.StatusCode, out)
	}
	return out
}
