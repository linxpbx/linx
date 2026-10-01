package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// System → Settings → Email (ADR-066): settings:read to see it; only a
// system admin changes or tests it; the password is never shown.
func TestEmailSetting(t *testing.T) {
	e := newTestEnv(t)
	_, sysAdmin := e.newCredential(auth.TypeAPIKey, auth.RoleSystemAdmin, "all")
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, reader := e.newCredential(auth.TypeAPIKey, auth.RoleSystemAdmin, "settings:read")

	r := e.do(http.MethodGet, "/api/v1/email", reader, nil)
	var got controlplaneapi.Email
	r.json(t, &got)
	if r.status != http.StatusOK || got.Enabled || got.Preset != "google" || got.PasswordSet || got.Status.Waiting != 0 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	body := map[string]any{"preset": "brevo", "username": "8a1b2c@smtp-brevo.com", "from_address": "pbx@example.com",
		"from_name": "Linx at Example Co", "password": "xsmtpsib-secret", "enabled": true}
	if r := e.patch("/api/v1/email", reader, "", body); r.status != http.StatusForbidden {
		t.Errorf("without settings:write: %d %s", r.status, r.body)
	}
	if r := e.patch("/api/v1/email", admin, "", body); r.status != http.StatusForbidden || !strings.Contains(string(r.body), "system_admin_only") {
		t.Errorf("an admin: %d %s", r.status, r.body)
	}
	r = e.patch("/api/v1/email", sysAdmin, got.Etag, body)
	r.json(t, &got)
	if r.status != http.StatusOK || !got.Enabled || !got.PasswordSet || got.Host != "smtp-relay.brevo.com" || got.Port != 587 ||
		got.Security != "starttls" || strings.Contains(string(r.body), "xsmtpsib") || r.header.Get("ETag") != got.Etag {
		t.Fatalf("%d %s", r.status, r.body)
	}
	for _, a := range e.email.Audits {
		if b, _ := json.Marshal(a.Detail); strings.Contains(string(b), "xsmtpsib") {
			t.Errorf("audit has the password: %s %s", a.Action, b)
		}
	}
	if r := e.patch("/api/v1/email", sysAdmin, `"0"`, map[string]any{"from_name": "Linx"}); r.status != http.StatusPreconditionFailed {
		t.Errorf("stale etag: %d %s", r.status, r.body)
	}
	if r := e.patch("/api/v1/email", sysAdmin, "", map[string]any{"security": "none"}); r.status != http.StatusBadRequest && r.status != http.StatusUnprocessableEntity {
		t.Errorf("plain: %d %s", r.status, r.body)
	}
	// The test goes to the caller's own address: an API key has none.
	if r := e.do(http.MethodPost, "/api/v1/email/test", sysAdmin, nil); r.status != http.StatusForbidden || !strings.Contains(string(r.body), "people_only") {
		t.Errorf("test with an API key: %d %s", r.status, r.body)
	}
}

// People → Add with "Send by email" (docs/ui/SCREENS_PHASE1F.md §5.2): the
// link is made and shown either way; the email is queued when email is on,
// and said not to be when it's off.
func TestInviteByEmail(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleSystemAdmin, "users:write")
	add := func(addr string) controlplaneapi.UserCreated {
		t.Helper()
		r := e.do(http.MethodPost, "/api/v1/users", admin, map[string]any{"email": addr, "name": "Sara Haddad", "role": "user", "send_email": true})
		var out controlplaneapi.UserCreated
		r.json(t, &out)
		if r.status != http.StatusCreated || out.SetupLinkToken == "" || out.Email == nil {
			t.Fatalf("%d %s", r.status, r.body)
		}
		return out
	}
	if out := add("sara@example.com"); out.Email.Queued || out.Email.Error == nil || !strings.Contains(*out.Email.Error, "Set up email first") {
		t.Fatalf("email off: %+v", out.Email)
	}
	c := email.Defaults(e.store.tenant)
	c.Enabled, c.Host, c.FromAddress, c.PasswordEnc = true, "smtp.example.com", "pbx@example.com", []byte{1}
	e.email.Put(c)
	out := add("chen@example.com")
	if !out.Email.Queued || out.Email.To != "chen@example.com" || len(e.email.Queued) != 1 || e.email.Queued[0].Kind != email.KindInvite ||
		e.email.Queued[0].To[0] != "chen@example.com" {
		t.Fatalf("email on: %+v %+v", out.Email, e.email.Queued)
	}
	// A new link, emailed too.
	r := e.do(http.MethodPost, "/api/v1/users/"+out.User.Id.String()+"/setup-link", admin, map[string]any{"send_email": true})
	var link controlplaneapi.SetupLinkIssued
	r.json(t, &link)
	if r.status != http.StatusOK || link.Email == nil || !link.Email.Queued || len(e.email.Queued) != 2 {
		t.Fatalf("new link: %d %s", r.status, r.body)
	}
}
