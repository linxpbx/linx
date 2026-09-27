package backupschedule

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

type fakeStore struct {
	schedule    Schedule
	lastRun     *time.Time
	runs        []Run
	updateCalls int
	requestArgs []time.Time
}

func (f *fakeStore) Schedule(ctx context.Context) (Schedule, error) { return f.schedule, nil }

func (f *fakeStore) UpdateSchedule(ctx context.Context, s Schedule, audit auth.AuditEntry) (Schedule, error) {
	f.updateCalls++
	f.schedule = s
	return s, nil
}

func (f *fakeStore) RequestRun(ctx context.Context, at time.Time, by string, audit auth.AuditEntry) error {
	f.requestArgs = append(f.requestArgs, at)
	f.schedule.RequestedAt, f.schedule.RequestedBy = &at, by
	return nil
}

func (f *fakeStore) LastRunStartedAt(ctx context.Context) (*time.Time, error) { return f.lastRun, nil }

func (f *fakeStore) InsertRun(ctx context.Context, r Run) error {
	f.runs = append(f.runs, r)
	f.schedule.RequestedAt = nil
	return nil
}

func (f *fakeStore) ListRuns(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]Run, error) {
	return f.runs, nil
}

func (f *fakeStore) DefaultTenant(ctx context.Context) (uuid.UUID, error) { return uuid.Nil, nil }

type fakeAlerter struct {
	fired    []string
	resolved []string
}

func (f *fakeAlerter) Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error {
	f.fired = append(f.fired, severity+":"+key)
	return nil
}

func (f *fakeAlerter) Resolve(ctx context.Context, tenant uuid.UUID, key string) error {
	f.resolved = append(f.resolved, key)
	return nil
}

func withPrincipal(ctx context.Context) context.Context {
	return auth.WithPrincipal(ctx, auth.SystemPrincipal(uuid.New()))
}

func TestDueAtDaily(t *testing.T) {
	sched := Schedule{Frequency: FrequencyDaily, TimeOfDay: 180} // 03:00
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	got := dueAt(sched, now)
	want := time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("dueAt() = %v, want %v", got, want)
	}
	// Before today's time: falls back to yesterday.
	now2 := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)
	got2 := dueAt(sched, now2)
	want2 := time.Date(2026, 9, 26, 3, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Fatalf("dueAt() = %v, want %v", got2, want2)
	}
}

func TestDueAtWeekly(t *testing.T) {
	// 2026-09-27 is a Sunday. Schedule: Wednesday (3) at 03:00.
	sched := Schedule{Frequency: FrequencyWeekly, TimeOfDay: 180, DayOfWeek: 3}
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) // Sunday
	got := dueAt(sched, now)
	want := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC) // preceding Wednesday
	if !got.Equal(want) {
		t.Fatalf("dueAt() = %v, want %v", got, want)
	}
	// Exactly on the scheduled weekday, before the time of day: falls back a full week.
	now2 := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC) // Wednesday, 01:00
	got2 := dueAt(sched, now2)
	want2 := time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Fatalf("dueAt() = %v, want %v", got2, want2)
	}
}

func TestDueAtMonthly(t *testing.T) {
	sched := Schedule{Frequency: FrequencyMonthly, TimeOfDay: 180, DayOfMonth: 15}
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	got := dueAt(sched, now)
	want := time.Date(2026, 9, 15, 3, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("dueAt() = %v, want %v", got, want)
	}
	// Before this month's day: falls back to last month's.
	now2 := time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC)
	got2 := dueAt(sched, now2)
	want2 := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Fatalf("dueAt() = %v, want %v", got2, want2)
	}
}

func TestPendingOff(t *testing.T) {
	st := &fakeStore{schedule: Schedule{Frequency: FrequencyOff}}
	s := &Service{Store: st, Now: func() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) }}
	due, trigger, err := s.Pending(context.Background())
	if err != nil || due || trigger != "" {
		t.Fatalf("Pending() = %v, %q, %v", due, trigger, err)
	}
}

func TestPendingManualRequestWins(t *testing.T) {
	at := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
	st := &fakeStore{schedule: Schedule{Frequency: FrequencyOff, RequestedAt: &at}}
	s := &Service{Store: st, Now: func() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) }}
	due, trigger, err := s.Pending(context.Background())
	if err != nil || !due || trigger != TriggerManual {
		t.Fatalf("Pending() = %v, %q, %v", due, trigger, err)
	}
}

func TestPendingScheduledNeverRun(t *testing.T) {
	st := &fakeStore{schedule: Schedule{Frequency: FrequencyDaily, TimeOfDay: 180}}
	s := &Service{Store: st, Now: func() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) }}
	due, trigger, err := s.Pending(context.Background())
	if err != nil || !due || trigger != TriggerScheduled {
		t.Fatalf("Pending() = %v, %q, %v", due, trigger, err)
	}
}

func TestPendingAlreadyCoveredThisPeriod(t *testing.T) {
	last := time.Date(2026, 9, 27, 3, 5, 0, 0, time.UTC) // ran just after today's 03:00 target
	st := &fakeStore{schedule: Schedule{Frequency: FrequencyDaily, TimeOfDay: 180}, lastRun: &last}
	s := &Service{Store: st, Now: func() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) }}
	due, _, err := s.Pending(context.Background())
	if err != nil || due {
		t.Fatalf("Pending() = %v, %v, want not due (already ran this period)", due, err)
	}
}

func TestPendingBeforeTodaysTimeStillCoversYesterdays(t *testing.T) {
	// now is 02:00, before today's 03:00 target: dueAt falls back to
	// yesterday's occurrence, which (with no prior run) is still uncovered.
	st := &fakeStore{schedule: Schedule{Frequency: FrequencyDaily, TimeOfDay: 180}}
	s := &Service{Store: st, Now: func() time.Time { return time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC) }}
	due, trigger, err := s.Pending(context.Background())
	if err != nil || !due || trigger != TriggerScheduled {
		t.Fatalf("Pending() = %v, %q, %v, want due (yesterday's occurrence uncovered)", due, trigger, err)
	}
}

func TestUpdateValidatesFrequency(t *testing.T) {
	st := &fakeStore{}
	s := &Service{Store: st}
	bad := "weirdly"
	ctx := withPrincipal(context.Background())
	if _, err := s.Update(ctx, Patch{Frequency: &bad}); err == nil {
		t.Fatal("Update() with a bad frequency should fail")
	}
}

func TestUpdateValidatesRanges(t *testing.T) {
	st := &fakeStore{}
	s := &Service{Store: st}
	ctx := withPrincipal(context.Background())
	tooLate := 1500
	if _, err := s.Update(ctx, Patch{TimeOfDay: &tooLate}); err == nil {
		t.Fatal("Update() with an out-of-range time_of_day should fail")
	}
	tooHighDOW := 9
	if _, err := s.Update(ctx, Patch{DayOfWeek: &tooHighDOW}); err == nil {
		t.Fatal("Update() with an out-of-range day_of_week should fail")
	}
	tooHighDOM := 31
	if _, err := s.Update(ctx, Patch{DayOfMonth: &tooHighDOM}); err == nil {
		t.Fatal("Update() with an out-of-range day_of_month should fail")
	}
}

func TestUpdateApplies(t *testing.T) {
	st := &fakeStore{}
	s := &Service{Store: st}
	ctx := withPrincipal(context.Background())
	freq := FrequencyWeekly
	tod, dow := 120, 2
	out, err := s.Update(ctx, Patch{Frequency: &freq, TimeOfDay: &tod, DayOfWeek: &dow})
	if err != nil {
		t.Fatal(err)
	}
	if out.Frequency != FrequencyWeekly || out.TimeOfDay != 120 || out.DayOfWeek != 2 {
		t.Fatalf("Update() = %+v", out)
	}
	if st.updateCalls != 1 {
		t.Fatalf("UpdateSchedule called %d times, want 1", st.updateCalls)
	}
}

func TestRequestRunSetsRequestedAt(t *testing.T) {
	st := &fakeStore{}
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	s := &Service{Store: st, Now: func() time.Time { return now }}
	ctx := withPrincipal(context.Background())
	if err := s.RequestRun(ctx); err != nil {
		t.Fatal(err)
	}
	if len(st.requestArgs) != 1 || !st.requestArgs[0].Equal(now) {
		t.Fatalf("RequestRun() didn't record %v: %v", now, st.requestArgs)
	}
}

func TestRecordRunResolvesOnSuccess(t *testing.T) {
	st, al := &fakeStore{}, &fakeAlerter{}
	s := &Service{Store: st, Alerts: al}
	tenant := uuid.New()
	err := s.RecordRun(context.Background(), Run{ID: uuid.New(), TenantID: tenant, Status: StatusSuccess,
		Destinations: []Destination{{Name: "local", OK: true, SnapshotID: "abc"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(al.resolved) != 1 || al.resolved[0] != AlertKey {
		t.Fatalf("resolved = %v", al.resolved)
	}
	if len(al.fired) != 0 {
		t.Fatalf("fired = %v, want none", al.fired)
	}
	if len(st.runs) != 1 {
		t.Fatalf("InsertRun called %d times, want 1", len(st.runs))
	}
}

func TestRecordRunFiresWarningOnPartial(t *testing.T) {
	st, al := &fakeStore{}, &fakeAlerter{}
	s := &Service{Store: st, Alerts: al}
	err := s.RecordRun(context.Background(), Run{ID: uuid.New(), TenantID: uuid.New(), Status: StatusPartial,
		Destinations: []Destination{{Name: "local", OK: true}, {Name: "nas", OK: false, Error: "no route to host"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(al.fired) != 1 || al.fired[0] != "warning:"+AlertKey {
		t.Fatalf("fired = %v", al.fired)
	}
}

func TestRecordRunFiresCriticalOnFailure(t *testing.T) {
	st, al := &fakeStore{}, &fakeAlerter{}
	s := &Service{Store: st, Alerts: al}
	err := s.RecordRun(context.Background(), Run{ID: uuid.New(), TenantID: uuid.New(), Status: StatusFailure, Error: "dump failed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(al.fired) != 1 || al.fired[0] != "critical:"+AlertKey {
		t.Fatalf("fired = %v", al.fired)
	}
}
