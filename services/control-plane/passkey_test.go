package main

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/webauthntest"
)

// passkeyStep posts to an .../options endpoint, has dev answer, and posts
// the answer back with the challenge cookie, as the web client does.
func passkeyStep(t *testing.T, env *testEnv, path string, cookies []*http.Cookie, csrf string, answer func([]byte) ([]byte, error), name string) *http.Response {
	t.Helper()
	resp := postJSON(t, env, path+"/options", map[string]any{}, cookies, csrf)
	options, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s/options: %d %s", path, resp.StatusCode, options)
	}
	var challenge *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.PasskeyCookieName {
			challenge = c
		}
	}
	if challenge == nil || !challenge.HttpOnly || !challenge.Secure || challenge.SameSite != http.SameSiteStrictMode || challenge.MaxAge != 300 {
		t.Fatalf("challenge cookie = %+v", challenge)
	}
	cred, err := answer(options)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"credential": json.RawMessage(cred)}
	if name != "" {
		body["name"] = name
	}
	return postJSON(t, env, path, body, append(cookies, challenge), csrf)
}

func TestPasskeyEndpoints(t *testing.T) {
	env := newTestEnv(t)
	_, token := createTestUser(t, env, "passkey@example.com", auth.RoleAdmin)
	dev := webauthntest.New("https://linx.example.com")

	// The link says who it's for and that passkeys can be offered.
	infoResp, err := http.Get(env.srv.URL + "/api/v1/setup-links/" + token)
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		Email           string `json:"email"`
		HasSecondStep   bool   `json:"has_second_step"`
		PasskeysOffered bool   `json:"passkeys_available"`
	}
	_ = json.NewDecoder(infoResp.Body).Decode(&info)
	infoResp.Body.Close()
	if info.Email != "passkey@example.com" || info.HasSecondStep || !info.PasskeysOffered {
		t.Fatalf("setup link info = %+v", info)
	}

	resp := passkeyStep(t, env, "/api/v1/setup-links/"+token+"/passkey", nil, "", dev.Create, "Test Mac")
	var status struct {
		Status        string   `json:"status"`
		RecoveryCodes []string `json:"recovery_codes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || status.Status != "signed_in" || len(status.RecoveryCodes) != 10 {
		t.Fatalf("setup with a passkey: %d %+v", resp.StatusCode, status)
	}

	// Answering without the challenge cookie (another browser) fails.
	opts := postJSON(t, env, "/api/v1/session/passkey/options", map[string]any{}, nil, "")
	options, _ := io.ReadAll(opts.Body)
	opts.Body.Close()
	cred, _ := dev.Get(options)
	stolen := postJSON(t, env, "/api/v1/session/passkey", map[string]any{"credential": json.RawMessage(cred)}, nil, "")
	stolen.Body.Close()
	if stolen.StatusCode != http.StatusBadRequest {
		t.Fatalf("answer without the challenge cookie: %d", stolen.StatusCode)
	}

	resp = passkeyStep(t, env, "/api/v1/session/passkey", nil, "", dev.Get, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("passkey sign-in: %d", resp.StatusCode)
	}
	cookies, csrf := cookiesAndCSRF(resp)
	if csrf == "" {
		t.Fatal("passkey sign-in should set the session cookies")
	}

	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/me", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	meResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var me struct {
		Pending      bool `json:"pending"`
		Passkeys     int  `json:"passkeys"`
		HasPassword  bool `json:"has_password"`
		PasswordOnly bool `json:"password_only"`
	}
	_ = json.NewDecoder(meResp.Body).Decode(&me)
	meResp.Body.Close()
	if me.Pending || me.Passkeys != 1 || me.HasPassword || me.PasswordOnly {
		t.Fatalf("/me = %+v", me)
	}

	// The session's own passkey endpoints need the CSRF header.
	noCSRF := postJSON(t, env, "/api/v1/me/passkeys/options", map[string]any{}, cookies, "")
	noCSRF.Body.Close()
	if noCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("without CSRF: %d", noCSRF.StatusCode)
	}
	resp = passkeyStep(t, env, "/api/v1/session/confirm/passkey", cookies, csrf, dev.Get, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("confirm with a passkey: %d", resp.StatusCode)
	}
	second := webauthntest.New("https://linx.example.com")
	resp = passkeyStep(t, env, "/api/v1/me/passkeys", cookies, csrf, second.Create, "Phone")
	added, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("adding a passkey: %d %s", resp.StatusCode, added)
	}

	listReq, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/me/passkeys", nil)
	for _, c := range cookies {
		listReq.AddCookie(c)
	}
	listResp, err := http.DefaultClient.Do(listReq)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&list)
	listResp.Body.Close()
	if len(list.Items) != 2 || list.Items[1].Name != "Phone" {
		t.Fatalf("passkeys = %+v", list)
	}
}
