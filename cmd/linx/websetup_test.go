package main

import (
	"bytes"
	"context"
	"net/netip"
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
	env := webTestEnv(t, hostRunner{}, "192.168.1.20")
	env.savedConfig = func() ([]byte, error) { return []byte("version: 1\ndomain:\n  name: lab.linxpbx.com\n"), nil }
	var out, errOut bytes.Buffer
	if code := runSetup(context.Background(), nil, &out, &errOut, env); code != 0 || !strings.Contains(out.String(), "already installed here, at https://lab.linxpbx.com") {
		t.Errorf("exit %d:\n%s%s", code, out.String(), errOut.String())
	}
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
		var out bytes.Buffer
		followInstall(context.Background(), &out, env, false)
		for _, want := range []string{
			"http://192.168.1.20:6464/install/" + st.Secret + "\n  (on a computer on the same network as this server)",
			"http://203.0.113.5:6464/install/" + st.Secret,
			"It works once, for one hour",
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
		if code := followInstall(context.Background(), &out, env, false); code != 0 || !strings.Contains(out.String(), "✕ The link expired") {
			t.Errorf("exit %d:\n%s", code, out.String())
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
