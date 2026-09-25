package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/siprelay"
)

// signedInPerson creates a person (with extension ext, if not nil), signs
// them in through their setup link, and returns their id and cookies.
func signedInPerson(t *testing.T, env *testEnv, email string, ext *uuid.UUID) (uuid.UUID, []*http.Cookie, string) {
	t.Helper()
	ctx := auth.WithPrincipal(context.Background(), auth.SystemPrincipal(env.store.tenant))
	u, token, err := env.accounts.CreateUser(ctx, auth.UserInput{Email: email, Name: "Rana Haddad", Role: auth.RoleUser, ExtensionID: ext})
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, env, "/api/v1/setup-links/"+token, map[string]string{"password": "correct horse battery staple"}, nil, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("setup link: %d", resp.StatusCode)
	}
	cookies, csrf := cookiesAndCSRF(resp)
	return u.ID, cookies, csrf
}

func (e *testEnv) newExtension(number string) pbx.Extension {
	e.t.Helper()
	now := time.Now().UTC()
	x := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: e.store.tenant, Number: number, DisplayName: "Rana Haddad",
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := e.pbxStore.CreateExtension(e.t.Context(), x, auth.AuditEntry{}); err != nil {
		e.t.Fatal(err)
	}
	return x
}

type webPhoneBody struct {
	DeviceID      uuid.UUID `json:"device_id"`
	SIPUsername   string    `json:"sip_username"`
	Password      string    `json:"password"`
	SIPURI        string    `json:"sip_uri"`
	WebsocketPath string    `json:"websocket_path"`
	DisplayName   string    `json:"display_name"`
	Extension     string    `json:"extension"`
	Turn          struct {
		URLs       []string  `json:"urls"`
		Username   string    `json:"username"`
		Credential string    `json:"credential"`
		ExpiresAt  time.Time `json:"expires_at"`
	} `json:"turn"`
}

func issueWebPhone(t *testing.T, env *testEnv, cookies []*http.Cookie, csrf string) (int, webPhoneBody, string) {
	t.Helper()
	resp := postJSON(t, env, "/api/v1/me/web-phone", nil, cookies, csrf)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var out webPhoneBody
	var p struct{ Code string }
	json.Unmarshal(b, &out)
	json.Unmarshal(b, &p)
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", resp.Header.Get("Cache-Control"))
	}
	return resp.StatusCode, out, p.Code
}

func TestWebPhoneIssued(t *testing.T) {
	env := newTestEnv(t)
	ext := env.newExtension("101")
	uid, cookies, csrf := signedInPerson(t, env, "rana@example.com", &ext.ID)

	status, first, code := issueWebPhone(t, env, cookies, csrf)
	if status != http.StatusOK {
		t.Fatalf("status %d %s", status, code)
	}
	if first.Extension != "101" || first.DisplayName != "Rana Haddad" || first.WebsocketPath != "/sip" ||
		first.SIPURI != "sip:"+first.SIPUsername+"@sip.linx.example.com" || first.Password == "" {
		t.Errorf("%+v", first)
	}
	if !strings.HasSuffix(first.Turn.Username, ":"+uid.String()) || first.Turn.Credential == "" || len(first.Turn.URLs) != 2 ||
		time.Until(first.Turn.ExpiresAt) < 59*time.Minute {
		t.Errorf("turn %+v", first.Turn)
	}
	d, err := env.pbxStore.Device(t.Context(), env.store.tenant, first.DeviceID)
	if err != nil || d.Kind != pbx.KindWeb || d.UserSessionID == nil || d.DigestHash != pbx.DigestHash(d.SIPUsername, first.Password) {
		t.Fatalf("device %+v %v", d, err)
	}

	// A reload: the same line, with a new password.
	_, second, _ := issueWebPhone(t, env, cookies, csrf)
	if second.SIPUsername != first.SIPUsername || second.Password == first.Password {
		t.Errorf("second: %s/%s after %s/%s", second.SIPUsername, second.Password, first.SIPUsername, first.Password)
	}
	d, _ = env.pbxStore.Device(t.Context(), env.store.tenant, first.DeviceID)
	if d.DigestHash != pbx.DigestHash(d.SIPUsername, second.Password) {
		t.Error("stored digest isn't the new password's")
	}

	// And fresh relay credentials on their own.
	req, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/me/turn-credentials", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	r := env.send(req)
	var tc struct{ Username, Credential string }
	r.json(t, &tc)
	if r.status != http.StatusOK || !strings.HasSuffix(tc.Username, ":"+uid.String()) || tc.Credential == "" {
		t.Errorf("turn-credentials: %d %s", r.status, r.body)
	}

	// The password is never shown by the admin device API, and can't be
	// reset there either.
	_, adminKey := env.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "devices:read", "devices:write")
	if r := env.do(http.MethodPost, "/api/v1/devices/"+first.DeviceID.String()+"/reset-password", adminKey, nil); r.status != http.StatusConflict ||
		r.problemCode(t) != "device_is_web" {
		t.Errorf("reset-password: %d %s", r.status, r.body)
	}
}

func TestWebPhoneRefused(t *testing.T) {
	env := newTestEnv(t)
	_, cookies, csrf := signedInPerson(t, env, "noext@example.com", nil)
	if status, _, code := issueWebPhone(t, env, cookies, csrf); status != http.StatusConflict || code != "no_extension" {
		t.Errorf("no extension: %d %s", status, code)
	}

	ext := env.newExtension("102")
	x := ext
	x.Enabled = false
	env.pbxStore.extensions[x.ID] = x
	_, cookies2, csrf2 := signedInPerson(t, env, "off@example.com", &ext.ID)
	if status, _, code := issueWebPhone(t, env, cookies2, csrf2); status != http.StatusConflict || code != "extension_unavailable" {
		t.Errorf("extension off: %d %s", status, code)
	}

	// Not a browser session: an API key.
	_, key := env.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	if r := env.do(http.MethodPost, "/api/v1/me/web-phone", key, nil); r.status != http.StatusBadRequest || r.problemCode(t) != "not_a_session" {
		t.Errorf("api key: %d %s", r.status, r.body)
	}
	if r := env.do(http.MethodGet, "/api/v1/me/turn-credentials", key, nil); r.status != http.StatusBadRequest {
		t.Errorf("api key turn: %d %s", r.status, r.body)
	}
	// No CSRF token: refused like any other write.
	if status, _, code := issueWebPhone(t, env, cookies, ""); status != http.StatusForbidden || code != "csrf_invalid" {
		t.Errorf("no csrf: %d %s", status, code)
	}
}

// fakeAsteriskWS answers every REGISTER with 200 and records what it got.
func fakeAsteriskWS(t *testing.T) (string, chan string) {
	got := make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"sip"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			got <- string(b)
			c.Write(context.Background(), websocket.MessageText, []byte("SIP/2.0 200 OK\r\nCall-ID: x\r\nCSeq: 1 REGISTER\r\n\r\n"))
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), got
}

func (e *testEnv) dialSIP(cookies []*http.Cookie, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	for _, c := range cookies {
		h.Add("Cookie", c.Name+"="+c.Value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/sip",
		&websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{"sip"}})
}

func register(user string) string {
	return "REGISTER sip:linx SIP/2.0\r\nFrom: <sip:" + user + "@linx>;tag=1\r\nTo: <sip:" + user +
		"@linx>\r\nCall-ID: x\r\nCSeq: 1 REGISTER\r\nContent-Length: 0\r\n\r\n"
}

func TestSIPRelay(t *testing.T) {
	env := newTestEnv(t)
	var got chan string
	env.asterisk, got = fakeAsteriskWS(t)
	ext := env.newExtension("103")
	uid, cookies, csrf := signedInPerson(t, env, "sip@example.com", &ext.ID)
	origin := "https://" + strings.TrimPrefix(env.srv.URL, "http://")

	status := func(resp *http.Response, err error) int {
		if resp == nil {
			t.Fatalf("no response: %v", err)
		}
		return resp.StatusCode
	}
	// Refused before any websocket: no session, wrong page, no line yet.
	if _, resp, err := env.dialSIP(nil, origin); status(resp, err) != http.StatusUnauthorized {
		t.Errorf("no cookie: %d", resp.StatusCode)
	}
	for _, o := range []string{"", "https://evil.example", "http://" + strings.TrimPrefix(env.srv.URL, "http://"), origin + "/x"} {
		if _, resp, err := env.dialSIP(cookies, o); status(resp, err) != http.StatusForbidden {
			t.Errorf("origin %q: %d", o, resp.StatusCode)
		}
	}
	if _, resp, err := env.dialSIP(cookies, origin); status(resp, err) != http.StatusConflict {
		t.Errorf("no line: %d", resp.StatusCode)
	}

	_, line, _ := issueWebPhone(t, env, cookies, csrf)
	c, _, err := env.dialSIP(cookies, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(t.Context(), websocket.MessageText, []byte(register(line.SIPUsername))); err != nil {
		t.Fatal(err)
	}
	if m := <-got; !strings.HasPrefix(m, "REGISTER ") {
		t.Fatalf("Asterisk got %q", m)
	}
	if _, b, err := c.Read(t.Context()); err != nil || !strings.HasPrefix(string(b), "SIP/2.0 200") {
		t.Fatalf("%q %v", b, err)
	}

	// Signing out drops the line at once and revokes the device.
	del, _ := http.NewRequest(http.MethodDelete, env.srv.URL+"/api/v1/session", nil)
	for _, ck := range cookies {
		del.AddCookie(ck)
	}
	del.Header.Set(auth.CSRFHeaderName, csrf)
	if r := env.send(del); r.status != http.StatusNoContent {
		t.Fatalf("sign out: %d %s", r.status, r.body)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Reason != siprelay.ReasonSignedOut {
		t.Fatalf("after sign-out: %v", err)
	}
	d, _ := env.pbxStore.Device(t.Context(), env.store.tenant, line.DeviceID)
	if d.RevokedAt == nil {
		t.Error("the signed-out session's line wasn't revoked")
	}
	if _, resp, err := env.dialSIP(cookies, origin); status(resp, err) != http.StatusUnauthorized {
		t.Errorf("after sign-out: %d", resp.StatusCode)
	}

	// Disabling the person drops their other sessions' lines too.
	cookies2, csrf2 := signIn(t, env, "sip@example.com")
	_, line2, _ := issueWebPhone(t, env, cookies2, csrf2)
	c2, _, err := env.dialSIP(cookies2, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.CloseNow()
	ctx2 := auth.WithPrincipal(t.Context(), auth.SystemPrincipal(env.store.tenant))
	if _, err := env.accounts.DisableUser(ctx2, uid); err != nil {
		t.Fatal(err)
	}
	_, _, err = c2.Read(ctx)
	if !errors.As(err, &ce) || ce.Reason != siprelay.ReasonSignedOut {
		t.Fatalf("after disabling: %v", err)
	}
	if d, _ := env.pbxStore.Device(t.Context(), env.store.tenant, line2.DeviceID); d.RevokedAt == nil {
		t.Error("the disabled person's line wasn't revoked")
	}
}

func signIn(t *testing.T, env *testEnv, email string) ([]*http.Cookie, string) {
	t.Helper()
	resp := postJSON(t, env, "/api/v1/session", map[string]string{"email": email, "password": "correct horse battery staple"}, nil, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sign in: %d", resp.StatusCode)
	}
	return cookiesAndCSRF(resp)
}

func TestSameOrigin(t *testing.T) {
	for origin, want := range map[string]bool{
		"https://meet.example.com":      true,
		"https://MEET.example.com":      true,
		"https://meet.example.com:8443": false,
		"http://meet.example.com":       false,
		"https://evil.example.com":      false,
		"https://meet.example.com/":     false,
		"null":                          false,
		"":                              false,
	} {
		r := httptest.NewRequest(http.MethodGet, "/sip", nil)
		r.Host = "meet.example.com"
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if got := sameOrigin(r); got != want {
			t.Errorf("Origin %q: %v, want %v", origin, got, want)
		}
	}
}
