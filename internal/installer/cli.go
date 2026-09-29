package installer

import (
	"os"
	"path/filepath"
)

// CLIPath is where setup installs the linx command, so `sudo linx doctor`
// and the rest work from anywhere (sudo's PATH includes /usr/local/bin).
const CLIPath = "/usr/local/bin/linx"

// CLIPlan copies the running linx binary (self) to CLIPath, unless it is
// already running from there. resolve follows symlinks (filepath.EvalSymlinks).
func CLIPlan(self string, resolve func(string) (string, error)) Plan {
	if self == "" {
		return nil
	}
	a, errA := resolve(self)
	b, errB := resolve(CLIPath)
	if errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b) {
		return nil
	}
	return Plan{{
		Title: "Install the linx command as " + CLIPath,
		Cmd:   &Cmd{Name: "install", Args: []string{"-m", "0755", "-o", "root", "-g", "root", self, CLIPath}},
	}}
}

// HelperBinariesPlan copies the helper programs that sit next to the
// running linx (self) to /usr/local/bin with it. The web install runs its
// later steps from CLIPath, where FirewallSyncPlan, BackupAgentPlan and
// OpsAgentPlan look for them next to linx: without this copy they found
// nothing and silently skipped all three (found on the VPS demo). Missing
// ones, and ones already in place, are left alone.
func HelperBinariesPlan(self string, resolve func(string) (string, error), stat func(string) (os.FileInfo, error)) Plan {
	if self == "" {
		return nil
	}
	var p Plan
	for _, dst := range []string{FirewallSyncPath, BackupAgentPath, OpsAgentPath} {
		src := filepath.Join(filepath.Dir(self), filepath.Base(dst))
		if _, err := stat(src); err != nil {
			continue
		}
		a, errA := resolve(src)
		b, errB := resolve(dst)
		if errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b) {
			continue
		}
		p = append(p, Step{
			Title: "Install " + filepath.Base(dst) + " as " + dst,
			Cmd:   &Cmd{Name: "install", Args: []string{"-m", "0755", "-o", "root", "-g", "root", src, dst}},
		})
	}
	return p
}
