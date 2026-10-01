package alert

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
)

func TestValidateEmailChannel(t *testing.T) {
	if msg := ValidateFor(KindEmail, Config{To: "sara@example.com, Ops@Example.com"}); msg != "" {
		t.Error(msg)
	}
	for _, to := range []string{"", "sara", "sara@example.com\r\nBcc: x@example.com", strings.Repeat("a@example.com,", 11)} {
		if ValidateFor(KindEmail, Config{To: to}) == "" {
			t.Errorf("accepted %q", to)
		}
	}
	if ValidateFor(KindEmail, Config{To: "a@example.com", URL: "https://x.example.com"}) == "" {
		t.Error("accepted another kind's field")
	}
}

type fakeQueue struct {
	tenant uuid.UUID
	to     []string
	c      email.Content
	err    error
}

func (q *fakeQueue) Enqueue(_ context.Context, tenant uuid.UUID, _ string, to []string, c email.Content, _ auth.AuditEntry) (uuid.UUID, error) {
	q.tenant, q.to, q.c = tenant, to, c
	return uuid.Nil, q.err
}

func TestSendEmailChannel(t *testing.T) {
	channel, tenant := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	s := newTestSender(&fakeDoer{})
	job := Job{
		Delivery:    Delivery{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ChannelID: channel, Kind: DeliveryFired, MaxAttempts: 1},
		ChannelKind: KindEmail,
		ConfigEnc:   sealedConfig(t, s.Sealer, channel, Config{To: "ops@example.com, sara@example.com"}),
		Alerts:      []Alert{{Severity: SeverityWarning, Title: `Line "UCM" is down`, Message: "It stopped answering.", Link: "/admin/lines"}},
	}
	if a, ok := s.Send(t.Context(), job); ok || a.Error == nil || !strings.Contains(*a.Error, "Set up email first") {
		t.Fatalf("no email: %+v", a)
	}
	q := &fakeQueue{}
	s.Email, s.WebAddress = q, "https://pbx.example.com"
	if _, ok := s.Send(t.Context(), job); !ok {
		t.Fatal("not queued")
	}
	if q.tenant != tenant || len(q.to) != 2 || q.c.Subject != `[Linx] Line "UCM" is down` ||
		!strings.Contains(q.c.Text, "It stopped answering.\n\nhttps://pbx.example.com/admin/lines") {
		t.Fatalf("%+v", q)
	}
	q.err = email.ErrOff
	if a, ok := s.Send(t.Context(), job); ok || *a.Error != email.ErrOff.Detail {
		t.Fatalf("email off: %+v", a)
	}
}

// notifyStore records which channels a notification went to.
type notifyStore struct {
	Store
	channels []Channel
	due      []uuid.UUID
}

func (n *notifyStore) EnabledChannels(context.Context, uuid.UUID) ([]Channel, error) {
	return n.channels, nil
}

func (n *notifyStore) Notify(_ context.Context, _ Alert, _ string, due, _ []uuid.UUID, _ time.Time, _ int, _ *WebhookEvent) error {
	n.due = due
	return nil
}

func TestEmailBrokenNeverByEmail(t *testing.T) {
	mail, ntfy := Channel{ID: uuid.Must(uuid.NewV7()), Kind: KindEmail, MinSeverity: SeverityInfo},
		Channel{ID: uuid.Must(uuid.NewV7()), Kind: KindNtfy, MinSeverity: SeverityInfo}
	st := &notifyStore{channels: []Channel{mail, ntfy}}
	e := &Engine{Store: st}
	e.notify(t.Context(), Alert{Key: EmailBrokenKey, Severity: SeverityWarning}, DeliveryFired, time.Now())
	if len(st.due) != 1 || st.due[0] != ntfy.ID {
		t.Fatalf("email broken went to %v", st.due)
	}
	e.notify(t.Context(), Alert{Key: "trunk.down:x", Severity: SeverityWarning}, DeliveryFired, time.Now())
	if len(st.due) != 2 {
		t.Fatalf("another alert went to %v", st.due)
	}
}
