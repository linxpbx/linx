package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/install"
	"linxpbx.com/linx/internal/installer"
)

func webTestEnv(t *testing.T, runner hostRunner, route string) setupEnv {
	env := testEnv("", nil)
	env.isRoot, env.interactive = true, false // no terminal: straight to the browser
	env.runner = runner
	now := time.Date(2026, 9, 28, 12, 4, 0, 0, time.UTC)
	env.web = webEnv{
		routeAddress:  func() (netip.Addr, bool) { return netip.MustParseAddr(route), true },
		publicAddress: func(context.Context) (netip.Addr, error) { return netip.MustParseAddr("203.0.113.5"), nil },
		statePath:     filepath.Join(t.TempDir(), "install-state.json"),
		now:           func() time.Time { return now },
		wait:          func(context.Context, time.Duration) bool { return false },
	}
	return env
}

func TestWebSetupDryRun(t *testing.T) {
	env := webTestEnv(t, hostRunner{"dpkg --print-architecture": "amd64"}, "192.168.1.20")
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), []string{"--dry-run"}, &out, &errOut, env); code != 0 {
		t.Fatalf("exit %d, stderr: %s\n%s", code, errOut.String(), out.String())
	}
	for _, want := range []string{
		"✓ Operating system: ubuntu 24.04",
		"✓ Ports 443 and 6464 are free",
		"Install Docker Engine and Docker Compose",
		"Install the linx command as /usr/local/bin/linx",
		"Write the installer's services configuration",
		"$ docker compose --file /etc/linx/install.yaml --env-file /etc/linx/install.env up --detach --wait",
		"Start the web install (linx-setup.service)",
		"Dry run: nothing was changed.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestWebSetupAlreadyInstalled(t *testing.T) {
	start := "systemd-run --unit linx-setup.service --description Linx setup: the Server settings page --collect --property Restart=on-failure --property RestartSec=5 /usr/local/bin/linx install-service --settings"
	t.Run("working", func(t *testing.T) {
		env := webTestEnv(t, hostRunner{start: ""}, "192.168.1.20")
		env.savedConfig = func() ([]byte, error) { return []byte("version: 1\ndomain:\n  name: lab.linxpbx.com\n"), nil }
		env.web.secureCheck = func(context.Context, string, int) error { return nil }
		var out, errOut bytes.Buffer
		code := runSetup(context.Background(), nil, &out, &errOut, env)
		for _, want := range []string{"Linx is installed at https://lab.linxpbx.com (working ✓)", "https://lab.linxpbx.com/admin/system/server", "Nothing was reopened"} {
			if code != 0 || !strings.Contains(out.String(), want) {
				t.Errorf("exit %d, missing %q:\n%s%s", code, want, out.String(), errOut.String())
			}
		}
	})
	repairStart := strings.Replace(start, "--settings", "--settings --repair", 1)
	repairRig := func(t *testing.T, runner hostRunner) (setupEnv, *[]string) {
		env := webTestEnv(t, runner, "192.168.1.20")
		env.savedConfig = func() ([]byte, error) {
			return []byte("version: 1\ndomain:\n  name: lab.linxpbx.com\nfront_door:\n  kind: linx-443\n"), nil
		}
		env.web.repairPath = filepath.Join(t.TempDir(), "repair-state.json")
		var ran []string
		env.web.execute = func(_ context.Context, p installer.Plan) error {
			for _, st := range p {
				if st.Cmd != nil {
					ran = append(ran, st.Cmd.String())
				}
				if st.File != nil {
					ran = append(ran, "> "+st.File.Path)
				}
			}
			return nil
		}
		return env, &ran
	}
	t.Run("address not answering: the repair page", func(t *testing.T) {
		env, ran := repairRig(t, hostRunner{repairStart: ""})
		env.web.secureCheck = func(context.Context, string, int) error { return errors.New("x509: certificate has expired") }
		var out, errOut bytes.Buffer
		code := runSetup(context.Background(), nil, &out, &errOut, env)
		rs, err := install.LoadRepairState(env.web.repairPath)
		if code != 0 || err != nil || rs.Problem != "x509: certificate has expired" || rs.NoSignIn {
			t.Fatalf("exit %d, state %+v %v:\n%s%s", code, rs, err, out.String(), errOut.String())
		}
		for _, want := range []string{
			"didn't answer from this server:\n  x509: certificate has expired",
			"If https://lab.linxpbx.com opens in your browser anyway",
			"https://192.168.1.20:6464/repair/" + rs.Secret + "\n",
			"sign in there as a system admin",
			"--new-link --no-sign-in",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("missing %q:\n%s", want, out.String())
			}
		}
		all := strings.Join(*ran, "\n")
		for _, want := range []string{"> /etc/linx/repair.yaml", "nft add element inet linx install_page { 0.0.0.0/0 }",
			"docker compose --file /etc/linx/compose.yaml --file /etc/linx/repair.yaml up --detach --wait control-plane"} {
			if !strings.Contains(all, want) {
				t.Errorf("didn't run %q:\n%s", want, all)
			}
		}
	})
	t.Run("--no-sign-in", func(t *testing.T) {
		env, _ := repairRig(t, hostRunner{repairStart: ""})
		env.web.secureCheck = func(context.Context, string, int) error { return nil }
		var out, errOut bytes.Buffer
		code := runSetup(context.Background(), []string{"--new-link", "--no-sign-in"}, &out, &errOut, env)
		rs, _ := install.LoadRepairState(env.web.repairPath)
		if code != 0 || !rs.NoSignIn || !strings.Contains(out.String(), "skips the sign-in") || strings.Contains(out.String(), "opens in your browser anyway") {
			t.Errorf("exit %d %+v:\n%s%s", code, rs, out.String(), errOut.String())
		}
	})
	t.Run("working again: the repair page closes", func(t *testing.T) {
		env, ran := repairRig(t, hostRunner{start: "", "systemctl is-active --quiet linx-setup.service": "", "systemctl stop linx-setup.service": ""})
		env.web.secureCheck = func(context.Context, string, int) error { return nil }
		if err := install.NewRepairState(env.web.now(), false, "").Save(env.web.repairPath); err != nil {
			t.Fatal(err)
		}
		// The stopped service closes it itself; here nothing did, so setup does.
		var out, errOut bytes.Buffer
		code := runSetup(context.Background(), nil, &out, &errOut, env)
		all := strings.Join(*ran, "\n")
		if _, err := os.Stat(env.web.repairPath); code != 0 || !errors.Is(err, fs.ErrNotExist) ||
			!strings.Contains(all, "nft flush set inet linx install_page") || !strings.Contains(out.String(), "Nothing was reopened") {
			t.Errorf("exit %d, %v:\n%s\n%s%s", code, err, all, out.String(), errOut.String())
		}
	})
}

func TestWebSetupStops(t *testing.T) {
	t.Run("port 6464 taken", func(t *testing.T) {
		env := webTestEnv(t, hostRunner{
			"ss -Hltnp sport = :6464": `LISTEN 0 4096 0.0.0.0:6464 0.0.0.0:* users:(("python3",pid=12,fd=3))`,
		}, "192.168.1.20")
		var out, errOut bytes.Buffer
		if code := runSetup(context.Background(), nil, &out, &errOut, env); code != 1 || !strings.Contains(out.String(), "Port 6464 is used by python3") {
			t.Errorf("exit %d:\n%s", code, out.String())
		}
	})
	t.Run("port 6464 held by Linx's own installer is fine", func(t *testing.T) {
		env := webTestEnv(t, hostRunner{
			"ss -Hltnp sport = :6464": `LISTEN 0 4096 192.168.1.20:6464 0.0.0.0:* users:(("docker-proxy",pid=12,fd=3))`,
			`docker inspect --format {{join .Config.Cmd " "}} linx-control-plane`: "install-server",
		}, "192.168.1.20")
		var out, errOut bytes.Buffer
		runSetup(context.Background(), nil, &out, &errOut, env)
		if strings.Contains(out.String(), "Port 6464 is used") {
			t.Errorf("stopped on its own installer:\n%s", out.String())
		}
	})
	t.Run("distro Docker with running containers", func(t *testing.T) {
		env := webTestEnv(t, hostRunner{
			"docker version --format {{.Server.Version}}":                  "24.0.7",
			"docker compose version --short":                               "2.20.0",
			"dpkg-query --show --showformat=${db:Status-Status} docker.io": "installed",
			"docker ps --quiet":                                            "a1\nb2\n",
		}, "192.168.1.20")
		var out, errOut bytes.Buffer
		code := runSetup(context.Background(), nil, &out, &errOut, env)
		if code != 1 || !strings.Contains(errOut.String(), "running 2 container(s)") || !strings.Contains(errOut.String(), "--replace-docker") {
			t.Errorf("exit %d:\n%s%s", code, out.String(), errOut.String())
		}
	})
}

func TestFollowInstall(t *testing.T) {
	active := hostRunner{"systemctl is-active --quiet linx-setup.service": ""}
	t.Run("unclaimed link at home", func(t *testing.T) {
		env := webTestEnv(t, active, "192.168.1.20")
		st := install.NewHostState(env.web.now(), install.Facts{PublicAddress: "203.0.113.5"})
		if err := st.Save(env.web.statePath); err != nil {
			t.Fatal(err)
		}
		// The first page's certificate, as setup keeps it: its fingerprint
		// is printed to check in the browser.
		certPEM, _, err := install.NewFirstPageCert([]netip.Addr{netip.MustParseAddr("192.168.1.20")}, env.web.now())
		if err != nil {
			t.Fatal(err)
		}
		fp, _ := install.Fingerprint(certPEM)
		env.readFile = func(p string) ([]byte, error) {
			if p == install.FirstPageTLSDir+"/"+install.FirstPageCertFile {
				return certPEM, nil
			}
			return nil, fs.ErrNotExist
		}
		var out bytes.Buffer
		followInstall(context.Background(), &out, env, false)
		for _, want := range []string{
			"check its SHA-256 fingerprint is:\n\n  " + fp + "\n",
			"https://192.168.1.20:6464/install/" + st.Secret + "\n  (on a computer on the same network as this server)",
			"https://203.0.113.5:6464/install/" + st.Secret,
			"It works once, for four hours",
			"Waiting for you in the browser",
		} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("output missing %q:\n%s", want, out.String())
			}
		}
	})
	t.Run("rented server", func(t *testing.T) {
		env := webTestEnv(t, active, "203.0.113.5")
		st := install.NewHostState(env.web.now(), install.Facts{PublicAddress: "203.0.113.5"})
		_ = st.Save(env.web.statePath)
		var out bytes.Buffer
		followInstall(context.Background(), &out, env, false)
		if strings.Count(out.String(), "/install/") != 1 || !strings.Contains(out.String(), "allow TCP port 6464 in your server provider's firewall") {
			t.Errorf("output:\n%s", out.String())
		}
	})
	t.Run("claimed, run again", func(t *testing.T) {
		env := webTestEnv(t, active, "192.168.1.20")
		st := install.NewHostState(env.web.now(), install.Facts{})
		st.Secret, st.View.LinkHash, st.View.SessionHash, st.View.ClaimedAt = "", "", strings.Repeat("a", 64), env.web.now()
		st.Progress = []install.Progress{{At: env.web.now(), Text: "Link opened (Chrome, 192.168.1.9)"}}
		_ = st.Save(env.web.statePath)
		var out bytes.Buffer
		followInstall(context.Background(), &out, env, true)
		if strings.Contains(out.String(), "/install/") || !strings.Contains(out.String(), "Being set up in another browser") ||
			!strings.Contains(out.String(), "--new-link") || !strings.Contains(out.String(), "✓ Link opened (Chrome, 192.168.1.9)") {
			t.Errorf("output:\n%s", out.String())
		}
	})
	t.Run("expired", func(t *testing.T) {
		env := webTestEnv(t, active, "192.168.1.20")
		st := install.NewHostState(env.web.now(), install.Facts{})
		st.Secret, st.View.LinkHash, st.View.Ended = "", "", install.EndedExpired
		st.Progress = []install.Progress{{At: env.web.now(), Text: "The link expired. Run sudo linx setup again for a new one.", Failed: true}}
		_ = st.Save(env.web.statePath)
		var out bytes.Buffer
		if code := followInstall(context.Background(), &out, env, true); code != 0 || !strings.Contains(out.String(), "✕ The link expired") {
			t.Errorf("exit %d:\n%s", code, out.String())
		}
	})
	// After --new-link the file still holds the cancelled link until the
	// service writes its new one: the terminal waits for that (found in
	// the install demo, where it printed the old progress and no link).
	t.Run("new link after a cancelled one", func(t *testing.T) {
		env := webTestEnv(t, active, "192.168.1.20")
		old := install.NewHostState(env.web.now(), install.Facts{})
		old.Secret, old.View.LinkHash, old.View.Ended = "", "", install.EndedCancelled
		old.Progress = []install.Progress{{At: env.web.now(), Text: "Link opened (Chrome, 192.168.1.9)"}}
		_ = old.Save(env.web.statePath)
		fresh := install.NewHostState(env.web.now(), install.Facts{})
		waits := 0
		env.web.wait = func(context.Context, time.Duration) bool {
			if waits++; waits == 2 {
				_ = fresh.Save(env.web.statePath)
			}
			return waits < 10
		}
		var out bytes.Buffer
		followInstall(context.Background(), &out, env, false)
		if !strings.Contains(out.String(), "/install/"+fresh.Secret) || strings.Contains(out.String(), "Link opened") {
			t.Errorf("output:\n%s", out.String())
		}
	})
}

func TestInstallFacts(t *testing.T) {
	env := webTestEnv(t, hostRunner{}, "192.168.1.20")
	lan := env.lan()
	f := installFacts(context.Background(), env, lan)
	if f.Where != install.WhereHome || f.LANAddress != "192.168.1.20" || f.PublicAddress != "203.0.113.5" || !strings.Contains(f.Hardware, "4 processor cores") {
		t.Errorf("facts: %+v", f)
	}
	if f := installFacts(context.Background(), env, installer.LAN{}); f.Where != install.WhereRented {
		t.Errorf("no LAN: %+v", f)
	}
}

// Words from outside in a progress line can't send escape sequences to the
// terminal (security review).
func TestPrintProgressDropsControlCharacters(t *testing.T) {
	var b strings.Builder
	printProgress(&b, install.Progress{Text: "Let's Encrypt said: \x1b]0;owned\x07no\x1b[2J", Failed: true})
	if strings.ContainsAny(b.String(), "\x1b\x07") {
		t.Errorf("printed %q", b.String())
	}
}

// TestSetupUpdateWaiting: on an installed server, setup says when this linx
// program isn't the version running and prints the update command, even
// while the Server settings page is already open (Demo B, 2026-10-03).
func TestSetupUpdateWaiting(t *testing.T) {
	const notice = "isn't the version of Linx running here"
	rig := func(t *testing.T, running string) setupEnv {
		env := webTestEnv(t, hostRunner{"systemctl is-active --quiet linx-setup.service": ""}, "192.168.1.20")
		env.savedConfig = func() ([]byte, error) { return []byte("version: 1\ndomain:\n  name: lab.linxpbx.com\n"), nil }
		env.web.secureCheck = func(context.Context, string, int) error { return nil }
		env.readFile = func(p string) ([]byte, error) {
			if p == "/etc/linx/.env" && running != "" {
				return []byte("# Generated by linx setup\nLINX_VERSION=" + running + "\nLINX_DOMAIN=lab.linxpbx.com\n"), nil
			}
			return nil, fs.ErrNotExist
		}
		return env
	}
	t.Run("different version, page open", func(t *testing.T) {
		env := rig(t, "sha-fedcba9876543210fedcba9876543210fedcba98")
		var out, errOut bytes.Buffer
		code := runSetup(context.Background(), nil, &out, &errOut, env)
		for _, want := range []string{
			"This linx program (0123456) isn't the version of Linx running here (fedcba9).",
			"sudo /home/owner/linx setup --config /etc/linx/setup.yaml",
			"Nothing was reopened",
		} {
			if code != 0 || !strings.Contains(out.String(), want) {
				t.Errorf("exit %d, missing %q:\n%s%s", code, want, out.String(), errOut.String())
			}
		}
	})
	t.Run("the installed command", func(t *testing.T) {
		env := rig(t, "sha-fedcba9876543210fedcba9876543210fedcba98")
		env.executable = "/usr/local/bin/linx"
		var out, errOut bytes.Buffer
		runSetup(context.Background(), nil, &out, &errOut, env)
		if !strings.Contains(out.String(), "  sudo linx setup --config /etc/linx/setup.yaml") {
			t.Errorf("want the plain command:\n%s", out.String())
		}
	})
	for name, running := range map[string]string{"same version": "sha-" + testCommit, "no .env": ""} {
		t.Run(name, func(t *testing.T) {
			env := rig(t, running)
			var out, errOut bytes.Buffer
			runSetup(context.Background(), nil, &out, &errOut, env)
			if strings.Contains(out.String(), notice) {
				t.Errorf("unexpected update notice:\n%s", out.String())
			}
		})
	}
	t.Run("not installed", func(t *testing.T) {
		env := rig(t, "sha-fedcba9876543210fedcba9876543210fedcba98")
		env.savedConfig = func() ([]byte, error) { return nil, fs.ErrNotExist }
		var out bytes.Buffer
		printUpdateWaiting(&out, env)
		if out.Len() != 0 {
			t.Errorf("unexpected output: %s", out.String())
		}
	})
}
