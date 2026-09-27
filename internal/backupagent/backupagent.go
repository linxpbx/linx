// Package backupagent is linx-backup-agent's logic (docs/BACKUP.md §8 step
// 3): once a tick, it asks the control plane whether a backup is due right
// now, runs `linx backup --json` if so, and reports the outcome back — the
// same host/container bridge internal/firewallsync uses, in the opposite
// direction (there, the host pulls desired state from the container; here,
// the host does the work and pushes the result back), since a container can
// never run a command on its host (no Docker socket mounts, ADR-thread
// model) and `linx backup` is, and must stay, a host binary (it reaches
// Postgres only via `docker exec ... pg_dump`, and reads Docker secrets no
// container is given).
package backupagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/backup"
)

// ControlPlaneContainer and ControlPlaneBinary are the same container and
// binary linx-firewall-sync, linx user and linx trunk run through:
// duplicated here, not imported, since services/control-plane is a
// container image, not a library a host binary can depend on.
const (
	ControlPlaneContainer = "linx-control-plane"
	ControlPlaneBinary    = "/usr/local/bin/service"
)

// Exec runs a command, optionally writing input to its stdin (nil: none),
// and returns its standard output. err is non-nil only when the command
// couldn't be made to produce any output at all (docker unreachable, the
// binary missing); a command that ran and reported a failure on stdout
// (linx backup --json exits 1 when every destination failed, but still
// prints a valid JSON summary) is not itself treated as this kind of error
// — Once inspects the JSON instead.
type Exec func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)

// Stream runs a command with stdin (nil: none) and stdout (nil: discarded)
// streamed rather than held in memory: backup files are up to about 2 GB.
type Stream func(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error

// destination mirrors cmd/linx's backup destResult and
// services/control-plane's reportDestination: the one JSON shape both ends
// already agree on.
type destination struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	Error      string `json:"error,omitempty"`
}

// backupJSON is linx backup --json's stdout shape.
type backupJSON struct {
	Destinations []destination `json:"destinations"`
	Error        string        `json:"error,omitempty"`
}

// report is what `linx-control-plane backup report` reads from stdin.
type report struct {
	Trigger      string        `json:"trigger"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
	Destinations []destination `json:"destinations"`
	Error        string        `json:"error,omitempty"`
}

// Env is what Once needs from the host.
type Env struct {
	Exec   Exec
	Stream Stream
	// LinxPath is the linx binary to run (installer.CLIPath in real use).
	LinxPath string
	// WorkDir is where backup files are kept on the host while they're
	// moved (a private folder inside it). Real use: /var/lib/linx, root's.
	WorkDir string
	Now     func() time.Time
	Log     *slog.Logger
}

func (e Env) log() *slog.Logger {
	if e.Log != nil {
		return e.Log
	}
	return slog.Default()
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Once checks whether a backup is due, and if so runs and reports one.
// Nothing happens if the control plane can't be reached (the same
// resilience as linx-firewall-sync: an unrelated outage doesn't lose a
// backup, it's just tried again next tick) or if nothing is due.
func (e Env) Once(ctx context.Context) error {
	out, err := e.cp(ctx, nil, "pending")
	if err != nil {
		return fmt.Errorf("checking whether a backup is due: %w: %s", err, strings.TrimSpace(string(out)))
	}
	trigger, due := parsePending(out)
	if !due {
		return nil
	}
	switch trigger {
	case "restore":
		return e.restore(ctx)
	case "export":
		return e.export(ctx)
	}
	return e.backup(ctx, trigger)
}

// backup runs linx backup and reports the run.
func (e Env) backup(ctx context.Context, trigger string) error {
	log := e.log()
	log.Info("a backup is due; running it", "trigger", trigger)
	started := e.now()
	jsonOut, runErr := e.Exec(ctx, nil, e.LinxPath, "backup", "--json")
	finished := e.now()
	var bj backupJSON
	if parseErr := json.Unmarshal(jsonOut, &bj); parseErr != nil {
		return fmt.Errorf("running linx backup: %w (output: %s)", errors.Join(runErr, parseErr), strings.TrimSpace(string(jsonOut)))
	}
	if runErr != nil {
		log.Warn("linx backup reported at least one failure", "err", runErr)
	}

	payload, err := json.Marshal(report{
		Trigger: trigger, StartedAt: started, FinishedAt: finished, Destinations: bj.Destinations, Error: bj.Error,
	})
	if err != nil {
		return err
	}
	if out, err := e.cp(ctx, payload, "report"); err != nil {
		return fmt.Errorf("reporting the backup's outcome: %w: %s", err, strings.TrimSpace(string(out)))
	}
	log.Info("backup reported")
	return nil
}

// cp runs a hidden `backup` subcommand of the control plane, with stdin
// when given.
func (e Env) cp(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	base := []string{"exec", ControlPlaneContainer, ControlPlaneBinary, "backup"}
	if stdin != nil {
		base = []string{"exec", "-i", ControlPlaneContainer, ControlPlaneBinary, "backup"}
	}
	return e.Exec(ctx, stdin, "docker", append(base, args...)...)
}

// exportResult is linx backup export --json's output (cmd/linx's
// exportResult).
type exportResult struct {
	OK           bool      `json:"ok"`
	Error        string    `json:"error"`
	SnapshotID   string    `json:"snapshot_id"`
	SnapshotTime time.Time `json:"snapshot_time"`
	Password     string    `json:"password"`
}

// export makes the backup file an admin asked for in System → Backups
// (docs/BACKUP.md §8 step 5): a backup now (reported like "back up now"),
// then the server's own backup folder packed into one file, handed to the
// control plane with its password for that admin only.
func (e Env) export(ctx context.Context) error {
	log := e.log()
	out, err := e.cp(ctx, nil, "export-take")
	if err != nil {
		return fmt.Errorf("taking the backup-file request: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil
	}
	var req struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal(out, &req); err != nil || req.ID == uuid.Nil {
		return fmt.Errorf("reading the backup-file request: %v", errors.Join(err, errors.New(strings.TrimSpace(string(out)))))
	}
	done := func(v map[string]any) error {
		v["id"] = req.ID
		payload, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if out, err := e.cp(ctx, payload, "export-done"); err != nil {
			return fmt.Errorf("reporting the backup file: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	fail := func(msg string) error {
		log.Warn("making a backup file failed", "err", msg)
		return done(map[string]any{"ok": false, "error": msg})
	}

	// The file should hold a backup from now; if this one fails, the file
	// still has the older ones (and says how old the newest is).
	if err := e.backup(ctx, "manual"); err != nil {
		log.Warn("backing up before making the backup file", "err", err)
	}

	dir, err := e.tempDir("export-*")
	if err != nil {
		return fail(fmt.Sprintf("Couldn't make room for the backup file on the server: %v", err))
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "backup.tar")
	resOut, runErr := e.Exec(ctx, nil, e.LinxPath, "backup", "export", "--json", "--out", file)
	var res exportResult
	if err := json.Unmarshal(resOut, &res); err != nil {
		return fail(fmt.Sprintf("linx backup export didn't finish: %v", errors.Join(runErr, err)))
	}
	if !res.OK {
		return fail(res.Error)
	}
	f, err := os.Open(file)
	if err != nil {
		return fail(err.Error())
	}
	err = e.Stream(ctx, f, nil, "docker", "exec", "-i", ControlPlaneContainer, ControlPlaneBinary, "backup", "export-put", req.ID.String())
	f.Close()
	if err != nil {
		return fail(fmt.Sprintf("Couldn't hand the backup file over: %v", err))
	}
	if err := done(map[string]any{"ok": true, "snapshot_id": res.SnapshotID, "snapshot_time": res.SnapshotTime, "password": res.Password}); err != nil {
		return err
	}
	log.Info("backup file ready", "snapshot", res.SnapshotID)
	return nil
}

// tempDir makes a private folder under WorkDir.
func (e Env) tempDir(pattern string) (string, error) {
	if err := os.MkdirAll(e.WorkDir, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(e.WorkDir, pattern)
}

// parsePending reads `backup pending`'s single line of output: "skip", or
// "run manual"/"run scheduled".
func parsePending(out []byte) (trigger string, due bool) {
	line := strings.TrimSpace(string(out))
	rest, ok := strings.CutPrefix(line, "run ")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// restoreRequest is `backup restore-take`'s output
// (services/control-plane/backup_cmd.go's restoreRequestJSON).
type restoreRequest struct {
	ID          uuid.UUID `json:"id"`
	Source      string    `json:"source"`
	Location    string    `json:"location"`
	Snapshot    string    `json:"snapshot"`
	Password    string    `json:"password"`
	RequestedBy string    `json:"requested_by"`
}

// restoreResult is linx restore --json's output (cmd/linx's restoreResult).
type restoreResult struct {
	OK           bool      `json:"ok"`
	Error        string    `json:"error"`
	Changed      bool      `json:"changed"`
	SnapshotID   string    `json:"snapshot_id"`
	SnapshotTime time.Time `json:"snapshot_time"`
}

// restore takes the setup wizard's restore request, runs linx restore with
// it (the password on stdin, never an argument) and reports back: a
// failure to the request itself, so the wizard can show it; a success to
// the restored database's audit log (the request was in the database it
// replaced). docs/BACKUP.md §4.
func (e Env) restore(ctx context.Context) error {
	log := e.log()
	cp := func(stdin []byte, args ...string) ([]byte, error) { return e.cp(ctx, stdin, args...) }
	out, err := cp(nil, "restore-take")
	if err != nil {
		return fmt.Errorf("taking the restore request: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil // taken by someone else, or withdrawn
	}
	var req restoreRequest
	if err := json.Unmarshal(out, &req); err != nil {
		return fmt.Errorf("reading the restore request: %w", err)
	}
	fail := func(msg string) error {
		payload, _ := json.Marshal(map[string]any{"id": req.ID, "error": msg})
		if out, err := cp(payload, "restore-failed"); err != nil {
			return fmt.Errorf("reporting a failed restore (%s): %w: %s", msg, err, strings.TrimSpace(string(out)))
		}
		log.Warn("restore failed", "err", msg)
		return nil
	}
	// The control plane checked these already; checked again here, on the
	// host that acts on them.
	if err := backup.CheckRestoreSource(req.Source, req.Location); err != nil {
		return fail(err.Error())
	}
	if err := backup.CheckSnapshot(req.Snapshot); err != nil {
		return fail(err.Error())
	}
	flag, location := "--path", req.Location
	switch req.Source {
	case backup.SourceDestination:
		flag = "--destination"
	case backup.SourceUpload:
		// Fetched from the control plane's transfer folder, unpacked by
		// linx restore --file, and removed from both places afterwards
		// however the restore goes.
		defer func() {
			if out, err := cp(nil, "upload-delete", req.Location); err != nil {
				log.Warn("removing the uploaded backup file", "err", err, "output", strings.TrimSpace(string(out)))
			}
		}()
		dir, err := e.tempDir("upload-*")
		if err != nil {
			return fail(fmt.Sprintf("Couldn't make room for the backup file on the server: %v", err))
		}
		defer os.RemoveAll(dir)
		flag, location = "--file", filepath.Join(dir, "backup.tar")
		f, err := os.OpenFile(location, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fail(err.Error())
		}
		err = e.Stream(ctx, nil, f, "docker", "exec", ControlPlaneContainer, ControlPlaneBinary, "backup", "upload-read", req.Location)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fail(fmt.Sprintf("Couldn't fetch the uploaded backup file: %v", err))
		}
	}
	log.Info("restoring from a backup", "source", req.Source, "location", req.Location, "snapshot", req.Snapshot)
	resOut, runErr := e.Exec(ctx, []byte(req.Password), e.LinxPath, "restore", "--yes", "--json", "--password-stdin",
		flag, location, req.Snapshot)
	var res restoreResult
	if err := json.Unmarshal(resOut, &res); err != nil {
		return fail(fmt.Sprintf("linx restore didn't finish: %v", errors.Join(runErr, err)))
	}
	if !res.OK {
		return fail(res.Error)
	}
	payload, err := json.Marshal(map[string]any{
		"source": req.Source, "location": req.Location, "snapshot_id": res.SnapshotID,
		"snapshot_time": res.SnapshotTime, "requested_by": req.RequestedBy,
	})
	if err != nil {
		return err
	}
	if out, err := cp(payload, "restore-done"); err != nil {
		return fmt.Errorf("restored, but recording it failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	log.Info("restored from a backup", "snapshot", res.SnapshotID)
	return nil
}
