package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/auth"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

func (e *testEnv) createAlertChannel(key string, body map[string]any) controlplaneapi.AlertChannelCreated {
	e.t.Helper()
	r := e.do(http.MethodPost, "/api/v1/alert-channels", key, body)
	if r.status != http.StatusCreated {
		e.t.Fatalf("create alert channel: %d %s", r.status, r.body)
	}
	var out controlplaneapi.AlertChannelCreated
	r.json(e.t, &out)
	return out
}

func TestAlertChannelEndpoints(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, reporter := e.newCredential(auth.TypeAPIKey, auth.RoleReporter, "all")

	var created controlplaneapi.AlertChannelCreated
	t.Run("create ntfy channel, no secret", func(t *testing.T) {
		created = e.createAlertChannel(admin, map[string]any{
			"kind": "ntfy", "name": "My phone",
			"config": map[string]any{"topic": "linx-alerts"},
		})
		c := created.AlertChannel
		if created.Secret != nil {
			t.Fatal("ntfy channel returned a secret")
		}
		if c.Kind != "ntfy" || c.MinSeverity != "info" || !c.Enabled || c.Etag != `"1"` {
			t.Fatalf("unexpected channel %+v", c)
		}
		r := e.do(http.MethodGet, "/api/v1/alert-channels/"+c.Id.String(), admin, nil)
		var body map[string]any
		r.json(t, &body)
		if _, ok := body["config"]; ok {
			t.Fatal("get response leaked config")
		}
		if r.header.Get("ETag") != `"1"` {
			t.Fatalf("ETag header = %q", r.header.Get("ETag"))
		}
		if !strings.Contains(strings.Join(e.alStore.auditActions(), ","), "alert_channel.create") {
			t.Fatal("no audit entry")
		}
	})

	t.Run("create webhook channel returns a generated secret", func(t *testing.T) {
		out := e.createAlertChannel(admin, map[string]any{
			"kind": "webhook", "name": "Automation",
			"config": map[string]any{"url": "https://hooks.example.com/alerts"},
		})
		if out.Secret == nil || !strings.HasPrefix(*out.Secret, "whsec_") {
			t.Fatalf("secret = %v", out.Secret)
		}
	})

	t.Run("scopes", func(t *testing.T) {
		if r := e.do(http.MethodGet, "/api/v1/alert-channels", reporter, nil); r.status != http.StatusOK {
			t.Fatalf("reporter list: %d %s", r.status, r.body)
		}
		r := e.do(http.MethodPost, "/api/v1/alert-channels", reporter, map[string]any{"kind": "ntfy", "name": "x", "config": map[string]any{"topic": "x"}})
		if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
			t.Fatalf("reporter create: %d %s", r.status, r.body)
		}
	})

	t.Run("bad config is refused", func(t *testing.T) {
		for _, tc := range []struct {
			body map[string]any
			code string
		}{
			{map[string]any{"kind": "ntfy", "name": "x", "config": map[string]any{}}, "channel_config_invalid"},
			{map[string]any{"kind": "ntfy", "name": "x", "config": map[string]any{"topic": "x", "bot_token": "t"}}, "channel_config_invalid"},
			{map[string]any{"kind": "gotify", "name": "x", "config": map[string]any{"app_token": "t"}}, "channel_config_invalid"},
			{map[string]any{"kind": "telegram", "name": "x", "config": map[string]any{"bot_token": "t"}}, "channel_config_invalid"},
			{map[string]any{"kind": "slack", "name": "x", "config": map[string]any{"url": "http://hooks.slack.com/x"}}, "url_invalid"},
			{map[string]any{"kind": "slack", "name": "x", "config": map[string]any{"url": "https://10.0.0.5/x"}}, "url_blocked"},
			{map[string]any{"kind": "carrier-pigeon", "name": "x", "config": map[string]any{}}, "request_invalid"},
			{map[string]any{"kind": "ntfy", "name": "x", "config": map[string]any{"topic": "x"}, "min_severity": "urgent"}, "request_invalid"},
		} {
			r := e.do(http.MethodPost, "/api/v1/alert-channels", admin, tc.body)
			if r.status < 400 || r.problemCode(t) != tc.code {
				t.Errorf("%v: got %d %s, want %s", tc.body, r.status, r.body, tc.code)
			}
		}
	})

	t.Run("quiet hours round trip and patch with If-Match", func(t *testing.T) {
		out := e.createAlertChannel(admin, map[string]any{
			"kind": "ntfy", "name": "Night owl", "config": map[string]any{"topic": "x"},
			"quiet_hours": map[string]any{"enabled": true, "start": "22:00", "end": "07:00", "timezone": "Europe/Istanbul"},
		})
		qh := out.AlertChannel.QuietHours
		if qh == nil || !qh.Enabled || *qh.Start != "22:00" || *qh.End != "07:00" || *qh.Timezone != "Europe/Istanbul" || !*qh.BypassCritical {
			t.Fatalf("quiet_hours = %+v", qh)
		}
		path := "/api/v1/alert-channels/" + out.AlertChannel.Id.String()
		if r := e.patch(path, admin, `"99"`, map[string]any{"name": "x"}); r.status != http.StatusPreconditionFailed {
			t.Fatalf("stale If-Match: %d %s", r.status, r.body)
		}
		r := e.patch(path, admin, `"1"`, map[string]any{"quiet_hours": map[string]any{"enabled": false}})
		var updated controlplaneapi.AlertChannel
		r.json(t, &updated)
		if r.status != http.StatusOK || updated.QuietHours != nil || updated.Etag != `"2"` {
			t.Fatalf("clearing quiet hours: %d %s", r.status, r.body)
		}
		if r := e.patch(path, admin, "", map[string]any{"quiet_hours": map[string]any{"enabled": true, "start": "22:00", "end": "07:00", "timezone": "Not/AZone"}}); r.problemCode(t) != "quiet_hours_invalid" {
			t.Fatalf("bad timezone: %d %s", r.status, r.body)
		}
	})

	t.Run("delete", func(t *testing.T) {
		out := e.createAlertChannel(admin, map[string]any{"kind": "ntfy", "name": "tmp", "config": map[string]any{"topic": "x"}})
		id := out.AlertChannel.Id.String()
		if r := e.do(http.MethodDelete, "/api/v1/alert-channels/"+id, admin, nil); r.status != http.StatusNoContent {
			t.Fatalf("delete: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodGet, "/api/v1/alert-channels/"+id, admin, nil); r.status != http.StatusNotFound {
			t.Fatalf("get after delete: %d", r.status)
		}
	})

}

func TestAlertChannelTest(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")

	var received map[string]any
	status := http.StatusOK
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(status)
	}))
	defer recv.Close()

	out := e.createAlertChannel(admin, map[string]any{"kind": "slack", "name": "x", "config": map[string]any{"url": "https://hooks.slack.com/placeholder"}})
	id := out.AlertChannel.Id

	// Point the channel at the local test server directly (bypassing the
	// SSRF guard, which is exercised in internal/safehttp), the same
	// technique webhooks_test.go uses.
	e.alStore.mu.Lock()
	c := e.alStore.channels[id]
	cfg, err := alert.Config{URL: recv.URL}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := e.alerts.Sealer.Seal("alert_channel:"+id.String(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.ConfigEnc = enc
	e.alStore.channels[id] = c
	e.alerts.Sender.Client = recv.Client()
	e.alStore.mu.Unlock()

	r := e.do(http.MethodPost, "/api/v1/alert-channels/"+id.String()+"/test", admin, nil)
	if r.status != http.StatusOK {
		t.Fatalf("test: %d %s", r.status, r.body)
	}
	var result controlplaneapi.AlertChannelTestResult
	r.json(t, &result)
	if !result.Succeeded || result.StatusCode == nil || *result.StatusCode != 200 {
		t.Fatalf("result = %+v", result)
	}
	if received["text"] == nil {
		t.Fatalf("no message received: %+v", received)
	}

	status = http.StatusInternalServerError
	r = e.do(http.MethodPost, "/api/v1/alert-channels/"+id.String()+"/test", admin, nil)
	r.json(t, &result)
	if result.Succeeded || result.Error == nil {
		t.Fatalf("failed test result = %+v", result)
	}
}

func TestListAlerts(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, reporter := e.newCredential(auth.TypeAPIKey, auth.RoleUser, "extensions:read")

	if r := e.do(http.MethodGet, "/api/v1/alerts", reporter, nil); r.status != http.StatusForbidden {
		t.Fatalf("without alerts:read: %d", r.status)
	}

	tenant := e.store.tenant
	now := time.Now()
	if _, _, err := e.alStore.Fire(context.Background(), tenant, "trunk.down:1", alert.SeverityCritical,
		"Trunk down", "SIP trunk unreachable", "", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.alStore.Fire(context.Background(), tenant, "disk.full:/", alert.SeverityWarning,
		"Disk almost full", "80% used", "", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.alStore.Resolve(context.Background(), tenant, "disk.full:/", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	r := e.do(http.MethodGet, "/api/v1/alerts", admin, nil)
	var list controlplaneapi.AlertList
	r.json(t, &list)
	if r.status != http.StatusOK || len(list.Items) != 2 {
		t.Fatalf("list: %d %s", r.status, r.body)
	}

	r = e.do(http.MethodGet, "/api/v1/alerts?status=open", admin, nil)
	r.json(t, &list)
	if len(list.Items) != 1 || list.Items[0].Key != "trunk.down:1" || list.Items[0].Status != "open" {
		t.Fatalf("open alerts: %+v", list.Items)
	}

	r = e.do(http.MethodGet, "/api/v1/alerts?status=resolved", admin, nil)
	r.json(t, &list)
	if len(list.Items) != 1 || list.Items[0].Key != "disk.full:/" || list.Items[0].ResolvedAt == nil {
		t.Fatalf("resolved alerts: %+v", list.Items)
	}
}
