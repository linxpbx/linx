// Package installer holds the building blocks of `linx setup`: host checks,
// resource-profile suggestion, Docker and container-UI install plans, and the
// setup.yaml answers file. Changes are expressed as a Plan of Steps so they can
// be shown to the owner (and tested) before anything runs.
package installer

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner runs host commands. Tests use a fake.
type Runner interface {
	// Run executes name with args and extra environment (KEY=VALUE) and
	// returns combined stdout and stderr.
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.Bytes(), err
}

// output runs a command and returns trimmed stdout+stderr.
func output(ctx context.Context, r Runner, name string, args ...string) (string, error) {
	b, err := r.Run(ctx, nil, name, args...)
	return strings.TrimSpace(string(b)), err
}

// Step is one change to the host: either a command or a file write.
type Step struct {
	Title string
	Cmd   *Cmd
	File  *File
}

// Cmd is a command to run.
type Cmd struct {
	Env  []string
	Name string
	Args []string
}

func (c Cmd) String() string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

// File is a file to write. Parent directories are created with DirMode.
type File struct {
	Path    string
	Data    []byte
	Mode    fs.FileMode
	DirMode fs.FileMode
}

// Plan is an ordered list of steps.
type Plan []Step

func cmdStep(title string, name string, args ...string) Step {
	return Step{Title: title, Cmd: &Cmd{Name: name, Args: args}}
}

func aptStep(title string, args ...string) Step {
	return Step{Title: title, Cmd: &Cmd{Env: []string{"DEBIAN_FRONTEND=noninteractive"}, Name: "apt-get", Args: args}}
}

func fileStep(title, path string, data []byte, mode, dirMode fs.FileMode) Step {
	return Step{Title: title, File: &File{Path: path, Data: data, Mode: mode, DirMode: dirMode}}
}

// StepError reports which step failed, with the tail of its output.
type StepError struct {
	Step   Step
	Output string
	Err    error
}

func (e *StepError) Error() string { return fmt.Sprintf("%s: %v", e.Step.Title, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// Execute runs the plan in order and stops at the first failure. progress is
// called before each step.
func (p Plan) Execute(ctx context.Context, r Runner, progress func(Step)) error {
	for _, s := range p {
		progress(s)
		switch {
		case s.File != nil:
			if err := writeFile(*s.File); err != nil {
				return &StepError{Step: s, Err: err}
			}
		case s.Cmd != nil:
			out, err := r.Run(ctx, s.Cmd.Env, s.Cmd.Name, s.Cmd.Args...)
			if err != nil {
				return &StepError{Step: s, Output: tail(string(out), 20), Err: err}
			}
		}
	}
	return nil
}

// writeFile writes atomically (temp file + rename) with the given mode.
func writeFile(f File) error {
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, f.DirMode); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.Path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(f.Mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(f.Data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.Path)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
