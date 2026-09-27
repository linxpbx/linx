package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/backupschedule"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/webhook"
)

const backupCmdUsage = `Usage:
  linx-control-plane backup pending
  linx-control-plane backup report
  linx-control-plane backup restore-take
  linx-control-plane backup restore-failed
  linx-control-plane backup restore-done
  linx-control-plane backup export-take
  linx-control-plane backup export-put ID
  linx-control-plane backup export-done
  linx-control-plane backup upload-read ID
  linx-control-plane backup upload-delete ID

pending  Prints "run restore", "run export", "run manual", "run scheduled" or "skip":
         whether a restore is waiting or a backup is due right now (docs/BACKUP.md §5, §8 step 3). linx-backup-agent
         runs this through docker exec, the same trust as linx user;
         nothing else calls it.
report   Reads a completed run's outcome as JSON from stdin (trigger,
         started_at, finished_at, destinations, error — the shape
         linx backup --json prints, with those three fields added) and
         records it: history, the "backup failure" alert, clearing any
         pending "back up now" request.
restore-take    Prints the waiting restore request as JSON, password
                included, and marks it running (docs/BACKUP.md §4); prints
                nothing if none is waiting.
restore-failed  Reads {"id", "error"} from stdin: that request failed.
restore-done    Reads a finished restore's details from stdin and writes
                them to the (restored) database's audit log.
export-take     Prints the waiting backup-file request as JSON ({"id"}) and
                marks it preparing (docs/BACKUP.md §8 step 5); prints
                nothing if none is waiting.
export-put      Reads the backup file for request ID from stdin into the
                transfer folder.
export-done     Reads {"id", "ok", "error", "snapshot_id", "snapshot_time",
                "password"} from stdin: the file is ready (or failed).
upload-read     Writes uploaded backup file ID to stdout.
upload-delete   Removes uploaded backup file ID.
`

// runBackupCommand runs `backup ...` against the database from the
// container's own configuration.
func runBackupCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, backupCmdUsage)
		return 0
	}
	// Moving a backup file (up to about 2 GB) takes longer than a query.
	timeout := 30 * time.Second
	if args[0] == "export-put" || args[0] == "upload-read" {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pool, err := db.Connect(ctx, db.ConfigFromEnv(os.Getenv))
	if err != nil {
		fmt.Fprintf(stderr, "Can't reach the Linx database: %v\n", err)
		return 1
	}
	defer pool.Close()
	st := store.New(pool)

	own, err := safehttp.OwnNetworks()
	if err != nil {
		fmt.Fprintf(stderr, "Can't read network interfaces: %v\n", err)
		return 1
	}
	policy := safehttp.Policy{Own: own, Allowlist: webhook.Allowlist(st)}
	guardedClient := safehttp.NewClient(policy, safehttp.Options{})
	encKey, err := dbsecret.LoadKey(dbsecret.KeyPathFromEnv(os.Getenv))
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the database encryption key: %v\n", err)
		return 1
	}
	sealer := dbsecret.NewSealer(encKey)
	alertSender := &alert.Sender{Client: guardedClient, Sealer: sealer, Now: time.Now}
	engine := &alert.Engine{Store: st, Sender: alertSender}
	svc := &backupschedule.Service{Store: st, Alerts: engine, Restores: st, Sealer: sealer, Downloads: st,
		Transfer: &backupschedule.Transfer{Dir: envOr(os.Getenv, "LINX_BACKUP_TRANSFER_DIR", backupschedule.DefaultTransferDir)}, Now: time.Now}

	switch args[0] {
	case "pending":
		return backupPending(ctx, svc, stdout, stderr)
	case "report":
		return backupReport(ctx, st, svc, stdin, stdout, stderr)
	case "restore-take":
		return restoreTake(ctx, svc, stdout, stderr)
	case "restore-failed":
		return restoreFailed(ctx, svc, stdin, stdout, stderr)
	case "restore-done":
		return restoreDone(ctx, st, svc, stdin, stdout, stderr)
	case "export-take":
		return exportTake(ctx, svc, stdout, stderr)
	case "export-put", "upload-read", "upload-delete":
		if len(args) != 2 {
			fmt.Fprint(stderr, backupCmdUsage)
			return 2
		}
		id, err := uuid.Parse(args[1])
		if err != nil {
			fmt.Fprintln(stderr, "Give the request's id.")
			return 2
		}
		switch args[0] {
		case "export-put":
			return exportPut(ctx, svc, id, stdin, stdout, stderr)
		case "upload-read":
			return uploadRead(svc, id, stdout, stderr)
		default:
			return uploadDelete(svc, id, stdout, stderr)
		}
	case "export-done":
		return exportDone(ctx, svc, stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown backup command %q.\n\n%s", args[0], backupCmdUsage)
		return 2
	}
}

func backupPending(ctx context.Context, svc *backupschedule.Service, stdout, stderr io.Writer) int {
	due, trigger, err := svc.Pending(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the backup schedule: %v\n", err)
		return 1
	}
	if !due {
		fmt.Fprintln(stdout, "skip")
		return 0
	}
	fmt.Fprintf(stdout, "run %s\n", trigger)
	return 0
}

// reportInput is what `report` reads from stdin: linx backup --json's own
// shape (destinations, error) plus what only linx-backup-agent knows.
type reportInput struct {
	Trigger      string              `json:"trigger"`
	StartedAt    time.Time           `json:"started_at"`
	FinishedAt   time.Time           `json:"finished_at"`
	Destinations []reportDestination `json:"destinations"`
	Error        string              `json:"error,omitempty"`
}

type reportDestination struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	Size       int64  `json:"size,omitempty"`
	Error      string `json:"error,omitempty"`
}

// tenantSource is the database access backupReport needs to attribute a
// system-driven run to the one tenant (internal/store.Store.DefaultTenant).
type tenantSource interface {
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
}

func backupReport(ctx context.Context, ts tenantSource, svc *backupschedule.Service, stdin io.Reader, stdout, stderr io.Writer) int {
	var in reportInput
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		fmt.Fprintf(stderr, "Couldn't read the report: %v\n", err)
		return 2
	}
	if in.Trigger != backupschedule.TriggerManual && in.Trigger != backupschedule.TriggerScheduled {
		fmt.Fprintf(stderr, "trigger must be %q or %q.\n", backupschedule.TriggerManual, backupschedule.TriggerScheduled)
		return 2
	}
	tenant, err := ts.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	id, err := uuid.NewV7()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	run := backupschedule.Run{
		ID: id, TenantID: tenant, Trigger: in.Trigger, StartedAt: in.StartedAt, FinishedAt: in.FinishedAt,
		Status: reportStatus(in), Error: in.Error, Destinations: make([]backupschedule.Destination, 0, len(in.Destinations)),
	}
	for _, d := range in.Destinations {
		run.Destinations = append(run.Destinations, backupschedule.Destination{Name: d.Name, OK: d.OK, SnapshotID: d.SnapshotID, Size: d.Size, Error: d.Error})
	}
	if err := svc.RecordRun(ctx, run); err != nil {
		fmt.Fprintf(stderr, "Couldn't record the backup: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Recorded backup %s (%s).\n", id, run.Status)
	return 0
}

// reportStatus computes the run's overall status from its destinations
// (docs/BACKUP.md §8 step 3): success if every one worked, partial if some
// did and some didn't, failure if none did (including no destination ever
// having been attempted, in.Error then explaining why).
func reportStatus(in reportInput) string {
	if len(in.Destinations) == 0 {
		return backupschedule.StatusFailure
	}
	ok, failed := 0, 0
	for _, d := range in.Destinations {
		if d.OK {
			ok++
		} else {
			failed++
		}
	}
	switch {
	case failed == 0:
		return backupschedule.StatusSuccess
	case ok == 0:
		return backupschedule.StatusFailure
	default:
		return backupschedule.StatusPartial
	}
}

// restoreRequestJSON is restore-take's output: exactly what
// linx-backup-agent needs to run linx restore (internal/backupagent's
// restoreRequest reads the same shape).
type restoreRequestJSON struct {
	ID          uuid.UUID `json:"id"`
	Source      string    `json:"source"`
	Location    string    `json:"location"`
	Snapshot    string    `json:"snapshot"`
	Password    string    `json:"password"`
	RequestedBy string    `json:"requested_by"`
}

func restoreTake(ctx context.Context, svc *backupschedule.Service, stdout, stderr io.Writer) int {
	r, found, err := svc.TakeRestore(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't take the restore request: %v\n", err)
		return 1
	}
	if !found {
		return 0
	}
	if err := json.NewEncoder(stdout).Encode(restoreRequestJSON{
		ID: r.ID, Source: r.Source, Location: r.Location, Snapshot: r.Snapshot, Password: r.Password, RequestedBy: r.RequestedBy,
	}); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	return 0
}

func restoreFailed(ctx context.Context, svc *backupschedule.Service, stdin io.Reader, stdout, stderr io.Writer) int {
	var in struct {
		ID    uuid.UUID `json:"id"`
		Error string    `json:"error"`
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil || in.ID == uuid.Nil || in.Error == "" {
		fmt.Fprintln(stderr, `Give {"id": "...", "error": "..."} on stdin.`)
		return 2
	}
	if err := svc.FailRestore(ctx, in.ID, in.Error); err != nil {
		fmt.Fprintf(stderr, "Couldn't record the failed restore: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Recorded the failed restore.")
	return 0
}

func restoreDone(ctx context.Context, ts tenantSource, svc *backupschedule.Service, stdin io.Reader, stdout, stderr io.Writer) int {
	var in struct {
		Source      string    `json:"source"`
		Location    string    `json:"location"`
		SnapshotID  string    `json:"snapshot_id"`
		SnapshotAt  time.Time `json:"snapshot_time"`
		RequestedBy string    `json:"requested_by"`
	}
	if err := json.NewDecoder(stdin).Decode(&in); err != nil || in.SnapshotID == "" {
		fmt.Fprintln(stderr, "Give the finished restore's details as JSON on stdin.")
		return 2
	}
	tenant, err := ts.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}
	if err := svc.RecordRestored(ctx, tenant, backupschedule.RestoredInfo{
		Source: in.Source, Location: in.Location, SnapshotID: in.SnapshotID, SnapshotAt: in.SnapshotAt, RequestedBy: in.RequestedBy,
	}); err != nil {
		fmt.Fprintf(stderr, "Couldn't record the restore: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Recorded the restore.")
	return 0
}

func exportTake(ctx context.Context, svc *backupschedule.Service, stdout, stderr io.Writer) int {
	d, found, err := svc.TakeDownload(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't take the backup-file request: %v\n", err)
		return 1
	}
	if !found {
		return 0
	}
	if err := json.NewEncoder(stdout).Encode(map[string]any{"id": d.ID}); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	return 0
}

func exportPut(ctx context.Context, svc *backupschedule.Service, id uuid.UUID, stdin io.Reader, stdout, stderr io.Writer) int {
	n, err := svc.PutDownloadFile(ctx, id, stdin)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't store the backup file: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Stored the backup file (%d bytes).\n", n)
	return 0
}

func exportDone(ctx context.Context, svc *backupschedule.Service, stdin io.Reader, stdout, stderr io.Writer) int {
	var in struct {
		ID           uuid.UUID `json:"id"`
		OK           bool      `json:"ok"`
		Error        string    `json:"error"`
		SnapshotID   string    `json:"snapshot_id"`
		SnapshotTime time.Time `json:"snapshot_time"`
		Password     string    `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 64<<10)).Decode(&in); err != nil || in.ID == uuid.Nil {
		fmt.Fprintln(stderr, "Give the backup file's outcome as JSON on stdin.")
		return 2
	}
	if err := svc.FinishDownload(ctx, backupschedule.DownloadResult{ID: in.ID, OK: in.OK, Error: in.Error,
		SnapshotID: in.SnapshotID, SnapshotTime: in.SnapshotTime, Password: in.Password}); err != nil {
		fmt.Fprintf(stderr, "Couldn't record the backup file: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Recorded the backup file.")
	return 0
}

func uploadRead(svc *backupschedule.Service, id uuid.UUID, stdout, stderr io.Writer) int {
	f, err := svc.ReadUpload(id)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't open the uploaded backup file: %v\n", err)
		return 1
	}
	defer f.Close()
	if _, err := io.Copy(stdout, f); err != nil {
		fmt.Fprintf(stderr, "Couldn't send the uploaded backup file: %v\n", err)
		return 1
	}
	return 0
}

func uploadDelete(svc *backupschedule.Service, id uuid.UUID, stdout, stderr io.Writer) int {
	if err := svc.RemoveUpload(id); err != nil {
		fmt.Fprintf(stderr, "Couldn't remove the uploaded backup file: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Removed the uploaded backup file.")
	return 0
}

// backupSweepInterval is how often expired backup files, their passwords
// and unused uploads are removed.
const backupSweepInterval = 5 * time.Minute

func sweepBackupFiles(ctx context.Context, svc *backupschedule.Service, log *slog.Logger) {
	t := time.NewTicker(backupSweepInterval)
	defer t.Stop()
	for {
		if err := svc.SweepDownloads(ctx); err != nil && ctx.Err() == nil {
			log.Warn("removing expired backup files", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
