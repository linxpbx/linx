package backupschedule

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/backup"
)

// DefaultTransferDir is the control plane's transfer folder (compose volume
// backup-transfer): backup files on their way to the browser (download)
// and from it (upload), docs/BACKUP.md §8 step 5. The control plane never
// opens what's in them — they're restic-encrypted, and only
// linx-backup-agent, on the host, makes or unpacks them — it only moves
// the bytes between the browser and the agent.
const DefaultTransferDir = "/var/lib/linx/backup-transfer"

// MaxUploadSize caps an uploaded backup file: the most a backup file may
// unpack to (backup.MaxArchiveSize, about 2 GB) plus room for the tar
// format's own headers.
const MaxUploadSize = backup.MaxArchiveSize + 128<<20

// UploadKeptFor is how long an uploaded file waits for its restore request
// before it's removed.
const UploadKeptFor = 2 * time.Hour

// ErrTooLarge is returned when an upload is over MaxUploadSize.
var ErrTooLarge = errors.New("backupschedule: upload too large")

// Transfer is the transfer folder.
type Transfer struct {
	Dir string
}

func (t *Transfer) DownloadPath(id uuid.UUID) string {
	return filepath.Join(t.Dir, "download-"+id.String()+".tar")
}

func (t *Transfer) UploadPath(id uuid.UUID) string {
	return filepath.Join(t.Dir, "upload-"+id.String()+".tar")
}

// save writes r to path through a temporary file, so a half-written file
// is never taken for a whole one; at most limit bytes (0: no limit).
func (t *Transfer) save(path string, r io.Reader, limit int64) (int64, error) {
	f, err := os.CreateTemp(t.Dir, ".part-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name()) //nolint:errcheck // gone after the rename; removes a failed one
	src := r
	if limit > 0 {
		src = io.LimitReader(r, limit+1)
	}
	n, err := io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if limit > 0 && n > limit {
		return 0, ErrTooLarge
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		return 0, err
	}
	return n, os.Rename(f.Name(), path)
}

// SaveDownload stores the file linx-backup-agent made for download id.
func (t *Transfer) SaveDownload(id uuid.UUID, r io.Reader) (int64, error) {
	return t.save(t.DownloadPath(id), r, 0)
}

// SaveUpload stores an uploaded backup file under a new id, replacing any
// earlier upload (only one restore can be waiting at a time).
func (t *Transfer) SaveUpload(r io.Reader) (uuid.UUID, int64, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, 0, err
	}
	n, err := t.save(t.UploadPath(id), r, MaxUploadSize)
	if err != nil {
		return uuid.Nil, 0, err
	}
	t.removeMatching(func(name string, _ os.FileInfo) bool {
		return strings.HasPrefix(name, "upload-") && name != filepath.Base(t.UploadPath(id))
	})
	return id, n, nil
}

// UploadExists reports whether upload id is there.
func (t *Transfer) UploadExists(id uuid.UUID) bool {
	info, err := os.Stat(t.UploadPath(id))
	return err == nil && info.Mode().IsRegular()
}

// RemoveUpload removes upload id (the agent is done with it).
func (t *Transfer) RemoveUpload(id uuid.UUID) error {
	err := os.Remove(t.UploadPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Sweep removes every download but keep (uuid.Nil: all of them), uploads
// older than UploadKeptFor and leftover temporary files.
func (t *Transfer) Sweep(keep uuid.UUID, now time.Time) {
	keepName := ""
	if keep != uuid.Nil {
		keepName = filepath.Base(t.DownloadPath(keep))
	}
	t.removeMatching(func(name string, info os.FileInfo) bool {
		switch {
		case strings.HasPrefix(name, "download-"):
			return name != keepName
		case strings.HasPrefix(name, "upload-"), strings.HasPrefix(name, ".part-"):
			return now.Sub(info.ModTime()) > UploadKeptFor
		}
		return false
	})
}

func (t *Transfer) removeMatching(match func(name string, info os.FileInfo) bool) {
	entries, err := os.ReadDir(t.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if match(e.Name(), info) {
			_ = os.Remove(filepath.Join(t.Dir, e.Name()))
		}
	}
}
