package main

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

func (e *testEnv) createDevice(key string, extension uuid.UUID, body map[string]any) controlplaneapi.DeviceCredentials {
	e.t.Helper()
	r := e.do(http.MethodPost, "/api/v1/extensions/"+extension.String()+"/devices", key, body)
	if r.status != http.StatusCreated {
		e.t.Fatalf("create device: %d %s", r.status, r.body)
	}
	var out controlplaneapi.DeviceCredentials
	r.json(e.t, &out)
	return out
}

func TestDeviceEndpoints(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all", "devices:write")
	_, noDevices := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	ext := e.createExtension(admin, map[string]any{"number": "201", "display_name": "Mohammed"})

	var created controlplaneapi.DeviceCredentials
	t.Run(`"all" doesn't include devices:write`, func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/extensions/"+ext.Id.String()+"/devices", noDevices, map[string]any{"name": "x"})
		if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})

	t.Run("create returns the SIP login once", func(t *testing.T) {
		created = e.createDevice(admin, ext.Id, map[string]any{"name": "Mohammed's iPhone"})
		d := created.Device
		if d.Name != "Mohammed's iPhone" || d.Kind != "softphone" || !d.Enabled || d.Etag != `"1"` {
			t.Fatalf("unexpected device %+v", d)
		}
		if len(d.SipUsername) == 0 || created.Password == "" {
			t.Fatalf("missing sip_username or password: %+v", created)
		}
		if created.Server != "sip.linx.example.com" || created.Port != 5061 || created.Transport != "tls" {
			t.Fatalf("sip settings = %+v", created)
		}
		r := e.do(http.MethodGet, "/api/v1/devices/"+d.Id.String(), admin, nil)
		if r.status != http.StatusOK || bytes.Contains(r.body, []byte(created.Password)) {
			t.Fatalf("get leaks the password: %d %s", r.status, r.body)
		}
		if !contains(e.pbxStore.auditActions(), "device.create") {
			t.Fatal("no audit entry")
		}
	})

	t.Run("only softphone works this slice", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/extensions/"+ext.Id.String()+"/devices", admin, map[string]any{"name": "x", "kind": "web"})
		if r.status != http.StatusUnprocessableEntity || r.problemCode(t) != "kind_not_supported" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})

	t.Run("list under the extension", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/extensions/"+ext.Id.String()+"/devices", admin, nil)
		var list controlplaneapi.DeviceList
		r.json(t, &list)
		if r.status != http.StatusOK || len(list.Items) != 1 || list.Items[0].Id != created.Device.Id {
			t.Fatalf("list: %d %s", r.status, r.body)
		}
	})

	t.Run("patch with If-Match", func(t *testing.T) {
		path := "/api/v1/devices/" + created.Device.Id.String()
		if r := e.patch(path, admin, `"99"`, map[string]any{"name": "x"}); r.status != http.StatusPreconditionFailed {
			t.Fatalf("stale If-Match: %d %s", r.status, r.body)
		}
		r := e.patch(path, admin, "", map[string]any{"name": "Renamed"})
		var out controlplaneapi.Device
		r.json(t, &out)
		if r.status != http.StatusOK || out.Name != "Renamed" || out.Etag != `"2"` {
			t.Fatalf("patch: %d %+v", r.status, out)
		}
	})

	t.Run("reset password issues a new login, same username", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/devices/"+created.Device.Id.String()+"/reset-password", admin, nil)
		if r.status != http.StatusOK {
			t.Fatalf("reset: %d %s", r.status, r.body)
		}
		var out controlplaneapi.DeviceCredentials
		r.json(t, &out)
		if out.Password == created.Password {
			t.Fatal("password didn't change")
		}
		if out.Device.SipUsername != created.Device.SipUsername {
			t.Fatal("username changed on reset")
		}
	})

	t.Run("revoke logs it out and is idempotent", func(t *testing.T) {
		path := "/api/v1/devices/" + created.Device.Id.String()
		if r := e.do(http.MethodDelete, path, admin, nil); r.status != http.StatusNoContent {
			t.Fatalf("revoke: %d %s", r.status, r.body)
		}
		r := e.do(http.MethodGet, path, admin, nil)
		var out controlplaneapi.Device
		r.json(t, &out)
		if r.status != http.StatusOK || out.Enabled || out.RevokedAt == nil {
			t.Fatalf("after revoke: %d %+v", r.status, out)
		}
		if r := e.do(http.MethodDelete, path, admin, nil); r.status != http.StatusNoContent {
			t.Fatalf("second revoke: %d %s", r.status, r.body)
		}
	})

	t.Run("a revoked device stays revoked", func(t *testing.T) {
		path := "/api/v1/devices/" + created.Device.Id.String()
		if r := e.patch(path, admin, "", map[string]any{"enabled": true}); r.status != http.StatusConflict || r.problemCode(t) != "device_revoked" {
			t.Fatalf("turning it back on: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodPost, path+"/reset-password", admin, nil); r.status != http.StatusConflict || r.problemCode(t) != "device_revoked" {
			t.Fatalf("new password: %d %s", r.status, r.body)
		}
	})

	t.Run("unknown extension", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/extensions/"+uuid.NewString()+"/devices", admin, map[string]any{"name": "x"})
		if r.status != http.StatusNotFound {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})
}
