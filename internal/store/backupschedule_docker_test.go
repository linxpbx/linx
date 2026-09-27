package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
)

// TestBackupScheduleDocker runs the backup schedule and history queries
// against real Postgres (docs/BACKUP.md §8 step 3). It needs Docker: make
// test-docker.
func TestBackupScheduleDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-backup-schedule-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "backup.schedule_updated", Result: auth.ResultOK}

	// Defaults from migration 0024: off, 03:00, Sunday, the 1st.
	sched, err := s.Schedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sched.Frequency != backupschedule.FrequencyOff || sched.TimeOfDay != 180 || sched.DayOfWeek != 0 || sched.DayOfMonth != 1 {
		t.Fatalf("default schedule = %+v", sched)
	}
	if sched.RequestedAt != nil {
		t.Fatalf("RequestedAt should start nil: %+v", sched)
	}

	if _, err := s.LastRunStartedAt(ctx); err != nil {
		t.Fatalf("LastRunStartedAt on an empty history: %v", err)
	} else if v, _ := s.LastRunStartedAt(ctx); v != nil {
		t.Fatalf("LastRunStartedAt = %v, want nil before any run", v)
	}

	updated, err := s.UpdateSchedule(ctx, backupschedule.Schedule{Frequency: backupschedule.FrequencyDaily, TimeOfDay: 120, DayOfWeek: 3, DayOfMonth: 15}, audit)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Frequency != backupschedule.FrequencyDaily || updated.TimeOfDay != 120 {
		t.Fatalf("UpdateSchedule() = %+v", updated)
	}
	reread, err := s.Schedule(ctx)
	if err != nil || reread.Frequency != backupschedule.FrequencyDaily {
		t.Fatalf("Schedule() after update = %+v, %v", reread, err)
	}

	requestedAt := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.RequestRun(ctx, requestedAt, "user:"+uuid.NewString(), audit); err != nil {
		t.Fatal(err)
	}
	afterRequest, err := s.Schedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRequest.RequestedAt == nil || !afterRequest.RequestedAt.Equal(requestedAt) {
		t.Fatalf("RequestedAt = %v, want %v", afterRequest.RequestedAt, requestedAt)
	}

	// Recording a run clears the pending request, is visible in history,
	// updates LastRunStartedAt, and fires an event.
	run := backupschedule.Run{
		ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Trigger: backupschedule.TriggerManual,
		StartedAt: requestedAt, FinishedAt: requestedAt.Add(30 * time.Second), Status: backupschedule.StatusSuccess,
		Destinations: []backupschedule.Destination{{Name: "local", OK: true, SnapshotID: "abc123"}},
	}
	if err := s.InsertRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	afterRun, err := s.Schedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if afterRun.RequestedAt != nil {
		t.Fatalf("RequestedAt should be cleared after a run is recorded: %+v", afterRun)
	}
	last, err := s.LastRunStartedAt(ctx)
	if err != nil || last == nil || !last.Equal(run.StartedAt) {
		t.Fatalf("LastRunStartedAt() = %v, %v, want %v", last, err, run.StartedAt)
	}
	runs, err := s.ListRuns(ctx, tenant, nil, 10)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID || runs[0].Destinations[0].SnapshotID != "abc123" {
		t.Fatalf("ListRuns() = %+v, %v", runs, err)
	}

	var eventCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE tenant_id = $1 AND type = 'backup.completed'`, tenant).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("backup.completed events = %d, want 1", eventCount)
	}

	// A failed run fires backup.failed instead.
	failedRun := backupschedule.Run{
		ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Trigger: backupschedule.TriggerScheduled,
		StartedAt: run.FinishedAt, FinishedAt: run.FinishedAt.Add(time.Second), Status: backupschedule.StatusFailure,
		Error: "dumping the database failed",
	}
	if err := s.InsertRun(ctx, failedRun); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE tenant_id = $1 AND type = 'backup.failed'`, tenant).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("backup.failed events = %d, want 1", eventCount)
	}

	runs, err = s.ListRuns(ctx, tenant, nil, 10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListRuns() after two runs = %+v, %v", runs, err)
	}
	// Newest first.
	if runs[0].ID != failedRun.ID {
		t.Fatalf("ListRuns()[0] = %v, want the more recent run %v", runs[0].ID, failedRun.ID)
	}

	// Cursor pagination: the same "before this id" convention as audit_log.
	page, err := s.ListRuns(ctx, tenant, &runs[0].ID, 10)
	if err != nil || len(page) != 1 || page[0].ID != run.ID {
		t.Fatalf("ListRuns() with a cursor = %+v, %v", page, err)
	}
}
