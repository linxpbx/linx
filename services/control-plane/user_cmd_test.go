package main

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
)

var setupLinkRE = regexp.MustCompile(`/setup/([A-Za-z0-9_-]+)`)

func runUserCmd(t *testing.T, st *fakeStore, accounts *auth.Accounts, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := userCommand(t.Context(), st, accounts, args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUserCommand(t *testing.T) {
	st := newFakeStore()
	accounts := &auth.Accounts{Store: st, Now: time.Now}
	st.addExtension(pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: st.tenant, Number: "101", DisplayName: "Front desk"})

	code, out, errOut := runUserCmd(t, st, accounts, "create", "--email", "Jamie@Example.com", "--name", "Jamie Lee", "--role", "admin", "--extension", "101")
	if code != 0 {
		t.Fatalf("create: code %d, stderr %q", code, errOut)
	}
	m := setupLinkRE.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("create output has no setup link: %q", out)
	}
	token := m[1]
	u, err := st.UserByEmail(t.Context(), st.tenant, "jamie@example.com")
	if err != nil {
		t.Fatalf("stored person not found: %v", err)
	}
	if u.ExtensionID == nil {
		t.Fatal("extension wasn't attached")
	}
	if link := mustLookupLink(t, st, token); link.UserID != u.ID {
		t.Fatal("printed token doesn't match the stored link")
	}

	// Unknown extension.
	code, _, errOut = runUserCmd(t, st, accounts, "create", "--email", "other@example.com", "--name", "Other", "--role", "user", "--extension", "999")
	if code == 0 || !strings.Contains(errOut, "no extension") {
		t.Fatalf("expected an unknown-extension error, got code %d, stderr %q", code, errOut)
	}

	code, out, _ = runUserCmd(t, st, accounts, "list")
	if code != 0 || !strings.Contains(out, "jamie@example.com") || !strings.Contains(out, "admin") {
		t.Fatalf("list: code %d, %q", code, out)
	}

	code, out, errOut = runUserCmd(t, st, accounts, "setup-link", "jamie@example.com")
	if code != 0 || !strings.Contains(out, "/setup/") {
		t.Fatalf("setup-link: code %d, %q %q", code, out, errOut)
	}

	// Locked out: list says so, unlock clears it and says so.
	until := time.Now().Add(10 * time.Minute)
	st.mu.Lock()
	locked := st.users[u.ID]
	locked.FailedAttempts, locked.LockedUntil = 6, &until
	st.users[u.ID] = locked
	st.mu.Unlock()
	if _, out, _ = runUserCmd(t, st, accounts, "list"); !strings.Contains(out, "locked until") {
		t.Fatalf("list doesn't show the lockout: %q", out)
	}
	code, out, errOut = runUserCmd(t, st, accounts, "unlock", "Jamie@example.com")
	if code != 0 || !strings.Contains(out, "can sign in again now") {
		t.Fatalf("unlock: code %d, %q %q", code, out, errOut)
	}
	if got, _ := st.User(t.Context(), st.tenant, u.ID); got.LockedUntil != nil || got.FailedAttempts != 0 {
		t.Fatalf("still locked: %+v", got)
	}
	if !slices.Contains(st.auditActions(), "user.unlock") {
		t.Fatal("unlock wasn't audited")
	}
	if code, _, errOut = runUserCmd(t, st, accounts, "unlock", "nobody@example.com"); code == 0 || !strings.Contains(errOut, "no person") {
		t.Fatalf("unknown person: code %d, %q", code, errOut)
	}
	if code, _, _ = runUserCmd(t, st, accounts, "unlock"); code != 2 {
		t.Fatalf("no email: code %d", code)
	}
}

func mustLookupLink(t *testing.T, st *fakeStore, token string) auth.SetupLink {
	t.Helper()
	l, err := st.SetupLinkByTokenHash(t.Context(), auth.HashSecret(token))
	if err != nil {
		t.Fatalf("looking up setup link: %v", err)
	}
	return l
}
