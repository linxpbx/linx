package store

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/webhook"
)

// TestWebhooksDocker runs the outbox, worker and delivery-log queries
// against real Postgres, delivering to a local HTTPS receiver. It needs
// Docker: make test-docker.
func TestWebhooksDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-webhooks-test")
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

	// The receiver answers with whatever status is set, and verifies every
	// signature with the secret it was given.
	var status atomic.Int32
	status.Store(200)
	var received, badSig atomic.Int32
	var secret atomic.Value
	recv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ts, _ := strconv.ParseInt(r.Header.Get("webhook-timestamp"), 10, 64)
		if !webhook.Verify(secret.Load().(string), r.Header.Get("webhook-id"), time.Unix(ts, 0), body, r.Header.Get("webhook-signature")) {
			badSig.Add(1)
		}
		received.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer recv.Close()

	sender := &webhook.Sender{Client: recv.Client(), Sealer: sealer, Now: time.Now}
	systemCtx := auth.WithPrincipal(ctx, auth.SystemPrincipal(tenant))
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "test", Result: auth.ResultOK}

	newEndpoint := func(types []string, enabled bool) webhook.Endpoint {
		t.Helper()
		id := uuid.Must(uuid.NewV7())
		sec, _ := webhook.NewSecret()
		secret.Store(sec)
		enc, err := sealer.Seal("webhook_endpoint:"+id.String(), []byte(sec))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		e := webhook.Endpoint{ID: id, TenantID: tenant, URL: recv.URL + "/hook", EventTypes: types, Enabled: enabled,
			SecretEnc: enc, Version: 1, CreatedBy: "system", CreatedAt: now, UpdatedAt: now}
		if err := s.CreateEndpoint(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		return e
	}
	emit := func(eventType string) webhook.Event {
		t.Helper()
		ev, err := webhook.NewEvent(tenant, eventType, map[string]string{"k": "v"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error { return InsertEvent(ctx, tx, ev) }); err != nil {
			t.Fatal(err)
		}
		return ev
	}
	deliveries := func(e webhook.Endpoint) []webhook.Delivery {
		t.Helper()
		ds, err := s.ListDeliveries(ctx, tenant, e.ID, "", nil, 100)
		if err != nil {
			t.Fatal(err)
		}
		return ds
	}

	// An event rolled back with its transaction never reaches the outbox.
	t.Run("rolled-back event is never sent", func(t *testing.T) {
		ev, _ := webhook.NewEvent(tenant, "call.missed", nil, time.Now())
		_ = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := InsertEvent(ctx, tx, ev); err != nil {
				t.Fatal(err)
			}
			return context.Canceled // roll back
		})
		var n int
		pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE id = $1", ev.ID).Scan(&n)
		if n != 0 {
			t.Fatal("rolled-back event is in the outbox")
		}
	})

	t.Run("dispatch fans out to subscribed, enabled endpoints only", func(t *testing.T) {
		off := newEndpoint(nil, false)
		other := newEndpoint([]string{"trunk.status_changed"}, true)
		all := newEndpoint(nil, true)
		missed := newEndpoint([]string{"call.missed"}, true)
		emit("call.missed")
		if n, err := s.DispatchEvents(ctx, time.Now(), webhook.MaxAttempts, 100); err != nil || n != 1 {
			t.Fatalf("DispatchEvents = %d, %v", n, err)
		}
		if n, _ := s.DispatchEvents(ctx, time.Now(), webhook.MaxAttempts, 100); n != 0 {
			t.Fatalf("second dispatch = %d, want 0", n)
		}
		for e, want := range map[*webhook.Endpoint]int{&off: 0, &other: 0, &all: 1, &missed: 1} {
			if got := len(deliveries(*e)); got != want {
				t.Errorf("endpoint %v: %d deliveries, want %d", e.EventTypes, got, want)
			}
		}
		// Clean up so later subtests see only their own endpoints' work.
		for _, e := range []webhook.Endpoint{off, other, all, missed} {
			if err := s.DeleteEndpoint(ctx, tenant, e.ID, audit); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("claim leases a delivery to one worker", func(t *testing.T) {
		e := newEndpoint(nil, true)
		ev := emit("trunk.status_changed")
		s.DispatchEvents(ctx, time.Now(), webhook.MaxAttempts, 100)
		now := time.Now()
		jobs, err := s.ClaimDeliveries(ctx, now, now.Add(time.Minute), 10)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("ClaimDeliveries = %d, %v", len(jobs), err)
		}
		j := jobs[0]
		if j.EventID != ev.ID || string(j.Body) != string(ev.Body) || j.URL != e.URL || len(j.SecretEnc) == 0 || j.MaxAttempts != webhook.MaxAttempts {
			t.Fatalf("job = %+v", j)
		}
		if again, _ := s.ClaimDeliveries(ctx, now, now.Add(time.Minute), 10); len(again) != 0 {
			t.Fatal("a leased delivery was claimed twice")
		}
		// Once the lease runs out (the worker died), another worker takes it.
		later := now.Add(2 * time.Minute)
		if again, _ := s.ClaimDeliveries(ctx, later, later.Add(time.Minute), 10); len(again) != 1 {
			t.Fatal("an expired lease wasn't taken over")
		}
		s.DeleteEndpoint(ctx, tenant, e.ID, audit)
	})

	t.Run("worker delivers, retries and records health", func(t *testing.T) {
		e := newEndpoint(nil, true)
		emit("call.missed")
		runWorker(t, ctx, s, sender, func() bool {
			ds := deliveries(e)
			return len(ds) == 1 && ds[0].Status == webhook.StatusSucceeded
		})
		if badSig.Load() != 0 {
			t.Fatal("the receiver couldn't verify a signature")
		}
		d, err := s.Delivery(ctx, tenant, deliveries(e)[0].ID)
		if err != nil || len(d.Log) != 1 || *d.Log[0].StatusCode != 200 || d.FinishedAt == nil {
			t.Fatalf("delivery = %+v, %v", d, err)
		}
		got, _ := s.Endpoint(ctx, tenant, e.ID)
		if got.LastSuccessAt == nil || got.FailingSince != nil {
			t.Fatalf("health after success: %+v", got)
		}

		status.Store(503)
		emit("call.missed")
		runWorker(t, ctx, s, sender, func() bool {
			ds, _ := s.ListDeliveries(ctx, tenant, e.ID, webhook.StatusPending, nil, 10)
			return len(ds) == 1 && ds[0].Attempts == 1
		})
		pending, _ := s.ListDeliveries(ctx, tenant, e.ID, webhook.StatusPending, nil, 10)
		if next := pending[0].NextAttemptAt; next == nil || time.Until(*next) < 3*time.Second || time.Until(*next) > 7*time.Second {
			t.Fatalf("next attempt at %v, want about 5 s from now", next)
		}
		got, _ = s.Endpoint(ctx, tenant, e.ID)
		if got.FailingSince == nil || !got.Enabled {
			t.Fatalf("health after failure: %+v", got)
		}
		status.Store(200)
		s.DeleteEndpoint(ctx, tenant, e.ID, audit)
	})

	t.Run("endpoint failing for 5 days is turned off", func(t *testing.T) {
		e := newEndpoint(nil, true)
		// Two deliveries: one being tried, one waiting; turning the endpoint
		// off cancels the waiting one.
		emit("call.missed")
		emit("call.ended")
		s.DispatchEvents(ctx, time.Now(), webhook.MaxAttempts, 100)
		jobs, _ := s.ClaimDeliveries(ctx, time.Now(), time.Now().Add(time.Minute), 1)
		sixDaysAgo := time.Now().Add(-6 * 24 * time.Hour)
		pool.Exec(ctx, "UPDATE webhook_endpoint SET failing_since = $2 WHERE id = $1", e.ID, sixDaysAgo)
		code := 500
		msg := "The receiver answered 500."
		o := webhook.Outcome{DeliveryID: jobs[0].ID, EndpointID: e.ID, TenantID: tenant, EventType: jobs[0].EventType,
			Attempt: webhook.Attempt{At: time.Now(), StatusCode: &code, Error: &msg}, Status: webhook.StatusFailed, Attempts: 1}
		reason, err := s.RecordAttempt(ctx, o, time.Now().Add(-webhook.FailingLimit))
		if err != nil || reason != webhook.DisabledFailing {
			t.Fatalf("RecordAttempt = %q, %v", reason, err)
		}
		got, _ := s.Endpoint(ctx, tenant, e.ID)
		if got.Enabled || got.DisabledReason == nil || *got.DisabledReason != "failing" || got.Version != 2 {
			t.Fatalf("endpoint = %+v", got)
		}
		if ds, _ := s.ListDeliveries(ctx, tenant, e.ID, webhook.StatusCancelled, nil, 10); len(ds) != 1 {
			t.Fatalf("%d cancelled deliveries, want 1", len(ds))
		}

		// After the receiver is fixed: turn it on and replay what was missed.
		got.Enabled, got.DisabledReason, got.DisabledAt = true, nil, nil
		got, err = s.UpdateEndpoint(ctx, got, audit)
		if err != nil || got.FailingSince != nil {
			t.Fatalf("re-enable: %+v, %v", got, err)
		}
		svc := &webhook.Service{Store: s, Sealer: sealer, Sender: sender, Now: time.Now}
		n, err := svc.ReplayFailed(systemCtx, e.ID, time.Now().Add(-time.Hour))
		if err != nil || n != 2 {
			t.Fatalf("ReplayFailed = %d, %v; want 2 (one failed, one cancelled)", n, err)
		}
		if n, _ := svc.ReplayFailed(systemCtx, e.ID, time.Now().Add(-time.Hour)); n != 0 {
			t.Fatalf("second ReplayFailed = %d, want 0 (already queued)", n)
		}
		s.DeleteEndpoint(ctx, tenant, e.ID, audit)
	})

	t.Run("410 Gone turns the endpoint off at once", func(t *testing.T) {
		e := newEndpoint(nil, true)
		status.Store(410)
		emit("call.missed")
		runWorker(t, ctx, s, sender, func() bool {
			got, _ := s.Endpoint(ctx, tenant, e.ID)
			return !got.Enabled
		})
		got, _ := s.Endpoint(ctx, tenant, e.ID)
		if *got.DisabledReason != webhook.DisabledGone {
			t.Fatalf("reason = %v", *got.DisabledReason)
		}
		status.Store(200)
	})

	t.Run("rotation signs with both secrets", func(t *testing.T) {
		e := newEndpoint(nil, true)
		svc := &webhook.Service{Store: s, Sealer: sealer, Sender: sender, Now: time.Now}
		// The receiver still holds the old secret; both signatures are sent.
		old := secret.Load().(string)
		if _, _, err := svc.RotateSecret(systemCtx, e.ID); err != nil {
			t.Fatal(err)
		}
		secret.Store(old)
		badSig.Store(0)
		d, err := svc.Test(systemCtx, e.ID)
		if err != nil || d.Status != webhook.StatusSucceeded || badSig.Load() != 0 {
			t.Fatalf("test after rotation: %+v, %v, bad signatures %d", d, err, badSig.Load())
		}
		// Test messages don't count towards the endpoint's health.
		status.Store(500)
		pool.Exec(ctx, "UPDATE webhook_endpoint SET failing_since = NULL WHERE id = $1", e.ID)
		svc.Test(systemCtx, e.ID)
		got, _ := s.Endpoint(ctx, tenant, e.ID)
		if got.FailingSince != nil {
			t.Fatal("a failed test marked the endpoint as failing")
		}
		status.Store(200)

		// A stale version is refused.
		got.Version = 1
		if _, err := s.UpdateEndpoint(ctx, got, audit); err != webhook.ErrVersionChanged {
			t.Fatalf("stale update = %v", err)
		}
	})

	t.Run("cleanup keeps 30 days", func(t *testing.T) {
		old := time.Now().Add(-31 * 24 * time.Hour)
		pool.Exec(ctx, "UPDATE webhook_delivery SET created_at = $1 WHERE status <> 'pending'", old)
		pool.Exec(ctx, "UPDATE event_outbox SET created_at = $1", old)
		pool.Exec(ctx, "UPDATE webhook_endpoint SET previous_secret_expires_at = $1 WHERE previous_secret_enc IS NOT NULL", old)
		if err := s.Cleanup(ctx, time.Now().Add(-webhook.Retention), time.Now()); err != nil {
			t.Fatal(err)
		}
		var deliveries, events, prev int
		pool.QueryRow(ctx, "SELECT count(*) FROM webhook_delivery WHERE status <> 'pending'").Scan(&deliveries)
		pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox e WHERE NOT EXISTS (SELECT 1 FROM webhook_delivery d WHERE d.event_id = e.id)").Scan(&events)
		pool.QueryRow(ctx, "SELECT count(*) FROM webhook_endpoint WHERE previous_secret_enc IS NOT NULL").Scan(&prev)
		if deliveries != 0 || events != 0 || prev != 0 {
			t.Fatalf("after cleanup: %d finished deliveries, %d orphan events, %d previous secrets", deliveries, events, prev)
		}
	})

	t.Run("outbound allowlist", func(t *testing.T) {
		cidr, host := "192.168.1.0/24", "nas.home.arpa"
		for _, e := range []webhook.AllowlistEntry{
			{ID: uuid.Must(uuid.NewV7()), CIDR: &cidr, CreatedBy: "system", CreatedAt: time.Now()},
			{ID: uuid.Must(uuid.NewV7()), Host: &host, CreatedBy: "system", CreatedAt: time.Now()},
		} {
			if err := s.CreateAllowlistEntry(ctx, e, audit); err != nil {
				t.Fatal(err)
			}
		}
		dup := webhook.AllowlistEntry{ID: uuid.Must(uuid.NewV7()), Host: &host, CreatedBy: "system", CreatedAt: time.Now()}
		if err := s.CreateAllowlistEntry(ctx, dup, audit); err != webhook.ErrDuplicate {
			t.Fatalf("duplicate = %v", err)
		}
		l, err := webhook.Allowlist(s)(ctx)
		if err != nil || len(l.Prefixes) != 1 || l.Prefixes[0].String() != cidr || len(l.Hosts) != 1 {
			t.Fatalf("allowlist = %+v, %v", l, err)
		}
	})
}

// runWorker runs a webhook worker until done reports true (or fails the
// test after 20 s).
func runWorker(t *testing.T, ctx context.Context, s *Store, sender *webhook.Sender, done func() bool) {
	t.Helper()
	wctx, cancel := context.WithCancel(ctx)
	w := &webhook.Worker{Store: s, Sender: sender, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Poll: 50 * time.Millisecond}
	finished := make(chan struct{})
	go func() { w.Run(wctx); close(finished) }()
	defer func() { cancel(); <-finished }()
	deadline := time.Now().Add(20 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("worker didn't finish in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
