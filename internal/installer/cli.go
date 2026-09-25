package installer

import "path/filepath"

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
