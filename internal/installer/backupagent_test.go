package installer

import (
	"io/fs"
	"os"
	"testing"
)

func TestBackupAgentPlan(t *testing.T) {
	resolve := func(p string) (string, error) {
		switch p {
		case "/home/owner/linx-backup-agent", "/usr/local/bin/linx-backup-agent":
			return p, nil
		}
		return "", fs.ErrNotExist
	}
	statOK := func(string) (os.FileInfo, error) { return nil, nil }
	statMissing := func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }

	plan := BackupAgentPlan("/home/owner/linx", resolve, statOK)
	if len(plan) != 6 || plan[0].Cmd.String() != "install -m 0755 -o root -g root /home/owner/linx-backup-agent /usr/local/bin/linx-backup-agent" {
		t.Fatalf("from a download: %+v", plan)
	}
	var enables, starts bool
	for _, s := range plan {
		if s.Cmd == nil {
			continue
		}
		switch s.Cmd.String() {
		case "systemctl enable linx-backup-agent.timer":
			enables = true
		case "systemctl restart linx-backup-agent.timer":
			starts = true
		}
	}
	if !enables || !starts {
		t.Errorf("timer not enabled/started: %+v", plan)
	}

	if plan := BackupAgentPlan("/usr/local/bin/linx", resolve, statOK); len(plan) != 5 {
		t.Errorf("already installed (still writes units): %+v", plan)
	}
	if plan := BackupAgentPlan("/home/owner/linx", resolve, statMissing); len(plan) != 0 {
		t.Errorf("not built alongside linx: %+v", plan)
	}
	if plan := BackupAgentPlan("", resolve, statOK); len(plan) != 0 {
		t.Errorf("unknown executable: %+v", plan)
	}
}
