package install

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func repairRig(t *testing.T, noSignIn bool) (*Server, http.Handler, RepairState, string) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "repair-state.json")
	rs := NewRepairState(time.Now(), noSignIn, "the certificate expired")
	if err := rs.Save(statePath); err != nil {
		t.Fatal(err)
	}
	srv, _ := settingsRig(t, &fakeSettings{view: homeSettings}, func(h *SettingsHost) { h.RepairPath = statePath })
	srv.Web = fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>Linx</title>")}}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Api", r.URL.Path) })
	settings := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("X-Settings", r.URL.Path) })
	waitFor(t, func() bool { _, open := srv.repairView(); return open })
	return srv, srv.RepairHandler(api, settings), rs, statePath
}

func repairGet(h http.Handler, method, path, cookie string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://203.0.113.5:6464"+path, nil)
	r.RemoteAddr = "198.51.100.7:40000"
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: RepairCookieName, Value: cookie})
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRepairLink(t *testing.T) {
	_, h, rs, statePath := repairRig(t, false)
	for _, p := range []string{"/", "/repair", "/api/v1/me", "/repair/" + NewSecret(), "/install/" + rs.Secret} {
		if w := repairGet(h, "GET", p, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s without the link: %d", p, w.Code)
		}
	}
	w := repairGet(h, "GET", "/repair/"+rs.Secret, "")
	var cookie string
	for _, c := range w.Result().Cookies() {
		if c.Name == RepairCookieName && c.Secure && c.HttpOnly && c.SameSite == http.SameSiteStrictMode {
			cookie = c.Value
		}
	}
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != RepairPage || cookie == "" {
		t.Fatalf("claim: %d %v", w.Code, w.Header())
	}
	if w := repairGet(h, "GET", "/repair/"+rs.Secret, ""); w.Code != http.StatusNotFound {
		t.Errorf("second claim: %d", w.Code)
	}
	saved, err := LoadRepairState(statePath)
	if err != nil || saved.Secret != "" || !Matches(cookie, saved.SessionHash) || saved.Address != "198.51.100.7" {
		t.Errorf("saved %+v %v", saved, err)
	}

	if w := repairGet(h, "GET", RepairStatePath, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), `"no_sign_in":false`) ||
		!strings.Contains(w.Body.String(), "the certificate expired") {
		t.Errorf("state: %d %s", w.Code, w.Body)
	}
	if w := repairGet(h, "GET", "/repair", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "<title>Linx") {
		t.Errorf("page: %d", w.Code)
	}
	for _, ok := range []string{"POST /api/v1/session", "POST /api/v1/session/mfa", "GET /api/v1/me", "POST /api/v1/server-settings"} {
		m, p, _ := strings.Cut(ok, " ")
		if w := repairGet(h, m, p, cookie); w.Header().Get("X-Api") != p {
			t.Errorf("%s not passed on: %d", ok, w.Code)
		}
	}
	for _, no := range []string{"GET /api/v1/users", "POST /api/v1/session/passkey/options", "GET /api/v1/sso/callback", "GET /sip", "GET " + RepairSettingsAPI} {
		m, p, _ := strings.Cut(no, " ")
		if w := repairGet(h, m, p, cookie); w.Code != http.StatusNotFound {
			t.Errorf("%s: %d", no, w.Code)
		}
	}
}

// The host keeps only a browser name and an address it recognises, as the
// install's claim does (security review): not whatever the control plane
// sends.
func TestRepairClaimKeepsOnlyKnownNames(t *testing.T) {
	p := filepath.Join(t.TempDir(), "repair-state.json")
	rs := NewRepairState(time.Now(), false, "")
	h := &SettingsHost{RepairPath: p, repair: rs}
	if !h.claim(Message{Secret: rs.Secret, SessionHash: Hash(NewSecret()), Browser: "\x1b]0;owned\x07Chrome", Address: "not an address\x1b[2J"}) {
		t.Fatal("claim refused")
	}
	saved, err := LoadRepairState(p)
	if err != nil || saved.Browser != "a browser" || saved.Address != "" {
		t.Errorf("saved %+v %v", saved, err)
	}
}

func TestRepairNoSignIn(t *testing.T) {
	_, h, rs, _ := repairRig(t, true)
	w := repairGet(h, "GET", "/repair/"+rs.Secret, "")
	cookie := ""
	for _, c := range w.Result().Cookies() {
		cookie = c.Value
	}
	if w := repairGet(h, "GET", RepairSettingsAPI, cookie); w.Header().Get("X-Settings") != RepairSettingsAPI {
		t.Errorf("settings: %d", w.Code)
	}
	if w := repairGet(h, "POST", RepairSettingsAPI, cookie); w.Code != http.StatusForbidden {
		t.Errorf("change from elsewhere: %d", w.Code)
	}
	if w := repairGet(h, "POST", RepairSettingsAPI+"/preview", cookie, "Origin", "https://203.0.113.5:6464", "Content-Type", "application/json"); w.Header().Get("X-Settings") == "" {
		t.Errorf("preview: %d", w.Code)
	}
}
