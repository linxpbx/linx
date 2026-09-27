package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
)

type fakeBackupStore struct {
	schedule backupschedule.Schedule
	lastRun  *time.Time
	runs     []backupschedule.Run
	tenant   uuid.UUID
}

func (f *fakeBackupStore) Schedule(context.Context) (backupschedule.Schedule, error) {
	return f.schedule, nil
}
func (f *fakeBackupStore) UpdateSchedule(_ context.Context, s backupschedule.Schedule, _ auth.AuditEntry) (backupschedule.Schedule, error) {
	f.schedule = s
	return s, nil
}
func (f *fakeBackupStore) RequestRun(_ context.Context, at time.Time, by string, _ auth.AuditEntry) error {
	f.schedule.RequestedAt, f.schedule.RequestedBy = &at, by
	return nil
}
func (f *fakeBackupStore) LastRunStartedAt(context.Context) (*time.Time, error) {
	return f.lastRun, nil
}
func (f *fakeBackupStore) InsertRun(_ context.Context, r backupschedule.Run) error {
	f.runs = append(f.runs, r)
	f.schedule.RequestedAt = nil
	return nil
}
func (f *fakeBackupStore) ListRuns(context.Context, uuid.UUID, *uuid.UUID, int) ([]backupschedule.Run, error) {
	return f.runs, nil
}
func (f *fakeBackupStore) DefaultTenant(context.Context) (uuid.UUID, error) { return f.tenant, nil }

type fakeBackupAlerter struct {
	fired    []string
	resolved []string
}

func (f *fakeBackupAlerter) Fire(_ context.Context, _ uuid.UUID, key, severity, _, _, _ string) error {
	f.fired = append(f.fired, severity+":"+key)
	return nil
}
func (f *fakeBackupAlerter) Resolve(_ context.Context, _ uuid.UUID, key string) error {
	f.resolved = append(f.resolved, key)
	return nil
}

func TestBackupPendingSkip(t *testing.T) {
	st := &fakeBackupStore{schedule: backupschedule.Schedule{Frequency: backupschedule.FrequencyOff}}
	svc := &backupschedule.Service{Store: st}
	var out, errb bytes.Buffer
	if code := backupPending(context.Background(), svc, &out, &errb); code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if out.String() != "skip\n" {
		t.Fatalf("out = %q, want \"skip\\n\"", out.String())
	}
}

func TestBackupPendingRunManual(t *testing.T) {
	at := time.Now()
	st := &fakeBackupStore{schedule: backupschedule.Schedule{Frequency: backupschedule.FrequencyOff, RequestedAt: &at}}
	svc := &backupschedule.Service{Store: st}
	var out, errb bytes.Buffer
	if code := backupPending(context.Background(), svc, &out, &errb); code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if out.String() != "run manual\n" {
		t.Fatalf("out = %q, want \"run manual\\n\"", out.String())
	}
}

func TestBackupReportSuccess(t *testing.T) {
	tenant := uuid.New()
	st := &fakeBackupStore{tenant: tenant}
	al := &fakeBackupAlerter{}
	svc := &backupschedule.Service{Store: st, Alerts: al}
	stdin := strings.NewReader(`{"trigger":"scheduled","started_at":"2026-09-27T03:00:00Z","finished_at":"2026-09-27T03:01:00Z",
		"destinations":[{"name":"local","ok":true,"snapshot_id":"abc123"}]}`)
	var out, errb bytes.Buffer
	code := backupReport(context.Background(), st, svc, stdin, &out, &errb)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errb.String())
	}
	if len(st.runs) != 1 || st.runs[0].Status != backupschedule.StatusSuccess || st.runs[0].TenantID != tenant {
		t.Fatalf("runs = %+v", st.runs)
	}
	if len(al.resolved) != 1 {
		t.Fatalf("resolved = %v, want the alert cleared", al.resolved)
	}
	if !strings.Contains(out.String(), "success") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestBackupReportPartialFailure(t *testing.T) {
	st := &fakeBackupStore{tenant: uuid.New()}
	al := &fakeBackupAlerter{}
	svc := &backupschedule.Service{Store: st, Alerts: al}
	stdin := strings.NewReader(`{"trigger":"manual","started_at":"2026-09-27T03:00:00Z","finished_at":"2026-09-27T03:01:00Z",
		"destinations":[{"name":"local","ok":true,"snapshot_id":"abc"},{"name":"nas","ok":false,"error":"no route to host"}]}`)
	var out, errb bytes.Buffer
	if code := backupReport(context.Background(), st, svc, stdin, &out, &errb); code != 0 {
		t.Fatalf("code, stderr %q", errb.String())
	}
	if st.runs[0].Status != backupschedule.StatusPartial {
		t.Fatalf("status = %q, want partial", st.runs[0].Status)
	}
	if len(al.fired) != 1 || !strings.HasPrefix(al.fired[0], "warning:") {
		t.Fatalf("fired = %v", al.fired)
	}
}

func TestBackupReportNoDestinationsIsFailure(t *testing.T) {
	st := &fakeBackupStore{tenant: uuid.New()}
	al := &fakeBackupAlerter{}
	svc := &backupschedule.Service{Store: st, Alerts: al}
	stdin := strings.NewReader(`{"trigger":"scheduled","started_at":"2026-09-27T03:00:00Z","finished_at":"2026-09-27T03:00:05Z",
		"destinations":[],"error":"dumping the database: exit status 1"}`)
	var out, errb bytes.Buffer
	if code := backupReport(context.Background(), st, svc, stdin, &out, &errb); code != 0 {
		t.Fatalf("code, stderr %q", errb.String())
	}
	if st.runs[0].Status != backupschedule.StatusFailure || st.runs[0].Error == "" {
		t.Fatalf("runs = %+v", st.runs)
	}
	if len(al.fired) != 1 || !strings.HasPrefix(al.fired[0], "critical:") {
		t.Fatalf("fired = %v", al.fired)
	}
}

func TestBackupReportRejectsBadTrigger(t *testing.T) {
	st := &fakeBackupStore{tenant: uuid.New()}
	svc := &backupschedule.Service{Store: st}
	stdin := strings.NewReader(`{"trigger":"whenever","destinations":[]}`)
	var out, errb bytes.Buffer
	if code := backupReport(context.Background(), st, svc, stdin, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

func TestBackupReportRejectsInvalidJSON(t *testing.T) {
	st := &fakeBackupStore{tenant: uuid.New()}
	svc := &backupschedule.Service{Store: st}
	var out, errb bytes.Buffer
	if code := backupReport(context.Background(), st, svc, strings.NewReader("not json"), &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
