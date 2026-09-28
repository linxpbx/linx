package installer

import (
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestOpsAgentPlan(t *testing.T) {
	resolve := func(p string) (string, error) {
		switch p {
		case "/home/owner/linx-ops-agent", "/usr/local/bin/linx-ops-agent":
			return p, nil
		}
		return "", fs.ErrNotExist
	}
	statOK := func(string) (os.FileInfo, error) { return nil, nil }
	statMissing := func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }

	plan := OpsAgentPlan("/home/owner/linx", resolve, statOK)
	if len(plan) != 5 || plan[0].Cmd.String() != "install -m 0755 -o root -g root /home/owner/linx-ops-agent /usr/local/bin/linx-ops-agent" {
		t.Fatalf("from a download: %+v", plan)
	}
	unit := string(plan[1].File.Data)
	if !strings.Contains(unit, "Restart=always") || !strings.Contains(unit, "ExecStart=/usr/local/bin/linx-ops-agent") {
		t.Errorf("unit:\n%s", unit)
	}
	var enables, starts bool
	for _, s := range plan {
		if s.Cmd == nil {
			continue
		}
		switch s.Cmd.String() {
		case "systemctl enable linx-ops-agent.service":
			enables = true
		case "systemctl restart linx-ops-agent.service":
			starts = true
		}
	}
	if !enables || !starts {
		t.Errorf("service not enabled/started: %+v", plan)
	}
	if plan := OpsAgentPlan("/usr/local/bin/linx", resolve, statOK); len(plan) != 4 {
		t.Errorf("already installed (still writes the unit): %+v", plan)
	}
	if plan := OpsAgentPlan("/home/owner/linx", resolve, statMissing); len(plan) != 0 {
		t.Errorf("not built alongside linx: %+v", plan)
	}
}
