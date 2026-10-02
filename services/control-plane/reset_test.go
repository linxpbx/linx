package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"testing"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// "Forgot your password?" end to end through the handlers (ADR-067): offered
// only once email is on; the same 202 for any email; the emailed link
// opens the reset, which signs the person in and emails that the password
// changed; the link then stops working.
func TestPasswordResetHandlers(t *testing.T) {
	env := newTestEnv(t)
	options := func() controlplaneapi.SignInOptions {
		t.Helper()
		resp, err := http.Get(env.srv.URL + "/api/v1/sign-in-options")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var o controlplaneapi.SignInOptions
		if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	request := func(addr string) int {
		t.Helper()
		resp := postJSON(t, env, "/api/v1/password-reset", map[string]string{"email": addr}, nil, "")
		resp.Body.Close()
		return resp.StatusCode
	}
	if options().PasswordReset {
		t.Fatal("offered with email off")
	}
	if got := request("sara@example.com"); got != http.StatusConflict {
		t.Fatalf("email off: %d", got)
	}

	c := email.Defaults(env.store.tenant)
	c.Enabled, c.Host, c.FromAddress, c.PasswordEnc = true, "smtp.example.com", "pbx@example.com", []byte{1}
	env.email.Put(c)
	if !options().PasswordReset {
		t.Fatal("not offered with email on")
	}
	_, setup := createTestUser(t, env, "sara@example.com", auth.RoleUser)
	resp := postJSON(t, env, "/api/v1/setup-links/"+setup, map[string]any{"password": "a fine long passphrase 1", "password_only": true}, nil, "")
	resp.Body.Close()

	for _, addr := range []string{"nobody@example.com", "sara@example.com"} {
		if got := request(addr); got != http.StatusAccepted {
			t.Fatalf("%s: %d", addr, got)
		}
	}
	if len(env.email.Queued) != 1 || env.email.Queued[0].Kind != email.KindReset || env.email.Queued[0].To[0] != "sara@example.com" {
		t.Fatalf("queued: %+v", env.email.Queued)
	}
	plain, err := env.accounts.Sealer.Open("email_outbox:"+env.email.Queued[0].ID.String(), env.email.Queued[0].ContentEnc)
	if err != nil {
		t.Fatal(err)
	}
	var content email.Content
	if err := json.Unmarshal(plain, &content); err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`https://pbx\.example\.com/reset/([A-Za-z0-9_-]+)`).FindStringSubmatch(content.Text)
	if m == nil {
		t.Fatalf("no link in %q", content.Text)
	}
	token := m[1]

	check := func() int {
		t.Helper()
		resp, err := http.Get(env.srv.URL + "/api/v1/reset-links/" + token)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if got := check(); got != http.StatusOK {
		t.Fatalf("fresh link: %d", got)
	}
	resp = postJSON(t, env, "/api/v1/reset-links/"+token, map[string]string{"password": "a new long passphrase 2"}, nil, "")
	defer resp.Body.Close()
	var status sessionStatusBody
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || status.Status != "signed_in" {
		t.Fatalf("reset: %d %+v", resp.StatusCode, status)
	}
	if cookies, csrf := cookiesAndCSRF(resp); len(cookies) == 0 || csrf == "" {
		t.Fatal("no session cookies")
	}
	if len(env.email.Queued) != 2 || env.email.Queued[1].Kind != email.KindPasswordChanged {
		t.Fatalf("queued after: %+v", env.email.Queued)
	}
	if got := check(); got != http.StatusBadRequest {
		t.Fatalf("used link: %d", got)
	}
}
