package alert

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/webhook"
)

func TestRetrySchedule(t *testing.T) {
	if MaxAttempts != len(retryDelays)+1 {
		t.Fatalf("MaxAttempts = %d, want %d", MaxAttempts, len(retryDelays)+1)
	}
	now := time.Now()
	for n := 1; n < MaxAttempts; n++ {
		if _, ok := NextRetry(n, MaxAttempts, now); !ok {
			t.Errorf("NextRetry(%d) = false, want a retry", n)
		}
	}
	if _, ok := NextRetry(MaxAttempts, MaxAttempts, now); ok {
		t.Error("a retry after the last attempt")
	}
	if _, ok := NextRetry(1, 1, now); ok {
		t.Error("a one-attempt delivery (test message) was retried")
	}
}

func TestSummarizeSingle(t *testing.T) {
	msg := newMessage(DeliveryFired, []Alert{
		{Severity: SeverityCritical, Title: "Trunk down", Message: "SIP trunk unreachable", Link: "https://example/trunks/1"},
	}, uuid.Must(uuid.NewV7()), time.Now())
	title, body, link, severity, resolved := summarize(msg)
	if title != "Trunk down" || body != "SIP trunk unreachable" || link != "https://example/trunks/1" ||
		severity != SeverityCritical || resolved {
		t.Errorf("summarize() = %q %q %q %q %v", title, body, link, severity, resolved)
	}
}

func TestSummarizeDigest(t *testing.T) {
	msg := newMessage(DeliveryDigest, []Alert{
		{Severity: SeverityWarning, Title: "Disk almost full", Message: "80% used"},
		{Severity: SeverityCritical, Title: "Trunk down", Message: "unreachable", Link: "https://x/1"},
	}, uuid.Must(uuid.NewV7()), time.Now())
	title, body, link, severity, resolved := summarize(msg)
	if title != "2 alerts" || severity != SeverityCritical || resolved || link != "" {
		t.Errorf("summarize() = %q sev=%q resolved=%v link=%q", title, severity, resolved, link)
	}
	if !strings.Contains(body, "Trunk down") || !strings.Contains(body, "Disk almost full") || !strings.Contains(body, "https://x/1") {
		t.Errorf("digest body missing an item: %q", body)
	}
}

func TestSummarizeAllResolvedDigest(t *testing.T) {
	msg := message{Kind: DeliveryDigest, Items: []messageItem{
		{Title: "A", Body: "a", Severity: SeverityWarning, Resolved: true},
		{Title: "B", Body: "b", Severity: SeverityCritical, Resolved: true},
	}}
	_, _, _, _, resolved := summarize(msg)
	if !resolved {
		t.Error("a digest of only resolved items wasn't marked resolved")
	}
}

// fakeDoer records the last request and returns a canned response.
type fakeDoer struct {
	lastReq  *http.Request
	lastBody []byte
	status   int
	body     string
	err      error
}

func (f *fakeDoer) Do(r *http.Request) (*http.Response, error) {
	f.lastReq = r
	if r.Body != nil {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		f.lastBody = buf[:n]
	}
	if f.err != nil {
		return nil, f.err
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: http.NoBody, Header: make(http.Header)}, nil
}

func newTestSender(d Doer) *Sender {
	var key [dbsecret.KeySize]byte
	return &Sender{Client: d, Sealer: dbsecret.NewSealer(key), Now: time.Now}
}

func sealedConfig(t *testing.T, sealer *dbsecret.Sealer, channel uuid.UUID, cfg Config) []byte {
	t.Helper()
	plain, err := cfg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := sealer.Seal(sealID(channel), plain)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func TestSenderDispatchesByChannelKind(t *testing.T) {
	channel := uuid.Must(uuid.NewV7())
	d := &fakeDoer{}
	s := newTestSender(d)
	enc := sealedConfig(t, s.Sealer, channel, Config{Topic: "linx"})
	job := Job{
		Delivery:    Delivery{ID: uuid.Must(uuid.NewV7()), ChannelID: channel, Kind: DeliveryFired, MaxAttempts: 1},
		ChannelKind: KindNtfy,
		ConfigEnc:   enc,
		Alerts:      []Alert{{Severity: SeverityWarning, Title: "T", Message: "M"}},
	}
	a, ok := s.Send(t.Context(), job)
	if !ok || a.StatusCode == nil || *a.StatusCode != 200 {
		t.Fatalf("Send() = %+v, ok=%v", a, ok)
	}
	if !strings.Contains(d.lastReq.URL.String(), "/linx") {
		t.Errorf("request URL = %s, want the ntfy topic in it", d.lastReq.URL)
	}
}

func TestSenderUnknownChannelKind(t *testing.T) {
	channel := uuid.Must(uuid.NewV7())
	s := newTestSender(&fakeDoer{})
	enc := sealedConfig(t, s.Sealer, channel, Config{})
	job := Job{Delivery: Delivery{ID: uuid.Must(uuid.NewV7()), ChannelID: channel, MaxAttempts: 1},
		ChannelKind: "carrier-pigeon", ConfigEnc: enc}
	a, ok := s.Send(t.Context(), job)
	if ok || a.Error == nil {
		t.Fatalf("Send() with an unknown kind = %+v, ok=%v", a, ok)
	}
}

func TestSenderBadConfigNeverPanics(t *testing.T) {
	s := newTestSender(&fakeDoer{})
	job := Job{Delivery: Delivery{ID: uuid.Must(uuid.NewV7()), MaxAttempts: 1}, ChannelKind: KindNtfy, ConfigEnc: []byte("garbage")}
	a, ok := s.Send(t.Context(), job)
	if ok || a.Error == nil {
		t.Fatalf("Send() with unreadable config = %+v, ok=%v", a, ok)
	}
}

func TestSendNtfy(t *testing.T) {
	d := &fakeDoer{}
	msg := newMessage(DeliveryFired, []Alert{{Severity: SeverityCritical, Title: "Trunk down", Message: "unreachable", Link: "https://x/1"}}, uuid.Must(uuid.NewV7()), time.Now())
	resp, err := sendNtfy(t.Context(), d, Config{ServerURL: "https://ntfy.example/", Topic: "ops", AccessToken: "tok"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d.lastReq.URL.String() != "https://ntfy.example/ops" {
		t.Errorf("URL = %s", d.lastReq.URL)
	}
	if got := d.lastReq.Header.Get("Title"); got != "Trunk down" {
		t.Errorf("Title header = %q", got)
	}
	if got := d.lastReq.Header.Get("Priority"); got != "5" {
		t.Errorf("Priority header = %q, want 5 for critical", got)
	}
	if got := d.lastReq.Header.Get("Click"); got != "https://x/1" {
		t.Errorf("Click header = %q", got)
	}
	if got := d.lastReq.Header.Get("Authorization"); got != "Bearer tok" {
		t.Errorf("Authorization header = %q", got)
	}
	if string(d.lastBody) != "unreachable" {
		t.Errorf("body = %q", d.lastBody)
	}
}

func TestSendNtfyDefaultServer(t *testing.T) {
	d := &fakeDoer{}
	msg := newMessage(DeliveryFired, []Alert{{Title: "x", Message: "y", Severity: SeverityInfo}}, uuid.Must(uuid.NewV7()), time.Now())
	resp, err := sendNtfy(t.Context(), d, Config{Topic: "linx"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d.lastReq.URL.String() != defaultNtfyServer+"/linx" {
		t.Errorf("URL = %s", d.lastReq.URL)
	}
}

func TestSendGotify(t *testing.T) {
	d := &fakeDoer{}
	msg := newMessage(DeliveryFired, []Alert{{Title: "Disk full", Message: "90% used", Severity: SeverityCritical}}, uuid.Must(uuid.NewV7()), time.Now())
	resp, err := sendGotify(t.Context(), d, Config{ServerURL: "https://gotify.example", AppToken: "tok en"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !strings.HasPrefix(d.lastReq.URL.String(), "https://gotify.example/message?token=") {
		t.Errorf("URL = %s", d.lastReq.URL)
	}
	var body struct {
		Title    string
		Message  string
		Priority int
	}
	if err := json.Unmarshal(d.lastBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.Title != "Disk full" || body.Priority != 8 {
		t.Errorf("body = %+v", body)
	}
}

func TestSendGotifyMissingServer(t *testing.T) {
	if _, err := sendGotify(t.Context(), &fakeDoer{}, Config{AppToken: "t"}, message{}); err == nil {
		t.Error("sendGotify without a server succeeded")
	}
}

func TestSendSlack(t *testing.T) {
	d := &fakeDoer{}
	msg := newMessage(DeliveryFired, []Alert{{Title: "T", Message: "M", Severity: SeverityWarning}}, uuid.Must(uuid.NewV7()), time.Now())
	resp, err := sendSlack(t.Context(), d, Config{URL: "https://hooks.slack.com/services/x"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d.lastReq.URL.String() != "https://hooks.slack.com/services/x" {
		t.Errorf("URL = %s", d.lastReq.URL)
	}
	var body struct{ Text string }
	json.Unmarshal(d.lastBody, &body)
	if !strings.Contains(body.Text, "T") || !strings.Contains(body.Text, "M") {
		t.Errorf("text = %q", body.Text)
	}
}

func TestSendTeamsBuildsAdaptiveCard(t *testing.T) {
	d := &fakeDoer{}
	msg := newMessage(DeliveryFired, []Alert{{Title: "T", Message: "M", Severity: SeverityCritical, Link: "https://x"}}, uuid.Must(uuid.NewV7()), time.Now())
	resp, err := sendTeams(t.Context(), d, Config{URL: "https://example.webhook.office.com/x"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var body map[string]any
	if err := json.Unmarshal(d.lastBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "message" {
		t.Fatalf("body = %v", body)
	}
	attachments, ok := body["attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("attachments = %v", body["attachments"])
	}
}

func TestSendTelegram(t *testing.T) {
	d := &fakeDoer{}
	msg := newMessage(DeliveryFired, []Alert{{Title: "T", Message: "M", Severity: SeverityInfo}}, uuid.Must(uuid.NewV7()), time.Now())
	resp, err := sendTelegram(t.Context(), d, Config{BotToken: "123:ABC", ChatID: "-100"}, msg)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if d.lastReq.URL.String() != "https://api.telegram.org/bot123:ABC/sendMessage" {
		t.Errorf("URL = %s", d.lastReq.URL)
	}
	var body struct {
		ChatID string `json:"chat_id"`
		Text   string
	}
	json.Unmarshal(d.lastBody, &body)
	if body.ChatID != "-100" {
		t.Errorf("chat_id = %q", body.ChatID)
	}
}

func TestSendTelegramMissingFields(t *testing.T) {
	if _, err := sendTelegram(t.Context(), &fakeDoer{}, Config{BotToken: "t"}, message{}); err == nil {
		t.Error("sendTelegram without a chat id succeeded")
	}
}

func TestSendGenericWebhookSignsLikeWebhookPackage(t *testing.T) {
	d := &fakeDoer{}
	secret, err := webhook.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	did := uuid.Must(uuid.NewV7())
	at := time.Now()
	msg := newMessage(DeliveryFired, []Alert{{Severity: SeverityCritical, Title: "T", Message: "M", Link: "https://x"}}, uuid.Must(uuid.NewV7()), time.Now())
	msg.DeliveryID, msg.At = did, at

	resp, err := sendGenericWebhook(t.Context(), d, Config{URL: "https://example.com/hook", Secret: secret}, msg)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := d.lastReq.Header.Get("webhook-id"); got != did.String() {
		t.Errorf("webhook-id = %q", got)
	}
	wantTS := strconv.FormatInt(at.Unix(), 10)
	if got := d.lastReq.Header.Get("webhook-timestamp"); got != wantTS {
		t.Errorf("webhook-timestamp = %q, want %q", got, wantTS)
	}
	sig := d.lastReq.Header.Get("webhook-signature")
	if !webhook.Verify(secret, did.String(), at, d.lastBody, sig) {
		t.Errorf("signature %q doesn't verify against internal/webhook's own Verify", sig)
	}
	var body genericWebhookBody
	if err := json.Unmarshal(d.lastBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.Type != "alert.fired" || len(body.Data.Alerts) != 1 || body.Data.Alerts[0].Title != "T" {
		t.Errorf("body = %+v", body)
	}
}

func TestSendGenericWebhookMissingSecret(t *testing.T) {
	if _, err := sendGenericWebhook(t.Context(), &fakeDoer{}, Config{URL: "https://x"}, message{}); err == nil {
		t.Error("sendGenericWebhook without a secret succeeded")
	}
}

// A quick end-to-end check that Sender.Send actually reads the response
// body into the attempt log against a real HTTP server.
func TestSenderReadsResponseExcerpt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server exploded"))
	}))
	defer srv.Close()

	channel := uuid.Must(uuid.NewV7())
	s := newTestSender(srv.Client())
	enc := sealedConfig(t, s.Sealer, channel, Config{URL: srv.URL})
	job := Job{
		Delivery:    Delivery{ID: uuid.Must(uuid.NewV7()), ChannelID: channel, Kind: DeliveryFired, MaxAttempts: 3, Attempts: 0},
		ChannelKind: KindSlack, ConfigEnc: enc,
		Alerts: []Alert{{Title: "T", Message: "M", Severity: SeverityWarning}},
	}
	a, ok := s.Send(t.Context(), job)
	if ok || a.StatusCode == nil || *a.StatusCode != 500 || a.ResponseExcerpt == nil || *a.ResponseExcerpt != "server exploded" {
		t.Fatalf("Send() = %+v, ok=%v", a, ok)
	}
	o := outcome(job, a, ok)
	if o.Status != DeliveryPending || o.NextAttemptAt == nil {
		t.Errorf("outcome() = %+v, want pending with a retry", o)
	}
}
