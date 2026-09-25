package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// createTestUser registers a person the way an admin would (through
// auth.Accounts directly, since there's no session yet to call the API
// with) and returns their id and set-password link token.
func createTestUser(t *testing.T, env *testEnv, email, role string) (uuid.UUID, string) {
	t.Helper()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Type: auth.TypeSystem, ID: "cli", TenantID: env.store.tenant, Role: auth.RoleSystemAdmin, Scopes: auth.Scopes,
	})
	u, token, err := env.accounts.CreateUser(ctx, auth.UserInput{Email: email, Name: "Test Person", Role: role})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u.ID, token
}

func postJSON(t *testing.T, env *testEnv, path string, body any, cookies []*http.Cookie, csrf string) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, env.srv.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if csrf != "" {
		req.Header.Set(auth.CSRFHeaderName, csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func cookiesAndCSRF(resp *http.Response) ([]*http.Cookie, string) {
	cookies := resp.Cookies()
	var csrf string
	for _, c := range cookies {
		if c.Name == auth.CSRFCookieName {
			csrf = c.Value
		}
	}
	return cookies, csrf
}

func TestSessionSignInSetsCookies(t *testing.T) {
	env := newTestEnv(t)
	_, token := createTestUser(t, env, "person@example.com", auth.RoleUser)

	resp := postJSON(t, env, "/api/v1/setup-links/"+token, map[string]string{"password": "a fine long passphrase 1"}, nil, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("setup-link: status = %d, body = %s", resp.StatusCode, body)
	}
	cookies, csrf := cookiesAndCSRF(resp)
	if csrf == "" {
		t.Fatal("expected a CSRF cookie")
	}
	var haveSession bool
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			haveSession = true
			if !c.HttpOnly || !c.Secure {
				t.Errorf("session cookie: HttpOnly=%v Secure=%v, want both true", c.HttpOnly, c.Secure)
			}
		}
		if c.Name == auth.CSRFCookieName && c.HttpOnly {
			t.Error("CSRF cookie must not be HttpOnly: client script needs to read it")
		}
	}
	if !haveSession {
		t.Fatal("expected a session cookie")
	}

	// GET /me with just the session cookie.
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/me", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	meResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /me: status = %d", meResp.StatusCode)
	}
	var me struct {
		Type  string `json:"type"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(meResp.Body).Decode(&me); err != nil {
		t.Fatal(err)
	}
	if me.Type != "user" || me.Email != "person@example.com" {
		t.Fatalf("GET /me = %+v", me)
	}

	// Signing out without the CSRF header is refused.
	noCSRF := postJSON(t, env, "/api/v1/session", nil, nil, "")
	noCSRF.Body.Close()
	req, _ = http.NewRequest(http.MethodDelete, env.srv.URL+"/api/v1/session", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	out, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out.Body.Close()
	if out.StatusCode != http.StatusForbidden {
		t.Fatalf("DELETE /session without CSRF: status = %d, want 403", out.StatusCode)
	}

	// With the CSRF header, it works.
	req, _ = http.NewRequest(http.MethodDelete, env.srv.URL+"/api/v1/session", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req.Header.Set(auth.CSRFHeaderName, csrf)
	out, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out.Body.Close()
	if out.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /session with CSRF: status = %d, want 204", out.StatusCode)
	}

	// The session is now gone.
	req, _ = http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/me", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	again, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	again.Body.Close()
	if again.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /me after sign-out: status = %d, want 401", again.StatusCode)
	}
}

func TestSessionAdminNeedsMFA(t *testing.T) {
	env := newTestEnv(t)
	_, token := createTestUser(t, env, "admin@example.com", auth.RoleAdmin)

	resp := postJSON(t, env, "/api/v1/setup-links/"+token, map[string]string{"password": "another fine passphrase"}, nil, "")
	var status struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if status.Status != "mfa_setup_required" {
		t.Fatalf("status = %q, want mfa_setup_required", status.Status)
	}
	cookies, csrf := cookiesAndCSRF(resp)

	// A pending session can't reach a scoped endpoint.
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/extensions", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	blocked, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	blocked.Body.Close()
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("pending session on a scoped endpoint: status = %d, want 403", blocked.StatusCode)
	}

	// It can begin MFA enrollment.
	enroll := postJSON(t, env, "/api/v1/me/mfa", nil, cookies, csrf)
	defer enroll.Body.Close()
	if enroll.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(enroll.Body)
		t.Fatalf("POST /me/mfa: status = %d, body = %s", enroll.StatusCode, body)
	}
	var enrollment struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(enroll.Body).Decode(&enrollment); err != nil {
		t.Fatal(err)
	}
	if enrollment.Secret == "" {
		t.Fatal("expected a secret")
	}
}

func TestSessionUnknownCookieIsRejected(t *testing.T) {
	env := newTestEnv(t)
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "totally-made-up"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// TestSignInNeedsJSON: a cross-site form (or text/plain) post can't sign a
// visitor's browser in to someone else's account.
func TestSignInNeedsJSON(t *testing.T) {
	env := newTestEnv(t)
	createTestUser(t, env, "form@example.com", auth.RoleUser)
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", ""} {
		req, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/session",
			strings.NewReader(`{"email":"form@example.com","password":"whatever it is"}`))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType || len(resp.Cookies()) != 0 {
			t.Errorf("Content-Type %q: status %d, %d cookies", ct, resp.StatusCode, len(resp.Cookies()))
		}
	}
}
