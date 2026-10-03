package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/siprelay"
)

// The app's way in to the /sip relay (docs/PHASE2.md §4, Phase 2 step 4): a
// set-up iPhone or iPad with its device token instead of a browser's cookie,
// and the same checks after that.

// testDevices is auth.DeviceAuthenticator over the fake store; the control
// plane's own is enroll.Service.DevicePrincipal.
type testDevices struct{ store *fakePbxStore }

func (d testDevices) DevicePrincipal(ctx context.Context, device uuid.UUID, now time.Time) (auth.Principal, error) {
	tenant, user, err := d.store.DevicePrincipalFor(ctx, device, now)
	if err != nil {
		return auth.Principal{}, auth.ErrNotFound
	}
	id := device
	return auth.Principal{Type: auth.TypeUser, ID: user.String(), TenantID: tenant, Role: auth.RoleUser,
		Scopes: auth.DeviceScopes(), DeviceID: &id}, nil
}

// newPhone is a phone that has been set up: its ios device, and its token.
func (e *testEnv) newPhone(extension, user uuid.UUID) (pbx.Device, string) {
	e.t.Helper()
	now := time.Now().UTC()
	username := pbx.NewSIPUsername()
	d := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: e.store.tenant, ExtensionID: extension,
		Name: "Rana's iPhone", Kind: pbx.KindIOS, SIPUsername: username,
		DigestHash: pbx.DigestHash(username, "a-password-the-app-asks-for"), Enabled: true,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := e.pbxStore.CreateDevice(e.t.Context(), d, auth.AuditEntry{}); err != nil {
		e.t.Fatal(err)
	}
	e.pbxStore.phones[d.ID] = user
	token, _, err := e.tokens.IssueDevice(d.ID, e.store.tenant, auth.DeviceScopes(), now)
	if err != nil {
		e.t.Fatal(err)
	}
	return d, token
}

// dialSIPAsPhone opens the relay the way the app does: a device token in the
// Authorization header, and no Origin, because the app is not a web page.
func (e *testEnv) dialSIPAsPhone(token string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+"/sip",
		&websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{"sip"}})
}

func TestPhoneSIPRelay(t *testing.T) {
	env := newTestEnv(t)
	var got chan string
	env.asterisk, got = fakeAsteriskWS(t)
	env.authn.Devices = testDevices{env.pbxStore}
	ext := env.newExtension("104")
	uid, _, _ := signedInPerson(t, env, "phone@example.com", &ext.ID)
	phone, token := env.newPhone(ext.ID, uid)

	// Nothing without a device token (with no token at all it is a browser
	// that isn't on Linx's own page), and nothing with a made-up one.
	if _, resp, _ := env.dialSIPAsPhone(""); resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("no token: %+v", resp)
	}
	if _, resp, _ := env.dialSIPAsPhone("not.a.token"); resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a made-up token: %+v", resp)
	}

	// The app's line: REGISTER goes through to Asterisk and the answer
	// comes back, with no Origin anywhere.
	c, _, err := env.dialSIPAsPhone(token)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	if err := c.Write(t.Context(), websocket.MessageText, []byte(register(phone.SIPUsername))); err != nil {
		t.Fatal(err)
	}
	if m := <-got; !strings.HasPrefix(m, "REGISTER ") {
		t.Fatalf("Asterisk got %q", m)
	}
	if _, b, err := c.Read(t.Context()); err != nil || !strings.HasPrefix(string(b), "SIP/2.0 200") {
		t.Fatalf("%q %v", b, err)
	}

	// It is held to its own phone line, exactly as a browser is.
	if err := c.Write(t.Context(), websocket.MessageText, []byte(register("d_someone_else"))); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	var ce websocket.CloseError
	if !errors.As(err, &ce) || ce.Reason != siprelay.ReasonNotYourLine {
		t.Fatalf("someone else's username: %v", err)
	}

	// Stopping the phone ("I've lost it") drops its line there and then,
	// and it can't come back.
	c2, _, err := env.dialSIPAsPhone(token)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.CloseNow()
	if err := c2.Write(t.Context(), websocket.MessageText, []byte(register(phone.SIPUsername))); err != nil {
		t.Fatal(err)
	}
	<-got
	if _, _, err := c2.Read(ctx); err != nil {
		t.Fatalf("the answer to the second REGISTER: %v", err)
	}
	if err := env.pbx.RevokeDevice(auth.WithPrincipal(t.Context(), auth.SystemPrincipal(env.store.tenant)), phone.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c2.Read(ctx); !errors.As(err, &ce) {
		t.Fatalf("after the phone was stopped: %v", err)
	}
	if _, resp, _ := env.dialSIPAsPhone(token); resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a stopped phone, with a token that hasn't expired yet: %+v", resp)
	}
}

func TestPhoneLineCheck(t *testing.T) {
	env := newTestEnv(t)
	sst := testSIPStore{fakeStore: env.store, fakePbxStore: env.pbxStore}
	ext := env.newExtension("105")
	uid, _, _ := signedInPerson(t, env, "check@example.com", &ext.ID)
	phone, _ := env.newPhone(ext.ID, uid)
	line := siprelay.Line{TenantID: env.store.tenant, UserID: uid, DeviceID: phone.ID, Username: phone.SIPUsername}

	if err := checkLine(t.Context(), sst, line, time.Now()); err != nil {
		t.Fatalf("a phone that is still set up: %v", err)
	}
	// The username this connection has been using must still be the
	// phone's own.
	other := line
	other.Username = "d_someone_else"
	if err := checkLine(t.Context(), sst, other, time.Now()); err == nil {
		t.Error("the check passed for another phone line")
	}
	// Expired, disabled, revoked, or its person gone: all the same answer.
	delete(env.pbxStore.phones, phone.ID)
	if err := checkLine(t.Context(), sst, line, time.Now()); err == nil {
		t.Error("the check passed for a phone that is no longer set up")
	}
}
