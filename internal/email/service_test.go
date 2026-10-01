package email

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
)

// memStore is email.Store in memory.
type memStore struct {
	mu     sync.Mutex
	cfg    *Config
	outbox []Message
	audits []auth.AuditEntry
}

func (m *memStore) EmailSettings(_ context.Context, _ uuid.UUID) (Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg == nil {
		return Config{}, auth.ErrNotFound
	}
	return *m.cfg, nil
}

func (m *memStore) SaveEmailSettings(_ context.Context, c Config, version int, a auth.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (m.cfg == nil && version != 0) || (m.cfg != nil && m.cfg.Version != version) {
		return auth.ErrVersionChanged
	}
	m.cfg = &c
	m.audits = append(m.audits, a)
	return nil
}

func (m *memStore) EnqueueEmail(_ context.Context, msg Message, a auth.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outbox = append(m.outbox, msg)
	m.audits = append(m.audits, a)
	return nil
}

func (m *memStore) ClaimEmails(_ context.Context, now, _ time.Time, limit int) ([]Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Message
	for _, msg := range m.outbox {
		if msg.Status == StatusPending && !msg.NextAttemptAt.After(now) && len(out) < limit && m.cfg != nil && m.cfg.Enabled {
			out = append(out, msg)
		}
	}
	return out, nil
}

func (m *memStore) FinishEmail(_ context.Context, msg Message, status, lastError string, next *time.Time, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.outbox {
		if m.outbox[i].ID == msg.ID {
			o := &m.outbox[i]
			o.Attempts++
			o.Status, o.LastError, o.NextAttemptAt = status, lastError, next
			if status != StatusPending {
				o.ContentEnc = nil
			}
			if status == StatusSent {
				o.SentAt = &now
			}
		}
	}
	return nil
}

func (m *memStore) NextEmailDue(context.Context) (*time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var next *time.Time
	for _, msg := range m.outbox {
		if msg.Status == StatusPending && (next == nil || msg.NextAttemptAt.Before(*next)) {
			next = msg.NextAttemptAt
		}
	}
	return next, nil
}

func (m *memStore) EmailStatus(context.Context, uuid.UUID, time.Time) (Status, error) {
	return Status{}, nil
}

func (m *memStore) CleanupEmails(context.Context, time.Time) error { return nil }

func (m *memStore) Audit(_ context.Context, a auth.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audits = append(m.audits, a)
	return nil
}

func (m *memStore) message(id uuid.UUID) Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.outbox {
		if msg.ID == id {
			return msg
		}
	}
	return Message{}
}

var testTenant = uuid.MustParse("01990000-0000-7000-8000-000000000001")

func newTestService(t *testing.T, f *fakeSMTP) (*Service, *memStore) {
	t.Helper()
	st := &memStore{}
	var key [dbsecret.KeySize]byte
	key[0] = 7
	return &Service{Store: st, Sealer: dbsecret.NewSealer(key), Sender: f.sender(), Now: time.Now}, st
}

func asRole(role string, confirmed bool) context.Context {
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{Type: auth.TypeUser,
		ID: "01990000-0000-7000-8000-0000000000aa", TenantID: testTenant, Role: role})
	sess := auth.UserSession{}
	if confirmed {
		sess.ConfirmedAt = ptr(time.Now())
	}
	return auth.WithSession(ctx, sess)
}

func apiCode(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func ptr[T any](v T) *T { return &v }

func TestUpdate(t *testing.T) {
	f := newFakeSMTP(t, true)
	s, st := newTestService(t, f)
	google := Patch{Preset: ptr("google"), FromAddress: ptr("PBX@example.com"), FromName: ptr("Linx at Example Co"),
		Password: ptr("abcd efgh ijkl mnop"), Enabled: ptr(true)}

	if _, err := s.Update(asRole(auth.RoleAdmin, true), google, ""); apiCode(err) != "system_admin_only" {
		t.Errorf("an admin: %v", err)
	}
	if _, err := s.Update(asRole(auth.RoleSystemAdmin, false), google, ""); err == nil {
		t.Error("saved without confirm it's you")
	}
	ctx := asRole(auth.RoleSystemAdmin, true)
	c, err := s.Update(ctx, google, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Host != "smtp.gmail.com" || c.Port != 465 || c.Security != SecurityTLS || c.FromAddress != "pbx@example.com" ||
		c.Username != "pbx@example.com" || !c.Enabled || c.Version != 1 {
		t.Fatalf("%+v", c)
	}
	pw, err := s.Sealer.Open(SealID(testTenant), c.PasswordEnc)
	if err != nil || string(pw) != "abcdefghijklmnop" {
		t.Fatalf("password %q %v", pw, err)
	}
	last := st.audits[len(st.audits)-1]
	if b, _ := json.Marshal(last.Detail); strings.Contains(string(b), "abcd") || last.Action != "email.update" {
		t.Errorf("audit: %s %s", last.Action, b)
	}

	// A new server needs the password again; a wrong ETag is refused.
	if _, err := s.Update(ctx, Patch{Preset: ptr("fastmail")}, ""); apiCode(err) != "password_required" {
		t.Errorf("moved without the password: %v", err)
	}
	if _, err := s.Update(ctx, Patch{FromName: ptr("Linx")}, `"9"`); apiCode(err) != "etag_mismatch" {
		t.Errorf("stale etag: %v", err)
	}
	// Arrived, then a password change asks for a new test.
	c, err = s.Update(ctx, Patch{Arrived: ptr(true)}, "")
	if err != nil || c.ArrivedAt == nil {
		t.Fatalf("arrived: %+v %v", c, err)
	}
	c, err = s.Update(ctx, Patch{Password: ptr("new-password")}, "")
	if err != nil || c.ArrivedAt != nil {
		t.Fatalf("new password: %+v %v", c, err)
	}

	for _, bad := range []struct {
		p    Patch
		code string
	}{
		{Patch{Preset: ptr("other"), Host: ptr("mail.example.com"), Port: ptr(25), Password: ptr("x")}, "port_invalid"},
		{Patch{Security: ptr("none"), Password: ptr("x")}, "security_invalid"},
		{Patch{FromAddress: ptr("pbx@example.com\r\nBcc: x@example.com"), Password: ptr("x")}, "from_address_invalid"},
		{Patch{FromName: ptr("Linx\nBcc: x")}, "from_name_invalid"},
		{Patch{HourlyLimit: ptr(5000)}, "limit_invalid"},
		{Patch{Preset: ptr("aol")}, "preset_invalid"},
	} {
		if _, err := s.Update(ctx, bad.p, ""); apiCode(err) != bad.code {
			t.Errorf("%+v: %v, want %s", bad.p, err, bad.code)
		}
	}
}

func TestTestEmail(t *testing.T) {
	f := newFakeSMTP(t, false)
	s, st := newTestService(t, f)
	ctx := asRole(auth.RoleSystemAdmin, true)
	if _, err := s.Update(ctx, Patch{Preset: ptr("other"), Host: ptr("mail.test"), Port: ptr(587), Security: ptr(SecuritySTARTTLS),
		Username: ptr(f.user), FromAddress: ptr("pbx@example.com"), Password: ptr(f.pass)}, ""); err != nil {
		t.Fatal(err)
	}
	r, err := s.Test(ctx, "me@example.com")
	if err != nil || r.Err != nil || len(r.Passed) != len(Stages) {
		t.Fatalf("%+v %v", r, err)
	}
	if got := f.mails(); len(got) != 1 || got[0].To[0] != "me@example.com" {
		t.Fatalf("%+v", got)
	}
	if a := st.audits[len(st.audits)-1]; a.Action != "email.test" || a.Result != auth.ResultOK {
		t.Errorf("audit %+v", a)
	}

	// The wrong password: connected, encrypted, certificate checked, then
	// stopped at signing in.
	if _, err := s.Update(ctx, Patch{Password: ptr("wrong")}, ""); err != nil {
		t.Fatal(err)
	}
	r, err = s.Test(ctx, "me@example.com")
	if err != nil || r.Err == nil || r.Err.Stage != StageSignIn || len(r.Passed) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	for i := 0; i < TestsPerMinute; i++ {
		_, err = s.Test(ctx, "me@example.com")
	}
	if apiCode(err) != "rate_limited" {
		t.Errorf("tests a minute: %v", err)
	}
	if _, err := s.Test(asRole(auth.RoleAdmin, true), "me@example.com"); apiCode(err) != "system_admin_only" {
		t.Errorf("an admin's test: %v", err)
	}
}

func TestQueue(t *testing.T) {
	f := newFakeSMTP(t, true)
	s, st := newTestService(t, f)
	sys := auth.AuditEntry{Actor: "system", Action: "email.queue", Result: auth.ResultOK}
	invite := Content{Subject: "Your Linx account at Example Co", Text: "Set up your account: https://pbx.example.com/setup/secret-token"}

	if _, err := s.Enqueue(context.Background(), testTenant, KindInvite, []string{"sara@example.com"}, invite, sys); !errors.Is(err, ErrOff) {
		t.Fatalf("while off: %v", err)
	}
	ctx := asRole(auth.RoleSystemAdmin, true)
	if _, err := s.Update(ctx, Patch{Preset: ptr("fastmail"), FromAddress: ptr(f.user), Password: ptr(f.pass), Enabled: ptr(true)}, ""); err != nil {
		t.Fatal(err)
	}
	s.Store.(*memStore).cfg.Host = "mail.test" // the fake answers for every address; its certificate is for mail.test
	var broken, working []string
	s.Broken = func(_ context.Context, _ uuid.UUID, d string) { broken = append(broken, d) }
	s.Working = func(context.Context, uuid.UUID) { working = append(working, "ok") }

	id, err := s.Enqueue(context.Background(), testTenant, KindInvite, []string{"sara@example.com"}, invite, sys)
	if err != nil {
		t.Fatal(err)
	}
	// Sealed in the queue: the link isn't readable there.
	if m := st.message(id); strings.Contains(string(m.ContentEnc), "secret-token") || len(m.ContentEnc) == 0 {
		t.Fatalf("content not sealed: %q", m.ContentEnc)
	}
	if _, err := s.sendDue(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	m := st.message(id)
	if m.Status != StatusSent || m.ContentEnc != nil || len(f.mails()) != 1 || !strings.Contains(f.mails()[0].Data, "secret-token") || len(working) != 1 {
		t.Fatalf("sent: %+v, %d mails", m, len(f.mails()))
	}

	// A wrong password: given up on at once (trying again won't help), and
	// the account is broken.
	f.pass = "changed-at-fastmail"
	id, _ = s.Enqueue(context.Background(), testTenant, KindInvite, []string{"sara@example.com"}, invite, sys)
	_, _ = s.sendDue(context.Background(), time.Now())
	if m := st.message(id); m.Status != StatusFailed || m.ContentEnc != nil || !strings.Contains(m.LastError, "user name and password") || len(broken) != 1 {
		t.Fatalf("wrong password: %+v %v", m, broken)
	}

	// A busy server: tried 3 times, a minute then 15 minutes apart.
	f.pass = "app-password"
	f.refuseRcpt = "451 4.3.0 Try again later"
	id, _ = s.Enqueue(context.Background(), testTenant, KindInvite, []string{"sara@example.com"}, invite, sys)
	at := time.Now()
	for i, gap := range []time.Duration{time.Minute, 15 * time.Minute} {
		s.Now = func() time.Time { return at }
		_, _ = s.sendDue(context.Background(), at)
		m := st.message(id)
		if m.Status != StatusPending || m.Attempts != i+1 || m.NextAttemptAt.Sub(at) != gap {
			t.Fatalf("try %d: %+v", i+1, m)
		}
		at = *m.NextAttemptAt
	}
	s.Now = func() time.Time { return at }
	_, _ = s.sendDue(context.Background(), at)
	if m := st.message(id); m.Status != StatusFailed || m.Attempts != MaxAttempts || len(broken) != 2 {
		t.Fatalf("last try: %+v %v", m, broken)
	}
	// A refused address alone isn't the account being broken.
	f.refuseRcpt = "550 5.1.1 No such user"
	_, _ = s.Enqueue(context.Background(), testTenant, KindInvite, []string{"nobody@example.com"}, invite, sys)
	_, _ = s.sendDue(context.Background(), at)
	if len(broken) != 2 {
		t.Errorf("one refused address counted as broken: %v", broken)
	}
	_ = http.StatusOK
}

func TestRunWakesOnEnqueue(t *testing.T) {
	f := newFakeSMTP(t, true)
	s, _ := newTestService(t, f)
	ctx := asRole(auth.RoleSystemAdmin, true)
	if _, err := s.Update(ctx, Patch{Preset: ptr("fastmail"), FromAddress: ptr(f.user), Password: ptr(f.pass), Enabled: ptr(true)}, ""); err != nil {
		t.Fatal(err)
	}
	s.Store.(*memStore).cfg.Host = "mail.test"
	run, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(run); close(done) }()
	defer func() { stop(); <-done }()
	if _, err := s.Enqueue(context.Background(), testTenant, KindTest, []string{"a@example.com"}, TestContent(),
		auth.AuditEntry{Actor: "system", Action: "email.queue", Result: auth.ResultOK}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(f.mails()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(f.mails()) != 1 {
		t.Fatal("the worker didn't send a queued email")
	}
}
