package backupschedule

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// Service is what the API's /backup-settings and /backups endpoints do.
type Service struct {
	Store  Store
	Alerts Alerter
	// Restores and Sealer are for restoring from a backup (restore.go);
	// nil where only the schedule is needed.
	Restores RestoreStore
	Sealer   Sealer
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

func audit(ctx context.Context, action string) (auth.Principal, auth.AuditEntry, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, auth.AuditEntry{}, errNoPrincipal
	}
	return p, auth.AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: action, Target: "backup-settings", Result: auth.ResultOK,
	}, nil
}

// Get returns the tenant's backup schedule.
func (s *Service) Get(ctx context.Context) (Schedule, error) {
	if _, ok := auth.PrincipalFromContext(ctx); !ok {
		return Schedule{}, errNoPrincipal
	}
	return s.Store.Schedule(ctx)
}

// Patch is a JSON Merge Patch of the schedule; nil fields stay as they are.
type Patch struct {
	Frequency  *string
	TimeOfDay  *int
	DayOfWeek  *int
	DayOfMonth *int
}

// Update validates and applies patch.
func (s *Service) Update(ctx context.Context, patch Patch) (Schedule, error) {
	cur, err := s.Store.Schedule(ctx)
	if err != nil {
		return Schedule{}, err
	}
	if patch.Frequency != nil {
		switch *patch.Frequency {
		case FrequencyOff, FrequencyDaily, FrequencyWeekly, FrequencyMonthly:
			cur.Frequency = *patch.Frequency
		default:
			return Schedule{}, invalid("backup_frequency_invalid", `frequency must be "off", "daily", "weekly" or "monthly".`)
		}
	}
	if patch.TimeOfDay != nil {
		if *patch.TimeOfDay < 0 || *patch.TimeOfDay > 1439 {
			return Schedule{}, invalid("backup_time_of_day_invalid", "time_of_day is minutes since midnight, 0 to 1439.")
		}
		cur.TimeOfDay = *patch.TimeOfDay
	}
	if patch.DayOfWeek != nil {
		if *patch.DayOfWeek < 0 || *patch.DayOfWeek > 6 {
			return Schedule{}, invalid("backup_day_of_week_invalid", "day_of_week is 0 (Sunday) to 6 (Saturday).")
		}
		cur.DayOfWeek = *patch.DayOfWeek
	}
	if patch.DayOfMonth != nil {
		if *patch.DayOfMonth < 1 || *patch.DayOfMonth > 28 {
			return Schedule{}, invalid("backup_day_of_month_invalid", "day_of_month is 1 to 28 (so every month has that day).")
		}
		cur.DayOfMonth = *patch.DayOfMonth
	}
	_, a, err := audit(ctx, "backup.schedule_updated")
	if err != nil {
		return Schedule{}, err
	}
	a.Detail = map[string]any{"frequency": cur.Frequency, "time_of_day": cur.TimeOfDay, "day_of_week": cur.DayOfWeek, "day_of_month": cur.DayOfMonth}
	return s.Store.UpdateSchedule(ctx, cur, a)
}

// RequestRun is "back up now" (docs/BACKUP.md §1): the next tick of
// linx-backup-agent picks it up (Pending), runs it, and clears the request
// when it reports back (RecordRun).
func (s *Service) RequestRun(ctx context.Context) error {
	p, a, err := audit(ctx, "backup.requested")
	if err != nil {
		return err
	}
	return s.Store.RequestRun(ctx, s.now(), p.Actor(), a)
}

// History returns a page of past runs, newest first.
func (s *Service) History(ctx context.Context, before *uuid.UUID, limit int) ([]Run, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	return s.Store.ListRuns(ctx, p.TenantID, before, limit)
}

// Pending decides whether a backup is due right now: a pending "back up
// now" request always wins (trigger "manual"); otherwise the schedule's own
// cadence, if the most recent run started before the current period's
// scheduled instant (docs/BACKUP.md §5). A pending restore comes before
// both (trigger "restore", docs/BACKUP.md §4). No principal: called from the
// hidden CLI subcommand linx-backup-agent polls, which has no session.
func (s *Service) Pending(ctx context.Context) (due bool, trigger string, err error) {
	if s.Restores != nil {
		// A restore waiting in the setup wizard goes first, and no backup
		// runs while one is under way (it would back up a database that's
		// about to be replaced).
		tenant, err := s.Store.DefaultTenant(ctx)
		if err != nil {
			return false, "", err
		}
		r, found, err := s.Restores.Restore(ctx, tenant)
		if err != nil {
			return false, "", err
		}
		switch {
		case found && r.Status == RestorePending:
			return true, TriggerRestore, nil
		case found && s.displayed(r).Status == RestoreRunning:
			return false, "", nil
		}
	}
	sched, err := s.Store.Schedule(ctx)
	if err != nil {
		return false, "", err
	}
	if sched.RequestedAt != nil {
		return true, TriggerManual, nil
	}
	if sched.Frequency == FrequencyOff {
		return false, "", nil
	}
	now := s.now()
	target := dueAt(sched, now)
	if now.Before(target) {
		return false, "", nil
	}
	last, err := s.Store.LastRunStartedAt(ctx)
	if err != nil {
		return false, "", err
	}
	if last != nil && !last.Before(target) {
		return false, "", nil // already covered this period
	}
	return true, TriggerScheduled, nil
}

// dueAt returns the most recent instant at or before now that sched's
// cadence calls for a backup, in now's own location (server-local time;
// there's no per-tenant timezone yet).
func dueAt(sched Schedule, now time.Time) time.Time {
	loc := now.Location()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	today := midnight.Add(time.Duration(sched.TimeOfDay) * time.Minute)
	switch sched.Frequency {
	case FrequencyWeekly:
		back := int(now.Weekday()) - sched.DayOfWeek
		if back < 0 {
			back += 7
		}
		target := today.AddDate(0, 0, -back)
		if target.After(now) {
			target = target.AddDate(0, 0, -7)
		}
		return target
	case FrequencyMonthly:
		target := time.Date(now.Year(), now.Month(), sched.DayOfMonth, 0, 0, 0, 0, loc).Add(time.Duration(sched.TimeOfDay) * time.Minute)
		if target.After(now) {
			target = target.AddDate(0, -1, 0)
		}
		return target
	default: // daily
		if today.After(now) {
			today = today.AddDate(0, 0, -1)
		}
		return today
	}
}

// RecordRun stores r's outcome and fires or resolves AlertKey. Called only
// from the hidden CLI subcommand (linx-backup-agent's report), never from
// an authenticated request, so there's no "confirm it's you" or audit entry
// here — the same reasoning as a trunk's status column.
func (s *Service) RecordRun(ctx context.Context, r Run) error {
	if err := s.Store.InsertRun(ctx, r); err != nil {
		return err
	}
	if s.Alerts == nil {
		return nil
	}
	switch r.Status {
	case StatusSuccess:
		return s.Alerts.Resolve(ctx, r.TenantID, AlertKey)
	case StatusPartial:
		return s.Alerts.Fire(ctx, r.TenantID, AlertKey, "warning", "A backup destination failed",
			fmt.Sprintf("At least one backup destination failed: %s.", failedNames(r.Destinations)), "")
	default: // failure
		msg := r.Error
		if msg == "" {
			msg = fmt.Sprintf("Every backup destination failed: %s.", failedNames(r.Destinations))
		}
		return s.Alerts.Fire(ctx, r.TenantID, AlertKey, "critical", "Backup failed", msg, "")
	}
}

func failedNames(dests []Destination) string {
	out := ""
	for _, d := range dests {
		if d.OK {
			continue
		}
		if out != "" {
			out += ", "
		}
		out += d.Name
	}
	if out == "" {
		return "no destination is configured"
	}
	return out
}
