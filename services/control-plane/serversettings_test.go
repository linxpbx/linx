package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/install"
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

func (f *fakeServerSettings) ChangeServerSettings(_ context.Context, c install.ServerChange) ([]install.FieldError, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refuse != "" {
		return nil, &install.Refused{Detail: f.refuse}
	}
	if c.Token == "bad" {
		return []install.FieldError{{Step: install.StepToken, Field: "token", Message: "That token can't see example.com at Cloudflare."}}, nil
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
		Token: "saved", Profile: "lite", Profiles: []install.ProfileOption{{Name: "lite", Description: "small"}}, ExpiresAt: time.Now().Add(time.Hour)}
	code, body := get()
	settings, _ := body["settings"].(map[string]any)
	if code != http.StatusOK || body["open"] != true || settings["token_saved"] != true || settings["domain"] != "example.com" {
		t.Fatalf("open: %d %v", code, body)
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
	if len(e.serverSettings.changes) != 1 || !e.serverSettings.changes[0].Portainer {
		t.Errorf("changes %+v", e.serverSettings.changes)
	}
	// Every change is in the activity log.
	n := 0
	for _, a := range e.store.audits {
		if a.Action == "system.server_settings" {
			n++
		}
	}
	if n != 3 {
		t.Errorf("%d audit entries", n)
	}
}
