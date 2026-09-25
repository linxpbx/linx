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
