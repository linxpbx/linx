package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

pending  Prints "run manual", "run scheduled" or "skip": whether a backup is
         due right now (docs/BACKUP.md §5, §8 step 3). linx-backup-agent
         runs this through docker exec, the same trust as linx user;
         nothing else calls it.
report   Reads a completed run's outcome as JSON from stdin (trigger,
         started_at, finished_at, destinations, error — the shape
         linx backup --json prints, with those three fields added) and
         records it: history, the "backup failure" alert, clearing any
         pending "back up now" request.
`

// runBackupCommand runs `backup ...` against the database from the
// container's own configuration.
func runBackupCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, backupCmdUsage)
		return 0
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
	svc := &backupschedule.Service{Store: st, Alerts: engine, Now: time.Now}

	switch args[0] {
	case "pending":
		return backupPending(ctx, svc, stdout, stderr)
	case "report":
		return backupReport(ctx, st, svc, stdin, stdout, stderr)
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
		run.Destinations = append(run.Destinations, backupschedule.Destination{Name: d.Name, OK: d.OK, SnapshotID: d.SnapshotID, Error: d.Error})
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
