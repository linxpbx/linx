package main

import (
	"bytes"
	"context"
	"encoding/json"
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

// fakeRestoreStore holds one restore request, its password "sealed" as
// plain text (the sealing itself is internal/backupschedule's to test).
type fakeRestoreStore struct {
	req    *backupschedule.Restore
	sealed []byte
	audits []auth.AuditEntry
}

func (f *fakeRestoreStore) SetupCompleted(context.Context) (bool, error) { return false, nil }
func (f *fakeRestoreStore) Restore(context.Context, uuid.UUID) (backupschedule.Restore, bool, error) {
	if f.req == nil {
		return backupschedule.Restore{}, false, nil
	}
	return *f.req, true, nil
}
func (f *fakeRestoreStore) PutRestore(_ context.Context, r backupschedule.Restore, sealed []byte, _ auth.AuditEntry) error {
	f.req, f.sealed = &r, sealed
	return nil
}
func (f *fakeRestoreStore) TakeRestore(_ context.Context, now time.Time) (backupschedule.Restore, []byte, bool, error) {
	if f.req == nil || f.req.Status != backupschedule.RestorePending {
		return backupschedule.Restore{}, nil, false, nil
	}
	f.req.Status, f.req.UpdatedAt = backupschedule.RestoreRunning, now
	s := f.sealed
	f.sealed = nil
	return *f.req, s, true, nil
}
func (f *fakeRestoreStore) FailRestore(_ context.Context, id uuid.UUID, msg string, _ time.Time) error {
	if f.req != nil && f.req.ID == id {
		f.req.Status, f.req.Error = backupschedule.RestoreFailed, msg
	}
	return nil
}
func (f *fakeRestoreStore) RecordRestored(_ context.Context, a auth.AuditEntry) error {
	f.audits = append(f.audits, a)
	return nil
}

type plainSealer struct{}

func (plainSealer) Seal(_ string, p []byte) ([]byte, error) { return p, nil }
func (plainSealer) Open(_ string, b []byte) ([]byte, error) { return b, nil }

func TestBackupRestoreCommands(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	rs := &fakeRestoreStore{
		req: &backupschedule.Restore{ID: id, Source: "folder", Location: "/var/backups/linx", Snapshot: "latest",
			Status: backupschedule.RestorePending, RequestedBy: "user:u1"},
		sealed: []byte("pw-123"),
	}
	st := &fakeBackupStore{schedule: backupschedule.Schedule{Frequency: backupschedule.FrequencyDaily}}
	svc := &backupschedule.Service{Store: st, Restores: rs, Sealer: plainSealer{}, Now: time.Now}
	var out, errb bytes.Buffer

	if code := backupPending(context.Background(), svc, &out, &errb); code != 0 || out.String() != "run restore\n" {
		t.Fatalf("pending: code %d, out %q, stderr %q", code, out.String(), errb.String())
	}
	out.Reset()
	if code := restoreTake(context.Background(), svc, &out, &errb); code != 0 {
		t.Fatalf("restore-take: code %d, stderr %q", code, errb.String())
	}
	var got restoreRequestJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.ID != id || got.Password != "pw-123" || got.Location != "/var/backups/linx" {
		t.Fatalf("restore-take printed %q (%v)", out.String(), err)
	}
	out.Reset()
	if code := restoreTake(context.Background(), svc, &out, &errb); code != 0 || out.Len() != 0 {
		t.Fatalf("second restore-take: code %d, out %q", code, out.String())
	}

	in := strings.NewReader(`{"id":"` + id.String() + `","error":"wrong password"}`)
	if code := restoreFailed(context.Background(), svc, in, &out, &errb); code != 0 || rs.req.Error != "wrong password" {
		t.Fatalf("restore-failed: code %d, request %+v, stderr %q", code, rs.req, errb.String())
	}
	if code := restoreFailed(context.Background(), svc, strings.NewReader(`{}`), &out, &errb); code != 2 {
		t.Errorf("restore-failed with no id: code %d", code)
	}

	done := strings.NewReader(`{"source":"folder","location":"/var/backups/linx","snapshot_id":"abc","snapshot_time":"2026-09-20T03:00:00Z","requested_by":"user:u1"}`)
	if code := restoreDone(context.Background(), st, svc, done, &out, &errb); code != 0 || len(rs.audits) != 1 || rs.audits[0].Action != "backup.restored" {
		t.Fatalf("restore-done: code %d, audits %+v, stderr %q", code, rs.audits, errb.String())
	}
}
