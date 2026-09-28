package main

import (
	"bytes"
	"context"
	"io/fs"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/hostinfo"
	"linxpbx.com/linx/internal/installer"
)

const (
	testCommit = "0123456789abcdef0123456789abcdef01234567"
	testToken  = "test-token-xxxxxxxxxxxxxxxxxxxx"
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

// pangolinConfig is a --config file with Pangolin in front, Portainer and
// test certificates.
const pangolinConfig = `version: 1
docker:
  install: true
container_ui: portainer
domain:
  name: lab.linxpbx.com
certificates:
  staging: true
front_door:
  kind: pangolin
  proxy_address: 192.168.1.30
  turn_udp_port: 3478
`

func testEnv(files map[string]string) setupEnv {
	return setupEnv{
		detect: func() hostinfo.Info {
			return hostinfo.Info{GOOS: "linux", OSID: "ubuntu", OSVersionID: "24.04", OSCodename: "noble",
				Arch: "amd64", CPUs: 4, MemBytes: 8 << 30, DiskFree: 100 << 30, DiskTotal: 120 << 30}
		},
		runner: hostRunner{"dpkg --print-architecture": "amd64"}, // no Docker installed
		lan: func() installer.LAN {
			return installer.LAN{Address: netip.MustParseAddr("192.168.1.20"), Network: netip.MustParsePrefix("192.168.1.0/24")}
		},
		savedConfig: func() ([]byte, error) { return nil, fs.ErrNotExist },
		readFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		commit:     testCommit,
		executable: "/home/owner/linx",
		resolve:    func(p string) (string, error) { return p, nil },
		stat:       func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist },
	}
}

func TestSetupConfigDryRun(t *testing.T) {
	env := testEnv(map[string]string{"s.yaml": pangolinConfig, installer.DNSTokenPath: testToken})
	var out, errOut bytes.Buffer
	code := runSetup(context.Background(), []string{"--dry-run", "--config", "s.yaml"}, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\nstdout: %s", code, errOut.String(), out.String())
	}
	for _, want := range []string{
		"Suggested resource profile: lite",
		"Docker isn't installed",
		"Install Docker Engine and Docker Compose",
		"Phones will be able to connect from your local network (192.168.1.0/24), where this server is 192.168.1.20.",
		"Install the firewall tools (nftables)",
		"Tell Docker to forward ports without a helper process per port",
		"$ systemctl restart docker",
		"Write the firewall rules: Allow phones from your local network (192.168.1.0/24) only",
		"$ systemctl reload-or-restart linx-firewall.service",
		"Start Portainer",
		"Create the internal certificate authority",
		"-c <script>",
		"Save the DNS token",
		"Get a test certificate for *.lab.linxpbx.com",
		"Write the Pangolin settings to add (/etc/linx/front-door/pangolin-dynamic-config.yml)",
		"Start the Linx services",
		"Point meet.lab.linxpbx.com, api.lab.linxpbx.com, turn.lab.linxpbx.com at this network's public address (DNS)",
		"$ docker compose --file /etc/linx/compose.yaml run --rm certd -records meet,api,turn",
		"Install the linx command as /usr/local/bin/linx",
		"Save your answers",
		"Dry run: nothing was changed.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

// TestSetupInstallsFirewallSync checks setup installs linx-firewall-sync
// (docs/TRUNKS.md §13 step 6) when it's built alongside linx, the same way
// it installs linx itself.
func TestSetupInstallsFirewallSync(t *testing.T) {
	env := testEnv(map[string]string{"s.yaml": pangolinConfig, installer.DNSTokenPath: testToken})
	env.stat = func(p string) (os.FileInfo, error) {
		if p == "/home/owner/linx-firewall-sync" {
			return nil, nil
		}
		return nil, fs.ErrNotExist
	}
	var out, errOut bytes.Buffer
	code := runSetup(context.Background(), []string{"--dry-run", "--config", "s.yaml"}, &out, &errOut, env)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\nstdout: %s", code, errOut.String(), out.String())
	}
	for _, want := range []string{
		"Install linx-firewall-sync as /usr/local/bin/linx-firewall-sync",
		"$ systemctl enable linx-firewall-sync.timer",
		"$ systemctl restart linx-firewall-sync.timer",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestPrintFirstAdmin(t *testing.T) {
	t.Run("creates the account", func(t *testing.T) {
		runner := hostRunner{
			"docker exec " + controlPlaneContainer + " " + controlPlaneBinary + " user create --email owner@example.com --name Owner --role system_admin": "Owner (owner@example.com, system_admin) can now set their password — it works once, for 24 hours:\n",
		}
		var out, errOut bytes.Buffer
		printFirstAdmin(context.Background(), &out, &errOut, setupEnv{runner: runner}, "owner@example.com", "Owner")
		if !strings.Contains(out.String(), "can now set their password") {
			t.Errorf("didn't relay the setup link:\n%s", out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("unexpected stderr: %s", errOut.String())
		}
	})
	t.Run("no email given", func(t *testing.T) {
		var out, errOut bytes.Buffer
		printFirstAdmin(context.Background(), &out, &errOut, setupEnv{}, "", "")
		if !strings.Contains(out.String(), "No first admin account created") {
			t.Errorf("didn't explain why no account was made:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "linx user create") {
			t.Errorf("didn't say how to create one later:\n%s", out.String())
		}
	})
	t.Run("docker exec fails", func(t *testing.T) {
		var out, errOut bytes.Buffer
		printFirstAdmin(context.Background(), &out, &errOut, setupEnv{runner: hostRunner{}}, "owner@example.com", "Owner")
		if !strings.Contains(errOut.String(), "Couldn't create the first admin account") {
			t.Errorf("didn't report the failure:\n%s", errOut.String())
		}
	})
}

func TestSetupConfigFile(t *testing.T) {
	const domain = "domain:\n  name: lab.linxpbx.com\n"
	tests := []struct {
		name     string
		yaml     string
		wantCode int
		wantErr  string
	}{
		{"docker not allowed", "version: 1\n", 1, "Linx needs Docker"},
		{"docker allowed", "version: 1\ndocker:\n  install: true\n" + domain, 0, ""},
		{"invalid", "version: 1\ncontainer_ui: dockge\n", 1, "container_ui"},
		{"no domain", "version: 1\ndocker:\n  install: true\n", 1, "domain.name: required"},
		{"no token", "version: 1\ndocker:\n  install: true\ndomain:\n  name: x.duckdns.org\n  dns_provider: duckdns\n", 1, "no DNS provider token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"s.yaml": tt.yaml}
			if tt.name != "no token" {
				files[installer.DNSTokenPath] = testToken + "\n"
			}
			env := testEnv(files)
			var out, errOut bytes.Buffer
			code := runSetup(context.Background(), []string{"--dry-run", "--config", "s.yaml"}, &out, &errOut, env)
			if code != tt.wantCode || !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("exit %d stderr %q, want %d containing %q", code, errOut.String(), tt.wantCode, tt.wantErr)
			}
		})
	}
}

func TestSetupRefusesUnknownBuild(t *testing.T) {
	env := testEnv(map[string]string{"s.yaml": "version: 1\ndocker:\n  install: true\ndomain:\n  name: lab.linxpbx.com\n",
		installer.DNSTokenPath: testToken})
	env.isRoot, env.commit = true, "unknown"
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), []string{"--config", "s.yaml"}, &out, &errOut, env); code != 1 || !strings.Contains(errOut.String(), "make build") {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestSetupNeedsRoot(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), nil, &out, &errOut, testEnv(nil)); code != 1 || !strings.Contains(errOut.String(), "sudo") {
		t.Errorf("non-root: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := runSetup(context.Background(), []string{"--owner-email", "a@b.co"}, &out, &errOut, testEnv(nil)); code != 2 || !strings.Contains(errOut.String(), "--config") {
		t.Errorf("owner flags without --config: exit %d, %q", code, errOut.String())
	}
}
