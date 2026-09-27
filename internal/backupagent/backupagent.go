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
	"log/slog"
	"strings"
	"time"
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
	Exec Exec
	// LinxPath is the linx binary to run (installer.CLIPath in real use).
	LinxPath string
	Now      func() time.Time
	Log      *slog.Logger
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
	log := e.log()
	out, err := e.Exec(ctx, nil, "docker", "exec", ControlPlaneContainer, ControlPlaneBinary, "backup", "pending")
	if err != nil {
		return fmt.Errorf("checking whether a backup is due: %w: %s", err, strings.TrimSpace(string(out)))
	}
	trigger, due := parsePending(out)
	if !due {
		return nil
	}
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
	if out, err := e.Exec(ctx, payload, "docker", "exec", "-i", ControlPlaneContainer, ControlPlaneBinary, "backup", "report"); err != nil {
		return fmt.Errorf("reporting the backup's outcome: %w: %s", err, strings.TrimSpace(string(out)))
	}
	log.Info("backup reported")
	return nil
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
