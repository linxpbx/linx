// Command linx-backup-agent runs a due backup and reports its outcome back
// to the control plane (docs/BACKUP.md §8 step 3): the bridge between a
// schedule the admin sets in the browser and `linx backup`, a host binary
// no container can run. It runs once and exits: linx setup installs it as
// a systemd timer (installer.BackupAgentPlan), not a long-running service.
// It must run as root (linx backup does) and reads/writes the control
// plane only through docker exec, the same trust as linx user.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"linxpbx.com/linx/internal/backup"
	"linxpbx.com/linx/internal/backupagent"
	"linxpbx.com/linx/internal/installer"
)

func main() {
	os.Exit(run())
}

func run() int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "linx-backup-agent must run as root (linx backup does). "+
			"linx setup installs it as a systemd timer; don't run it by hand.")
		return 1
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", "linx-backup-agent")
	// A backup can genuinely take a while (a large database, a slow remote
	// destination); this is a much longer budget than linx-firewall-sync's
	// read-only check.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	env := backupagent.Env{Exec: execRun(log), Stream: streamRun, LinxPath: installer.CLIPath,
		WorkDir: filepath.Dir(backup.StagingDir), Log: log}
	if err := env.Once(ctx); err != nil {
		log.Error("running a due backup", "err", err)
		return 1
	}
	return 0
}

// execRun returns stdout only: linx backup --json and friends print one
// JSON line there, and notes (a newly generated password's location, a
// retention warning) on stderr, which goes to the log instead of being
// mixed into what's parsed.
func execRun(log *slog.Logger) backupagent.Exec {
	return func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		err := cmd.Run()
		msg := strings.TrimSpace(stderr.String())
		if err != nil && msg != "" {
			err = fmt.Errorf("%w: %s", err, msg)
		} else if msg != "" {
			log.Info("note", "command", filepath.Base(name), "text", msg)
		}
		return out.Bytes(), err
	}
}

// streamRun runs a command whose input or output is a backup file.
func streamRun(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}
