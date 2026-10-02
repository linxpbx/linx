package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/install"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

type fakeServerSettings struct {
	mu      sync.Mutex
	view    *install.ServerView
	changes []install.ServerChange
	refuse  string
}

func (f *fakeServerSettings) ServerSettings() *install.ServerView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view
}

func (f *fakeServerSettings) PreviewServerSettings(_ context.Context, c install.ServerChange) (install.ServerPreview, error) {
	p := install.ServerPreview{Address: "https://example.com", Steps: []string{"Save your settings"}}
	if c.Domain != "" {
		p.Address = "https://" + c.Domain
		p.AddRecords = []install.Record{{Type: "A", Name: c.Domain, Value: "203.0.113.5"}}
		p.Setup = &install.DoorSetup{Files: []install.SetupFile{{Title: "the block for Pangolin", Text: "tcp:"}}}
	}
	if c.FrontDoor == "proxy" {
		p.Setup = &install.DoorSetup{Steps: []string{"On your router, keep TCP port 443 going to your front door."},
			Card: &install.DoorCard{Proxy: c.ProxyAddress, Routes: []install.DoorRoute{{Name: "example.com", Address: "192.168.1.10:8443", ProxyProtocol: true}},
				Guides: []install.DoorGuide{{ID: "pangolin", Title: "Pangolin", Steps: []string{"Open Traefik's file."}}}}}
	}
	if c.Token == "bad" {
		p.Errors = []install.FieldError{{Step: install.StepToken, Field: "token", Message: "That token can't see example.com at Cloudflare."}}
	}
	return p, nil
}

func (f *fakeServerSettings) ChangeServerSettings(_ context.Context, c install.ServerChange) ([]install.FieldError, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != "" {
		return nil, &install.Refused{Detail: f.refuse}
	}
	if c.Token == "bad" {
		return []install.FieldError{{Step: install.StepToken, Field: "token", Message: "That token can't see example.com at Cloudflare."}}, nil
	}
	if c.Domain != "" && !c.DoorDone {
		return []install.FieldError{{Step: install.StepDoorDone, Field: "door_done", Message: "Do the steps first."}}, nil
	}
	f.changes = append(f.changes, c)
	return nil, nil
}

func TestServerSettingsEndpoints(t *testing.T) {
	e := newBackupFileEnv(t)
	get := func() (int, map[string]any) {
		resp := e.do("GET", "/api/v1/server-settings", "", nil, false)
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	post := func(body string) *http.Response {
		return e.do("POST", "/api/v1/server-settings", "application/json", strings.NewReader(body), true)
	}

	// An admin who isn't a system admin: no.
	e.become(auth.RoleAdmin, 0)
	if code, body := get(); code != http.StatusForbidden || body["code"] != "system_admin_only" {
		t.Errorf("admin: %d %v", code, body)
	}
	e.become(auth.RoleSystemAdmin, 0)
	if code, body := get(); code != http.StatusOK || body["open"] != false {
		t.Errorf("closed: %d %v", code, body)
	}
	if code := problemCode(t, post(`{"profile":"standard","portainer":false}`)); code != "server_settings_closed" {
		t.Errorf("change while closed: %s", code)
	}

	e.serverSettings.view = &install.ServerView{Where: install.WhereHome, FrontDoor: "pangolin", Domain: "example.com", Provider: "cloudflare",
		Token: "saved", Profile: "lite", Profiles: []install.ProfileOption{{Name: "lite", Description: "small"}}, ExpiresAt: time.Now().Add(time.Hour),
		DoorSetup: &install.DoorSetup{Steps: []string{"On your router, keep TCP port 443 going to your front door."},
			Card: &install.DoorCard{Proxy: "192.168.1.211", Guides: []install.DoorGuide{{ID: "pangolin", Title: "Pangolin", Steps: []string{"Open Traefik's file."}}}}}}
	code, body := get()
	settings, _ := body["settings"].(map[string]any)
	if code != http.StatusOK || body["open"] != true || settings["token_saved"] != true || settings["domain"] != "example.com" {
		t.Fatalf("open: %d %v", code, body)
	}
	// Show the steps: the front door in use, its card whole.
	door, _ := settings["door_setup"].(map[string]any)
	if card, _ := door["card"].(map[string]any); card["proxy"] != "192.168.1.211" {
		t.Errorf("door setup: %v", settings["door_setup"])
	}

	// A change needs "confirm it's you".
	e.become(auth.RoleSystemAdmin, time.Hour)
	if code := problemCode(t, post(`{"profile":"standard","portainer":false}`)); code != "confirm_required" {
		t.Errorf("unconfirmed: %s", code)
	}
	e.become(auth.RoleSystemAdmin, 0)
	if resp := post(`{"profile":"standard","portainer":true}`); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("change: %d", resp.StatusCode)
	}
	if code := problemCode(t, post(`{"profile":"standard","portainer":false,"token":"bad"}`)); code != "token_refused" {
		t.Errorf("bad token: %s", code)
	}
	e.serverSettings.refuse = "A change is still being made. Wait for it to finish."
	if code := problemCode(t, post(`{"profile":"lite","portainer":false}`)); code != "server_settings_refused" {
		t.Errorf("refused: %s", code)
	}
	if code := problemCode(t, post(`{"profile":"huge","portainer":false}`)); code == "" {
		t.Error("unknown size accepted")
	}
	e.serverSettings.refuse = ""
	// A new domain: the preview first (no confirm needed), then the change.
	e.become(auth.RoleSystemAdmin, time.Hour)
	resp := e.do("POST", "/api/v1/server-settings/preview", "application/json", strings.NewReader(`{"profile":"lite","portainer":false,"domain":"example.org"}`), true)
	var p map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&p)
	resp.Body.Close()
	setup, _ := p["setup"].(map[string]any)
	if resp.StatusCode != http.StatusOK || p["address"] != "https://example.org" || setup == nil || len(p["add_records"].([]any)) != 1 {
		t.Errorf("preview: %d %v", resp.StatusCode, p)
	}
	// Another program in front: the front-door card comes through whole.
	resp = e.do("POST", "/api/v1/server-settings/preview", "application/json",
		strings.NewReader(`{"profile":"lite","portainer":false,"front_door":"proxy","proxy_address":"192.168.1.211"}`), true)
	p = nil
	_ = json.NewDecoder(resp.Body).Decode(&p)
	resp.Body.Close()
	setup, _ = p["setup"].(map[string]any)
	card, _ := setup["card"].(map[string]any)
	if guides, _ := card["guides"].([]any); resp.StatusCode != http.StatusOK || card["proxy"] != "192.168.1.211" || len(guides) != 1 {
		t.Errorf("preview card: %d %v", resp.StatusCode, p)
	}
	e.become(auth.RoleSystemAdmin, 0)
	if code := problemCode(t, post(`{"profile":"lite","portainer":false,"domain":"example.org"}`)); code != "server_settings_invalid" {
		t.Errorf("not ticked: %s", code)
	}
	if resp := post(`{"profile":"lite","portainer":false,"domain":"example.org","front_door":"linx-443","door_done":true}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("move: %d", resp.StatusCode)
	}
	if code := problemCode(t, post(`{"profile":"lite","portainer":false,"front_door":"somewhere"}`)); code == "" {
		t.Error("unknown front door accepted")
	}
	// Another DNS company's key, and Stop.
	if resp := post(`{"profile":"lite","portainer":false,"dns_key":{"provider":"porkbun","key":{"api_key":"pk1_k","secret_api_key":"sk1_s"}},"dns_by_hand":true}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("porkbun: %d", resp.StatusCode)
	}
	if code := problemCode(t, post(`{"profile":"lite","portainer":false,"dns_key":{"provider":"gandi","token":"x"}}`)); code == "" {
		t.Error("unknown DNS company accepted")
	}
	if c := e.serverSettings.changes[len(e.serverSettings.changes)-1]; c.Key == nil || c.Key.Provider != "porkbun" || c.Key.Fields["secret_api_key"] != "sk1_s" ||
		c.DNSByHand == nil || !*c.DNSByHand {
		t.Errorf("porkbun change %+v", c)
	}
	e.serverSettings.changes = e.serverSettings.changes[:len(e.serverSettings.changes)-1]
	for _, a := range e.store.audits {
		if b, _ := json.Marshal(a.Detail); strings.Contains(string(b), "sk1_s") {
			t.Errorf("the key is in the activity log: %s", b)
		}
	}
	if len(e.serverSettings.changes) != 2 || !e.serverSettings.changes[0].Portainer || e.serverSettings.changes[1].Domain != "example.org" ||
		e.serverSettings.changes[1].FrontDoor != "linx-443" {
		t.Errorf("changes %+v", e.serverSettings.changes)
	}
	// Every change is in the activity log.
	n := 0
	for _, a := range e.store.audits {
		if a.Action == "system.server_settings" {
			n++
		}
	}
	if n != 6 { // the five above and the Porkbun key (the unknown company never reaches setup)
		t.Errorf("%d audit entries", n)
	}
}

// A repair link that skips the sign-in reaches the page's own operations
// without a session, and the activity log names setup as who did it.
func TestRepairSettingsHandler(t *testing.T) {
	e := newTestEnv(t)
	e.serverSettings.view = &install.ServerView{Where: install.WhereRented, FrontDoor: "linx-443", Domain: "example.com", Provider: "cloudflare",
		Profile: "lite", Repair: true, NoSignIn: true, ExpiresAt: time.Now().Add(time.Hour)}
	h := e.apiServer.RepairSettingsHandler(e.store.tenant)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = "198.51.100.7:4000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do("GET", install.RepairSettingsAPI, ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"no_sign_in":true`) {
		t.Errorf("get: %d %s", w.Code, w.Body)
	}
	if w := do("POST", install.RepairSettingsAPI+"/preview", `{"profile":"lite","portainer":false,"domain":"example.org"}`); w.Code != 200 ||
		!strings.Contains(w.Body.String(), "https://example.org") {
		t.Errorf("preview: %d %s", w.Code, w.Body)
	}
	if w := do("POST", install.RepairSettingsAPI, `{"profile":"lite","portainer":false,"sneaky":1}`); w.Code != 400 {
		t.Errorf("unknown field: %d", w.Code)
	}
	if w := do("POST", install.RepairSettingsAPI, `{"profile":"standard","portainer":false}`); w.Code != http.StatusAccepted {
		t.Errorf("change: %d %s", w.Code, w.Body)
	}
	found := false
	for _, a := range e.store.audits {
		if a.Action == "system.server_settings" && a.Actor == controlplaneapi.RepairActor && a.IP.String() == "198.51.100.7" && a.Detail["repair_page"] == true {
			found = true
		}
	}
	if !found {
		t.Errorf("audit %+v", e.store.audits)
	}
}
