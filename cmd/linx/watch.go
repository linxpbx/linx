package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"linxpbx.com/linx/internal/backupagent"
	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/watch"
)

const watchUsage = `Usage:
  sudo linx watch          Run the checks and tell Linx about anything new
  sudo linx watch --now    The same, and print what it found

Normally a timer runs this every few minutes. It raises an alert the first
time a problem appears and says so again when it clears, so it reaches you
the way every other Linx alert does. A problem that is already open is not
reported twice.
`

// watchEnv is everything linx watch touches on the host, so tests can fake
// it.
type watchEnv struct {
	isRoot      bool
	savedConfig func() ([]byte, error)
	checks      doctor.Env
	// send hands the report to the control plane. Real use: docker exec,
	// the same way the backup agent reports.
	send func(ctx context.Context, payload []byte) ([]byte, error)
}

func realWatchEnv() watchEnv {
	d := realDoctorEnv()
	return watchEnv{
		isRoot: d.isRoot, savedConfig: d.savedConfig, checks: d.checks,
		send: func(ctx context.Context, payload []byte) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "docker", "exec", "-i",
				backupagent.ControlPlaneContainer, backupagent.ControlPlaneBinary, "watch", "report")
			cmd.Stdin = bytes.NewReader(payload)
			return cmd.CombinedOutput()
		},
	}
}

func runWatch(ctx context.Context, env watchEnv, args []string, stdout, stderr io.Writer) int {
	now := false
	for _, a := range args {
		switch a {
		case "--now":
			now = true
		case "help", "-h", "--help":
			fmt.Fprint(stdout, watchUsage)
			return 0
		default:
			fmt.Fprintf(stderr, "Unknown option %q.\n\n%s", a, watchUsage)
			return 2
		}
	}
	if !env.isRoot {
		fmt.Fprintln(stderr, "linx watch reads Linx's settings and certificates and must run as root. Try: sudo linx watch")
		return 1
	}
	b, err := env.savedConfig()
	if err != nil {
		fmt.Fprintln(stderr, "Linx isn't set up on this server yet. Run: sudo linx setup")
		return 1
	}
	cfg, err := installer.ParseConfig(strings.NewReader(string(b)))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	problems := watch.Find(findings(doctor.Run(ctx, env.checks, cfg)))
	trigger := "timer"
	if now {
		trigger = "now"
	}
	payload, err := json.Marshal(watch.Report{Trigger: trigger, Problems: problems})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if out, err := env.send(ctx, payload); err != nil {
		// The control plane being unreachable is itself the kind of thing
		// worth knowing, but there is nowhere to report it to: the next run
		// tries again, as the backup agent and the firewall sync do.
		fmt.Fprintf(stderr, "Can't tell Linx what the checks found: %v: %s\n", err, strings.TrimSpace(string(out)))
		return 1
	}
	if now {
		if len(problems) == 0 {
			fmt.Fprintln(stdout, "Nothing wrong that linx doctor can see.")
			return 0
		}
		fmt.Fprintf(stdout, "%s:\n", plural(len(problems), "problem"))
		for _, p := range problems {
			fmt.Fprintf(stdout, "  %-8s %s\n", p.Severity, p.Title)
		}
		fmt.Fprintln(stdout, "\nAnything new has been sent to your alert channels. See them all with: sudo linx doctor")
	}
	return 0
}

// findings flattens doctor's sections into what watch reads. doctor's own
// levels are installer.Level, and watch keeps its own copies so the control
// plane doesn't link doctor; this is where the two meet, and
// TestWatchLevelsMatchDoctors holds them together.
func findings(sections []doctor.Section) []watch.Finding {
	var out []watch.Finding
	for _, sec := range sections {
		for _, r := range sec.Results {
			out = append(out, watch.Finding{Section: sec.Name, Level: int(r.Level), Message: r.Message, Fix: r.Fix})
		}
	}
	return out
}
