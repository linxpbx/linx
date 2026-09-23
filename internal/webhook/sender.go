package webhook

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/version"
)

// MaxAttempts is how many times a delivery is tried: once, then after each
// of retryDelays.
const MaxAttempts = 8

// retryDelays follow a failed attempt (≈ 27 h in all; docs/API.md §4).
var retryDelays = []time.Duration{
	5 * time.Second, 5 * time.Minute, 30 * time.Minute,
	2 * time.Hour, 5 * time.Hour, 10 * time.Hour, 10 * time.Hour,
}

// NextRetry returns when to try again after the attempts-th attempt
// failed, ±10 % so a receiver coming back isn't hit by every retry at once,
// or false when there are no tries left.
func NextRetry(attempts, maxAttempts int, now time.Time) (time.Time, bool) {
	if attempts >= maxAttempts || attempts < 1 || attempts > len(retryDelays) {
		return time.Time{}, false
	}
	d := retryDelays[attempts-1]
	jitter := time.Duration((rand.Float64()*0.2 - 0.1) * float64(d))
	return now.Add(d + jitter), true
}

// excerptSize is how much of a response body the delivery log keeps.
const excerptSize = 4 << 10

// drainSize bounds how much more of a response is read (and thrown away)
// so the connection can be reused.
const drainSize = 64 << 10

// Doer sends HTTP requests: the safehttp client in production.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Sender makes one signed delivery attempt.
type Sender struct {
	Client Doer
	Sealer *dbsecret.Sealer
	Now    func() time.Time
}

// Send posts job's body to its endpoint and reports the attempt. It never
// returns an error: a failure is an attempt with Error set.
func (s *Sender) Send(ctx context.Context, job Job) (a Attempt, succeeded, gone bool) {
	start := s.Now()
	a.At = start
	fail := func(msg string) (Attempt, bool, bool) {
		a.DurationMS = int(s.Now().Sub(start).Milliseconds())
		a.Error = &msg
		return a, false, false
	}

	secrets, err := s.secrets(job, start)
	if err != nil {
		// Never logs the secret; this means the key or row is broken.
		return fail("Linx couldn't read this endpoint's signing secret. Rotate the secret to fix it.")
	}
	msgID := job.EventID.String()
	sig, err := Sign(secrets, msgID, start, job.Body)
	if err != nil {
		return fail("Linx couldn't sign the message. Rotate the secret to fix it.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.URL, bytes.NewReader(job.Body))
	if err != nil {
		return fail("That isn't a valid URL.")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Linx-Webhooks/"+version.Version)
	req.Header.Set("webhook-id", msgID)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(start.Unix(), 10))
	req.Header.Set("webhook-signature", sig)

	resp, err := s.Client.Do(req)
	if err != nil {
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
	switch {
	case code >= 200 && code < 300:
		return a, true, false
	case code == http.StatusGone:
		msg := "The receiver answered 410 Gone, so this endpoint was turned off."
		a.Error = &msg
		return a, false, true
	case code >= 300 && code < 400:
		msg := fmt.Sprintf("The receiver answered %d (a redirect). Linx doesn't follow redirects; use the final URL.", code)
		a.Error = &msg
	default:
		msg := fmt.Sprintf("The receiver answered %d %s.", code, http.StatusText(code))
		a.Error = &msg
	}
	return a, false, false
}

// secrets opens the current secret, plus the previous one while it's still
// valid after a rotation.
func (s *Sender) secrets(job Job, now time.Time) ([]string, error) {
	cur, err := s.Sealer.Open(sealID(job.EndpointID), job.SecretEnc)
	if err != nil {
		return nil, err
	}
	out := []string{string(cur)}
	if len(job.PreviousSecretEnc) > 0 && job.PreviousSecretExpiresAt != nil && now.Before(*job.PreviousSecretExpiresAt) {
		prev, err := s.Sealer.Open(sealID(job.EndpointID), job.PreviousSecretEnc)
		if err != nil {
			return nil, err
		}
		out = append(out, string(prev))
	}
	return out, nil
}

// outcome turns an attempt on job into what's recorded.
func outcome(job Job, a Attempt, succeeded, gone bool) Outcome {
	o := Outcome{
		DeliveryID: job.ID, EndpointID: job.EndpointID, TenantID: job.TenantID, EventType: job.EventType,
		Attempt: a, Attempts: job.Attempts + 1, Succeeded: succeeded, Gone: gone,
	}
	switch {
	case succeeded:
		o.Status = StatusSucceeded
	case gone:
		o.Status = StatusFailed
	default:
		if next, ok := NextRetry(o.Attempts, job.MaxAttempts, a.At); ok {
			o.Status = StatusPending
			o.NextAttemptAt = &next
		} else {
			o.Status = StatusFailed
		}
	}
	return o
}

// cleanText makes a response body safe to store as text: valid UTF-8, no
// NUL bytes (Postgres text refuses them).
func cleanText(b []byte) string {
	return strings.ReplaceAll(strings.ToValidUTF8(string(b), "\uFFFD"), "\x00", "\uFFFD")
}
