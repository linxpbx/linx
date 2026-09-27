// Package backup wraps restic (docs/BACKUP.md, ADR-055) for Linx's own
// backup and restore: a database dump and the Docker secrets a restore
// can't regenerate, sealed together in one restic repository so the two
// files a backup produces are always used as a pair.
package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Runner runs restic, writing its standard output to stdout (nil discards
// it) with env appended to the process's own environment (nil needs
// nothing extra; only an S3 destination's credentials do). The returned
// error, if any, must already read as a complete, human-readable message
// (its caller's stderr folded in) — this package never inspects a
// separate stderr stream, only the error text.
type Runner func(ctx context.Context, stdout io.Writer, env []string, name string, args ...string) error

// Target is where one restic operation runs: which repository, its
// password, and any extra environment variables its backend needs (an
// S3-compatible destination's credentials; nil for local and SFTP, which
// carry everything they need in Repo and Extra restic options instead).
type Target struct {
	Repo     string
	Password string
	Env      []string
	// Extra is additional restic global options (e.g. an SFTP
	// destination's "-o sftp.command=...", docs/BACKUP.md §3), inserted
	// right after --password-file.
	Extra []string
}

// KeysDirName is the fixed directory name secret files are stored under
// inside a snapshot, so restore knows what to ask restic to extract without
// depending on the host's own secrets path (which can differ between the
// server that made the backup and the one restoring it).
const KeysDirName = "linx-keys"

// StagingDir is where `linx backup` stages the database dump and secret
// files before handing them to restic, and where `linx restore`
// asks restic to extract them back to. A fixed path, not a random temp
// directory: restic stores the exact path it was given, and a restore
// often runs on a different, freshly installed server that only shares
// this constant with the one that made the backup, not a directory name
// (docs/BACKUP.md §2).
const StagingDir = "/var/lib/linx/backup-staging"

// KeysPath is exactly where secret files are staged at backup time and
// extracted to at restore time (StagingDir/KeysDirName).
var KeysPath = filepath.Join(StagingDir, KeysDirName)

// PairTag names the tag every backup snapshot carries: the pair id shared
// by its database dump and its keys, so a restore can refuse to mix a
// keys file from one backup with a database from another (docs/BACKUP.md
// §2 — that combination leaves every sealed secret unrecoverable).
const PairTag = "pair"

// NewPassword returns a new repository password: generated, never
// admin-chosen (ADR-055, matching a device's SIP password, ADR-033).
func NewPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewPairID returns a new pair id for one backup's dump and keys.
func NewPairID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

// passwordFile writes password to a private temp file restic reads with
// --password-file, so the password is never a process argument another
// user on the host could see in `ps`. cleanup removes it; always call it.
func passwordFile(password string) (path string, cleanup func(), err error) {
	f, err := os.CreateTemp("", "linx-backup-pw-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.Remove(f.Name()) }
	if _, err := f.WriteString(password); err != nil {
		f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}

// resticArgs starts every restic command: the repository, its password
// file, and any of Target's extra global options.
func resticArgs(t Target, pf string) []string {
	args := []string{"-r", t.Repo, "--password-file", pf}
	return append(args, t.Extra...)
}

// InitRepo creates the repository unless one is already there. Whether one
// is there is asked of restic itself (`cat config`, which reads the
// repository's config with the password) rather than read from init's
// error text, which differs between restic versions ("config file already
// exists" in 0.16, Ubuntu 24.04's): a backup runs unattended on a schedule
// and mustn't fail forever after its first run.
func InitRepo(ctx context.Context, runner Runner, t Target) error {
	pf, cleanup, err := passwordFile(t.Password)
	if err != nil {
		return err
	}
	defer cleanup()
	if runner(ctx, nil, t.Env, "restic", append(resticArgs(t, pf), "cat", "config")...) == nil {
		return nil
	}
	if err := runner(ctx, nil, t.Env, "restic", append(resticArgs(t, pf), "init")...); err != nil {
		return fmt.Errorf("restic init: %w", err)
	}
	return nil
}

// summary is the line restic --json backup prints with type "summary".
type summary struct {
	MessageType         string `json:"message_type"`
	SnapshotID          string `json:"snapshot_id"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
}

// Backup runs one restic backup of dumpFile (the database dump) and
// KeysPath (the secret files staged there, docs/BACKUP.md §2), tagged
// with pairID, and returns the new snapshot's id and size: the bytes backed
// up (the dump plus the keys), before restic's compression and
// deduplication.
func Backup(ctx context.Context, runner Runner, t Target, pairID, dumpFile string) (snapshotID string, size int64, err error) {
	pf, cleanup, err := passwordFile(t.Password)
	if err != nil {
		return "", 0, err
	}
	defer cleanup()
	args := append(resticArgs(t, pf), "backup", dumpFile, KeysPath, "--tag", PairTag+":"+pairID, "--json")
	var stdout bytes.Buffer
	if err := runner(ctx, &stdout, t.Env, "restic", args...); err != nil {
		return "", 0, fmt.Errorf("restic backup: %w", err)
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		var s summary
		if json.Unmarshal([]byte(line), &s) == nil && s.MessageType == "summary" {
			snapshotID, size = s.SnapshotID, s.TotalBytesProcessed
		}
	}
	if snapshotID == "" {
		return "", 0, errors.New("restic backup didn't report a snapshot id")
	}
	return snapshotID, size, nil
}

// Forget prunes old snapshots per the retention policy, keeping the most
// recent keepLast regardless of age (docs/BACKUP.md §5's default of 14,
// changed by the caller for weekly/monthly schedules).
func Forget(ctx context.Context, runner Runner, t Target, keepLast int) error {
	pf, cleanup, err := passwordFile(t.Password)
	if err != nil {
		return err
	}
	defer cleanup()
	args := append(resticArgs(t, pf), "forget", "--keep-last", fmt.Sprint(keepLast), "--prune")
	if err := runner(ctx, nil, t.Env, "restic", args...); err != nil {
		return fmt.Errorf("restic forget: %w", err)
	}
	return nil
}

// snapshotMeta is restic snapshots --json's per-snapshot shape (the fields
// this package reads).
type snapshotMeta struct {
	ID      string    `json:"id"`
	ShortID string    `json:"short_id"`
	Time    time.Time `json:"time"`
	Tags    []string  `json:"tags"`
}

// Snapshot is one backup in a repository.
type Snapshot struct {
	// ID is restic's full snapshot id: "latest" resolved, so a restore
	// works on exactly the backup it looked at.
	ID     string
	Time   time.Time
	PairID string
}

// FindSnapshot looks up snapshotID ("latest" or an id) and returns it with
// its pair id. It fails for a snapshot with no pair tag (not a Linx backup).
func FindSnapshot(ctx context.Context, runner Runner, t Target, snapshotID string) (Snapshot, error) {
	pf, cleanup, err := passwordFile(t.Password)
	if err != nil {
		return Snapshot{}, err
	}
	defer cleanup()
	args := append(resticArgs(t, pf), "snapshots", snapshotID, "--json")
	var stdout bytes.Buffer
	if err := runner(ctx, &stdout, t.Env, "restic", args...); err != nil {
		if wrongPassword(err) {
			return Snapshot{}, ErrWrongPassword
		}
		return Snapshot{}, fmt.Errorf("restic snapshots: %w", err)
	}
	var snaps []snapshotMeta
	if err := json.Unmarshal(stdout.Bytes(), &snaps); err != nil || len(snaps) == 0 {
		return Snapshot{}, fmt.Errorf("backup %s not found", snapshotID)
	}
	m := snaps[len(snaps)-1]
	for _, tag := range m.Tags {
		if id, ok := strings.CutPrefix(tag, PairTag+":"); ok {
			return Snapshot{ID: m.ID, Time: m.Time, PairID: id}, nil
		}
	}
	return Snapshot{}, fmt.Errorf("backup %s isn't a Linx backup (it has no pair id)", snapshotID)
}

// ErrWrongPassword is FindSnapshot's answer when the password given
// doesn't open the repository, in the plain words a restore shows.
var ErrWrongPassword = errors.New("the password doesn't open this backup. Check it's the password saved with this backup")

// wrongPassword reports whether restic refused the repository's password:
// every version since 0.9 says "wrong password or no key found" (0.17 and
// later also exit with code 12, which the Runner doesn't pass on).
func wrongPassword(err error) bool {
	return strings.Contains(err.Error(), "wrong password or no key found")
}

// RestoreFiles extracts a snapshot's staging directory — the database dump
// and the keys, always together (docs/BACKUP.md §2) — into target: they
// land at target+StagingDir, restic keeping each file's original path. It
// never touches the real secrets path or the database itself; the caller
// puts them in place once it's satisfied.
func RestoreFiles(ctx context.Context, runner Runner, t Target, snapshotID, target string) error {
	pf, cleanup, err := passwordFile(t.Password)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	args := append(resticArgs(t, pf), "restore", snapshotID, "--target", target, "--include", filepath.ToSlash(StagingDir))
	if err := runner(ctx, nil, t.Env, "restic", args...); err != nil {
		return fmt.Errorf("restic restore: %w", err)
	}
	return nil
}
