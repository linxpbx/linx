package installer

import (
	"strings"
	"testing"
)

func TestWatchPlanInstallsATimerThatRunsTheCheck(t *testing.T) {
	plan := WatchPlan()
	var units, enabled string
	for _, s := range plan {
		if s.File != nil {
			units += s.File.Path + "\n" + string(s.File.Data) + "\n"
		}
		if s.Cmd != nil {
			enabled += s.Cmd.Name + " " + strings.Join(s.Cmd.Args, " ") + "\n"
		}
	}
	for _, want := range []string{watchUnitPath, watchTimerPath, CLIPath + " watch", "OnUnitActiveSec=5min"} {
		if !strings.Contains(units, want) {
			t.Errorf("the plan's files don't mention %q:\n%s", want, units)
		}
	}
	// A oneshot: nothing is resident between runs, which is the whole
	// reason this costs almost nothing (docs/RESOURCES.md §3j).
	if !strings.Contains(units, "Type=oneshot") {
		t.Error("the service isn't a oneshot")
	}
	if !strings.Contains(enabled, "enable "+WatchTimer) || !strings.Contains(enabled, "restart "+WatchTimer) {
		t.Errorf("the timer isn't turned on and started:\n%s", enabled)
	}
}

// Unlike the other helpers there is no binary to copy: the checks are
// doctor's and they are already in the linx that setup installed. So the
// plan is never empty, and a one-binary dev build still gets the timer.
func TestWatchPlanNeedsNoBinaryOfItsOwn(t *testing.T) {
	if len(WatchPlan()) == 0 {
		t.Fatal("no plan")
	}
	for _, s := range WatchPlan() {
		if s.Cmd != nil && s.Cmd.Name == "install" {
			t.Errorf("the plan copies a binary: %v", s.Cmd.Args)
		}
	}
}
