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

	"github.com/google/uuid"
)

// Runner runs restic, writing its standard output to stdout (nil discards
// it). The returned error, if any, must already read as a complete,
// human-readable message (its caller's stderr folded in) — this package
// never inspects a separate stderr stream, only the error text.
type Runner func(ctx context.Context, stdout io.Writer, name string, args ...string) error

// KeysDirName is the fixed directory name secret files are stored under
// inside a snapshot, so restore knows what to ask restic to extract without
// depending on the host's own secrets path (which can differ between the
// server that made the backup and the one restoring it).
const KeysDirName = "linx-keys"

// StagingDir is where `linx backup` stages the database dump and secret
// files before handing them to restic, and where `linx restore-secrets`
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

// alreadyInitializedMarker is in restic's own message when a repository
// already exists; InitRepo treats that as success, not failure (backup
// runs unattended on a schedule and mustn't fail forever after its first
// run against an already-initialized repository).
const alreadyInitializedMarker = "already initialized"

// InitRepo creates repo if it doesn't already have a restic config.
func InitRepo(ctx context.Context, runner Runner, repo, password string) error {
	pf, cleanup, err := passwordFile(password)
	if err != nil {
		return err
	}
	defer cleanup()
	err = runner(ctx, nil, "restic", "-r", repo, "--password-file", pf, "init")
	if err != nil && !strings.Contains(err.Error(), alreadyInitializedMarker) {
		return fmt.Errorf("restic init: %w", err)
	}
	return nil
}

// summary is the line restic --json backup prints with type "summary".
type summary struct {
	MessageType string `json:"message_type"`
	SnapshotID  string `json:"snapshot_id"`
}

// Backup runs one restic backup of dumpFile (the database dump) and
// KeysPath (the secret files staged there, docs/BACKUP.md §2), tagged
// with pairID, and returns the new snapshot's id.
func Backup(ctx context.Context, runner Runner, repo, password, pairID, dumpFile string) (snapshotID string, err error) {
	pf, cleanup, err := passwordFile(password)
	if err != nil {
		return "", err
	}
	defer cleanup()
	var stdout bytes.Buffer
	err = runner(ctx, &stdout, "restic", "-r", repo, "--password-file", pf, "backup", dumpFile, KeysPath,
		"--tag", PairTag+":"+pairID, "--json")
	if err != nil {
		return "", fmt.Errorf("restic backup: %w", err)
	}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		var s summary
		if json.Unmarshal([]byte(line), &s) == nil && s.MessageType == "summary" {
			snapshotID = s.SnapshotID
		}
	}
	if snapshotID == "" {
		return "", errors.New("restic backup didn't report a snapshot id")
	}
	return snapshotID, nil
}

// Forget prunes old snapshots per the retention policy, keeping the most
// recent keepLast regardless of age (docs/BACKUP.md §5's default of 14,
// changed by the caller for weekly/monthly schedules).
func Forget(ctx context.Context, runner Runner, repo, password string, keepLast int) error {
	pf, cleanup, err := passwordFile(password)
	if err != nil {
		return err
	}
	defer cleanup()
	err = runner(ctx, nil, "restic", "-r", repo, "--password-file", pf, "forget",
		"--keep-last", fmt.Sprint(keepLast), "--prune")
	if err != nil {
		return fmt.Errorf("restic forget: %w", err)
	}
	return nil
}

// snapshotMeta is restic snapshots --json's per-snapshot shape (the fields
// this package reads).
type snapshotMeta struct {
	ShortID string   `json:"short_id"`
	Tags    []string `json:"tags"`
}

// PairIDForSnapshot returns the pair id a snapshot was tagged with, so
// restore can compare it against what's already on disk (docs/BACKUP.md
// §4) before overwriting anything.
func PairIDForSnapshot(ctx context.Context, runner Runner, repo, password, snapshotID string) (string, error) {
	pf, cleanup, err := passwordFile(password)
	if err != nil {
		return "", err
	}
	defer cleanup()
	var stdout bytes.Buffer
	if err := runner(ctx, &stdout, "restic", "-r", repo, "--password-file", pf, "snapshots", snapshotID, "--json"); err != nil {
		return "", fmt.Errorf("restic snapshots: %w", err)
	}
	var snaps []snapshotMeta
	if err := json.Unmarshal(stdout.Bytes(), &snaps); err != nil || len(snaps) == 0 {
		return "", fmt.Errorf("snapshot %s not found", snapshotID)
	}
	for _, tag := range snaps[0].Tags {
		if id, ok := strings.CutPrefix(tag, PairTag+":"); ok {
			return id, nil
		}
	}
	return "", fmt.Errorf("snapshot %s has no pair id (not a Linx backup?)", snapshotID)
}

// RestoreKeys extracts the keys directory (only) from a snapshot into
// target (the two secret files end up at target+KeysPath, restic's own
// restore-preserves-the-original-path behaviour) — this function never
// touches the real secrets path itself, so a bug here can't silently
// corrupt a running system's credentials mid-copy; the caller copies the
// individual files out from there once it's satisfied.
func RestoreKeys(ctx context.Context, runner Runner, repo, password, snapshotID, target string) error {
	pf, cleanup, err := passwordFile(password)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	err = runner(ctx, nil, "restic", "-r", repo, "--password-file", pf, "restore", snapshotID,
		"--target", target, "--include", filepath.ToSlash(KeysPath))
	if err != nil {
		return fmt.Errorf("restic restore: %w", err)
	}
	return nil
}
