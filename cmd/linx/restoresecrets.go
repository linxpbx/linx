package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/term"

	"linxpbx.com/linx/internal/backup"
	"linxpbx.com/linx/internal/installer"
)

const restoreSecretsUsage = `linx restore-secrets — restore the encryption keys from a backup

Usage:
  sudo linx restore-secrets --yes [--password-file PATH] REPO SNAPSHOT

  REPO             The restic repository the backup is in.
  SNAPSHOT         Which backup (a snapshot id; "latest" for the newest one).
  --yes            Required: this overwrites this server's own encryption
                   keys and can't be undone.
  --password-file  Read the repository's password from this file instead
                   of asking for it.

This is the first of restore's two steps (docs/BACKUP.md §4): run this once,
over SSH, on a freshly installed server before finishing setup in the
browser, which does the second step (the database itself). It only ever
touches the two secret files a restore can't do without; nothing else on
this server is changed.
`

type restoreSecretsEnv struct {
	isRoot       bool
	terminal     bool
	secretsDir   string // defaults to installer.SecretsDir; overridden in tests only
	readPassword func() (string, error)
	run          func(ctx context.Context, w io.Writer, name string, args ...string) error
}

func realRestoreSecretsEnv() restoreSecretsEnv {
	be := realBackupEnv()
	return restoreSecretsEnv{
		isRoot:     be.isRoot,
		terminal:   term.IsTerminal(int(os.Stdin.Fd())),
		secretsDir: installer.SecretsDir,
		readPassword: func() (string, error) {
			fmt.Fprint(os.Stderr, "Repository password: ")
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(os.Stderr)
			return string(b), err
		},
		run: be.run,
	}
}

func runRestoreSecrets(ctx context.Context, args []string, stdout, stderr io.Writer, env restoreSecretsEnv) int {
	fs := flag.NewFlagSet("restore-secrets", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, restoreSecretsUsage) }
	yes := fs.Bool("yes", false, "")
	passwordFile := fs.String("password-file", "", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprint(stderr, restoreSecretsUsage)
		return 2
	}
	repo, snapshotID := fs.Arg(0), fs.Arg(1)

	if !env.isRoot {
		fmt.Fprintln(stderr, "linx restore-secrets must run as root. Try: sudo linx restore-secrets ...")
		return 1
	}
	if !*yes {
		fmt.Fprintf(stderr, "This overwrites this server's own encryption keys:\n\n  %s\n  %s\n\nIt can't be undone. Run again with --yes once you're sure.\n",
			filepath.Join(env.secretsDir, "linx_db_encryption_key"), filepath.Join(env.secretsDir, "linx_jwt_signing_key"))
		return 1
	}

	password := ""
	if *passwordFile != "" {
		b, err := os.ReadFile(*passwordFile)
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't read %s: %v\n", *passwordFile, err)
			return 1
		}
		password = string(b)
	} else if env.terminal {
		p, err := env.readPassword()
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't read the password: %v\n", err)
			return 1
		}
		password = p
	} else {
		fmt.Fprintln(stderr, "Give the repository password with --password-file, or run this from a terminal.")
		return 1
	}

	pairID, err := backup.PairIDForSnapshot(ctx, env.run, repo, password, snapshotID)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't read that snapshot: %v\n", err)
		return 1
	}

	target, err := os.MkdirTemp("", "linx-restore-*")
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	defer os.RemoveAll(target)

	if err := backup.RestoreKeys(ctx, env.run, repo, password, snapshotID, target); err != nil {
		fmt.Fprintf(stderr, "Couldn't restore the keys: %v\n", err)
		return 1
	}

	restoredKeys := filepath.Join(target, backup.KeysPath)
	if err := os.MkdirAll(env.secretsDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	for _, name := range []string{"linx_db_encryption_key", "linx_jwt_signing_key"} {
		b, err := os.ReadFile(filepath.Join(restoredKeys, name))
		if err != nil {
			fmt.Fprintf(stderr, "The snapshot is missing %s: %v\n", name, err)
			return 1
		}
		if err := os.WriteFile(filepath.Join(env.secretsDir, name), b, 0o600); err != nil {
			fmt.Fprintf(stderr, "Couldn't write %s: %v\n", name, err)
			return 1
		}
	}

	fmt.Fprintf(stdout, "Restored this backup's encryption keys (pair %s).\n"+
		"Restart the Linx stack, then finish restoring in the browser's setup wizard (docs/BACKUP.md §4) — it will refuse any database that isn't from this same pair.\n", pairID)
	return 0
}
