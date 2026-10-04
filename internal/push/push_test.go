package push_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/push"
)

// Waking a sleeping phone, end to end, against a stand-in Apple inside the
// test process (docs/PHASE2.md §5). Nothing here reaches a network.

// apple records what it was sent and answers as Apple's push service does.
type apple struct {
	*httptest.Server
	mu       sync.Mutex
	requests []request
	status   int
	reason   string
}

type request struct {
	Path     string
	Topic    string
	PushType string
	Priority string
	Expires  string
	Bearer   string
	Body     map[string]any
}

func newApple(t *testing.T) *apple {
	t.Helper()
	a := &apple{status: http.StatusOK}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		a.requests = append(a.requests, request{
			Path: r.URL.Path, Topic: r.Header.Get("apns-topic"), PushType: r.Header.Get("apns-push-type"),
			Priority: r.Header.Get("apns-priority"), Expires: r.Header.Get("apns-expiration"),
			Bearer: r.Header.Get("authorization"), Body: body,
		})
		status, reason := a.status, a.reason
		a.mu.Unlock()
		if status == http.StatusOK {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"reason":"` + reason + `"}`))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	a.Server = server
	return a
}

func (a *apple) refuse(status int, reason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.reason = status, reason
}

func (a *apple) sent() []request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]request(nil), a.requests...)
}

// store is the database, in memory.
type store struct {
	mu       sync.Mutex
	settings push.Settings
	keyEnc   []byte
	devices  []push.Device
	forgot   []string
	saved    []string
}

func (s *store) PushSettings(context.Context) (push.Settings, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings, s.keyEnc, nil
}

func (s *store) SavePushSettings(_ context.Context, in push.Settings, keyEnc []byte, _ int, _ auth.AuditEntry) (push.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.Version = s.settings.Version + 1
	s.settings, s.keyEnc = in, keyEnc
	return in, nil
}

func (s *store) WakeDevices(_ context.Context, names []string) ([]push.Device, error) {
	out := []push.Device{}
	for _, d := range s.devices {
		for _, name := range names {
			if d.SIPUsername == name && (d.VoIPToken != "" || (d.CallAlerts && d.AlertToken != "")) {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func (s *store) AlertDevices(context.Context, uuid.UUID) ([]push.Device, error) {
	out := []push.Device{}
	for _, d := range s.devices {
		if d.AlertToken != "" {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *store) ForgetPushToken(_ context.Context, device uuid.UUID, kind string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgot = append(s.forgot, device.String()+":"+kind)
	return nil
}

func (s *store) SavePushTokens(_ context.Context, device uuid.UUID, t push.Tokens, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, strings.Join([]string{
		device.String(), t.VoIP, t.Alert, t.Environment, strconv.FormatBool(t.CallAlerts)}, "|"))
	return nil
}

// testKey is a .p8 of our own: Apple's are ordinary P-256 keys in PKCS#8.
func testKey(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

type env struct {
	gateway *push.Gateway
	store   *store
	apple   *apple
	tenant  uuid.UUID
	now     time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	var sealKey [32]byte
	if _, err := rand.Read(sealKey[:]); err != nil {
		t.Fatal(err)
	}
	a := newApple(t)
	host, err := url.Parse(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{store: &store{}, apple: a, tenant: uuid.New(),
		now: time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)}
	sealer := dbsecret.NewSealer(sealKey)
	sealed, err := sealer.Seal("push_settings:"+e.tenant.String(), testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	e.store.settings = push.Settings{
		Tenant: e.tenant, Enabled: true, TeamID: "ABCDE12345", KeyID: "KEY1234567",
		BundleID: "com.linxpbx.app", Environment: push.Production, WaitMS: push.DefaultWaitMS,
		HasKey: true, Version: 1,
	}
	e.store.keyEnc = sealed
	e.gateway = &push.Gateway{Store: e.store, Sealer: sealer, Now: func() time.Time { return e.now },
		HTTP: a.Client(), Host: host.Host}
	return e
}

func (e *env) phone(voip, alert string) push.Device {
	d := push.Device{DeviceID: uuid.New(), Tenant: e.tenant, UserID: uuid.New(),
		SIPUsername: "d_ab12cd34", VoIPToken: voip, AlertToken: alert}
	e.store.devices = append(e.store.devices, d)
	return d
}

func TestWakeSendsOneVoIPPush(t *testing.T) {
	e := newEnv(t)
	d := e.phone("aabbccdd", "")
	e.gateway.Wake(context.Background(), []string{d.SIPUsername}, push.Call{
		ID: "1759500000.7", From: "+971500000001", At: e.now})

	sent := e.apple.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d pushes, want 1", len(sent))
	}
	got := sent[0]
	if got.Path != "/3/device/aabbccdd" {
		t.Errorf("path = %q", got.Path)
	}
	// A wake push is always a ringing call, on the app's VoIP topic, right
	// now or not at all (docs/PHASE2.md §14 item 1).
	if got.Topic != "com.linxpbx.app.voip" || got.PushType != "voip" {
		t.Errorf("topic %q, type %q", got.Topic, got.PushType)
	}
	if got.Priority != "10" || got.Expires != "0" {
		t.Errorf("priority %q, expiration %q", got.Priority, got.Expires)
	}
	linx, _ := got.Body["linx"].(map[string]any)
	if linx["call"] != "1759500000.7" || linx["from"] != "+971500000001" {
		t.Errorf("payload = %v", got.Body)
	}
	// Nothing else is ever told to Apple: no name, no token, no extension.
	if len(linx) != 3 {
		t.Errorf("the wake push carries more than the call, the caller and the time: %v", linx)
	}
	// The provider token is a signed JWT with the key's id on it.
	header, _, ok := strings.Cut(strings.TrimPrefix(got.Bearer, "bearer "), ".")
	if !ok {
		t.Fatalf("authorization = %q", got.Bearer)
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		t.Fatal(err)
	}
	var head map[string]string
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	if head["alg"] != "ES256" || head["kid"] != "KEY1234567" {
		t.Errorf("provider token header = %v", head)
	}
}

func TestACallWhereCallKitMayNotBeUsed(t *testing.T) {
	e := newEnv(t)
	// A phone in mainland China: no VoIP token at all, a notification
	// token, and the flag that says why (ADR-078).
	d := e.phone("", "eeff0011")
	d.CallAlerts = true
	e.store.devices[len(e.store.devices)-1] = d
	e.gateway.Wake(context.Background(), []string{d.SIPUsername}, push.Call{
		ID: "1759500000.9", From: "+971500000002", At: e.now})

	sent := e.apple.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d pushes, want 1", len(sent))
	}
	got := sent[0]
	// Never a VoIP push: Apple kills an app that takes one without ringing
	// a call, and this app can't ring one (docs/PHASE2.md §14 item 1).
	if got.Topic != "com.linxpbx.app" || got.PushType != "alert" {
		t.Errorf("topic %q, type %q", got.Topic, got.PushType)
	}
	if got.Path != "/3/device/eeff0011" {
		t.Errorf("path = %q", got.Path)
	}
	// It is worth delivering only while the caller is still there.
	if got.Priority != "10" || got.Expires == "0" {
		t.Errorf("priority %q, expiration %q", got.Priority, got.Expires)
	}
	aps, _ := got.Body["aps"].(map[string]any)
	if aps["interruption-level"] != "time-sensitive" {
		t.Errorf("aps = %v", aps)
	}
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "Incoming call" || alert["body"] != "+971500000002" {
		t.Errorf("what the lock screen shows = %v", alert)
	}
	linx, _ := got.Body["linx"].(map[string]any)
	if linx["call"] != "1759500000.9" || linx["from"] != "+971500000002" || linx["kind"] != "call" {
		t.Errorf("payload = %v", got.Body)
	}
	// The same three facts, and the kind that says which notification it
	// is. Nothing else reaches Apple.
	if len(linx) != 4 {
		t.Errorf("the call notification carries more than the call, the caller and the time: %v", linx)
	}
	if s := e.gateway.Snapshot(); s.Sent["call"] != 1 || s.Sent["wake"] != 0 {
		t.Errorf("counted as %v", s.Sent)
	}
}

func TestWakeTimesTheArrival(t *testing.T) {
	e := newEnv(t)
	d := e.phone("aabbccdd", "")
	e.gateway.Wake(context.Background(), []string{d.SIPUsername}, push.Call{ID: "c1", From: "101", At: e.now})
	e.now = e.now.Add(1200 * time.Millisecond)
	e.gateway.Registered(d.SIPUsername)

	s := e.gateway.Snapshot()
	if s.Woke != 1 || s.SlowestWake < 1.1 || s.SlowestWake > 1.3 {
		t.Errorf("woke %d phones in %.2fs", s.Woke, s.SlowestWake)
	}
	// A phone signing in on its own, with no push behind it, isn't timed.
	e.gateway.Registered(d.SIPUsername)
	if again := e.gateway.Snapshot(); again.Woke != 1 {
		t.Errorf("woke counted twice: %d", again.Woke)
	}
}

func TestDeadTokenIsForgotten(t *testing.T) {
	e := newEnv(t)
	d := e.phone("aabbccdd", "")
	e.apple.refuse(http.StatusGone, "Unregistered")
	e.gateway.Wake(context.Background(), []string{d.SIPUsername}, push.Call{ID: "c1", From: "101", At: e.now})

	if len(e.store.forgot) != 1 || e.store.forgot[0] != d.DeviceID.String()+":voip" {
		t.Errorf("forgot = %v", e.store.forgot)
	}
	if s := e.gateway.Snapshot(); s.Dead != 1 || s.Sent[push.KindWake] != 0 {
		t.Errorf("stats = %+v", s)
	}
}

func TestAppleRefusingIsNotFatal(t *testing.T) {
	e := newEnv(t)
	d := e.phone("aabbccdd", "")
	e.apple.refuse(http.StatusInternalServerError, "InternalServerError")
	e.gateway.Wake(context.Background(), []string{d.SIPUsername}, push.Call{ID: "c1", From: "101", At: e.now})

	if len(e.store.forgot) != 0 {
		t.Errorf("a token was forgotten over a passing failure: %v", e.store.forgot)
	}
	if s := e.gateway.Snapshot(); s.Failed[push.KindWake] != 1 {
		t.Errorf("stats = %+v", s)
	}
}

func TestOneWakePushPerCallPerPhone(t *testing.T) {
	e := newEnv(t)
	d := e.phone("aabbccdd", "")
	for range push.WakesPerMinute + 5 {
		e.gateway.Wake(context.Background(), []string{d.SIPUsername}, push.Call{ID: "c", From: "101", At: e.now})
	}
	if sent := len(e.apple.sent()); sent != push.WakesPerMinute {
		t.Errorf("sent %d pushes, want the limit of %d", sent, push.WakesPerMinute)
	}
}

func TestQuietNotifications(t *testing.T) {
	e := newEnv(t)
	e.phone("aabbccdd", "eeff0011")
	e.gateway.Missed(context.Background(), uuid.New(), "+971500000002")
	e.gateway.NewVoicemail(context.Background(), uuid.New(), "")

	sent := e.apple.sent()
	if len(sent) != 2 {
		t.Fatalf("sent %d notifications, want 2", len(sent))
	}
	// An ordinary notification, on the app's own topic — never a VoIP push,
	// which Apple only allows for a ringing call.
	for _, got := range sent {
		if got.Topic != "com.linxpbx.app" || got.PushType != "alert" {
			t.Errorf("topic %q, type %q", got.Topic, got.PushType)
		}
		if got.Path != "/3/device/eeff0011" {
			t.Errorf("a notification went to the wrong token: %s", got.Path)
		}
	}
	first, _ := sent[0].Body["aps"].(map[string]any)
	alert, _ := first["alert"].(map[string]any)
	if alert["title"] != "Missed call" || alert["body"] != "+971500000002" {
		t.Errorf("missed call said %v", alert)
	}
	second, _ := sent[1].Body["aps"].(map[string]any)
	alert2, _ := second["alert"].(map[string]any)
	if alert2["title"] != "New voicemail" || alert2["body"] != "Number withheld" {
		t.Errorf("voicemail said %v", alert2)
	}
}

func TestNothingIsSentWhilePushIsOff(t *testing.T) {
	e := newEnv(t)
	e.store.settings.Enabled = false
	d := e.phone("aabbccdd", "eeff0011")
	e.gateway.Wake(context.Background(), []string{d.SIPUsername}, push.Call{ID: "c1", From: "101", At: e.now})
	e.gateway.Missed(context.Background(), uuid.New(), "101")
	if sent := e.apple.sent(); len(sent) != 0 {
		t.Errorf("sent %d pushes with the gateway off", len(sent))
	}
}

func TestSaveNeedsASystemAdminAndAKey(t *testing.T) {
	e := newEnv(t)
	e.store.settings = push.Settings{Tenant: e.tenant, Environment: push.Production, WaitMS: push.DefaultWaitMS}
	e.store.keyEnc = nil

	ordinary := auth.WithPrincipal(context.Background(),
		auth.Principal{Type: auth.TypeUser, ID: uuid.New().String(), TenantID: e.tenant, Role: auth.RoleAdmin})
	if _, err := e.gateway.Save(ordinary, push.Input{Enabled: true}, ""); err == nil {
		t.Error("an ordinary admin saved the Apple key")
	}

	ctx := systemAdmin(e.tenant, e.now)
	if _, err := e.gateway.Save(ctx, push.Input{Enabled: true, TeamID: "ABCDE12345", KeyID: "KEY1234567",
		BundleID: "com.linxpbx.app", Environment: push.Production, WaitMS: 5000}, ""); err == nil {
		t.Error("push was turned on with no key at all")
	}

	saved, err := e.gateway.Save(ctx, push.Input{Enabled: true, TeamID: "abcde12345", KeyID: "key1234567",
		BundleID: "com.linxpbx.app", Environment: push.Production, WaitMS: 5000, Key: testKey(t)}, "")
	if err != nil {
		t.Fatalf("saving with a key: %v", err)
	}
	if !saved.HasKey || saved.TeamID != "ABCDE12345" || saved.WaitMS != 5000 {
		t.Errorf("saved = %+v", saved)
	}
	// The key is sealed, never kept as it was typed.
	if strings.Contains(string(e.store.keyEnc), "PRIVATE KEY") {
		t.Error("the Apple key was stored in the clear")
	}
	// Something that isn't a key at all is refused with words, not a crash.
	if _, err := e.gateway.Save(ctx, push.Input{Enabled: true, TeamID: "ABCDE12345", KeyID: "KEY1234567",
		BundleID: "com.linxpbx.app", Environment: push.Production, Key: []byte("hello")}, ""); err == nil {
		t.Error("a file that isn't a key was accepted")
	}
}

func TestSaveRefusesAStaleEtag(t *testing.T) {
	e := newEnv(t)
	ctx := systemAdmin(e.tenant, e.now)
	if _, err := e.gateway.Save(ctx, push.Input{WaitMS: 1000, Environment: push.Production}, `"7"`); err == nil {
		t.Error("a stale If-Match was accepted")
	}
}

func TestSaveTokensChecksWhatThePhoneSends(t *testing.T) {
	e := newEnv(t)
	device := uuid.New()
	if err := e.gateway.SaveTokens(context.Background(), device,
		push.Tokens{VoIP: "AABBCC", Environment: push.Sandbox}); err != nil {
		t.Fatalf("saving a token: %v", err)
	}
	if len(e.store.saved) != 1 || !strings.Contains(e.store.saved[0], "|aabbcc||sandbox|false") {
		t.Errorf("saved = %v", e.store.saved)
	}
	if err := e.gateway.SaveTokens(context.Background(), device,
		push.Tokens{VoIP: "not-a-token", Environment: push.Production}); err == nil {
		t.Error("a token that isn't hex was accepted")
	}
	if err := e.gateway.SaveTokens(context.Background(), device,
		push.Tokens{VoIP: "aabb", Environment: "elsewhere"}); err == nil {
		t.Error("an unknown push service was accepted")
	}
	// A phone that may not use CallKit sends a notification token and the
	// flag, and never a VoIP token with it (ADR-078).
	if err := e.gateway.SaveTokens(context.Background(), device,
		push.Tokens{VoIP: "aabb", Alert: "ccdd", Environment: push.Production, CallAlerts: true}); err == nil {
		t.Error("a VoIP token was accepted from a phone that can't use CallKit")
	}
	if err := e.gateway.SaveTokens(context.Background(), device,
		push.Tokens{Alert: "CCDD", Environment: push.Production, CallAlerts: true}); err != nil {
		t.Fatalf("saving a China phone's tokens: %v", err)
	}
	if last := e.store.saved[len(e.store.saved)-1]; !strings.Contains(last, "||ccdd|production|true") {
		t.Errorf("saved = %v", last)
	}
}

func TestParseKeyRefusesWhatIsntOne(t *testing.T) {
	if _, err := push.ParseKey("ABCDE12345", "KEY1234567", []byte("-----BEGIN PRIVATE KEY-----\nnope\n-----END PRIVATE KEY-----")); err == nil {
		t.Error("nonsense PEM was accepted")
	}
	var dead *push.Dead
	if errors.As(error(&push.Dead{Reason: "Unregistered"}), &dead); dead.Reason != "Unregistered" {
		t.Errorf("dead token error = %v", dead)
	}
}

// systemAdmin is a context the way the API middleware builds one. There is
// no session on it, so "confirm it's you" passes as it does for the command
// line (internal/auth RequireConfirmed).
func systemAdmin(tenant uuid.UUID, _ time.Time) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{
		Type: auth.TypeUser, ID: uuid.New().String(), TenantID: tenant, Role: auth.RoleSystemAdmin,
		Scopes: auth.Scopes,
	})
}
