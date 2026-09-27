// Package backupschedule is the admin-portal side of backup and restore
// (docs/BACKUP.md §5, §8 step 3): the schedule an admin sets, the history of
// what actually ran, and deciding when a run is due. It never runs restic
// itself and never touches a repository — that stays entirely in
// internal/backup and the host's `linx backup` (docs/BACKUP.md §7: this
// package's own process, the control plane, has no way to run a host
// command). The bridge is `linx-backup-agent` (internal/backupagent),
// which polls this package's Pending/RecordRun through a hidden CLI
// subcommand (services/control-plane/backup_cmd.go), the same pattern
// internal/firewallsync already uses for a different host/container
// boundary.
package backupschedule

import (
	"context"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Frequency values (docs/BACKUP.md §5).
const (
	FrequencyOff     = "off"
	FrequencyDaily   = "daily"
	FrequencyWeekly  = "weekly"
	FrequencyMonthly = "monthly"
)

// Trigger values: why a run happened.
const (
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
	// TriggerRestore is Pending's answer when a restore is waiting; never
	// a Run's trigger.
	TriggerRestore = "restore"
	// TriggerExport is Pending's answer when a backup file was asked for
	// (docs/BACKUP.md §8 step 5): the agent backs up (reported as a manual
	// run) and then makes the file. Never a Run's trigger either.
	TriggerExport = "export"
)

// Status values: how a run went.
const (
	StatusSuccess = "success" // every destination worked
	StatusPartial = "partial" // at least one worked, at least one didn't
	StatusFailure = "failure" // none did (including the dump itself failing)
)

// AlertKey is the "backup failure" alert's key (docs/API.md §2's catalog
// entry, unused until this step). One alert for the whole tenant, not per
// destination: docs/BACKUP.md §5 describes a single alert for "a failed
// scheduled backup".
const AlertKey = "backup.failure"

// Schedule is the tenant's backup schedule (docs/BACKUP.md §5). TimeOfDay is
// minutes since midnight, server-local time. DayOfWeek is 0 (Sunday) to 6
// (Saturday), used only when Frequency is weekly. DayOfMonth is 1 to 28
// (never 29-31, so every month has that day), used only when monthly.
type Schedule struct {
	Frequency   string
	TimeOfDay   int
	DayOfWeek   int
	DayOfMonth  int
	RequestedAt *time.Time
	RequestedBy string
}

// Destination is one destination's outcome within a Run.
type Destination struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	// Size is the bytes backed up (the dump plus the keys), before
	// restic's compression and deduplication.
	Size  int64  `json:"size,omitempty"`
	Error string `json:"error,omitempty"`
}

// Run is one backup attempt's history entry.
type Run struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Trigger      string
	StartedAt    time.Time
	FinishedAt   time.Time
	Status       string
	Destinations []Destination
	// Error is set when Status is failure and no destination even ran (the
	// dump itself failed); otherwise each Destination carries its own error.
	Error string
}

// Store is the database access this package needs (internal/store
// implements it).
type Store interface {
	// Schedule and UpdateSchedule read and write the single schedule row
	// (pbx_setting, like every other admin-portal setting).
	Schedule(ctx context.Context) (Schedule, error)
	UpdateSchedule(ctx context.Context, s Schedule, audit auth.AuditEntry) (Schedule, error)
	// RequestRun sets RequestedAt/RequestedBy ("back up now"); audit is
	// still written even though nothing else about the schedule changed.
	RequestRun(ctx context.Context, at time.Time, by string, audit auth.AuditEntry) error
	// LastRunStartedAt is the most recent run's start time, for deciding
	// whether a scheduled run is due; nil if there's never been one.
	LastRunStartedAt(ctx context.Context) (*time.Time, error)
	// InsertRun records a completed attempt, clears any pending request,
	// and fires or resolves AlertKey — all in one transaction. tenant comes
	// from the caller (DefaultTenant when there's no session, e.g. from the
	// hidden CLI subcommand `linx-control-plane backup report`).
	InsertRun(ctx context.Context, r Run) error
	// ListRuns returns a page of history, newest first.
	ListRuns(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]Run, error)
	// DefaultTenant is store.Store.DefaultTenant: the one tenant a
	// system-driven caller (no session) acts as.
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
}

// Alerter is the part of the alert engine this package uses.
type Alerter interface {
	Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
	Resolve(ctx context.Context, tenant uuid.UUID, key string) error
}
