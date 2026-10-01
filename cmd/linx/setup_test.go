package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
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

func testEnv(stdin string, files map[string]string) setupEnv {
	return setupEnv{
		detect: func() hostinfo.Info {
			return hostinfo.Info{GOOS: "linux", OSID: "ubuntu", OSVersionID: "24.04", OSCodename: "noble",
				Arch: "amd64", CPUs: 4, MemBytes: 8 << 30, DiskFree: 100 << 30, DiskTotal: 120 << 30}
		},
		runner:      hostRunner{"dpkg --print-architecture": "amd64"}, // no Docker installed
		stdin:       strings.NewReader(stdin),
		interactive: true,
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
		readSecret: func() (string, error) { return testToken, nil },
		commit:     testCommit,
		executable: "/home/owner/linx",
		resolve:    func(p string) (string, error) { return p, nil },
		stat:       func(string) (os.FileInfo, error) { return nil, fs.ErrNotExist },
	}
}

func TestSetupInteractiveDryRun(t *testing.T) {
	// Answers: set up in the terminal, accept profile, install Docker, another program in front (a bad then
	// a good address; a bad then a good UDP port), choose Portainer, a bad then a good domain, (token),
	// the DNS company (Cloudflare), keep test certificates, skip the email, skip the owner email and name.
	env := testEnv("2\n\n\ny\n2\n8.8.8.8\n192.168.1.30\n5061\n3478\n2\n*.bad\nlab.linxpbx.com\n\n\n\n\n\n", nil)
	var out, errOut bytes.Buffer
	code := runSetup(context.Background(), []string{"--dry-run"}, &out, &errOut, env)
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
		"is not a valid domain name",
		"Edit zone DNS",
		"Save the DNS token",
		"Get a test certificate for lab.linxpbx.com and *.lab.linxpbx.com",
		"isn't a home-network address",
		"some (UniFi) don't, so use 3478 then",
		"Give a port number, like 443 or 3478.",
		"Write the Pangolin settings to add (/etc/linx/front-door/pangolin-dynamic-config.yml)",
		"Write the front door's steps (/etc/linx/front-door/FRONT-DOOR.txt)",
		"Start the Linx services",
		"Point lab.linxpbx.com, turn.lab.linxpbx.com at this network's public address; sip.lab.linxpbx.com at 192.168.1.20 (for desk phones at home) (DNS)",
		"$ docker compose --file /etc/linx/compose.yaml run --rm certd -records @,turn",
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
	env := testEnv("2\n\n\ny\n2\n8.8.8.8\n192.168.1.30\n5061\n3478\n2\n*.bad\nlab.linxpbx.com\n\n\n\n\n\n", nil)
	env.stat = func(p string) (os.FileInfo, error) {
		if p == "/home/owner/linx-firewall-sync" {
			return nil, nil
		}
		return nil, fs.ErrNotExist
	}
	var out, errOut bytes.Buffer
	code := runSetup(context.Background(), []string{"--dry-run"}, &out, &errOut, env)
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
			"docker exec " + controlPlaneContainer + " " + controlPlaneBinary + " user create --first-admin --email owner@example.com --name Owner --role system_admin": "Owner (owner@example.com, system_admin) can now set their password — it works once, for 24 hours:\n",
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
	// Setup run again on a server that has a system admin (a web install):
	// no "create one" hint (found in the install demo).
	t.Run("no email given, a system admin exists", func(t *testing.T) {
		runner := hostRunner{
			"docker exec " + controlPlaneContainer + " " + controlPlaneBinary + " user list": "EMAIL  NAME  ROLE  STATUS  MFA  ID\nme@example.com  Me  system_admin  active  off  01a0\n",
		}
		var out, errOut bytes.Buffer
		printFirstAdmin(context.Background(), &out, &errOut, setupEnv{runner: runner}, "", "")
		if out.Len() != 0 || errOut.Len() != 0 {
			t.Errorf("said something:\n%s%s", out.String(), errOut.String())
		}
	})
	t.Run("no email given", func(t *testing.T) {
		var out, errOut bytes.Buffer
		printFirstAdmin(context.Background(), &out, &errOut, setupEnv{runner: hostRunner{}}, "", "")
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
		// A rented server that skipped the token in the web install runs
		// setup again (or updates) without one (found on the VPS demo).
		{"no token, skipped on purpose", "version: 1\ndocker:\n  install: true\n" + domain +
			"certificates:\n  staging: false\n  email: owner@example.com\n  no_dns_token: true\nfront_door:\n  kind: linx-443\n", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files := map[string]string{"s.yaml": tt.yaml}
			if !strings.HasPrefix(tt.name, "no token") {
				files[installer.DNSTokenPath] = testToken + "\n"
			}
			env := testEnv("", files)
			env.interactive = false
			var out, errOut bytes.Buffer
			code := runSetup(context.Background(), []string{"--dry-run", "--config", "s.yaml"}, &out, &errOut, env)
			if code != tt.wantCode || !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("exit %d stderr %q, want %d containing %q", code, errOut.String(), tt.wantCode, tt.wantErr)
			}
		})
	}
}

func TestSetupKeepsSavedToken(t *testing.T) {
	// Saved answers and token; the owner accepts every default.
	// First: the terminal, not the Server settings page.
	env := testEnv("2\n"+strings.Repeat("\n", 12), map[string]string{installer.DNSTokenPath: "saved-token-xxxxxxxxxxxxxxxxxxx\n"})
	env.savedConfig = func() ([]byte, error) { return []byte("version: 1\ndomain:\n  name: lab.linxpbx.com\n"), nil }
	env.readSecret = func() (string, error) { t.Error("asked for a token although one is saved"); return "", io.EOF }
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), []string{"--dry-run"}, &out, &errOut, env); code != 0 {
		t.Fatalf("exit %d, stderr: %s\nstdout: %s", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "Keep the saved DNS token?") {
		t.Errorf("didn't offer the saved token:\n%s", out.String())
	}
}

func TestAskDomainOffersSavedTokenForChangedDomain(t *testing.T) {
	for _, tc := range []struct {
		name, oldDomain, newDomain, provider string
		input                                string // after the domain line
		wantOffer, wantKeep                  bool
	}{
		// Corrected within the same zone: offered, and Enter keeps it.
		{"same zone", "sip.lab.linxpbx.com", "lab.linxpbx.com", installer.DNSCloudflare, "\n\n\n\n", true, true},
		// Another zone: offered, but Enter means paste a new one.
		{"other zone", "lab.linxpbx.com", "pbx.example.com", installer.DNSCloudflare, "\n\n", true, false},
		// Another provider: the token can't work there, so not offered.
		{"other provider", "lab.linxpbx.com", "me.duckdns.org", installer.DNSCloudflare, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv("", map[string]string{installer.DNSTokenPath: "saved-token-xxxxxxxxxxxxxxxxxxx\n"})
			asked := false
			env.readSecret = func() (string, error) { asked = true; return "new-token-xxxxxxxxxxxxxxxxxxxxx", nil }
			var out bytes.Buffer
			p := &prompter{in: bufio.NewReader(strings.NewReader(tc.newDomain + "\n" + tc.input + "\n\n")), out: &out}
			cfg := installer.Config{Domain: installer.DomainConfig{Name: tc.oldDomain, DNSProvider: tc.provider},
				Certificates: installer.CertificateConfig{Staging: true}}
			token, err := askDomain(p, &cfg, true, env)
			if err != nil {
				t.Fatalf("askDomain: %v\n%s", err, out.String())
			}
			if offered := strings.Contains(out.String(), "Keep the saved DNS token (saved for "+tc.oldDomain+")?"); offered != tc.wantOffer {
				t.Errorf("offered = %v, want %v:\n%s", offered, tc.wantOffer, out.String())
			}
			if kept := token == "saved-token-xxxxxxxxxxxxxxxxxxx"; kept != tc.wantKeep || asked == kept {
				t.Errorf("kept = %v (asked for a new one: %v), want kept %v", kept, asked, tc.wantKeep)
			}
		})
	}
}

func TestSetupRefusesUnknownBuild(t *testing.T) {
	env := testEnv("", map[string]string{"s.yaml": "version: 1\ndocker:\n  install: true\ndomain:\n  name: lab.linxpbx.com\n",
		installer.DNSTokenPath: testToken})
	env.interactive, env.isRoot, env.commit = false, true, "unknown"
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), []string{"--config", "s.yaml"}, &out, &errOut, env); code != 1 || !strings.Contains(errOut.String(), "make build") {
		t.Errorf("exit %d, stderr %q", code, errOut.String())
	}
}

func TestSetupNeedsRoot(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), nil, &out, &errOut, testEnv("", nil)); code != 1 || !strings.Contains(errOut.String(), "sudo") {
		t.Errorf("non-root: exit %d, %q", code, errOut.String())
	}
	errOut.Reset()
	if code := runSetup(context.Background(), []string{"--owner-email", "a@b.co"}, &out, &errOut, testEnv("", nil)); code != 2 || !strings.Contains(errOut.String(), "--config") {
		t.Errorf("owner flags without --config: exit %d, %q", code, errOut.String())
	}
}

// TestSetupChoosesBrowserOrTerminal: in a terminal, setup asks; Enter
// picks the browser. Without one (a script), the browser, unasked.
func TestSetupChoosesBrowserOrTerminal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		stdin       string
		interactive bool
		asked       bool
		want        string
	}{
		{"enter picks the browser", "\n", true, true, "Start the web install"},
		{"terminal", "2\n\nMars/Olympus\nAsia/Dubai\ny\n5\n\nlab.linxpbx.com\n\n\n\n\n\n", true, true, "isn't a time zone this server knows"},
		{"no terminal", "", false, false, "Start the web install"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testEnv(tc.stdin, nil)
			env.interactive = tc.interactive
			env.web = webEnv{routeAddress: func() (netip.Addr, bool) { return netip.MustParseAddr("192.168.1.20"), true }}
			var out, errOut bytes.Buffer
			if code := runSetup(context.Background(), []string{"--dry-run"}, &out, &errOut, env); code != 0 {
				t.Fatalf("exit %d, stderr: %s\n%s", code, errOut.String(), out.String())
			}
			if asked := strings.Contains(out.String(), "How do you want to finish setting up Linx?"); asked != tc.asked {
				t.Errorf("asked = %v:\n%s", asked, out.String())
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("output missing %q:\n%s", tc.want, out.String())
			}
		})
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
