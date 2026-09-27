package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"linxpbx.com/linx/internal/backup"
	"linxpbx.com/linx/internal/db"
)

const restoreUsage = `linx restore — put everything back from a backup

Usage:
  sudo linx restore --yes (--path FOLDER | --destination NAME | --file FILE) [--password-file PATH | --password-stdin] [--json] [SNAPSHOT]

  --path FOLDER        A backup folder on this server (a copy of an old
                       server's /var/backups/linx, a second drive, ...).
  --destination NAME   A destination set up on this server with
                       linx backup destination add (for a remote one, add it
                       here first with the same address and credentials).
  --file FILE          A backup file downloaded from System → Backups
                       (or made with sudo linx backup export).
  SNAPSHOT             Which backup: "latest" (the default) or its id.
  --yes                Required: this replaces every person, setting and
                       key on this server, and can't be undone from here.
  --password-file      Read the backup's password from this file instead of
                       asking for it; --password-stdin reads it from stdin.
  --json               Print one JSON line with the outcome (linx-backup-agent
                       uses this for the setup wizard's restore).

What it does (docs/BACKUP.md §4): reads the backup, loads its database next
to the live one and checks it, then stops the control plane, puts the
backup's two encryption keys and its database in place together, and starts
Linx again. Everyone signs in again afterwards. The database and keys from
before are kept (the database as linx_before_restore, the keys as
*.before-restore in the secrets folder) until the next restore.

The setup wizard's "Restore from a backup" does the same without SSH.
`

// Containers from deploy/compose/compose.yaml.
const (
	restoreControlPlane = "linx-control-plane"
	restoreAsterisk     = "linx-asterisk"
)

// restoreKeys are the Docker secrets a backup carries (docs/BACKUP.md §2),
// the same list stageKeys backs up.
var restoreKeys = []string{"linx_db_encryption_key", "linx_jwt_signing_key"}

type restoreEnv struct {
	backupEnv
	terminal     bool
	readPassword func() (string, error)
	stdin        io.Reader
	// workDir is where the backup's files are unpacked (a private temp
	// folder inside it). Real use: /var/lib/linx, not /tmp: the dump is
	// the whole database.
	workDir  string
	runInput backup.RunInput
	sleep    func(time.Duration)
	// healthTimeout is how long to wait for the control plane to come back.
	healthTimeout time.Duration
}

func realRestoreEnv() restoreEnv {
	be := realBackupEnv()
	return restoreEnv{
		backupEnv: be,
		terminal:  term.IsTerminal(int(os.Stdin.Fd())),
		readPassword: func() (string, error) {
			fmt.Fprint(os.Stderr, "The backup's password: ")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		},
		stdin:   os.Stdin,
		workDir: filepath.Dir(backup.StagingDir),
		runInput: func(ctx context.Context, stdin io.Reader, w io.Writer, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			var stderr bytes.Buffer
			cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, w, &stderr
			if err := cmd.Run(); err != nil {
				if s := strings.TrimSpace(stderr.String()); s != "" {
					return errors.New(s)
				}
				return err
			}
			return nil
		},
		sleep:         time.Sleep,
		healthTimeout: 3 * time.Minute,
	}
}

// restoreResult is --json's output (internal/backupagent's restoreResult
// reads the same shape). Changed says whether anything on the server was
// replaced: false means it's exactly as it was.
type restoreResult struct {
	OK           bool      `json:"ok"`
	Error        string    `json:"error,omitempty"`
	Changed      bool      `json:"changed"`
	SnapshotID   string    `json:"snapshot_id,omitempty"`
	SnapshotTime time.Time `json:"snapshot_time,omitzero"`
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer, env restoreEnv) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, restoreUsage) }
	yes := fs.Bool("yes", false, "")
	path := fs.String("path", "", "")
	destName := fs.String("destination", "", "")
	file := fs.String("file", "", "")
	passwordFile := fs.String("password-file", "", "")
	passwordStdin := fs.Bool("password-stdin", false, "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	snapshot := "latest"
	switch fs.NArg() {
	case 0:
	case 1:
		snapshot = fs.Arg(0)
	default:
		fmt.Fprint(stderr, restoreUsage)
		return 2
	}

	fail := func(changed bool, format string, a ...any) int {
		msg := fmt.Sprintf(format, a...)
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(restoreResult{Error: msg, Changed: changed})
		} else {
			fmt.Fprintln(stderr, msg)
		}
		return 1
	}

	if !env.isRoot {
		return fail(false, "linx restore must run as root. Try: sudo linx restore ...")
	}
	given := 0
	for _, v := range []string{*path, *destName, *file} {
		if v != "" {
			given++
		}
	}
	if given != 1 {
		return fail(false, "Say where the backup is: --path FOLDER, --destination NAME or --file FILE (one of them).")
	}
	if err := backup.CheckSnapshot(snapshot); err != nil {
		return fail(false, "%s.", capitalizeFirst(err.Error()))
	}
	if !*yes {
		return fail(false, "This replaces every person, setting and key on this server with the backup's, and signs everyone out. "+
			"Run again with --yes once you're sure.")
	}

	password, err := restorePassword(env, *passwordFile, *passwordStdin)
	if err != nil {
		return fail(false, "%v", err)
	}

	var target backup.Target
	var dest *backup.Destination
	switch {
	case *file != "":
		dir, cleanup, err := unpackBackupFile(env, *file)
		if err != nil {
			return fail(false, "%v", err)
		}
		defer cleanup()
		target = backup.Target{Repo: dir, Password: password}
	case *path != "":
		if err := backup.CheckFolder(*path); err != nil {
			return fail(false, "%s.", capitalizeFirst(err.Error()))
		}
		target = backup.Target{Repo: *path, Password: password}
	default:
		if err := backup.CheckDestinationName(*destName); err != nil {
			return fail(false, "%s.", capitalizeFirst(err.Error()))
		}
		m := env.destinationsManifest()
		d, found, err := m.Find(*destName)
		if err != nil {
			return fail(false, "Couldn't read this server's backup destinations: %v", err)
		}
		if !found {
			return fail(false, "There's no backup destination named %q on this server (sudo linx backup destination list).", *destName)
		}
		if target, err = m.TargetWithPassword(d, password); err != nil {
			return fail(false, "%v", err)
		}
		dest = &d
	}

	res, err := restoreEverything(ctx, env, target, snapshot, stderr)
	if err != nil {
		return fail(res.Changed, "%v", err)
	}
	res.OK = true
	// Later backups carry on in the same repository with the password
	// that just worked.
	if err := saveRestoredPassword(env, *path, dest, password); err != nil {
		fmt.Fprintf(stderr, "Restored, but couldn't save the backup's password for later backups: %v\n", err)
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(res)
	} else {
		fmt.Fprintf(stdout, "Restored the backup from %s (%s). Linx is running again; everyone signs in with their accounts from the backup.\n",
			res.SnapshotTime.Local().Format("2 Jan 2006 15:04"), res.SnapshotID[:min(8, len(res.SnapshotID))])
	}
	return 0
}

// unpackBackupFile unpacks a backup file (docs/BACKUP.md §8 step 5) into a
// private folder under env.workDir; cleanup removes it.
func unpackBackupFile(env restoreEnv, file string) (dir string, cleanup func(), err error) {
	f, err := os.Open(file)
	if err != nil {
		return "", nil, fmt.Errorf("couldn't open the backup file: %w", err)
	}
	defer f.Close()
	if err := os.MkdirAll(env.workDir, 0o700); err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp(env.workDir, "backup-file-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	if _, err := backup.UnpackRepository(f, dir); err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

func restorePassword(env restoreEnv, file string, fromStdin bool) (string, error) {
	switch {
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("couldn't read %s: %w", file, err)
		}
		return strings.TrimSpace(string(b)), nil
	case fromStdin:
		b, err := io.ReadAll(io.LimitReader(env.stdin, 4096))
		if err != nil {
			return "", fmt.Errorf("couldn't read the password: %w", err)
		}
		return strings.TrimSpace(string(b)), nil
	case env.terminal:
		p, err := env.readPassword()
		if err != nil {
			return "", fmt.Errorf("couldn't read the password: %w", err)
		}
		return p, nil
	default:
		return "", errors.New("give the backup's password with --password-file or --password-stdin, or run this from a terminal")
	}
}

// restoreEverything does the restore itself. Until the control plane is
// stopped nothing on the server has changed, and a failure leaves it
// exactly as it was (Changed false).
func restoreEverything(ctx context.Context, env restoreEnv, t backup.Target, snapshotID string, progress io.Writer) (restoreResult, error) {
	runner := backup.Runner(env.run)
	snap, err := backup.FindSnapshot(ctx, runner, t, snapshotID)
	if errors.Is(err, backup.ErrWrongPassword) {
		return restoreResult{}, err
	}
	if err != nil {
		return restoreResult{}, fmt.Errorf("couldn't read that backup: %w", err)
	}
	res := restoreResult{SnapshotID: snap.ID, SnapshotTime: snap.Time}

	if err := os.MkdirAll(env.workDir, 0o700); err != nil {
		return res, err
	}
	work, err := os.MkdirTemp(env.workDir, "restore-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)
	fmt.Fprintln(progress, "Reading the backup...")
	if err := backup.RestoreFiles(ctx, runner, t, snap.ID, work); err != nil {
		return res, fmt.Errorf("couldn't read the backup's files: %w", err)
	}
	staged := filepath.Join(work, backup.StagingDir)
	keys := map[string][]byte{}
	for _, name := range restoreKeys {
		b, err := os.ReadFile(filepath.Join(staged, backup.KeysDirName, name))
		if err != nil || len(bytes.TrimSpace(b)) == 0 {
			return res, fmt.Errorf("the backup is missing its %s", name)
		}
		keys[name] = b
	}
	dump, err := os.Open(filepath.Join(staged, "database.pgcustom"))
	if err != nil {
		return res, errors.New("the backup is missing its database")
	}
	defer dump.Close()

	pg := backup.Postgres{Container: backupPostgresContainer, User: backupDBUser, Database: backupDBName,
		Run: runner, RunInput: env.runInput, Sleep: env.sleep, Reference: db.ReferenceScript}
	fmt.Fprintln(progress, "Loading the backup's database next to the live one...")
	if err := pg.Load(ctx, dump); err != nil {
		return res, err
	}
	fmt.Fprintln(progress, "Checking it's a Linx database...")
	if _, _, err := pg.Check(ctx); err != nil {
		return res, err
	}
	if err := pg.Prepare(ctx); err != nil {
		_ = pg.DropLoaded(ctx)
		return res, err
	}

	// From here on the server changes.
	fmt.Fprintln(progress, "Stopping Linx and putting the backup in place...")
	docker := func(args ...string) error { return env.run(ctx, nil, nil, "docker", args...) }
	if err := docker("stop", restoreControlPlane); err != nil {
		_ = pg.DropLoaded(ctx)
		return res, fmt.Errorf("couldn't stop the control plane: %w", err)
	}
	undoKeys, err := replaceKeys(env.secretsDir, keys)
	if err == nil {
		if err = pg.Swap(ctx); err != nil {
			undoKeys()
		}
	}
	if err != nil {
		_ = pg.DropLoaded(ctx)
		_ = docker("start", restoreControlPlane)
		return res, err
	}
	res.Changed = true

	fmt.Fprintln(progress, "Starting Linx again...")
	if err := docker("start", restoreControlPlane); err != nil {
		return res, fmt.Errorf("the backup is in place, but the control plane didn't start: %w", err)
	}
	// Asterisk's database connections were ended by the swap; a restart
	// also reloads everything the restored database says.
	if err := docker("restart", restoreAsterisk); err != nil {
		fmt.Fprintf(progress, "Couldn't restart Asterisk (%v); restart it with docker restart %s.\n", err, restoreAsterisk)
	}
	if err := waitHealthy(ctx, env, restoreControlPlane); err != nil {
		return res, fmt.Errorf("the backup is in place, but %w (sudo linx doctor; the database from before is kept as %s)", err, pg.BeforeName())
	}
	return res, nil
}

// replaceKeys writes the backup's keys over the live ones, keeping each
// live one as NAME.before-restore; undo puts them back. Written in place
// (not renamed over), so the file Docker mounts as the secret is the one
// that changes.
func replaceKeys(dir string, keys map[string][]byte) (undo func(), err error) {
	old := map[string][]byte{}
	undo = func() {
		for name, b := range old {
			_ = os.WriteFile(filepath.Join(dir, name), b, 0o600)
		}
	}
	for _, name := range restoreKeys {
		path := filepath.Join(dir, name)
		b, err := os.ReadFile(path)
		if err != nil {
			undo()
			return func() {}, fmt.Errorf("reading %s: %w", path, err)
		}
		if err := os.WriteFile(path+".before-restore", b, 0o600); err != nil {
			undo()
			return func() {}, err
		}
		old[name] = b
		if err := os.WriteFile(path, keys[name], 0o600); err != nil {
			undo()
			return func() {}, fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return undo, nil
}

// waitHealthy waits for a container's own health check to pass.
func waitHealthy(ctx context.Context, env restoreEnv, container string) error {
	deadline := time.Now().Add(env.healthTimeout)
	for {
		var out bytes.Buffer
		err := env.run(ctx, &out, nil, "docker", "inspect", "--format", "{{.State.Health.Status}}", container)
		if err == nil && strings.TrimSpace(out.String()) == "healthy" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the control plane didn't come back healthy within %s", env.healthTimeout)
		}
		env.sleep(2 * time.Second)
	}
}

// saveRestoredPassword keeps backing up to the repository just restored
// from: a destination's own password file, or the default local one's when
// the folder was the default repository and no destination is set up.
func saveRestoredPassword(env restoreEnv, path string, dest *backup.Destination, password string) error {
	m := env.destinationsManifest()
	name := ""
	switch {
	case dest != nil:
		name = dest.Name
	case path == defaultBackupRepo:
		dests, err := m.Load()
		if err != nil || len(dests) > 0 {
			return err
		}
		name = defaultDestinationName
	default:
		return nil
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.PasswordPath(name), []byte(password), 0o600)
}
