package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"linxpbx.com/linx/internal/backup"
)

const backupExportUsage = `linx backup export — the backups kept on this server, as one file

Usage:
  sudo linx backup export --out FILE [--json]

Writes the backups kept on this server (the first local destination, or
` + defaultBackupRepo + ` when none is set up) to FILE: the same file
System → Backups → Download gives you, and that the setup wizard's "A backup
file on my computer" or "sudo linx restore --file FILE" takes back
(docs/BACKUP.md §8 step 5). It stays encrypted: it needs that backup's
password, which is in ` + "/etc/linx/secrets/linx-backup/NAME.password" + `.

--json prints one JSON line with the outcome, the password included
(linx-backup-agent passes it to the browser that asked, once).
`

// exportResult is --json's output (internal/backupagent's exportResult
// reads the same shape).
type exportResult struct {
	OK           bool      `json:"ok"`
	Error        string    `json:"error,omitempty"`
	Destination  string    `json:"destination,omitempty"`
	Size         int64     `json:"size,omitempty"`
	SnapshotID   string    `json:"snapshot_id,omitempty"`
	SnapshotTime time.Time `json:"snapshot_time,omitzero"`
	Password     string    `json:"password,omitempty"`
}

func runBackupExport(ctx context.Context, args []string, stdout, stderr io.Writer, env backupEnv) int {
	fs := flag.NewFlagSet("backup export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, backupExportUsage) }
	out := fs.String("out", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *out == "" {
		fmt.Fprint(stderr, backupExportUsage)
		return 2
	}
	fail := func(format string, a ...any) int {
		msg := fmt.Sprintf(format, a...)
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(exportResult{Error: msg})
		} else {
			fmt.Fprintln(stderr, msg)
		}
		return 1
	}
	if !env.isRoot {
		return fail("linx backup export must run as root. Try: sudo linx backup export --out FILE")
	}
	d, err := localDestination(env)
	if err != nil {
		return fail("%v", err)
	}
	target, err := env.destinationsManifest().Target(d)
	if err != nil {
		return fail("No backup has been made on this server yet (%v).", err)
	}
	snap, err := backup.FindSnapshot(ctx, backup.Runner(env.run), target, "latest")
	if err != nil {
		return fail("No backup has been made on this server yet (%v).", err)
	}

	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fail("Couldn't create %s: %v", *out, err)
	}
	err = backup.PackRepository(d.Path, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(*out)
		return fail("Couldn't write the backup file: %v", err)
	}
	info, err := os.Stat(*out)
	if err != nil {
		return fail("%v", err)
	}
	res := exportResult{OK: true, Destination: d.Name, Size: info.Size(), SnapshotID: snap.ID, SnapshotTime: snap.Time}
	if *asJSON {
		res.Password = target.Password
		_ = json.NewEncoder(stdout).Encode(res)
		return 0
	}
	fmt.Fprintf(stdout, "Wrote %s (%d MB; newest backup in it: %s). Its password is in %s.\n",
		*out, (res.Size+(1<<20)-1)>>20, snap.Time.Local().Format("2 Jan 2006 15:04"), env.destinationsManifest().PasswordPath(d.Name))
	return 0
}

// localDestination is the repository a backup file is made from: the first
// local destination, or the default one when no destination is set up.
func localDestination(env backupEnv) (backup.Destination, error) {
	dests, err := env.destinationsManifest().Load()
	if err != nil {
		return backup.Destination{}, fmt.Errorf("couldn't read this server's backup destinations: %w", err)
	}
	if len(dests) == 0 {
		return backup.Destination{Name: defaultDestinationName, Kind: backup.KindLocal, Path: defaultBackupRepo}, nil
	}
	for _, d := range dests {
		if d.Kind == backup.KindLocal {
			return d, nil
		}
	}
	return backup.Destination{}, errors.New("backups don't go to a folder on this server, so there's nothing here to download: " +
		"add one with sudo linx backup destination add --kind local --path /var/backups/linx local")
}
