package installer

import (
	"io/fs"
	"os"
	"testing"
)

func TestFirewallSyncPlan(t *testing.T) {
	resolve := func(p string) (string, error) {
		switch p {
		case "/home/owner/linx-firewall-sync", "/usr/local/bin/linx-firewall-sync":
			return p, nil
		}
		return "", fs.ErrNotExist
	}
	statOK := func(string) (os.FileInfo, error) { return nil, nil }
	statMissing := func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }

	plan := FirewallSyncPlan("/home/owner/linx", resolve, statOK)
	if len(plan) != 6 || plan[0].Cmd.String() != "install -m 0755 -o root -g root /home/owner/linx-firewall-sync /usr/local/bin/linx-firewall-sync" {
		t.Fatalf("from a download: %+v", plan)
	}
	var enables, starts bool
	for _, s := range plan {
		if s.Cmd == nil {
			continue
		}
		switch s.Cmd.String() {
		case "systemctl enable linx-firewall-sync.timer":
			enables = true
		case "systemctl restart linx-firewall-sync.timer":
			starts = true
		}
	}
	if !enables || !starts {
		t.Errorf("timer not enabled/started: %+v", plan)
	}

	if plan := FirewallSyncPlan("/usr/local/bin/linx", resolve, statOK); len(plan) != 5 {
		t.Errorf("already installed (still writes units): %+v", plan)
	}
	if plan := FirewallSyncPlan("/home/owner/linx", resolve, statMissing); len(plan) != 0 {
		t.Errorf("not built alongside linx: %+v", plan)
	}
	if plan := FirewallSyncPlan("", resolve, statOK); len(plan) != 0 {
		t.Errorf("unknown executable: %+v", plan)
	}
}
