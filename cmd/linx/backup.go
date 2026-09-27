package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"linxpbx.com/linx/internal/backup"
	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/safehttp"
)

// Container and database names from deploy/compose/compose.yaml (matching
// internal/doctor/platform.go's own constants for the same container).
const (
	backupPostgresContainer = "linx-postgres"
	backupDBUser            = "linx"
	backupDBName            = "linx"
)

// defaultBackupRepo is the local destination `linx backup` uses when the
// admin hasn't configured any destination at all yet (docs/BACKUP.md §3,
// "Local") — zero-config still backs something up.
const defaultBackupRepo = "/var/backups/linx"

// defaultDestinationName is the implicit destination used for
// defaultBackupRepo (and its saved repository password's file name).
const defaultDestinationName = "local"

// backupKeepLast is the default retention (docs/BACKUP.md §5) applied
// after every manual `linx backup` run; the schedule (a later build step)
// will vary this by daily/weekly/monthly.
const backupKeepLast = 14

const backupUsage = `linx backup — back up the database and the keys a restore needs

Usage:
  sudo linx backup [--json]            Back up to every configured destination
  sudo linx backup destination add --kind local --path PATH NAME
  sudo linx backup destination add --kind sftp --host HOST --user USER --remote-path PATH [--port 22] NAME
  sudo linx backup destination add --kind s3 --endpoint URL --bucket BUCKET --access-key-id ID --secret-access-key SECRET [--region REGION] NAME
  sudo linx backup destination list
  sudo linx backup destination remove NAME

--json prints one JSON summary line instead of plain text (linx-backup-agent
uses this to report the run back to the control plane) and never shows a
newly generated password — run without it by hand at least once per
destination to see it.

(Flags before NAME, same as linx restore-secrets: Go's flag parser stops at
the first non-flag argument.)

With no destination configured, backs up to ` + defaultBackupRepo + ` only.
A destination's repository password is generated and shown once when it's
added — save it somewhere safe, separate from the backup itself. Losing it
means that destination's backups can never be read back.

An sftp destination needs a public key added to the remote server's
authorized_keys — shown once when it's added.
`

// backupEnv is everything backup touches on the host, so tests can fake it
// with temporary directories instead of Linx's real, root-only paths.
type backupEnv struct {
	isRoot bool
	// secretsDir, stagingDir default to installer.SecretsDir and
	// backup.StagingDir; overridden in tests only.
	secretsDir, stagingDir string
	// run runs a host command, writing its stdout to w (nil discards it)
	// with env appended to its own environment, and returns an error with
	// stderr's text already folded in.
	run func(ctx context.Context, w io.Writer, env []string, name string, args ...string) error
	// lookup resolves a destination host, for the outbound safety check
	// (docs/BACKUP.md §7). Real use: backup.DefaultLookup.
	lookup backup.Lookup
	// ownNetworks returns this host's own network ranges. Real use:
	// safehttp.OwnNetworks.
	ownNetworks func() ([]netip.Prefix, error)
}

func realBackupEnv() backupEnv {
	return backupEnv{
		isRoot:      os.Geteuid() == 0,
		secretsDir:  installer.SecretsDir,
		stagingDir:  backup.StagingDir,
		lookup:      backup.DefaultLookup,
		ownNetworks: safehttp.OwnNetworks,
		run: func(ctx context.Context, w io.Writer, env []string, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			if len(env) > 0 {
				cmd.Env = append(os.Environ(), env...)
			}
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

// destinationsManifest is where env's configured destinations and their
// credentials live (a subdirectory of the Docker secrets directory: it's
// exactly as sensitive, root-only, never in git or the database).
func (env backupEnv) destinationsManifest() backup.Manifest {
	return backup.Manifest{Dir: filepath.Join(env.secretsDir, "linx-backup")}
}

// destResult and jsonResult are --json's stdout shape: exactly the fields
// services/control-plane/backup_cmd.go's `report` reads back
// (backupschedule.Destination/Run), so linx-backup-agent can pass this
// straight through as that command's stdin without reshaping it.
type destResult struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	Error      string `json:"error,omitempty"`
}

type jsonResult struct {
	Destinations []destResult `json:"destinations"`
	// Error is set only when no destination was even attempted (staging or
	// the database dump failed first).
	Error string `json:"error,omitempty"`
}

func runBackup(ctx context.Context, args []string, stdout, stderr io.Writer, env backupEnv) int {
	if len(args) > 0 && args[0] == "destination" {
		return runBackupDestination(ctx, args[1:], stdout, stderr, env)
	}
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, backupUsage) }
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprint(stderr, backupUsage)
		return 2
	}
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx backup must run as root. Try: sudo linx backup")
		return 1
	}

	dests, err := env.destinationsManifest().Load()
	if err != nil {
		return backupFailedBeforeStart(*asJSON, stdout, stderr, "Couldn't read configured destinations", err)
	}
	if len(dests) == 0 {
		dests = []backup.Destination{{Name: defaultDestinationName, Kind: backup.KindLocal, Path: defaultBackupRepo}}
	}

	if err := prepareStaging(ctx, env); err != nil {
		return backupFailedBeforeStart(*asJSON, stdout, stderr, "Couldn't prepare the backup", err)
	}
	defer os.RemoveAll(env.stagingDir) //nolint:errcheck // best effort; nothing sensitive stays if this fails silently on a read-only fs

	pairID, err := backup.NewPairID()
	if err != nil {
		return backupFailedBeforeStart(*asJSON, stdout, stderr, "", err)
	}
	dumpFile := filepath.Join(env.stagingDir, "database.pgcustom")

	results := make([]destResult, 0, len(dests))
	failed := 0
	for _, d := range dests {
		snapshotID, password, err := backupOneDestination(ctx, env, d, pairID, dumpFile, stderr)
		r := destResult{Name: d.Name, OK: err == nil, SnapshotID: snapshotID}
		if err != nil {
			r.Error = err.Error()
			failed++
		}
		results = append(results, r)
		switch {
		case *asJSON && password != "":
			fmt.Fprintf(stderr, "%s: a repository password was generated; read it once with: sudo cat %s\n",
				d.Name, env.destinationsManifest().PasswordPath(d.Name))
		case !*asJSON && err != nil:
			fmt.Fprintf(stderr, "%s: %v\n", d.Name, err)
		case !*asJSON:
			fmt.Fprintf(stdout, "%s: backed up (snapshot %s).\n", d.Name, snapshotID)
			if password != "" {
				fmt.Fprintf(stdout, "%s's repository password (shown once — save it somewhere safe, separate from the backup itself):\n\n  %s\n\n", d.Name, password)
			}
		}
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(jsonResult{Destinations: results})
	}
	if failed == len(dests) {
		return 1
	}
	return 0
}

// backupFailedBeforeStart reports a failure that happened before any
// destination was even tried.
func backupFailedBeforeStart(asJSON bool, stdout, stderr io.Writer, prefix string, err error) int {
	msg := err.Error()
	if prefix != "" {
		msg = fmt.Sprintf("%s: %s", prefix, msg)
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(jsonResult{Destinations: []destResult{}, Error: msg})
	} else {
		fmt.Fprintln(stderr, msg)
	}
	return 1
}

// prepareStaging clears and recreates the staging directory, dumps the
// database into it, and stages the keys (and every configured
// destination's own credentials, so a disaster-recovery restore can still
// reach a remote copy — docs/BACKUP.md §2). It runs once regardless of how
// many destinations the backup then goes to.
func prepareStaging(ctx context.Context, env backupEnv) error {
	if err := os.RemoveAll(env.stagingDir); err != nil {
		return fmt.Errorf("preparing a staging directory: %w", err)
	}
	if err := os.MkdirAll(env.stagingDir, 0o700); err != nil {
		return fmt.Errorf("preparing a staging directory: %w", err)
	}
	dumpFile := filepath.Join(env.stagingDir, "database.pgcustom")
	if err := dumpDatabase(ctx, env.run, dumpFile); err != nil {
		return fmt.Errorf("dumping the database: %w", err)
	}
	keysPath := filepath.Join(env.stagingDir, backup.KeysDirName)
	if err := stageKeys(env.secretsDir, keysPath); err != nil {
		return fmt.Errorf("staging the encryption keys: %w", err)
	}
	return nil
}

// backupOneDestination backs up to d, generating and saving its repository
// password the first time it's used (returned as password only then — the
// caller decides whether it's safe to show, e.g. never in --json mode).
// warn receives a non-fatal "backed up, but couldn't apply retention"
// notice; it never affects the returned error.
func backupOneDestination(ctx context.Context, env backupEnv, d backup.Destination, pairID, dumpFile string, warn io.Writer) (snapshotID, password string, err error) {
	m := env.destinationsManifest()
	if _, statErr := os.Stat(m.PasswordPath(d.Name)); os.IsNotExist(statErr) {
		var genErr error
		password, genErr = backup.NewPassword()
		if genErr != nil {
			return "", "", genErr
		}
		if genErr := os.MkdirAll(m.Dir, 0o700); genErr != nil {
			return "", "", genErr
		}
		if genErr := os.WriteFile(m.PasswordPath(d.Name), []byte(password), 0o600); genErr != nil {
			return "", "", genErr
		}
	}
	target, err := m.Target(d)
	if err != nil {
		return "", "", err
	}
	runner := backup.Runner(env.run)
	if err := backup.InitRepo(ctx, runner, target); err != nil {
		return "", "", fmt.Errorf("preparing the repository: %w", err)
	}
	snapshotID, err = backup.Backup(ctx, runner, target, pairID, dumpFile)
	if err != nil {
		return "", "", fmt.Errorf("backup failed: %w", err)
	}
	if err := backup.Forget(ctx, runner, target, backupKeepLast); err != nil {
		fmt.Fprintf(warn, "%s: backed up, but couldn't apply retention: %v\n", d.Name, err)
	}
	return snapshotID, password, nil
}

// dumpDatabase runs pg_dump inside the Postgres container (it trusts local
// connections, the same as linx doctor's own queries) and writes the
// result to path.
func dumpDatabase(ctx context.Context, run func(context.Context, io.Writer, []string, string, ...string) error, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return run(ctx, f, nil, "docker", "exec", backupPostgresContainer, "pg_dump",
		"--username", backupDBUser, "--dbname", backupDBName, "--format", "custom")
}

// stageKeys copies the Docker secrets a restore can't regenerate
// (docs/BACKUP.md §2) from secretsDir into keysPath, which restic then
// backs up.
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
