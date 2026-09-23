package alert

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/version"
)

// MaxAttempts and NextRetry: same retry schedule as webhooks (docs/API.md
// §5 "the same retries as webhooks"). Deliberately duplicated rather than
// exported from internal/webhook, to keep the two packages independent;
// internal/webhook_test and internal/alert_test each check their own
// schedule matches docs/API.md.
const MaxAttempts = 8

var retryDelays = []time.Duration{
	5 * time.Second, 5 * time.Minute, 30 * time.Minute,
	2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 10 * time.Hour,
}

// NextRetry returns when to try again after the attempts-th attempt
// failed, or false when there are no tries left.
func NextRetry(attempts, maxAttempts int, now time.Time) (time.Time, bool) {
	if attempts >= maxAttempts || attempts < 1 || attempts > len(retryDelays) {
		return time.Time{}, false
	}
	return now.Add(retryDelays[attempts-1]), true
}

const (
	excerptSize = 4 << 10
	drainSize   = 64 << 10
)

// Doer sends HTTP requests: the SSRF-guarded client (internal/safehttp) in
// production.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// message is what one channel send contains: one alert normally, or
// several combined into a quiet-hours digest.
type message struct {
	Kind       string // fired, reminder, resolved, test or digest (Delivery.Kind)
	DeliveryID uuid.UUID
	At         time.Time
	Items      []messageItem
}

type messageItem struct {
	Severity, Title, Body, Link string
	Resolved                    bool
}

func newMessage(kind string, alerts []Alert, deliveryID uuid.UUID, at time.Time) message {
	m := message{Kind: kind, DeliveryID: deliveryID, At: at}
	for _, a := range alerts {
		m.Items = append(m.Items, messageItem{
			Severity: a.Severity, Title: a.Title, Body: a.Message, Link: a.Link, Resolved: kind == DeliveryResolved,
		})
	}
	return m
}

// summarize turns msg into one title, body and link a channel can send as
// a single request: the alert itself when msg has one item, or a
// bulleted "N alerts" digest when a quiet-hours hold combined several
// (each line marked with its severity or RESOLVED). severity is the
// highest among the items (for priority/colour); resolved is true only
// when every item is a resolved notice.
func summarize(msg message) (title, body, link, severity string, resolved bool) {
	if len(msg.Items) == 1 {
		it := msg.Items[0]
		return it.Title, it.Body, it.Link, it.Severity, it.Resolved
	}
	resolved = true
	severity = SeverityInfo
	var b strings.Builder
	for i, it := range msg.Items {
		if i > 0 {
			b.WriteString("\n")
		}
		tag := strings.ToUpper(it.Severity)
		if it.Resolved {
			tag = "RESOLVED"
		} else {
			resolved = false
		}
		fmt.Fprintf(&b, "[%s] %s: %s", tag, it.Title, it.Body)
		if it.Link != "" {
			fmt.Fprintf(&b, " (%s)", it.Link)
		}
		if severityRank[it.Severity] > severityRank[severity] {
			severity = it.Severity
		}
	}
	return fmt.Sprintf("%d alerts", len(msg.Items)), b.String(), "", severity, resolved
}

// sendFunc posts msg through one channel kind and returns the raw HTTP
// response, or an error if no request could be sent at all (bad config,
// refused by the client before or during the request).
type sendFunc func(ctx context.Context, client Doer, cfg Config, msg message) (*http.Response, error)

var kindSenders = map[string]sendFunc{
	KindNtfy:     sendNtfy,
	KindGotify:   sendGotify,
	KindSlack:    sendSlack,
	KindTeams:    sendTeams,
	KindTelegram: sendTelegram,
	KindWebhook:  sendGenericWebhook,
}

// postJSON is the shared "build a POST request, send it, read the
// response" used by every sender but the generic webhook (which also
// signs).
func postJSON(ctx context.Context, client Doer, url string, headers map[string]string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, &configError{"That isn't a valid URL."}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Linx-Alerts/"+version.Version)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

// configError is a channel misconfiguration caught while building the
// request (never a network problem).
type configError struct{ detail string }

func (e *configError) Error() string { return e.detail }

// Sender makes one channel delivery attempt.
type Sender struct {
	Client Doer
	Sealer *dbsecret.Sealer
	Now    func() time.Time
}

// Send posts job's message through its channel and reports the attempt.
// It never returns an error: a failure is an attempt with Error set.
func (s *Sender) Send(ctx context.Context, job Job) (a Attempt, succeeded bool) {
	start := s.Now()
	a.At = start
	fail := func(msg string) (Attempt, bool) {
		a.DurationMS = int(s.Now().Sub(start).Milliseconds())
		a.Error = &msg
		return a, false
	}

	plain, err := s.Sealer.Open(sealID(job.ChannelID), job.ConfigEnc)
	if err != nil {
		return fail("Linx couldn't read this channel's settings.")
	}
	cfg, err := UnmarshalConfig(plain)
	if err != nil {
		return fail("Linx couldn't read this channel's settings.")
	}
	send, ok := kindSenders[job.ChannelKind]
	if !ok {
		return fail(fmt.Sprintf("%q isn't a channel kind Linx knows how to send to.", job.ChannelKind))
	}
	msg := newMessage(job.Kind, job.Alerts, job.ID, start)

	resp, err := send(ctx, s.Client, cfg, msg)
	if err != nil {
		var ce *configError
		if errors.As(err, &ce) {
			return fail(ce.detail)
		}
		return fail(safehttp.Describe(err))
	}
	defer resp.Body.Close()
	excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, excerptSize))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainSize))

	a.DurationMS = int(s.Now().Sub(start).Milliseconds())
	code := resp.StatusCode
	a.StatusCode = &code
	if len(excerpt) > 0 {
		text := cleanText(excerpt)
		a.ResponseExcerpt = &text
	}
	if code >= 200 && code < 300 {
		return a, true
	}
	msg2 := fmt.Sprintf("The receiver answered %d %s.", code, http.StatusText(code))
	a.Error = &msg2
	return a, false
}

// outcome turns an attempt on job into what's recorded.
func outcome(job Job, a Attempt, succeeded bool) Outcome {
	o := Outcome{DeliveryID: job.ID, ChannelID: job.ChannelID, TenantID: job.TenantID, Attempt: a,
		Attempts: job.Attempts + 1, Succeeded: succeeded}
	switch {
	case succeeded:
		o.Status = DeliverySucceeded
	default:
		if next, ok := NextRetry(o.Attempts, job.MaxAttempts, a.At); ok {
			o.Status = DeliveryPending
			o.NextAttemptAt = &next
		} else {
			o.Status = DeliveryFailed
		}
	}
	return o
}

// cleanText makes a response body safe to store as text.
func cleanText(b []byte) string {
	return strings.ReplaceAll(strings.ToValidUTF8(string(b), "�"), "\x00", "�")
}

// msgID gives one channel send a stable id for signing (used by the
// generic webhook sender): the delivery's own row id, a UUIDv7 string.
func msgID(deliveryID uuid.UUID) string { return deliveryID.String() }
