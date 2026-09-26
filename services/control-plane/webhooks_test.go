package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/webhook"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

func (e *testEnv) patch(path, bearer, ifMatch string, body any) response {
	e.t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPatch, e.srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	return e.send(req)
}

func (e *testEnv) createWebhook(key string, body map[string]any) controlplaneapi.WebhookWithSecret {
	e.t.Helper()
	r := e.do(http.MethodPost, "/api/v1/webhooks", key, body)
	if r.status != http.StatusCreated {
		e.t.Fatalf("create webhook: %d %s", r.status, r.body)
	}
	var out controlplaneapi.WebhookWithSecret
	r.json(e.t, &out)
	return out
}

func TestWebhookEndpoints(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, reporter := e.newCredential(auth.TypeAPIKey, auth.RoleReporter, "all")

	var created controlplaneapi.WebhookWithSecret
	t.Run("create returns the secret once", func(t *testing.T) {
		created = e.createWebhook(admin, map[string]any{
			"url": "https://hooks.example.com/linx", "description": "CRM",
			"event_types": []string{"call.missed", "call.missed", "trunk.status_changed"},
		})
		if !strings.HasPrefix(created.Secret, webhook.SecretPrefix) {
			t.Fatalf("secret = %q", created.Secret)
		}
		w := created.Webhook
		if !w.Enabled || w.Etag != `"1"` || strings.Join(w.EventTypes, ",") != "call.missed,trunk.status_changed" {
			t.Fatalf("unexpected webhook %+v", w)
		}
		r := e.do(http.MethodGet, "/api/v1/webhooks/"+w.Id.String(), admin, nil)
		if r.status != http.StatusOK || bytes.Contains(r.body, []byte("whsec_")) {
			t.Fatalf("get: %d %s", r.status, r.body)
		}
		if r.header.Get("ETag") != `"1"` {
			t.Fatalf("ETag header = %q", r.header.Get("ETag"))
		}
		if !strings.Contains(strings.Join(e.whStore.auditActions(), ","), "webhook.create") {
			t.Fatal("no audit entry")
		}
	})

	t.Run("scopes", func(t *testing.T) {
		if r := e.do(http.MethodGet, "/api/v1/webhooks", reporter, nil); r.status != http.StatusOK {
			t.Fatalf("reporter list: %d %s", r.status, r.body)
		}
		r := e.do(http.MethodPost, "/api/v1/webhooks", reporter, map[string]any{"url": "https://hooks.example.com/x"})
		if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
			t.Fatalf("reporter create: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodPost, "/api/v1/webhooks/"+created.Webhook.Id.String()+"/test", reporter, nil); r.status != http.StatusForbidden {
			t.Fatalf("reporter test: %d", r.status)
		}
	})

	t.Run("bad input is refused", func(t *testing.T) {
		for _, tc := range []struct {
			body map[string]any
			code string
		}{
			{map[string]any{"url": "http://hooks.example.com/x"}, "url_invalid"},
			{map[string]any{"url": "https://user:pw@hooks.example.com/x"}, "url_invalid"},
			{map[string]any{"url": "https://10.0.0.5/x"}, "url_blocked"},
			{map[string]any{"url": "https://nas.home.arpa/x"}, "url_blocked"},
			{map[string]any{"url": "https://169.254.169.254/latest"}, "url_blocked"},
			{map[string]any{"url": "https://127.0.0.1/x"}, "url_blocked"},
			{map[string]any{"url": "https://[::ffff:127.0.0.1]/x"}, "url_blocked"},
			{map[string]any{"url": "https://hooks.example.com/x", "event_types": []string{"nope"}}, "event_type_unknown"},
			{map[string]any{"url": "https://hooks.example.com/x", "secret": "mine"}, "request_invalid"},
		} {
			r := e.do(http.MethodPost, "/api/v1/webhooks", admin, tc.body)
			if r.status < 400 || r.status >= 500 || r.problemCode(t) != tc.code {
				t.Errorf("%v: got %d %s, want %s", tc.body, r.status, r.body, tc.code)
			}
		}
	})

	t.Run("patch with If-Match", func(t *testing.T) {
		path := "/api/v1/webhooks/" + created.Webhook.Id.String()
		r := e.patch(path, admin, `"99"`, map[string]any{"description": "x"})
		if r.status != http.StatusPreconditionFailed || r.problemCode(t) != "etag_mismatch" {
			t.Fatalf("stale If-Match: %d %s", r.status, r.body)
		}
		r = e.patch(path, admin, `"1"`, map[string]any{"description": "Sales CRM", "enabled": false})
		if r.status != http.StatusOK {
			t.Fatalf("patch: %d %s", r.status, r.body)
		}
		var w controlplaneapi.Webhook
		r.json(t, &w)
		if w.Description != "Sales CRM" || w.Enabled || w.DisabledReason == nil || *w.DisabledReason != "admin" || w.Etag != `"2"` {
			t.Fatalf("after patch: %+v", w)
		}
		if w.Url != "https://hooks.example.com/linx" || len(w.EventTypes) != 2 {
			t.Fatal("fields not in the patch changed")
		}
		r = e.patch(path, admin, "", map[string]any{"enabled": true})
		var on controlplaneapi.Webhook
		r.json(t, &on)
		if r.status != http.StatusOK || !on.Enabled || on.DisabledReason != nil {
			t.Fatalf("re-enable: %d %s", r.status, r.body)
		}
		if r := e.patch(path, admin, "", map[string]any{"url": "https://10.1.1.1/"}); r.problemCode(t) != "url_blocked" {
			t.Fatalf("patch to private url: %d %s", r.status, r.body)
		}
	})

	t.Run("rotate secret keeps the old one for a day", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/webhooks/"+created.Webhook.Id.String()+"/rotate-secret", admin, nil)
		if r.status != http.StatusOK {
			t.Fatalf("rotate: %d %s", r.status, r.body)
		}
		var out controlplaneapi.WebhookWithSecret
		r.json(t, &out)
		if out.Secret == created.Secret || !strings.HasPrefix(out.Secret, webhook.SecretPrefix) {
			t.Fatal("rotation didn't issue a new secret")
		}
		exp := out.Webhook.PreviousSecretExpiresAt
		if exp == nil || time.Until(*exp) < 23*time.Hour {
			t.Fatalf("previous_secret_expires_at = %v", exp)
		}
	})

	t.Run("delete", func(t *testing.T) {
		w := e.createWebhook(admin, map[string]any{"url": "https://hooks.example.com/tmp"}).Webhook
		if r := e.do(http.MethodDelete, "/api/v1/webhooks/"+w.Id.String(), admin, nil); r.status != http.StatusNoContent {
			t.Fatalf("delete: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodGet, "/api/v1/webhooks/"+w.Id.String(), admin, nil); r.status != http.StatusNotFound {
			t.Fatalf("get after delete: %d", r.status)
		}
	})
}

func TestWebhookTestDelivery(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	created := e.createWebhook(admin, map[string]any{"url": "https://hooks.example.com/linx"})

	var got struct {
		headers http.Header
		body    []byte
	}
	status := http.StatusOK
	recv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.headers = r.Header.Clone()
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		io.WriteString(w, "thanks\x00\xff")
	}))
	defer recv.Close()
	// The receiver is on loopback, which the SSRF guard never allows; the
	// guard is tested in internal/safehttp. Point the endpoint there directly.
	e.webhooks.Sender.Client = recv.Client()
	e.whStore.mu.Lock()
	ep := e.whStore.endpoints[created.Webhook.Id]
	ep.URL = recv.URL + "/hook"
	e.whStore.endpoints[ep.ID] = ep
	e.whStore.mu.Unlock()

	r := e.do(http.MethodPost, "/api/v1/webhooks/"+created.Webhook.Id.String()+"/test", admin, nil)
	if r.status != http.StatusOK {
		t.Fatalf("test: %d %s", r.status, r.body)
	}
	var d controlplaneapi.WebhookDelivery
	r.json(t, &d)
	if d.Status != "succeeded" || d.Log == nil || len(*d.Log) != 1 || *(*d.Log)[0].StatusCode != 200 {
		t.Fatalf("delivery = %s", r.body)
	}
	if ex := (*d.Log)[0].ResponseExcerpt; ex == nil || !strings.HasPrefix(*ex, "thanks") || strings.ContainsRune(*ex, 0) {
		t.Fatalf("excerpt = %v", ex)
	}

	// What a receiver checks, per Standard Webhooks.
	id, tsHeader, sig := got.headers.Get("webhook-id"), got.headers.Get("webhook-timestamp"), got.headers.Get("webhook-signature")
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil || id != d.EventId.String() {
		t.Fatalf("headers id=%q ts=%q", id, tsHeader)
	}
	if !webhook.Verify(created.Secret, id, time.Unix(ts, 0), got.body, sig) {
		t.Fatalf("signature %q doesn't verify with the endpoint's secret", sig)
	}
	var msg struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(got.body, &msg); err != nil || msg.Type != webhook.TestEventType || msg.Data["webhook_id"] != created.Webhook.Id.String() {
		t.Fatalf("body = %s", got.body)
	}
	if got.headers.Get("Content-Type") != "application/json" {
		t.Fatalf("Content-Type = %q", got.headers.Get("Content-Type"))
	}

	// The delivery log shows it; a failed test is tried once and says why.
	status = http.StatusInternalServerError
	r = e.do(http.MethodPost, "/api/v1/webhooks/"+created.Webhook.Id.String()+"/test", admin, nil)
	r.json(t, &d)
	if d.Status != "failed" || d.Attempts != 1 || (*d.Log)[0].Error == nil || !strings.Contains(*(*d.Log)[0].Error, "500") {
		t.Fatalf("failed test = %s", r.body)
	}
	r = e.do(http.MethodGet, "/api/v1/webhooks/"+created.Webhook.Id.String()+"/deliveries?status=failed", admin, nil)
	var list controlplaneapi.WebhookDeliveryList
	r.json(t, &list)
	if r.status != http.StatusOK || len(list.Items) != 1 || list.Items[0].Id != d.Id || list.Items[0].Log != nil {
		t.Fatalf("deliveries: %d %s", r.status, r.body)
	}
	// Test messages aren't replayed.
	if r := e.do(http.MethodPost, "/api/v1/webhook-deliveries/"+d.Id.String()+"/replay", admin, nil); r.problemCode(t) != "delivery_is_test" {
		t.Fatalf("replay test: %d %s", r.status, r.body)
	}
}

func TestOutboundAllowlist(t *testing.T) {
	e := newTestEnv(t)
	_, all := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all", "outbound_allowlist:write")

	t.Run(`"all" doesn't include outbound_allowlist:write`, func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/outbound-allowlist", all, map[string]any{"value": "192.168.1.0/24"})
		if r.status != http.StatusForbidden {
			t.Fatalf("got %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodGet, "/api/v1/outbound-allowlist", all, nil); r.status != http.StatusOK {
			t.Fatalf("list: %d", r.status)
		}
	})

	t.Run("refused values", func(t *testing.T) {
		for _, v := range []string{"8.8.8.0/24", "0.0.0.0/0", "127.0.0.1", "169.254.169.254", "172.0.0.0/8", "::/0", "not a host!", "-bad.example"} {
			r := e.do(http.MethodPost, "/api/v1/outbound-allowlist", admin, map[string]any{"value": v})
			if r.status != http.StatusUnprocessableEntity {
				t.Errorf("%q: got %d %s, want 422", v, r.status, r.body)
			}
		}
	})

	t.Run("allowing a host lets a webhook use it", func(t *testing.T) {
		if r := e.do(http.MethodPost, "/api/v1/webhooks", all, map[string]any{"url": "https://nas.home.arpa/hook"}); r.problemCode(t) != "url_blocked" {
			t.Fatalf("before: %d %s", r.status, r.body)
		}
		r := e.do(http.MethodPost, "/api/v1/outbound-allowlist", admin, map[string]any{"value": "NAS.home.arpa.", "description": "Synology"})
		if r.status != http.StatusCreated {
			t.Fatalf("create: %d %s", r.status, r.body)
		}
		var entry controlplaneapi.AllowlistEntry
		r.json(t, &entry)
		if entry.Value != "nas.home.arpa" || entry.Kind != "host" {
			t.Fatalf("entry = %+v", entry)
		}
		if r := e.do(http.MethodPost, "/api/v1/outbound-allowlist", admin, map[string]any{"value": "nas.home.arpa"}); r.status != http.StatusConflict {
			t.Fatalf("duplicate: %d %s", r.status, r.body)
		}
		e.createWebhook(all, map[string]any{"url": "https://nas.home.arpa/hook"})

		if r := e.do(http.MethodDelete, "/api/v1/outbound-allowlist/"+entry.Id.String(), admin, nil); r.status != http.StatusNoContent {
			t.Fatalf("delete: %d %s", r.status, r.body)
		}
		if r := e.do(http.MethodPost, "/api/v1/webhooks", all, map[string]any{"url": "https://nas.home.arpa/hook"}); r.problemCode(t) != "url_blocked" {
			t.Fatalf("after delete: %d %s", r.status, r.body)
		}
	})

	t.Run("a single address becomes a /32", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/outbound-allowlist", admin, map[string]any{"value": "10.0.0.5"})
		var entry controlplaneapi.AllowlistEntry
		r.json(t, &entry)
		if r.status != http.StatusCreated || entry.Value != "10.0.0.5/32" || entry.Kind != "cidr" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
		e.createWebhook(all, map[string]any{"url": "https://10.0.0.5:8443/hook"})
	})
}
