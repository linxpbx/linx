package main

import (
	"bytes"
	"context"
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
	code := userCommand(t.Context(), st, accounts, &fakeAnnouncer{}, args, &out, &errb)
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

type fakeAnnouncer struct{ titles, messages []string }

func (f *fakeAnnouncer) Announce(_ context.Context, _ uuid.UUID, _, _, title, message, _ string) error {
	f.titles, f.messages = append(f.titles, title), append(f.messages, message)
	return nil
}

// The last system admin lost their authenticator and recovery codes:
// nobody can reset them in the browser, so the server does.
func TestUserReset2FA(t *testing.T) {
	st := newFakeStore()
	accounts := &auth.Accounts{Store: st, Now: time.Now}
	alerts := &fakeAnnouncer{}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := userCommand(t.Context(), st, accounts, alerts, args, &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, _, errOut := run("create", "--email", "owner@example.com", "--name", "Owner", "--role", "system_admin"); code != 0 {
		t.Fatalf("create: %q", errOut)
	}
	u, _ := st.UserByEmail(t.Context(), st.tenant, "owner@example.com")

	// No second step yet: nothing to reset, and it says what to do instead.
	code, out, _ := run("reset-2fa", "owner@example.com")
	if code != 0 || !strings.Contains(out, "no authenticator app or passkey to reset") {
		t.Fatalf("nothing to reset: code %d, %q", code, out)
	}
	if len(alerts.titles) != 0 {
		t.Fatal("alerted about nothing")
	}

	st.mu.Lock()
	withMFA := st.users[u.ID]
	withMFA.MFAEnabled, withMFA.MFASecretEnc, withMFA.RecoveryCodeHashes = true, []byte("sealed"), [][]byte{[]byte("h")}
	st.users[u.ID] = withMFA
	st.mu.Unlock()
	session := auth.UserSession{ID: uuid.Must(uuid.NewV7()), UserID: u.ID, TenantID: st.tenant, TokenHash: []byte("t"),
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), IdleExpiresAt: time.Now().Add(time.Hour)}
	if err := st.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := run("reset-2fa", " Owner@Example.com ")
	if code != 0 || !strings.Contains(out, "signed out everywhere") || !strings.Contains(out, "must set up a new passkey") {
		t.Fatalf("reset: code %d, %q %q", code, out, errOut)
	}
	got, _ := st.User(t.Context(), st.tenant, u.ID)
	if got.MFAEnabled || got.MFASecretEnc != nil || len(got.RecoveryCodeHashes) != 0 || got.PasswordHash != u.PasswordHash {
		t.Errorf("after reset: %+v", got)
	}
	if s, _ := st.SessionByTokenHash(t.Context(), []byte("t")); s.RevokedAt == nil {
		t.Error("their session wasn't ended")
	}
	if !slices.Contains(st.auditActions(), "user.mfa_reset") {
		t.Error("not audited")
	}
	if len(alerts.titles) != 1 || !strings.Contains(alerts.messages[0], "owner@example.com") {
		t.Errorf("alerts = %+v", alerts)
	}

	if code, _, errOut := run("reset-2fa", "nobody@example.com"); code != 1 || !strings.Contains(errOut, "no person") {
		t.Errorf("unknown person: code %d, %q", code, errOut)
	}
	if code, _, _ := run("reset-2fa"); code != 2 {
		t.Errorf("no email: code %d", code)
	}
}

// The web install's first admin (docs/INSTALL.md §5): the link's token is
// the one the install page already holds, and a second first admin is
// never made.
func TestUserCreateFirstAdmin(t *testing.T) {
	st := newFakeStore()
	accounts := &auth.Accounts{Store: st, Now: time.Now}
	token := auth.NewSecret()
	userStdin = strings.NewReader(token + "\n")
	t.Cleanup(func() { userStdin = nil })

	code, out, errOut := runUserCmd(t, st, accounts, "create", "--email", "owner@example.com", "--name", "Owner",
		"--role", "system_admin", "--first-admin", "--setup-token-stdin")
	if code != 0 || !strings.Contains(out, "/setup/"+token) {
		t.Fatalf("first admin: %d %q %q", code, out, errOut)
	}
	if link := mustLookupLink(t, st, token); link.UserID == uuid.Nil {
		t.Fatal("the given token isn't the stored link")
	}

	userStdin = strings.NewReader(auth.NewSecret() + "\n")
	code, _, errOut = runUserCmd(t, st, accounts, "create", "--email", "second@example.com", "--name", "Second",
		"--role", "system_admin", "--first-admin", "--setup-token-stdin")
	if code != exitFirstAdminExists || !strings.Contains(errOut, "already has a system admin") {
		t.Errorf("second first admin: %d %q", code, errOut)
	}
	if _, err := st.UserByEmail(t.Context(), st.tenant, "second@example.com"); err == nil {
		t.Error("a second first admin was created")
	}

	userStdin = strings.NewReader("not-a-token\n")
	code, _, errOut = runUserCmd(t, st, accounts, "create", "--email", "x@example.com", "--name", "X", "--role", "user", "--setup-token-stdin")
	if code == 0 || !strings.Contains(errOut, "isn't one Linx makes") {
		t.Errorf("bad token: %d %q", code, errOut)
	}
	if code, _, _ := runUserCmd(t, st, accounts, "create", "--email", "y@example.com", "--name", "Y", "--role", "admin", "--first-admin"); code != 2 {
		t.Errorf("--first-admin for another role: %d", code)
	}
}
