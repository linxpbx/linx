package backupschedule

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backup"
)

// Download statuses (docs/BACKUP.md §8 step 5).
const (
	DownloadNone      = "none"
	DownloadPending   = "pending"   // waiting for linx-backup-agent's next tick
	DownloadPreparing = "preparing" // the agent is backing up and making the file
	DownloadReady     = "ready"
	DownloadFailed    = "failed"
)

// DownloadKeptFor is how long a ready backup file (and its password) stays
// in the transfer folder for the admin who asked.
const DownloadKeptFor = time.Hour

// Download is a request for a backup file.
type Download struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Status       string
	Error        string
	Size         int64
	SnapshotID   string
	SnapshotTime *time.Time
	RequestedBy  string
	RequestedAt  time.Time
	UpdatedAt    time.Time
	ExpiresAt    *time.Time
}

// DownloadStore is the database access downloads need (internal/store
// implements it).
type DownloadStore interface {
	// Download returns the tenant's download request; found is false if
	// there's none.
	Download(ctx context.Context, tenant uuid.UUID) (d Download, found bool, err error)
	// PutDownload replaces the tenant's request (its password cleared).
	PutDownload(ctx context.Context, d Download, audit auth.AuditEntry) error
	// TakeDownload marks a pending request preparing and returns it; found
	// is false if nothing is pending.
	TakeDownload(ctx context.Context, now time.Time) (d Download, found bool, err error)
	// FinishDownload records request d.ID's outcome (Status ready or
	// failed), with the sealed password when ready; a no-op unless it's
	// still preparing.
	FinishDownload(ctx context.Context, d Download, passwordEnc []byte) error
	// DownloadPassword returns request id's sealed password (nil if none).
	DownloadPassword(ctx context.Context, id uuid.UUID) ([]byte, error)
	// ExpireDownloads clears the password of every ready request past its
	// expiry.
	ExpireDownloads(ctx context.Context, now time.Time) error
	// WriteAudit writes one audit entry.
	WriteAudit(ctx context.Context, audit auth.AuditEntry) error
}

func downloadRowID(id uuid.UUID) string { return "backup_download:" + id.String() }

var (
	errDownloadInProgress = &apihttp.Error{Status: http.StatusConflict, Code: "download_in_progress",
		Detail: "A backup file is already being made."}
	errDownloadNotReady = &apihttp.Error{Status: http.StatusNotFound, Code: "download_not_ready",
		Detail: "There's no backup file ready for you. Ask for one first."}
	errNoDownloadStore = errors.New("backupschedule: Downloads, Sealer or Transfer not set")
)

// downloadStale is how long a download may go preparing without a report
// before it's shown as failed (the agent's own limit is 30 minutes).
const downloadStale = RestoreStaleAfter

// shownDownload returns d as the caller should see it: a request preparing
// too long is failed, an expired one is gone.
func (s *Service) shownDownload(d Download) Download {
	now := s.now()
	switch {
	case d.Status == DownloadPreparing && now.Sub(d.UpdatedAt) > downloadStale:
		d.Status, d.Error = DownloadFailed, "The server stopped reporting on this backup file. Check sudo journalctl -u linx-backup-agent on the server, then try again."
	case d.Status == DownloadReady && d.ExpiresAt != nil && !now.Before(*d.ExpiresAt):
		return Download{TenantID: d.TenantID, Status: DownloadNone}
	}
	return d
}

// DownloadStatus returns the tenant's backup-file request (Status
// DownloadNone if there isn't one). mine says whether the caller asked for
// it: only they may fetch the file or its password.
func (s *Service) DownloadStatus(ctx context.Context) (d Download, mine bool, err error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return Download{}, false, errNoPrincipal
	}
	if s.Downloads == nil {
		return Download{}, false, errNoDownloadStore
	}
	cur, found, err := s.Downloads.Download(ctx, p.TenantID)
	if err != nil {
		return Download{}, false, err
	}
	if !found {
		return Download{TenantID: p.TenantID, Status: DownloadNone}, false, nil
	}
	cur = s.shownDownload(cur)
	return cur, cur.Status != DownloadNone && cur.RequestedBy == p.Actor(), nil
}

// RequestDownload asks linx-backup-agent for a backup file: it backs up
// now, then packs the backups kept on the server into one file for the
// caller (docs/BACKUP.md §8 step 5). A session confirms it's them first
// (docs/BACKUP.md §6: whoever holds the file and its password holds the
// whole system).
func (s *Service) RequestDownload(ctx context.Context) (Download, error) {
	p, a, err := audit(ctx, "backup.download_requested")
	if err != nil {
		return Download{}, err
	}
	if s.Downloads == nil || s.Transfer == nil {
		return Download{}, errNoDownloadStore
	}
	if err := auth.RequireConfirmed(ctx, s.now()); err != nil {
		return Download{}, err
	}
	cur, found, err := s.Downloads.Download(ctx, p.TenantID)
	if err != nil {
		return Download{}, err
	}
	if found {
		if st := s.shownDownload(cur).Status; st == DownloadPending || st == DownloadPreparing {
			return Download{}, errDownloadInProgress
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Download{}, err
	}
	now := s.now()
	d := Download{ID: id, TenantID: p.TenantID, Status: DownloadPending, RequestedBy: p.Actor(), RequestedAt: now, UpdatedAt: now}
	a.Target = "backup_download:" + id.String()
	if err := s.Downloads.PutDownload(ctx, d, a); err != nil {
		return Download{}, err
	}
	s.Transfer.Sweep(uuid.Nil, now)
	return d, nil
}

// mineReady returns the caller's ready download, or errDownloadNotReady.
func (s *Service) mineReady(ctx context.Context) (Download, error) {
	d, mine, err := s.DownloadStatus(ctx)
	if err != nil {
		return Download{}, err
	}
	if !mine || d.Status != DownloadReady {
		return Download{}, errDownloadNotReady
	}
	return d, nil
}

// OpenDownload opens the caller's ready backup file for sending to the
// browser. The caller closes it.
func (s *Service) OpenDownload(ctx context.Context) (*os.File, Download, error) {
	d, err := s.mineReady(ctx)
	if err != nil {
		return nil, Download{}, err
	}
	f, err := os.Open(s.Transfer.DownloadPath(d.ID))
	if err != nil {
		return nil, Download{}, errDownloadNotReady
	}
	_, a, err := audit(ctx, "backup.downloaded")
	if err == nil {
		a.Target = "backup_download:" + d.ID.String()
		err = s.Downloads.WriteAudit(ctx, a)
	}
	if err != nil {
		f.Close()
		return nil, Download{}, err
	}
	return f, d, nil
}

// DownloadPassword shows the caller the password their backup file needs:
// the repository's own (docs/BACKUP.md §6: showing it again needs "confirm
// it's you").
func (s *Service) DownloadPassword(ctx context.Context) (string, error) {
	d, err := s.mineReady(ctx)
	if err != nil {
		return "", err
	}
	if err := auth.RequireConfirmed(ctx, s.now()); err != nil {
		return "", err
	}
	sealed, err := s.Downloads.DownloadPassword(ctx, d.ID)
	if err != nil {
		return "", err
	}
	if sealed == nil {
		return "", errDownloadNotReady
	}
	pw, err := s.Sealer.Open(downloadRowID(d.ID), sealed)
	if err != nil {
		return "", err
	}
	_, a, err := audit(ctx, "backup.password_shown")
	if err != nil {
		return "", err
	}
	a.Target = "backup_download:" + d.ID.String()
	if err := s.Downloads.WriteAudit(ctx, a); err != nil {
		return "", err
	}
	return string(pw), nil
}

// TakeDownload hands a pending request to linx-backup-agent and marks it
// preparing.
func (s *Service) TakeDownload(ctx context.Context) (Download, bool, error) {
	if s.Downloads == nil {
		return Download{}, false, errNoDownloadStore
	}
	return s.Downloads.TakeDownload(ctx, s.now())
}

// PutDownloadFile stores the file the agent made for request id.
func (s *Service) PutDownloadFile(ctx context.Context, id uuid.UUID, r io.Reader) (int64, error) {
	if s.Downloads == nil || s.Transfer == nil {
		return 0, errNoDownloadStore
	}
	tenant, err := s.Store.DefaultTenant(ctx)
	if err != nil {
		return 0, err
	}
	d, found, err := s.Downloads.Download(ctx, tenant)
	if err != nil {
		return 0, err
	}
	if !found || d.ID != id || d.Status != DownloadPreparing {
		return 0, errors.New("that backup file isn't being made any more")
	}
	return s.Transfer.SaveDownload(id, r)
}

// DownloadResult is what the agent reports about a backup file.
type DownloadResult struct {
	ID           uuid.UUID
	OK           bool
	Error        string
	SnapshotID   string
	SnapshotTime time.Time
	Password     string
}

// FinishDownload records the agent's outcome for a backup file: ready (the
// file is in the transfer folder, its password sealed) or failed.
func (s *Service) FinishDownload(ctx context.Context, res DownloadResult) error {
	if s.Downloads == nil || s.Transfer == nil || s.Sealer == nil {
		return errNoDownloadStore
	}
	now := s.now()
	d := Download{ID: res.ID, UpdatedAt: now}
	var sealed []byte
	info, statErr := os.Stat(s.Transfer.DownloadPath(res.ID))
	switch {
	case !res.OK:
		d.Status, d.Error = DownloadFailed, res.Error
	case statErr != nil:
		d.Status, d.Error = DownloadFailed, "The backup file didn't arrive. Try again."
	case res.Password == "":
		d.Status, d.Error = DownloadFailed, "The backup's password didn't arrive. Try again."
	default:
		var err error
		if sealed, err = s.Sealer.Seal(downloadRowID(res.ID), []byte(res.Password)); err != nil {
			return err
		}
		exp := now.Add(DownloadKeptFor)
		st := res.SnapshotTime
		d.Status, d.Size, d.SnapshotID, d.SnapshotTime, d.ExpiresAt = DownloadReady, info.Size(), res.SnapshotID, &st, &exp
	}
	if d.Status == DownloadFailed && d.Error == "" {
		d.Error = "The backup file couldn't be made."
	}
	if err := s.Downloads.FinishDownload(ctx, d, sealed); err != nil {
		return err
	}
	if d.Status != DownloadReady {
		_ = os.Remove(s.Transfer.DownloadPath(res.ID))
	}
	return nil
}

// SweepDownloads removes expired backup files and their passwords, and
// uploads nobody restored. The control plane runs it every few minutes.
func (s *Service) SweepDownloads(ctx context.Context) error {
	if s.Downloads == nil || s.Transfer == nil {
		return nil
	}
	now := s.now()
	if err := s.Downloads.ExpireDownloads(ctx, now); err != nil {
		return err
	}
	tenant, err := s.Store.DefaultTenant(ctx)
	if err != nil {
		return err
	}
	keep := uuid.Nil
	d, found, err := s.Downloads.Download(ctx, tenant)
	if err != nil {
		return err
	}
	if found {
		if shown := s.shownDownload(d); shown.Status == DownloadReady || shown.Status == DownloadPreparing {
			keep = d.ID
		}
	}
	s.Transfer.Sweep(keep, now)
	return nil
}

// AcceptUpload stores a backup file uploaded from the setup wizard for a
// restore (docs/BACKUP.md §8 step 5), under the same rules as asking for
// the restore itself. The file isn't opened here: linx-backup-agent
// unpacks it on the host.
func (s *Service) AcceptUpload(ctx context.Context, r io.Reader) (uuid.UUID, int64, error) {
	if s.Transfer == nil {
		return uuid.Nil, 0, errNoDownloadStore
	}
	if _, err := s.restoreAllowed(ctx); err != nil {
		return uuid.Nil, 0, err
	}
	id, n, err := s.Transfer.SaveUpload(r)
	if errors.Is(err, ErrTooLarge) {
		return uuid.Nil, 0, &apihttp.Error{Status: http.StatusRequestEntityTooLarge, Code: "upload_too_large",
			Detail: "That file is larger than a Linx backup file can be (about 2 GB)."}
	}
	if err != nil {
		return uuid.Nil, 0, err
	}
	_, a, err := audit(ctx, "backup.file_uploaded")
	if err != nil {
		return uuid.Nil, 0, err
	}
	a.Target = "backup_upload:" + id.String()
	a.Detail = map[string]any{"size": n}
	if s.Downloads != nil {
		if err := s.Downloads.WriteAudit(ctx, a); err != nil {
			return uuid.Nil, 0, err
		}
	}
	return id, n, nil
}

// ReadUpload opens upload id for linx-backup-agent.
func (s *Service) ReadUpload(id uuid.UUID) (*os.File, error) {
	if s.Transfer == nil {
		return nil, errNoDownloadStore
	}
	if err := backup.CheckUploadID(id.String()); err != nil {
		return nil, err
	}
	return os.Open(s.Transfer.UploadPath(id))
}

// RemoveUpload removes upload id once the agent is done with it.
func (s *Service) RemoveUpload(id uuid.UUID) error {
	if s.Transfer == nil {
		return errNoDownloadStore
	}
	return s.Transfer.RemoveUpload(id)
}
