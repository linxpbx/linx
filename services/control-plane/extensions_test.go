package main

import (
	"net/http"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/auth"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

func (e *testEnv) createExtension(key string, body map[string]any) controlplaneapi.Extension {
	e.t.Helper()
	r := e.do(http.MethodPost, "/api/v1/extensions", key, body)
	if r.status != http.StatusCreated {
		e.t.Fatalf("create extension: %d %s", r.status, r.body)
	}
	var out controlplaneapi.Extension
	r.json(e.t, &out)
	return out
}

func TestExtensionEndpoints(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all", "devices:write")
	_, reporter := e.newCredential(auth.TypeAPIKey, auth.RoleReporter, "all")

	var created controlplaneapi.Extension
	t.Run("create", func(t *testing.T) {
		created = e.createExtension(admin, map[string]any{"number": "101", "display_name": "Reception"})
		if created.Number != "101" || created.DisplayName != "Reception" || !created.Enabled || created.Etag != `"1"` {
			t.Fatalf("unexpected extension %+v", created)
		}
		r := e.do(http.MethodGet, "/api/v1/extensions/"+created.Id.String(), admin, nil)
		if r.status != http.StatusOK || r.header.Get("ETag") != `"1"` {
			t.Fatalf("get: %d %s, etag %q", r.status, r.body, r.header.Get("ETag"))
		}
		if !contains(e.pbxStore.auditActions(), "extension.create") {
			t.Fatal("no audit entry")
		}
	})

	t.Run("duplicate number is refused", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/extensions", admin, map[string]any{"number": "101", "display_name": "Someone else"})
		if r.status != http.StatusConflict || r.problemCode(t) != "number_duplicate" {
			t.Fatalf("duplicate: %d %s", r.status, r.body)
		}
	})

	t.Run("numbers that look like outside or emergency numbers are refused", func(t *testing.T) {
		for number, detail := range map[string]string{
			"0123": "can't start with 0",
			"999":  "999 is an emergency number in United Arab Emirates",
		} {
			r := e.do(http.MethodPost, "/api/v1/extensions", admin, map[string]any{"number": number, "display_name": "X"})
			if r.status != http.StatusUnprocessableEntity || r.problemCode(t) != "number_reserved" || !strings.Contains(string(r.body), detail) {
				t.Fatalf("%s: %d %s", number, r.status, r.body)
			}
		}
	})

	t.Run("bad input is refused", func(t *testing.T) {
		for _, tc := range []struct {
			body map[string]any
			code string
		}{
			{map[string]any{"number": "1", "display_name": "x"}, "number_invalid"},
			{map[string]any{"number": "1234567", "display_name": "x"}, "number_invalid"},
			{map[string]any{"number": "102", "display_name": ""}, "display_name_invalid"},
		} {
			r := e.do(http.MethodPost, "/api/v1/extensions", admin, tc.body)
			if r.status < 400 || r.problemCode(t) != tc.code {
				t.Errorf("%v: got %d %s, want %s", tc.body, r.status, r.body, tc.code)
			}
		}
	})

	t.Run("scopes", func(t *testing.T) {
		if r := e.do(http.MethodGet, "/api/v1/extensions", reporter, nil); r.status != http.StatusOK {
			t.Fatalf("reporter list: %d %s", r.status, r.body)
		}
		r := e.do(http.MethodPost, "/api/v1/extensions", reporter, map[string]any{"number": "199", "display_name": "x"})
		if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
			t.Fatalf("reporter create: %d %s", r.status, r.body)
		}
	})

	t.Run("patch with If-Match", func(t *testing.T) {
		path := "/api/v1/extensions/" + created.Id.String()
		if r := e.patch(path, admin, `"99"`, map[string]any{"display_name": "x"}); r.status != http.StatusPreconditionFailed {
			t.Fatalf("stale If-Match: %d %s", r.status, r.body)
		}
		r := e.patch(path, admin, `"1"`, map[string]any{"display_name": "Front Desk", "enabled": false})
		if r.status != http.StatusOK {
			t.Fatalf("patch: %d %s", r.status, r.body)
		}
		var out controlplaneapi.Extension
		r.json(t, &out)
		if out.DisplayName != "Front Desk" || out.Enabled || out.Etag != `"2"` || out.Number != "101" {
			t.Fatalf("after patch: %+v", out)
		}
	})

	t.Run("delete revokes its devices and frees the number", func(t *testing.T) {
		ext := e.createExtension(admin, map[string]any{"number": "150", "display_name": "Temp"})
		dev := e.createDevice(admin, ext.Id, map[string]any{"name": "Test phone"})

		if r := e.do(http.MethodDelete, "/api/v1/extensions/"+ext.Id.String(), admin, nil); r.status != http.StatusNoContent {
			t.Fatalf("delete: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodGet, "/api/v1/extensions/"+ext.Id.String(), admin, nil); r.status != http.StatusNotFound {
			t.Fatalf("get after delete: %d", r.status)
		}
		r := e.do(http.MethodGet, "/api/v1/devices/"+dev.Device.Id.String(), admin, nil)
		var got controlplaneapi.Device
		r.json(t, &got)
		if r.status != http.StatusOK || got.Enabled {
			t.Fatalf("device after extension delete: %d %+v", r.status, got)
		}

		// The number is free again.
		again := e.createExtension(admin, map[string]any{"number": "150", "display_name": "Reused"})
		if again.Number != "150" {
			t.Fatalf("reused number: %+v", again)
		}
	})
}

func contains(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}
