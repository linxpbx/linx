package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"linxpbx.com/linx/internal/backup"
	"linxpbx.com/linx/internal/installer"
)

// Container and database names from deploy/compose/compose.yaml (matching
// internal/doctor/platform.go's own constants for the same container).
const (
	backupPostgresContainer = "linx-postgres"
	backupDBUser            = "linx"
	backupDBName            = "linx"
)

// defaultBackupRepo is where `linx backup` keeps its restic repository
// when the admin hasn't set one (docs/BACKUP.md §3, "Local").
const defaultBackupRepo = "/var/backups/linx"

// backupPasswordName is the repository password `linx backup` generates
// the first time it runs against a repository (docs/BACKUP.md §6: never
// admin-chosen, shown once), a file alongside Linx's other Docker secrets.
const backupPasswordName = "linx_backup_repo_password"

// backupKeepLast is the default retention (docs/BACKUP.md §5) applied
// after every manual `linx backup` run; the schedule (a later build step)
// will vary this by daily/weekly/monthly.
const backupKeepLast = 14

const backupUsage = `linx backup — back up the database and the keys a restore needs

Usage:
  sudo linx backup [--repo PATH]

  --repo PATH   Where the restic repository lives (default: ` + defaultBackupRepo + `).
                A local path for now; remote destinations are a later addition.

The first run against a repository generates its password and shows it
once — save it somewhere safe, separate from the backup itself. Losing it
means the backup can never be read back.
`

// backupEnv is everything backup touches on the host, so tests can fake it
// with temporary directories instead of Linx's real, root-only paths.
type backupEnv struct {
	isRoot bool
	// secretsDir, stagingDir default to installer.SecretsDir and
	// backup.StagingDir; overridden in tests only.
	secretsDir, stagingDir string
	// run runs a host command, writing its stdout to w (nil discards it),
	// and returns an error with stderr's text already folded in.
	run func(ctx context.Context, w io.Writer, name string, args ...string) error
}

func realBackupEnv() backupEnv {
	return backupEnv{
		isRoot:     os.Geteuid() == 0,
		secretsDir: installer.SecretsDir,
		stagingDir: backup.StagingDir,
		run: func(ctx context.Context, w io.Writer, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			var stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = w, &stderr
			if err := cmd.Run(); err != nil {
				if s := strings.TrimSpace(stderr.String()); s != "" {
					return errors.New(s)
				}
				return err
			}
			return nil
		},
	}
}

func runBackup(ctx context.Context, args []string, stdout, stderr io.Writer, env backupEnv) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, backupUsage) }
	repo := fs.String("repo", defaultBackupRepo, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx backup must run as root. Try: sudo linx backup")
		return 1
	}
	keysPath := filepath.Join(env.stagingDir, backup.KeysDirName)

	if err := os.RemoveAll(env.stagingDir); err != nil {
		fmt.Fprintf(stderr, "Couldn't prepare a staging directory: %v\n", err)
		return 1
	}
	defer os.RemoveAll(env.stagingDir) //nolint:errcheck // best effort; nothing sensitive stays if this fails silently on a read-only fs
	if err := os.MkdirAll(env.stagingDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "Couldn't prepare a staging directory: %v\n", err)
		return 1
	}

	password, isNew, err := backupPassword(filepath.Join(env.secretsDir, backupPasswordName))
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't prepare the repository password: %v\n", err)
		return 1
	}

	if err := backup.InitRepo(ctx, env.run, *repo, password); err != nil {
		fmt.Fprintf(stderr, "Couldn't prepare the backup repository: %v\n", err)
		return 1
	}

	dumpFile := filepath.Join(env.stagingDir, "database.pgcustom")
	if err := dumpDatabase(ctx, env.run, dumpFile); err != nil {
		fmt.Fprintf(stderr, "Couldn't dump the database: %v\n", err)
		return 1
	}
	if err := stageKeys(env.secretsDir, keysPath); err != nil {
		fmt.Fprintf(stderr, "Couldn't stage the encryption keys: %v\n", err)
		return 1
	}

	pairID, err := backup.NewPairID()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	snapshotID, err := backup.Backup(ctx, env.run, *repo, password, pairID, dumpFile)
	if err != nil {
		fmt.Fprintf(stderr, "Backup failed: %v\n", err)
		return 1
	}
	if err := backup.Forget(ctx, env.run, *repo, password, backupKeepLast); err != nil {
		// The backup itself succeeded; losing old snapshots to retention
		// isn't worth failing the whole run over.
		fmt.Fprintf(stderr, "Backed up, but couldn't apply retention: %v\n", err)
	}

	fmt.Fprintf(stdout, "Backed up to %s (snapshot %s).\n", *repo, snapshotID)
	if isNew {
		fmt.Fprintf(stdout, "\nThis repository's password (shown once — save it somewhere safe, separate from the backup itself):\n\n  %s\n\n", password)
	}
	return 0
}

// backupPassword reads the repository password if one was already
// generated, or generates and saves a new one (docs/BACKUP.md §6).
func backupPassword(path string) (password string, isNew bool, err error) {
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), false, nil
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	password, err = backup.NewPassword()
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
		return "", false, err
	}
	return password, true, nil
}

// dumpDatabase runs pg_dump inside the Postgres container (it trusts local
// connections, the same as linx doctor's own queries) and writes the
// result to path.
func dumpDatabase(ctx context.Context, run func(context.Context, io.Writer, string, ...string) error, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return run(ctx, f, "docker", "exec", backupPostgresContainer, "pg_dump",
		"--username", backupDBUser, "--dbname", backupDBName, "--format", "custom")
}

// stageKeys copies the Docker secrets a restore can't regenerate
// (docs/BACKUP.md §2) from secretsDir into keysPath, which restic then
// backs up. keysPath must equal backup.KeysPath in real use (Backup always
// backs that path up); tests may use another path with a fake Runner that
// doesn't care.
func stageKeys(secretsDir, keysPath string) error {
	if err := os.MkdirAll(keysPath, 0o700); err != nil {
		return err
	}
	for _, name := range []string{"linx_db_encryption_key", "linx_jwt_signing_key"} {
		src := filepath.Join(secretsDir, name)
		b, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("reading %s: %w", src, err)
		}
		if err := os.WriteFile(filepath.Join(keysPath, name), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}
