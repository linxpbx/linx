package backupschedule

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backup"
)

// Restore statuses (docs/BACKUP.md §4). A finished restore has no status of
// its own: it replaces the whole database, this request row included, and
// is recorded in the restored database's audit log instead.
const (
	RestoreNone    = "none"
	RestorePending = "pending" // waiting for linx-backup-agent's next tick
	RestoreRunning = "running" // the agent took it
	RestoreFailed  = "failed"
)

// RestoreStaleAfter is how long a taken request may go without a report
// before it's shown as failed: the agent's own time limit is 30 minutes
// (cmd/linx-backup-agent), so after this it's no longer running.
const RestoreStaleAfter = 45 * time.Minute

// Restore is a request to restore from a backup: where from, which backup,
// and what's happened to it.
type Restore struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Source      string // backup.SourceFolder or backup.SourceDestination
	Location    string // the folder's path, or the destination's name
	Snapshot    string // "latest" or a snapshot id
	Status      string
	Error       string
	RequestedBy string
	RequestedAt time.Time
	UpdatedAt   time.Time
}

// RestoreInput is what the setup wizard sends.
type RestoreInput struct {
	Source   string
	Location string
	Snapshot string // "" means "latest"
	Password string
}

// TakenRestore is a request as the agent receives it, with its password.
type TakenRestore struct {
	Restore
	Password string
}

// Sealer seals and opens a secret stored in one database row
// (dbsecret.Sealer).
type Sealer interface {
	Seal(rowID string, plaintext []byte) ([]byte, error)
	Open(rowID string, blob []byte) ([]byte, error)
}

// RestoreStore is the database access restoring needs (internal/store
// implements it).
type RestoreStore interface {
	// SetupCompleted reports whether the setup wizard has been finished.
	SetupCompleted(ctx context.Context) (bool, error)
	// Restore returns the tenant's request; found is false if there's none.
	Restore(ctx context.Context, tenant uuid.UUID) (r Restore, found bool, err error)
	// PutRestore replaces the tenant's request (there's only ever one).
	PutRestore(ctx context.Context, r Restore, passwordEnc []byte, audit auth.AuditEntry) error
	// TakeRestore marks a pending request running, clears its sealed
	// password and returns it (with the sealed password as it was); found
	// is false if nothing is pending.
	TakeRestore(ctx context.Context, now time.Time) (r Restore, passwordEnc []byte, found bool, err error)
	// FailRestore marks request id failed with message; a no-op if that
	// request isn't there (a restore that got as far as replacing the
	// database took this row with it).
	FailRestore(ctx context.Context, id uuid.UUID, message string, now time.Time) error
	// RecordRestored writes the audit entry for a finished restore.
	RecordRestored(ctx context.Context, audit auth.AuditEntry) error
}

func restoreRowID(id uuid.UUID) string { return "backup_restore:" + id.String() }

var (
	errRestoreSystemAdmin = &apihttp.Error{Status: http.StatusForbidden, Code: "system_admin_only",
		Detail: "Only a system admin, signed in, can restore from a backup."}
	errRestoreAfterSetup = &apihttp.Error{Status: http.StatusConflict, Code: "restore_after_setup",
		Detail: "Restoring from the browser is offered only before setup is finished. On a running server, use sudo linx restore."}
	errRestoreInProgress = &apihttp.Error{Status: http.StatusConflict, Code: "restore_in_progress",
		Detail: "A restore is already under way."}
	errNoRestoreStore = errors.New("backupschedule: RestoreStore or Sealer not set")
)

// displayed returns r with a request that's been running too long shown as
// failed.
func (s *Service) displayed(r Restore) Restore {
	if r.Status == RestoreRunning && s.now().Sub(r.UpdatedAt) > RestoreStaleAfter {
		r.Status = RestoreFailed
		r.Error = "The server stopped reporting on this restore. Check sudo journalctl -u linx-backup-agent on the server, then try again."
	}
	return r
}

// RestoreStatus returns the tenant's restore request; Status is RestoreNone
// if there isn't one.
func (s *Service) RestoreStatus(ctx context.Context) (Restore, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Restore{}, errNoPrincipal
	}
	if s.Restores == nil {
		return Restore{}, errNoRestoreStore
	}
	r, found, err := s.Restores.Restore(ctx, p.TenantID)
	if err != nil {
		return Restore{}, err
	}
	if !found {
		return Restore{TenantID: p.TenantID, Status: RestoreNone}, nil
	}
	return s.displayed(r), nil
}

// RequestRestore asks linx-backup-agent to restore everything from a backup
// (docs/BACKUP.md §4): only a system admin's own session, freshly
// confirmed, and only before setup is finished — it replaces every person,
// setting and key on this server.
func (s *Service) RequestRestore(ctx context.Context, in RestoreInput) (Restore, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Restore{}, errNoPrincipal
	}
	if s.Restores == nil || s.Sealer == nil {
		return Restore{}, errNoRestoreStore
	}
	if _, isSession := auth.SessionFromContext(ctx); !isSession || p.Role != auth.RoleSystemAdmin {
		return Restore{}, errRestoreSystemAdmin
	}
	if in.Snapshot == "" {
		in.Snapshot = "latest"
	}
	if err := backup.CheckRestoreSource(in.Source, in.Location); err != nil {
		return Restore{}, invalid("restore_location_invalid", capitalize(err.Error())+".")
	}
	if err := backup.CheckSnapshot(in.Snapshot); err != nil {
		return Restore{}, invalid("restore_snapshot_invalid", capitalize(err.Error())+".")
	}
	if in.Password == "" || len(in.Password) > 1024 {
		return Restore{}, invalid("restore_password_missing", "Enter the backup's password.")
	}
	if err := auth.RequireConfirmed(ctx, s.now()); err != nil {
		return Restore{}, err
	}
	done, err := s.Restores.SetupCompleted(ctx)
	if err != nil {
		return Restore{}, err
	}
	if done {
		return Restore{}, errRestoreAfterSetup
	}
	cur, found, err := s.Restores.Restore(ctx, p.TenantID)
	if err != nil {
		return Restore{}, err
	}
	if found {
		if st := s.displayed(cur).Status; st == RestorePending || st == RestoreRunning {
			return Restore{}, errRestoreInProgress
		}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Restore{}, err
	}
	now := s.now()
	r := Restore{
		ID: id, TenantID: p.TenantID, Source: in.Source, Location: in.Location, Snapshot: in.Snapshot,
		Status: RestorePending, RequestedBy: p.Actor(), RequestedAt: now, UpdatedAt: now,
	}
	sealed, err := s.Sealer.Seal(restoreRowID(id), []byte(in.Password))
	if err != nil {
		return Restore{}, err
	}
	_, a, err := audit(ctx, "backup.restore_requested")
	if err != nil {
		return Restore{}, err
	}
	a.Target = "backup_restore:" + id.String()
	a.Detail = map[string]any{"source": in.Source, "location": in.Location, "snapshot": in.Snapshot}
	if err := s.Restores.PutRestore(ctx, r, sealed, a); err != nil {
		return Restore{}, err
	}
	return r, nil
}

// TakeRestore hands a pending request, password included, to
// linx-backup-agent (through the hidden CLI subcommand) and marks it
// running. found is false when nothing is pending.
func (s *Service) TakeRestore(ctx context.Context) (TakenRestore, bool, error) {
	if s.Restores == nil || s.Sealer == nil {
		return TakenRestore{}, false, errNoRestoreStore
	}
	r, sealed, found, err := s.Restores.TakeRestore(ctx, s.now())
	if err != nil || !found {
		return TakenRestore{}, false, err
	}
	password, err := s.Sealer.Open(restoreRowID(r.ID), sealed)
	if err != nil {
		// Taken already (the password is gone either way): say why.
		_ = s.Restores.FailRestore(ctx, r.ID, "The backup's password couldn't be read back. Try again.", s.now())
		return TakenRestore{}, false, err
	}
	return TakenRestore{Restore: r, Password: string(password)}, true, nil
}

// FailRestore records that request id failed, with a message for the
// wizard to show.
func (s *Service) FailRestore(ctx context.Context, id uuid.UUID, message string) error {
	if s.Restores == nil {
		return errNoRestoreStore
	}
	return s.Restores.FailRestore(ctx, id, message, s.now())
}

// RestoredInfo is what the agent reports about a finished restore.
type RestoredInfo struct {
	Source      string
	Location    string
	SnapshotID  string
	SnapshotAt  time.Time
	RequestedBy string
}

// RecordRestored writes a finished restore into the restored database's
// audit log (the request itself was in the database it replaced).
func (s *Service) RecordRestored(ctx context.Context, tenant uuid.UUID, info RestoredInfo) error {
	if s.Restores == nil {
		return errNoRestoreStore
	}
	return s.Restores.RecordRestored(ctx, auth.AuditEntry{
		TenantID: &tenant, Actor: auth.TypeSystem + ":linx-backup-agent", Action: "backup.restored",
		Target: "backup-snapshot:" + info.SnapshotID, Result: auth.ResultOK,
		Detail: map[string]any{
			"source": info.Source, "location": info.Location, "snapshot_id": info.SnapshotID,
			"snapshot_time": info.SnapshotAt, "requested_by": info.RequestedBy,
		},
	})
}

func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
