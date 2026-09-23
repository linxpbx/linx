package main

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/auth"
)

var keyRE = regexp.MustCompile(`linx_[a-z2-7]{12}_[A-Za-z0-9_-]{43}`)

func runKeyCmd(t *testing.T, st *fakeStore, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := apiKeyCommand(t.Context(), st, args, &out, &errb, time.Now())
	return code, out.String(), errb.String()
}

func TestAPIKeyCommand(t *testing.T) {
	st := newFakeStore()

	code, out, errOut := runKeyCmd(t, st, "create", "--name", "My laptop", "--role", "admin", "--scopes", "all,api_keys:write", "--allow-ip", "203.0.113.0/24")
	if code != 0 {
		t.Fatalf("create: code %d, stderr %q", code, errOut)
	}
	key := keyRE.FindString(out)
	if key == "" || !strings.Contains(out, "won't be shown again") {
		t.Fatalf("create output has no key: %q", out)
	}
	publicID, secret, _ := auth.ParseAPIKey(key)
	c, err := st.CredentialByPublicID(t.Context(), auth.TypeAPIKey, publicID)
	if err != nil || !auth.SecretMatches(c.SecretHash, secret) {
		t.Fatalf("stored key doesn't match printed key: %v", err)
	}
	if c.CreatedBy != "system:cli" || !slices.Contains(c.Scopes, "api_keys:write") || len(c.AllowedIPs) != 1 {
		t.Fatalf("unexpected stored key %+v", c)
	}
	if !slices.Contains(st.auditActions(), "api_key.create") {
		t.Fatal("create not audited")
	}

	code, out, _ = runKeyCmd(t, st, "list")
	if code != 0 || !strings.Contains(out, "linx_"+publicID+"...") || strings.Contains(out, secret) {
		t.Fatalf("list: code %d, %q", code, out)
	}

	code, out, errOut = runKeyCmd(t, st, "revoke", "linx_"+publicID+"...")
	if code != 0 || !strings.Contains(out, "Revoked") {
		t.Fatalf("revoke: code %d, %q %q", code, out, errOut)
	}
	c, _ = st.CredentialByPublicID(t.Context(), auth.TypeAPIKey, publicID)
	if c.RevokedAt == nil {
		t.Fatal("not revoked")
	}
	if _, out, _ = runKeyCmd(t, st, "list"); !strings.Contains(out, "revoked") {
		t.Fatalf("list after revoke: %q", out)
	}

	for name, args := range map[string][]string{
		"no role":        {"create", "--name", "x"},
		"no name":        {"create", "--role", "admin"},
		"bad scope":      {"create", "--name", "x", "--role", "admin", "--scopes", "root"},
		"unquoted name":  {"create", "--name", "My", "laptop", "--role", "admin"},
		"zero days":      {"create", "--name", "x", "--role", "admin", "--expires-in-days", "0"},
		"too many days":  {"create", "--name", "x", "--role", "admin", "--expires-in-days", "1000"},
		"revoke nothing": {"revoke"},
		"revoke junk":    {"revoke", "!!"},
		"unknown":        {"rotate"},
	} {
		if code, _, errOut := runKeyCmd(t, st, args...); code != 2 || errOut == "" {
			t.Errorf("%s: code %d, stderr %q; want 2 with a message", name, code, errOut)
		}
	}
	if code, _, _ := runKeyCmd(t, st, "revoke", "aaaaaaaaaaaa"); code != 1 {
		t.Errorf("revoking an unknown key: code %d, want 1", code)
	}
}
