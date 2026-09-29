package installer

import (
	"io/fs"
	"testing"
)

func TestCLIPlan(t *testing.T) {
	resolve := func(p string) (string, error) {
		switch p {
		case "/home/owner/linx":
			return p, nil
		case "/usr/local/bin/linx":
			return p, nil
		case "/usr/bin/linx": // a symlink to the installed one
			return "/usr/local/bin/linx", nil
		}
		return "", fs.ErrNotExist
	}
	plan := CLIPlan("/home/owner/linx", resolve)
	if len(plan) != 1 || plan[0].Cmd.String() != "install -m 0755 -o root -g root /home/owner/linx /usr/local/bin/linx" {
		t.Fatalf("from a download: %+v", plan)
	}
	if plan := CLIPlan("/usr/local/bin/linx", resolve); len(plan) != 0 {
		t.Errorf("already installed: %+v", plan)
	}
	if plan := CLIPlan("/usr/bin/linx", resolve); len(plan) != 0 {
		t.Errorf("through a symlink to the installed one: %+v", plan)
	}
	if plan := CLIPlan("", resolve); len(plan) != 0 {
		t.Errorf("unknown executable: %+v", plan)
	}
}

// The web install runs its later steps from /usr/local/bin/linx, so the
// helpers must be copied there with it (found on the VPS demo: all three
// were silently skipped).
func TestHelperBinariesPlan(t *testing.T) {
	have := map[string]bool{"/home/owner/linx-firewall-sync": true, "/home/owner/linx-ops-agent": true}
	stat := func(p string) (fs.FileInfo, error) {
		if have[p] {
			return nil, nil
		}
		return nil, fs.ErrNotExist
	}
	resolve := func(p string) (string, error) { return p, nil }
	plan := HelperBinariesPlan("/home/owner/linx", resolve, stat)
	if len(plan) != 2 ||
		plan[0].Cmd.String() != "install -m 0755 -o root -g root /home/owner/linx-firewall-sync /usr/local/bin/linx-firewall-sync" ||
		plan[1].Cmd.String() != "install -m 0755 -o root -g root /home/owner/linx-ops-agent /usr/local/bin/linx-ops-agent" {
		t.Errorf("%+v", plan)
	}
	if plan := HelperBinariesPlan("/usr/local/bin/linx", resolve, func(string) (fs.FileInfo, error) { return nil, nil }); len(plan) != 0 {
		t.Errorf("already in place: %+v", plan)
	}
}
