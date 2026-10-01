package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/email"
)

// TestEmailDocker runs email's queries (migration 0030) against real
// Postgres: the setting with optimistic concurrency and its checks (never
// plain, never port 25, on only with a password), and the queue: claiming
// due emails within the hourly limit and leases, retries, content wiped
// once sent or failed, the card's counts, and cleanup. make test-docker.
func TestEmailDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-email-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "email.update", Result: auth.ResultOK}

	if _, err := s.EmailSettings(ctx, tenant); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("before saving: %v", err)
	}
	c := email.Defaults(tenant)
	c.Host, c.FromAddress, c.Username, c.Version, c.UpdatedAt = "smtp.gmail.com", "pbx@example.com", "pbx@example.com", 1, now
	c.Enabled = true
	if err := s.SaveEmailSettings(ctx, c, 0, audit); err == nil {
		t.Fatal("turned on without a password")
	}
	c.PasswordEnc, c.HourlyLimit = []byte{1, 2, 3}, 2
	if err := s.SaveEmailSettings(ctx, c, 0, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveEmailSettings(ctx, c, 0, audit); !errors.Is(err, auth.ErrVersionChanged) {
		t.Fatalf("a second first save: %v", err)
	}
	for _, bad := range []func(*email.Config){
		func(c *email.Config) { c.Security = "none" },
		func(c *email.Config) { c.Port = 25 },
		func(c *email.Config) { c.HourlyLimit = 0 },
	} {
		b := c
		b.Version = 2
		bad(&b)
		if err := s.SaveEmailSettings(ctx, b, 1, audit); err == nil {
			t.Errorf("stored %+v", b)
		}
	}
	got, err := s.EmailSettings(ctx, tenant)
	if err != nil || !got.Enabled || got.Host != c.Host || got.HourlyLimit != 2 || got.ArrivedAt != nil || got.Version != 1 {
		t.Fatalf("%+v %v", got, err)
	}

	// Three emails due now, the limit is two an hour.
	ids := make([]uuid.UUID, 3)
	for i := range ids {
		ids[i] = uuid.Must(uuid.NewV7())
		due := now.Add(-time.Duration(3-i) * time.Second)
		if err := s.EnqueueEmail(ctx, email.Message{ID: ids[i], TenantID: tenant, Kind: email.KindInvite,
			To: []string{"sara@example.com"}, ContentEnc: []byte{9}, NextAttemptAt: &due, CreatedAt: now},
			auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "email.queue", Result: auth.ResultOK}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.ClaimEmails(ctx, now, now.Add(time.Minute), 10)
	if err != nil || len(jobs) != 2 || jobs[0].ID != ids[0] || jobs[1].ID != ids[1] || string(jobs[0].ContentEnc) != "\x09" || jobs[0].To[0] != "sara@example.com" {
		t.Fatalf("claim: %+v %v", jobs, err)
	}
	// Leased, and the two being sent count against the hour: nothing more.
	if again, err := s.ClaimEmails(ctx, now, now.Add(time.Minute), 10); err != nil || len(again) != 0 {
		t.Fatalf("claimed past the limit: %+v %v", again, err)
	}
	// One sent, one to try again in a minute.
	retry := now.Add(time.Minute)
	if err := s.FinishEmail(ctx, jobs[0], email.StatusSent, "", nil, now); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishEmail(ctx, jobs[1], email.StatusPending, "Linx couldn't connect", &retry, now); err != nil {
		t.Fatal(err)
	}
	// One left this hour: the third, given up on.
	jobs, err = s.ClaimEmails(ctx, now, now.Add(time.Minute), 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != ids[2] {
		t.Fatalf("the hour's last: %+v %v", jobs, err)
	}
	if err := s.FinishEmail(ctx, jobs[0], email.StatusFailed, "The mail server won't send to sara@example.com.", nil, now); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox WHERE content_enc IS NOT NULL`).Scan(&left); err != nil || left != 1 {
		t.Errorf("content kept on %d emails (%v)", left, err)
	}
	st, err := s.EmailStatus(ctx, tenant, now)
	if err != nil || st.LastSentAt == nil || st.SentLastHour != 1 || st.Waiting != 1 || st.LastError == "" {
		t.Fatalf("status: %+v %v", st, err)
	}
	if next, err := s.NextEmailDue(ctx); err != nil || next == nil || !next.Equal(retry) {
		t.Fatalf("next due: %v %v", next, err)
	}

	// The retry is due, and a fourth: one fits the hour, the retry first.
	fourth := uuid.Must(uuid.NewV7())
	if err := s.EnqueueEmail(ctx, email.Message{ID: fourth, TenantID: tenant, Kind: email.KindTest,
		To: []string{"a@example.com"}, ContentEnc: []byte{1}, NextAttemptAt: &retry, CreatedAt: now},
		auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "email.queue", Result: auth.ResultOK}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * time.Minute)
	jobs, err = s.ClaimEmails(ctx, later, later.Add(time.Minute), 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != ids[1] || jobs[0].Attempts != 1 {
		t.Fatalf("within the limit: %+v %v", jobs, err)
	}
	if err := s.FinishEmail(ctx, jobs[0], email.StatusSent, "", nil, later); err != nil {
		t.Fatal(err)
	}
	if jobs, err := s.ClaimEmails(ctx, later, later.Add(time.Minute), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("past the limit: %+v %v", jobs, err)
	}
	hourOn := now.Add(61 * time.Minute)
	jobs, err = s.ClaimEmails(ctx, hourOn, hourOn.Add(time.Minute), 10)
	if err != nil || len(jobs) != 1 || jobs[0].ID != fourth {
		t.Fatalf("an hour on: %+v %v", jobs, err)
	}
	// An email sent after a failure clears it from the card.
	if err := s.FinishEmail(ctx, jobs[0], email.StatusSent, "", nil, hourOn); err != nil {
		t.Fatal(err)
	}
	if st, err := s.EmailStatus(ctx, tenant, hourOn); err != nil || st.LastError != "" || st.Waiting != 0 {
		t.Fatalf("after a send: %+v %v", st, err)
	}

	// Off: nothing is claimed.
	c.Enabled, c.Version = false, 2
	if err := s.SaveEmailSettings(ctx, c, 1, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueEmail(ctx, email.Message{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Kind: email.KindTest,
		To: []string{"a@example.com"}, ContentEnc: []byte{1}, NextAttemptAt: &now, CreatedAt: now},
		auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "email.queue", Result: auth.ResultOK}); err != nil {
		t.Fatal(err)
	}
	if jobs, err := s.ClaimEmails(ctx, hourOn, hourOn.Add(time.Minute), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("while off: %+v %v", jobs, err)
	}
	if next, err := s.NextEmailDue(ctx); err != nil || next != nil {
		t.Fatalf("next due while off: %v %v", next, err)
	}

	if err := s.CleanupEmails(ctx, hourOn.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_outbox`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("after cleanup: %d rows (%v)", rows, err)
	}
}
