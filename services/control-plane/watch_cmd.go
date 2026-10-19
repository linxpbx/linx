package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/safehttp"
	"linxpbx.com/linx/internal/store"
	"linxpbx.com/linx/internal/watch"
	"linxpbx.com/linx/internal/webhook"
)

const watchCmdUsage = `Usage:
  watch report   Read a watch run's problems on stdin and raise alerts for them

Run by linx watch on the host, through docker exec, like the backup agent's
commands. Not part of the API: only someone who controls Docker on this
server can reach it.
`

// runWatchCommand runs `watch ...` against the database from the
// container's own configuration.
func runWatchCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, watchCmdUsage)
		return 0
	}
	if args[0] != "report" {
		fmt.Fprintf(stderr, "Unknown watch command %q.\n\n%s", args[0], watchCmdUsage)
		return 2
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
	encKey, err := dbsecret.LoadKey(dbsecret.KeyPathFromEnv(os.Getenv))
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the database encryption key: %v\n", err)
		return 1
	}
	sealer := dbsecret.NewSealer(encKey)
	client := safehttp.NewClient(safehttp.Policy{Own: own, Allowlist: webhook.Allowlist(st)}, safehttp.Options{})
	engine := &alert.Engine{Store: st, Sender: &alert.Sender{Client: client, Sealer: sealer, Now: time.Now}}
	return watchReport(ctx, st, engine, stdin, stdout, stderr)
}

// watchAlerts is what watchReport needs of the store, so a test can stand in
// for it.
type watchAlerts interface {
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
	ListAlerts(ctx context.Context, tenant uuid.UUID, status string, before *uuid.UUID, limit int) ([]alert.Alert, error)
}

// watchEngine is the half of the alert engine this uses.
type watchEngine interface {
	Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
	Resolve(ctx context.Context, tenant uuid.UUID, key string) error
}

// watchReport raises an alert for each problem a watch run found, and
// resolves the ones that have cleared.
//
// Nothing here decides whether to tell the admin: firing an alert that is
// already open only bumps it, so a problem that lasts a week is mentioned
// once and then reminded about on the engine's own schedule. That is the
// whole reason the problems carry stable keys.
func watchReport(ctx context.Context, st watchAlerts, engine watchEngine, stdin io.Reader, stdout, stderr io.Writer) int {
	var rep watch.Report
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&rep); err != nil {
		fmt.Fprintf(stderr, "Can't read the watch report: %v\n", err)
		return 2
	}
	tenant, err := st.DefaultTenant(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the Linx database: %v\n", err)
		return 1
	}

	found := make(map[string]bool, len(rep.Problems))
	for _, p := range rep.Problems {
		if !strings.HasPrefix(p.Key, watch.KeyPrefix) {
			// Only this command's own keys, so a wrong or tampered-with
			// report can't open an alert that looks like another part of
			// Linx raised it.
			fmt.Fprintf(stderr, "Ignoring a problem whose key isn't the watch's: %q\n", p.Key)
			continue
		}
		found[p.Key] = true
		if err := engine.Fire(ctx, tenant, p.Key, p.Severity, p.Title, p.Message, ""); err != nil {
			fmt.Fprintf(stderr, "Can't raise an alert: %v\n", err)
			return 1
		}
	}

	// Anything the watch had open and no longer sees has cleared. Only
	// alerts this command raised are touched.
	open, err := st.ListAlerts(ctx, tenant, alert.StatusOpen, nil, 200)
	if err != nil {
		fmt.Fprintf(stderr, "Can't read the open alerts: %v\n", err)
		return 1
	}
	cleared := 0
	for _, a := range open {
		if !strings.HasPrefix(a.Key, watch.KeyPrefix) || found[a.Key] {
			continue
		}
		if err := engine.Resolve(ctx, tenant, a.Key); err != nil {
			fmt.Fprintf(stderr, "Can't resolve an alert: %v\n", err)
			return 1
		}
		cleared++
	}
	fmt.Fprintf(stdout, "problems %d cleared %d\n", len(found), cleared)
	return 0
}
