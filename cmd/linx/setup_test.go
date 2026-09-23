package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os/exec"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/hostinfo"
)

// hostRunner fakes a host where only the listed commands exist.
type hostRunner map[string]string

func (h hostRunner) Run(_ context.Context, _ []string, name string, args ...string) ([]byte, error) {
	out, ok := h[strings.Join(append([]string{name}, args...), " ")]
	if !ok {
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	return []byte(out), nil
}

func testEnv(stdin string, files map[string]string) setupEnv {
	return setupEnv{
		detect: func() hostinfo.Info {
			return hostinfo.Info{GOOS: "linux", OSID: "ubuntu", OSVersionID: "24.04", OSCodename: "noble",
				Arch: "amd64", CPUs: 4, MemBytes: 8 << 30, DiskFree: 100 << 30, DiskTotal: 120 << 30}
		},
		runner:      hostRunner{"dpkg --print-architecture": "amd64"}, // no Docker installed
		stdin:       strings.NewReader(stdin),
		interactive: true,
		lanAddress:  func() netip.Addr { return netip.MustParseAddr("192.168.1.20") },
		savedConfig: func() ([]byte, error) { return nil, fs.ErrNotExist },
		readFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
	}
}

func TestSetupInteractiveDryRun(t *testing.T) {
	// Answers: accept profile, install Docker, choose Portainer.
	env := testEnv("\ny\n2\n", nil)
	var out, errOut bytes.Buffer
	code := runSetup(context.Background(), []string{"--dry-run"}, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\nstdout: %s", code, errOut.String(), out.String())
	}
	for _, want := range []string{
		"Suggested resource profile: lite",
		"Docker isn't installed",
		"Install Docker Engine and Docker Compose",
		"Start Portainer",
		"Create the internal certificate authority",
		"-c <script>",
		"Save your answers",
		"Dry run: nothing was changed.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetupConfigFile(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantCode int
		wantErr  string
	}{
		{"docker not allowed", "version: 1\n", 1, "Linx needs Docker"},
		{"docker allowed", "version: 1\ndocker:\n  install: true\n", 0, ""},
		{"invalid", "version: 1\ncontainer_ui: dockge\n", 1, "container_ui"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := testEnv("", map[string]string{"s.yaml": tt.yaml})
			env.interactive = false
			var out, errOut bytes.Buffer
			code := runSetup(context.Background(), []string{"--dry-run", "--config", "s.yaml"}, &out, &errOut, env)
			if code != tt.wantCode || !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("exit %d stderr %q, want %d containing %q", code, errOut.String(), tt.wantCode, tt.wantErr)
			}
		})
	}
}

func TestSetupNeedsRootAndTerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), nil, &out, &errOut, testEnv("", nil)); code != 1 || !strings.Contains(errOut.String(), "sudo") {
		t.Errorf("non-root: exit %d, %q", code, errOut.String())
	}
	env := testEnv("", nil)
	env.interactive = false
	errOut.Reset()
	if code := runSetup(context.Background(), []string{"--dry-run"}, &out, &errOut, env); code != 1 || !strings.Contains(errOut.String(), "--config") {
		t.Errorf("no terminal: exit %d, %q", code, errOut.String())
	}
}

func TestPrompterChoose(t *testing.T) {
	p := &prompter{in: bufio.NewReader(strings.NewReader("9\nperformance\n")), out: &bytes.Buffer{}}
	got, err := p.choose("?", []string{"lite", "standard", "performance"}, nil, "lite")
	if err != nil || got != "performance" {
		t.Errorf("choose = %q, %v", got, err)
	}
	p = &prompter{in: bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{}}
	if _, err := p.confirm("?", true); !errors.Is(err, io.EOF) {
		t.Errorf("confirm at EOF: %v", err)
	}
}
