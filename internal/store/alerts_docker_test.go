package store

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/dbsecret"
)

// TestAlertsDocker runs the channel, alert and delivery queries against
// real Postgres. It needs Docker: make test-docker.
func TestAlertsDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-alerts-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}

	var key [dbsecret.KeySize]byte
	rand.Read(key[:])
	sealer := dbsecret.NewSealer(key)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "test", Result: auth.ResultOK}

	newChannel := func(t *testing.T, enabled bool, qh *alert.QuietHours) alert.Channel {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		cfg, err := alert.Config{Topic: "x"}.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		enc, err := sealer.Seal("alert_channel:"+id.String(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		c := alert.Channel{ID: id, TenantID: tenant, Kind: alert.KindNtfy, Name: "test", ConfigEnc: enc,
			MinSeverity: alert.SeverityInfo, QuietHours: qh, Enabled: enabled, Version: 1,
			CreatedBy: "system", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateChannel(ctx, c, audit); err != nil {
			t.Fatal(err)
		}
		return c
	}

	t.Run("Fire upserts, justOpened only on a fresh open", func(t *testing.T) {
		key := "test.fire:" + uuid.Must(uuid.NewV7()).String()
		// Postgres keeps microseconds; Linux clocks give nanoseconds (macOS
		// only microseconds, which hid this locally). Compare like with like.
		now := time.Now().Truncate(time.Microsecond)
		a1, justOpened, err := s.Fire(ctx, tenant, key, alert.SeverityWarning, "T1", "M1", "", now)
		if err != nil || !justOpened || a1.Status != alert.StatusOpen || !a1.StableSince.Equal(a1.FirstSeenAt) {
			t.Fatalf("first fire: %+v, justOpened=%v, err=%v", a1, justOpened, err)
		}
		later := now.Add(time.Minute)
		a2, justOpened, err := s.Fire(ctx, tenant, key, alert.SeverityCritical, "T2", "M2", "https://x", later)
		if err != nil || justOpened || a2.ID != a1.ID || a2.Severity != alert.SeverityCritical || a2.Title != "T2" ||
			!a2.LastSeenAt.Equal(later) || !a2.StableSince.Equal(a1.StableSince) {
			t.Fatalf("second fire: %+v, justOpened=%v, err=%v", a2, justOpened, err)
		}

		resolved, wasOpen, err := s.Resolve(ctx, tenant, key, later.Add(time.Minute))
		if err != nil || !wasOpen || resolved.Status != alert.StatusResolved {
			t.Fatalf("resolve: %+v, wasOpen=%v, err=%v", resolved, wasOpen, err)
		}
		_, wasOpen, err = s.Resolve(ctx, tenant, key, later.Add(2*time.Minute))
		if err != nil || wasOpen {
			t.Fatalf("resolve again: wasOpen=%v, err=%v", wasOpen, err)
		}

		// Re-firing after resolution is a fresh occurrence: new row, new id.
		a3, justOpened, err := s.Fire(ctx, tenant, key, alert.SeverityWarning, "T3", "M3", "", later.Add(3*time.Minute))
		if err != nil || !justOpened || a3.ID == a1.ID {
			t.Fatalf("re-fire after resolve: %+v, justOpened=%v, err=%v", a3, justOpened, err)
		}

		got, err := s.ListAlerts(ctx, tenant, "", nil, 100)
		if err != nil {
			t.Fatal(err)
		}
		var seen int
		for _, a := range got {
			if a.Key == key {
				seen++
			}
		}
		if seen != 2 {
			t.Fatalf("ListAlerts shows %d rows for %s, want 2 (one resolved, one open)", seen, key)
		}
	})

	t.Run("due queries and Notify mark bookkeeping and emit a webhook event", func(t *testing.T) {
		due := newChannel(t, true, nil)
		key := "test.notify:" + uuid.Must(uuid.NewV7()).String()
		stableSince := time.Now().Add(-alert.StableFor - time.Minute)
		a, _, err := s.Fire(ctx, tenant, key, alert.SeverityCritical, "Trunk down", "unreachable", "", stableSince)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		list, err := s.DueToNotify(ctx, now.Add(-alert.StableFor), 100)
		if err != nil {
			t.Fatal(err)
		}
		if !containsAlert(list, a.ID) {
			t.Fatalf("DueToNotify() didn't include the stable alert: %+v", list)
		}

		ev := &alert.WebhookEvent{Type: "alert.fired", Data: map[string]any{"alert_id": a.ID, "key": a.Key}}
		if err := s.Notify(ctx, a, alert.DeliveryFired, []uuid.UUID{due.ID}, nil, now, alert.MaxAttempts, ev); err != nil {
			t.Fatal(err)
		}

		list, err = s.DueToNotify(ctx, now.Add(-alert.StableFor), 100)
		if err != nil {
			t.Fatal(err)
		}
		if containsAlert(list, a.ID) {
			t.Fatal("a notified alert is still due to notify")
		}

		deliveries, err := s.ClaimAlertDeliveries(ctx, now, now.Add(time.Minute), 10)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, j := range deliveries {
			if j.ChannelID == due.ID {
				found = true
				if len(j.Alerts) != 1 || j.Alerts[0].ID != a.ID || j.ChannelKind != alert.KindNtfy {
					t.Errorf("job = %+v", j)
				}
			}
		}
		if !found {
			t.Fatal("the pending delivery wasn't claimed")
		}

		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE tenant_id = $1 AND type = 'alert.fired'", tenant).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("event_outbox has %d alert.fired rows, want 1", n)
		}

		// A reminder is due only once the interval has passed.
		reminders, err := s.DueForReminder(ctx, now.Add(time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		if !containsAlert(reminders, a.ID) {
			t.Fatal("DueForReminder() with a future cutoff should include the just-notified alert")
		}
		reminders, err = s.DueForReminder(ctx, now.Add(-time.Hour), 100)
		if err != nil {
			t.Fatal(err)
		}
		if containsAlert(reminders, a.ID) {
			t.Fatal("DueForReminder() with a past cutoff shouldn't include a just-notified alert")
		}

		resolvedAlert, _, err := s.Resolve(ctx, tenant, key, now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		resolvedDue, err := s.DueForResolvedNotice(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if !containsAlert(resolvedDue, resolvedAlert.ID) {
			t.Fatal("DueForResolvedNotice() didn't include the resolved, notified alert")
		}
		if err := s.Notify(ctx, resolvedAlert, alert.DeliveryResolved, []uuid.UUID{due.ID}, nil, now.Add(2*time.Minute), alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		resolvedDue, err = s.DueForResolvedNotice(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if containsAlert(resolvedDue, resolvedAlert.ID) {
			t.Fatal("a resolved-notified alert is still due for a resolved notice")
		}
	})

	t.Run("held deliveries flush into one digest", func(t *testing.T) {
		// Store.Notify takes the due/held split as given (the Engine
		// decides that from quiet hours, tested in internal/alert); here
		// the test picks "held" directly to exercise HeldChannels/FlushHeld.
		channel := newChannel(t, true, nil)
		now := time.Now()

		a1, _, err := s.Fire(ctx, tenant, "test.hold.1:"+uuid.NewString(), alert.SeverityWarning, "A1", "m1", "", now)
		if err != nil {
			t.Fatal(err)
		}
		a2, _, err := s.Fire(ctx, tenant, "test.hold.2:"+uuid.NewString(), alert.SeverityWarning, "A2", "m2", "", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(ctx, a1, alert.DeliveryFired, nil, []uuid.UUID{channel.ID}, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(ctx, a2, alert.DeliveryFired, nil, []uuid.UUID{channel.ID}, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}

		held, err := s.HeldChannels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !containsChannel(held, channel.ID) {
			t.Fatalf("HeldChannels() = %+v, want channel %s", held, channel.ID)
		}

		if err := s.FlushHeld(ctx, channel.ID, now.Add(time.Minute), alert.MaxAttempts); err != nil {
			t.Fatal(err)
		}
		held, err = s.HeldChannels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if containsChannel(held, channel.ID) {
			t.Fatal("channel still shows as held after flushing")
		}

		jobs, err := s.ClaimAlertDeliveries(ctx, now.Add(time.Minute), now.Add(2*time.Minute), 10)
		if err != nil {
			t.Fatal(err)
		}
		var digest *alert.Job
		for i := range jobs {
			if jobs[i].ChannelID == channel.ID {
				digest = &jobs[i]
			}
		}
		if digest == nil || digest.Kind != alert.DeliveryDigest || len(digest.Alerts) != 2 {
			t.Fatalf("digest job = %+v", digest)
		}
		code := 200
		if err := s.RecordAlertAttempt(ctx, alert.Outcome{DeliveryID: digest.ID, ChannelID: channel.ID, TenantID: tenant,
			Attempt: alert.Attempt{At: now.Add(2 * time.Minute), StatusCode: &code}, Status: alert.DeliverySucceeded, Succeeded: true, Attempts: 1}); err != nil {
			t.Fatal(err)
		}

		// Flushing again with nothing held is a no-op, not an empty digest.
		if err := s.FlushHeld(ctx, channel.ID, now.Add(3*time.Minute), alert.MaxAttempts); err != nil {
			t.Fatal(err)
		}
		jobs, err = s.ClaimAlertDeliveries(ctx, now.Add(3*time.Minute), now.Add(4*time.Minute), 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			if j.ChannelID == channel.ID {
				t.Fatal("FlushHeld with nothing held created another delivery")
			}
		}
	})

	t.Run("disabling a channel cancels its queued deliveries", func(t *testing.T) {
		channel := newChannel(t, true, nil)
		now := time.Now()
		a, _, err := s.Fire(ctx, tenant, "test.cancel:"+uuid.NewString(), alert.SeverityWarning, "T", "M", "", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(ctx, a, alert.DeliveryFired, []uuid.UUID{channel.ID}, nil, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		channel.Enabled = false
		if _, err := s.UpdateChannel(ctx, channel, audit); err != nil {
			t.Fatal(err)
		}
		jobs, err := s.ClaimAlertDeliveries(ctx, now, now.Add(time.Minute), 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			if j.ChannelID == channel.ID {
				t.Fatal("a disabled channel's delivery was claimed")
			}
		}
	})

	t.Run("RecordAlertAttempt logs and retries, and survives a deleted channel", func(t *testing.T) {
		channel := newChannel(t, true, nil)
		now := time.Now()
		a, _, err := s.Fire(ctx, tenant, "test.attempt:"+uuid.NewString(), alert.SeverityWarning, "T", "M", "", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(ctx, a, alert.DeliveryFired, []uuid.UUID{channel.ID}, nil, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		jobs, err := s.ClaimAlertDeliveries(ctx, now, now.Add(time.Minute), 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("ClaimAlertDeliveries: %d, %v", len(jobs), err)
		}
		job := jobs[0]
		code := 500
		errMsg := "boom"
		o := alert.Outcome{DeliveryID: job.ID, ChannelID: job.ChannelID, TenantID: job.TenantID,
			Attempt: alert.Attempt{At: now, StatusCode: &code, Error: &errMsg, DurationMS: 5}, Attempts: 1}
		if next, ok := alert.NextRetry(1, job.MaxAttempts, now); ok {
			o.Status, o.NextAttemptAt = alert.DeliveryPending, &next
		}
		if err := s.RecordAlertAttempt(ctx, o); err != nil {
			t.Fatal(err)
		}
		var status string
		var attempts int
		if err := pool.QueryRow(ctx, "SELECT status, attempts FROM alert_delivery WHERE id = $1", job.ID).Scan(&status, &attempts); err != nil {
			t.Fatal(err)
		}
		if status != alert.DeliveryPending || attempts != 1 {
			t.Fatalf("after one failed attempt: status=%s attempts=%d", status, attempts)
		}

		// Deleting the channel cascades the delivery; recording another
		// attempt for it must not error (docs/API.md-style resilience,
		// mirrored from the webhook delivery path).
		if err := s.DeleteChannel(ctx, tenant, channel.ID, audit); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAlertAttempt(ctx, o); err != nil {
			t.Fatalf("RecordAlertAttempt after the channel was deleted: %v", err)
		}
	})

	t.Run("Notify is a claim: stale or repeated scans queue nothing", func(t *testing.T) {
		channel := newChannel(t, true, nil)
		defer s.DeleteChannel(ctx, tenant, channel.ID, audit) // its queued deliveries go with it
		now := time.Now()
		countFor := func(id uuid.UUID) int {
			var n int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM alert_delivery WHERE $1 = ANY (alert_ids)", id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n
		}

		// Resolved between the scan and the fan-out: the flap hold-back
		// absorbs it, so no "fired" (and later no "resolved") is sent.
		flap, _, err := s.Fire(ctx, tenant, "test.flap:"+uuid.NewString(), alert.SeverityWarning, "T", "M", "", now.Add(-10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Resolve(ctx, tenant, flap.Key, now); err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(ctx, flap, alert.DeliveryFired, []uuid.UUID{channel.ID}, nil, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		if n := countFor(flap.ID); n != 0 {
			t.Fatalf("a resolved alert got %d fired deliveries", n)
		}
		due, err := s.DueForResolvedNotice(ctx, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if containsAlert(due, flap.ID) {
			t.Fatal("an alert that was never notified is due for a resolved notice")
		}

		// Two engines (or two ticks) with the same scan result: one wins.
		a, _, err := s.Fire(ctx, tenant, "test.twice:"+uuid.NewString(), alert.SeverityWarning, "T", "M", "", now.Add(-10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if err := s.Notify(ctx, a, alert.DeliveryFired, []uuid.UUID{channel.ID}, nil, now, alert.MaxAttempts, nil); err != nil {
				t.Fatal(err)
			}
		}
		if n := countFor(a.ID); n != 1 {
			t.Fatalf("two Notify calls queued %d deliveries, want 1", n)
		}
	})

	t.Run("escalation makes a reminder due at once", func(t *testing.T) {
		channel := newChannel(t, true, nil)
		defer s.DeleteChannel(ctx, tenant, channel.ID, audit) // its queued deliveries go with it
		now := time.Now()
		key := "test.escalate:" + uuid.NewString()
		a, _, err := s.Fire(ctx, tenant, key, alert.SeverityWarning, "Cert", "21 days left", "", now.Add(-10*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(ctx, a, alert.DeliveryFired, []uuid.UUID{channel.ID}, nil, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		cutoff := now.Add(-alert.ReminderInterval)
		// Same severity again: no early reminder.
		if _, _, err := s.Fire(ctx, tenant, key, alert.SeverityWarning, "Cert", "20 days left", "", now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if due, _ := s.DueForReminder(ctx, cutoff, 1000); containsAlert(due, a.ID) {
			t.Fatal("a re-fire at the same severity made a reminder due")
		}
		// More severe: reminder due now, carrying the new severity.
		if _, _, err := s.Fire(ctx, tenant, key, alert.SeverityCritical, "Cert", "6 days left", "", now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		due, err := s.DueForReminder(ctx, cutoff, 1000)
		if err != nil {
			t.Fatal(err)
		}
		var got *alert.Alert
		for i := range due {
			if due[i].ID == a.ID {
				got = &due[i]
			}
		}
		if got == nil || got.Severity != alert.SeverityCritical {
			t.Fatalf("after escalating, reminder due = %+v", got)
		}
		// And the reminder claim works from that state.
		if err := s.Notify(ctx, *got, alert.DeliveryReminder, []uuid.UUID{channel.ID}, nil, now.Add(3*time.Minute), alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		if due, _ := s.DueForReminder(ctx, cutoff, 1000); containsAlert(due, a.ID) {
			t.Fatal("still due for a reminder after one was sent")
		}
	})

	t.Run("one-shot and held-back alerts", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Microsecond)
		key := "test.oneshot:" + uuid.NewString()
		a, _, err := s.FireWith(ctx, tenant, key, alert.SeverityCritical, "Emergency call", "999", "", now,
			alert.FireOptions{StableSince: now.Add(-alert.StableFor), OneShot: true})
		if err != nil {
			t.Fatal(err)
		}
		due, err := s.DueToNotify(ctx, now.Add(-alert.StableFor), 1000)
		if err != nil || !containsAlert(due, a.ID) {
			t.Fatalf("one-shot not due at once: %v", err)
		}
		if err := s.Notify(ctx, a, alert.DeliveryFired, nil, nil, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		var status string
		var resolvedNotified *time.Time
		if err := pool.QueryRow(ctx, `SELECT status, resolved_notified_at FROM alert WHERE id = $1`, a.ID).Scan(&status, &resolvedNotified); err != nil {
			t.Fatal(err)
		}
		if status != "resolved" || resolvedNotified == nil {
			t.Fatalf("one-shot after notify: %s %v", status, resolvedNotified)
		}
		if due, _ := s.DueForResolvedNotice(ctx, 1000); containsAlert(due, a.ID) {
			t.Error("a one-shot alert wants a resolved notice")
		}

		// Held back 2 minutes instead of 5.
		held, _, err := s.FireWith(ctx, tenant, "test.held:"+uuid.NewString(), alert.SeverityWarning, "Line down", "x", "", now,
			alert.FireOptions{StableSince: now.Add(2*time.Minute - alert.StableFor)})
		if err != nil {
			t.Fatal(err)
		}
		if due, _ := s.DueToNotify(ctx, now.Add(time.Minute-alert.StableFor), 1000); containsAlert(due, held.ID) {
			t.Error("due after 1 minute")
		}
		if due, _ := s.DueToNotify(ctx, now.Add(2*time.Minute-alert.StableFor), 1000); !containsAlert(due, held.ID) {
			t.Error("not due after 2 minutes")
		}
	})

	t.Run("CleanupAlerts keeps 30 days", func(t *testing.T) {
		channel := newChannel(t, true, nil)
		now := time.Now()
		a, _, err := s.Fire(ctx, tenant, "test.cleanup:"+uuid.NewString(), alert.SeverityWarning, "T", "M", "", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(ctx, a, alert.DeliveryFired, []uuid.UUID{channel.ID}, nil, now, alert.MaxAttempts, nil); err != nil {
			t.Fatal(err)
		}
		jobs, err := s.ClaimAlertDeliveries(ctx, now, now.Add(time.Minute), 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("claim: %d, %v", len(jobs), err)
		}
		code := 200
		if err := s.RecordAlertAttempt(ctx, alert.Outcome{DeliveryID: jobs[0].ID, ChannelID: channel.ID, TenantID: tenant,
			Attempt: alert.Attempt{At: now, StatusCode: &code}, Status: alert.DeliverySucceeded, Succeeded: true, Attempts: 1}); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-31 * 24 * time.Hour)
		if _, err := pool.Exec(ctx, "UPDATE alert_delivery SET created_at = $1 WHERE id = $2", old, jobs[0].ID); err != nil {
			t.Fatal(err)
		}
		if err := s.CleanupAlerts(ctx, now.Add(-alert.Retention)); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM alert_delivery WHERE id = $1", jobs[0].ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("an old finished delivery survived cleanup")
		}
	})

	t.Run("end to end through the real Engine and Sender", func(t *testing.T) {
		var received atomic.Int32
		var lastBody atomic.Value
		recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			lastBody.Store(string(b))
			received.Add(1)
			w.WriteHeader(200)
		}))
		defer recv.Close()

		id := uuid.Must(uuid.NewV7())
		cfg, err := alert.Config{URL: recv.URL}.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		enc, err := sealer.Seal("alert_channel:"+id.String(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		c := alert.Channel{ID: id, TenantID: tenant, Kind: alert.KindSlack, Name: "e2e", ConfigEnc: enc,
			MinSeverity: alert.SeverityInfo, Enabled: true, Version: 1, CreatedBy: "system", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateChannel(ctx, c, audit); err != nil {
			t.Fatal(err)
		}

		sender := &alert.Sender{Client: recv.Client(), Sealer: sealer, Now: time.Now}
		engine := &alert.Engine{Store: s, Sender: sender, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Poll: 50 * time.Millisecond}
		wctx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { engine.Run(wctx); close(done) }()
		defer func() { stop(); <-done }()

		// Fire an alert that's already "stable" (as if it opened 6 minutes
		// ago), so the engine's next tick notifies immediately rather than
		// this test waiting out the real 5-minute hold-back.
		key := "test.e2e:" + uuid.NewString()
		if _, _, err := s.Fire(ctx, tenant, key, alert.SeverityCritical, "End to end", "it works", "", now.Add(-alert.StableFor-time.Second)); err != nil {
			t.Fatal(err)
		}

		deadline := time.Now().Add(15 * time.Second)
		for received.Load() == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the engine never delivered the alert")
			}
			time.Sleep(50 * time.Millisecond)
		}
		body, _ := lastBody.Load().(string)
		if body == "" {
			t.Fatal("empty delivery body")
		}
	})
}

func containsAlert(list []alert.Alert, id uuid.UUID) bool {
	for _, a := range list {
		if a.ID == id {
			return true
		}
	}
	return false
}

func containsChannel(list []alert.Channel, id uuid.UUID) bool {
	for _, c := range list {
		if c.ID == id {
			return true
		}
	}
	return false
}
