package calltest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/doctor"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/trunkconf"
	"linxpbx.com/linx/internal/wgconf"
)

// wireguardImage is linx-wireguard (make image SERVICE=wireguard).
func wireguardImage() string {
	if img := os.Getenv("LINX_WIREGUARD_IMAGE"); img != "" {
		return img
	}
	return "linx-wireguard:dev"
}

// The tunnel's addresses: the provider's end, and Linx's.
const (
	wgProvider = "10.99.0.1"
	wgLinx     = "10.99.0.2"
)

// TestWireGuardDocker is docs/TRUNKS.md §13 step 5's suite: a provider
// reachable only through a WireGuard tunnel (wgpeer, with a plain-UDP SIPp
// inside its end), the real linx-wireguard image in Asterisk's network
// namespace as compose.yaml runs it, and the profile and trunk rendered
// exactly as the control plane does. Calls go out and come in through the
// tunnel, with Linx's tunnel address (not the LAN's) in what it sends; a
// tunnel that vanishes takes its trunk out of Asterisk rather than letting
// its traffic take the normal route; and after Asterisk restarts, the
// agent follows it into its new namespace and the line comes back.
func TestWireGuardDocker(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") == "1" && exec.Command("docker", "image", "inspect", wireguardImage()).Run() != nil {
		t.Skipf("no %s image: run make image SERVICE=wireguard first", wireguardImage())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tracker := &pbx.CallTracker{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	e := start(t, ctx, func(e *env) ari.App {
		tracker.Store = e.store
		return tracker
	})
	eventually(t, "Asterisk's ARI connection", 30*time.Second, tracker.Connected)
	if dir := os.Getenv("LINX_CALLTEST_LOGS"); dir != "" {
		t.Cleanup(func() {
			for _, c := range []string{astName, prefix + "-wireguard", prefix + "-wgpeer", prefix + "-prov-wg"} {
				out, _ := exec.Command("docker", "logs", c).CombinedOutput()
				os.WriteFile(filepath.Join(dir, c+".log"), out, 0o644)
			}
		})
	}
	e.buildTool("wgpeer")
	wgDir, statusDir := filepath.Join(e.dir, "wireguard"), filepath.Join(e.dir, "wireguard-status")
	for _, d := range []string{wgDir, statusDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The agent runs as another user than this test: as compose.yaml's
	// status volume, writable for it, and its files emptied as root.
	os.Chmod(statusDir, 0o777)
	t.Cleanup(func() {
		exec.Command("docker", "rm", "--force", prefix+"-wireguard").Run()
		exec.Command("docker", "run", "--rm", "--user", "0:0", "--volume", statusDir+":/s", "--entrypoint", "sh", sippImage,
			"-c", "rm -rf /s/*").Run()
	})

	var key [dbsecret.KeySize]byte
	sealer := dbsecret.NewSealer(key)
	svc := &trunk.Service{Store: e.store, Sealer: sealer, Now: time.Now}
	admin := auth.WithPrincipal(ctx, auth.Principal{Type: auth.TypeSystem, ID: "cli", TenantID: e.tenant, Role: auth.RoleSystemAdmin})
	renderer := &trunkconf.Renderer{Store: e.store, Sealer: sealer, Dir: filepath.Join(e.dir, "trunks"), WireGuardDir: wgDir,
		Log: slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))}
	render := func() {
		t.Helper()
		if _, err := renderer.RenderOnce(ctx); err != nil {
			t.Fatal(err)
		}
		// compose.yaml's volumes give Asterisk's group, and the agent's
		// user, read access; here they're other users than this test.
		for _, f := range []string{filepath.Join(e.dir, "trunks", "*"), filepath.Join(wgDir, "*")} {
			names, _ := filepath.Glob(f)
			for _, n := range names {
				os.Chmod(n, 0o644)
			}
		}
	}
	endpointLoaded := func(tr trunk.Trunk) func() bool {
		return func() bool { return strings.Contains(e.asteriskCLI("pjsip show endpoints"), tr.Endpoint()) }
	}

	staff, err := svc.CreateCallPermissionLevel(admin, trunk.CallPermissionLevelInput{Name: "Staff",
		AllowedCategories: []string{"landline", "mobile", "national"}})
	if err != nil {
		t.Fatal(err)
	}
	alice := e.newPhone("101", "Alice")
	alice.ext.CallPermissionLevelID = &staff.ID
	if _, err := e.store.UpdateExtension(ctx, alice.ext, e.audit("extension.update")); err != nil {
		t.Fatal(err)
	}
	bob := e.newPhone("102", "Bob")
	bobPhone := e.sipp("bob", "register.xml", bob, "-oocsf", "/scenarios/answer.xml", "-d", "600000")
	t.Cleanup(func() { exec.Command("docker", "rm", "--force", bobPhone).Run() })
	eventually(t, "Bob online", 15*time.Second, func() bool {
		d, _, _ := e.store.DeviceBySIPUsername(ctx, bob.dev.SIPUsername)
		return d.Online
	})

	// The provider: its end of the tunnel on the network outside, and a
	// SIPp in that end's namespace answering only on its tunnel address.
	linxKey, _ := wgtypes.GeneratePrivateKey()
	peerKey, _ := wgtypes.GeneratePrivateKey()
	peer := prefix + "-wgpeer"
	docker(t, ctx, "run", "--detach", "--name", peer, "--network", outsideNet, "--user", "0:0", "--cap-add", "NET_ADMIN",
		"--volume", filepath.Join(e.dir, "wgpeer")+":/wgpeer:ro", "--entrypoint", "/wgpeer/wgpeer", sippImage,
		"-key", peerKey.String(), "-peer", linxKey.PublicKey().String(), "-addr", wgProvider, "-allowed", wgLinx)
	eventually(t, "the provider's tunnel end", 15*time.Second, func() bool { return strings.Contains(e.logs(peer), "wg0 up") })
	endpoint := e.ipOn(peer, outsideNet)
	prov := e.provider("wg", "container:"+peer, "", "u1", 5060, "", append([]string{"-i", wgProvider}, plainSets...)...)

	// linx-wireguard, as compose.yaml runs it.
	agent := prefix + "-wireguard"
	docker(t, ctx, "run", "--detach", "--name", agent, "--restart", "unless-stopped", "--network", "container:"+astName,
		"--user", "0:0", "--read-only", "--cap-drop", "ALL", "--cap-add", "NET_ADMIN", "--cap-add", "SETUID", "--cap-add", "SETGID",
		"--security-opt", "no-new-privileges:true", "--env", "LINX_WIREGUARD_INTERVAL=1s",
		"--volume", wgDir+":/var/lib/linx/wireguard:ro", "--volume", statusDir+":/var/lib/linx/wireguard-status",
		wireguardImage())

	var profile trunk.WireGuardProfile
	var tr trunk.Trunk
	t.Run("the provider's configuration, narrowed to its trunk", func(t *testing.T) {
		conf := fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s/24
DNS = 1.1.1.1

[Peer]
PublicKey = %s
Endpoint = %s:51820
AllowedIPs = 0.0.0.0/0
`, linxKey, wgLinx, peerKey.PublicKey(), endpoint)
		profile, err = svc.CreateWireGuardProfile(admin, trunk.WireGuardProfileInput{Name: "Provider VPN", Config: conf})
		if err != nil {
			t.Fatal(err)
		}
		if profile.PublicKey != linxKey.PublicKey().String() || len(profile.Notes) != 1 {
			t.Errorf("profile %+v", profile)
		}
		// Plain UDP inside the tunnel: no ADR-023 confirmation needed.
		tr, err = svc.CreateTrunk(admin, trunk.TrunkInput{Name: "Through the tunnel", Kind: trunk.KindLANPeer, Host: wgProvider,
			Port: ptr(5060), Transport: trunk.TransportUDP, MediaEncryption: trunk.MediaNone, WireGuardProfileID: &profile.ID})
		if err != nil {
			t.Fatal(err)
		}
		if tr.Unencrypted() {
			t.Error("a trunk through WireGuard counts as unencrypted")
		}
		if _, err := svc.CreateDID(admin, tr.ID, trunk.DIDInput{Number: "+97142000201", ExtensionID: &bob.ext.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.SetOutboundOrder(admin, []uuid.UUID{tr.ID}); err != nil {
			t.Fatal(err)
		}
		render()
		cfg, err := wgconf.ReadConfig(wgDir)
		if err != nil || len(cfg.Tunnels) != 1 || fmt.Sprint(cfg.Tunnels[0].AllowedIPs) != "["+wgProvider+"]" {
			t.Fatalf("tunnels %+v (%v)", cfg, err)
		}

		eventually(t, "the tunnel's first handshake", 30*time.Second, func() bool {
			s, err := wgconf.ReadStatus(statusDir)
			return err == nil && s.Tunnels[profile.ID.String()].LastHandshake != nil
		})
		e.eventuallyOr(t, "the trunk in Asterisk once its tunnel routes", 30*time.Second, endpointLoaded(tr), agent)
		mon := &trunk.Monitor{Store: e.store, Alerts: noAlerts{}, Dir: filepath.Join(e.dir, "trunk-status"),
			Tunnels: e.store, WireGuardDir: statusDir, Log: slog.New(slog.DiscardHandler)}
		if err := mon.Check(ctx); err != nil {
			t.Fatal(err)
		}
		if got, err := e.store.WireGuardProfile(ctx, e.tenant, profile.ID); err != nil || got.Status != wgconf.StateUp || got.LastHandshakeAt == nil {
			t.Errorf("profile status %q %v (%v)", got.Status, got.LastHandshakeAt, err)
		}
		if bad, ok := doctor.PlainSIPTransports(e.asteriskCLI("pjsip show transports")); !ok || len(bad) > 0 {
			t.Errorf("doctor counts the tunnel's transports as phone ones: %v", bad)
		}
	})
	if tr.ID == uuid.Nil {
		t.Fatal("no trunk")
	}

	outgoing := func(t *testing.T, name, number string) {
		t.Helper()
		before := strings.Count(e.logs(prov), "INVITE for")
		e.run(name, "call.xml", alice, "-s", number, "-d", "1000")
		if l := e.logs(prov); strings.Count(l, "INVITE for") == before || !strings.Contains(l, "audio RTP/AVP") {
			t.Errorf("the provider's log:\n%s\n--- Asterisk:\n%s", l, e.callLogs(strings.TrimPrefix(number, "0")))
		}
	}

	t.Run("a call out through the tunnel, from Linx's tunnel address", func(t *testing.T) {
		e.asteriskCLI("pjsip set logger on")
		outgoing(t, "wg-out", "0501234567")
		e.asteriskCLI("pjsip set logger off")
		// What Asterisk told the provider: its tunnel address for SIP and
		// audio, never the LAN address phones use.
		_, invite, _ := strings.Cut(e.logs(astName), "INVITE sip:+971501234567@"+wgProvider+":5060 SIP/2.0")
		invite, _, _ = strings.Cut(invite, "a=sendrecv")
		for _, want := range []string{"Via: SIP/2.0/UDP " + wgLinx + ":5063", "Contact: <sip:asterisk@" + wgLinx + ":5063>", "c=IN IP4 " + wgLinx} {
			if !strings.Contains(invite, want) {
				t.Errorf("Asterisk's INVITE to the provider doesn't carry %q:\n%s", want, invite)
			}
		}
		if strings.Contains(invite, lanAddr) {
			t.Errorf("the LAN address went to the provider:\n%s", invite)
		}
	})

	t.Run("a call in through the tunnel rings the DID's extension", func(t *testing.T) {
		out, err := exec.CommandContext(ctx, "docker", "exec", prov, "sipp", wgLinx+":5063", "-t", "u1", "-i", wgProvider, "-p", "5070",
			"-sf", "/scenarios/provider-call-plain.xml", "-s", "+97142000201", "-d", "1500", "-m", "1", "-nostdin",
			"-timeout", sipTimeout, "-timeout_error", "-trace_err", "-error_file", "/dev/stderr").CombinedOutput()
		if err != nil {
			t.Fatalf("the provider's call to Bob's DID: %v\n%s\n%s", err, out, e.asteriskLogs())
		}
		if in := e.callEnded("+97142000201"); in["direction"] != pbx.DirectionInbound || in["trunk_id"] != tr.ID.String() {
			t.Errorf("inbound call.ended = %v", in)
		}
	})

	t.Run("no tunnel, no trunk: nothing takes the normal route", func(t *testing.T) {
		docker(t, ctx, "stop", agent)
		iface := wgconf.InterfaceName(profile.ID)
		docker(t, ctx, "run", "--rm", "--network", "container:"+astName, "--user", "0:0", "--cap-add", "NET_ADMIN",
			"--volume", filepath.Join(e.dir, "wgpeer")+":/wgpeer:ro", "--entrypoint", "/wgpeer/wgpeer", sippImage, "-delete", iface)
		eventually(t, "the trunk leaving Asterisk", 20*time.Second, func() bool { return !endpointLoaded(tr)() })
		before := strings.Count(e.logs(prov), "INVITE for")
		e.run("wg-down", "call-message.xml", alice, "-s", "0501234568")
		if got := e.callEnded("0501234568")["outcome"]; got != pbx.OutcomeNoLines {
			t.Errorf("outcome %v, want %s", got, pbx.OutcomeNoLines)
		}
		if strings.Count(e.logs(prov), "INVITE for") != before {
			t.Error("a call reached the provider with its tunnel gone")
		}
		if !strings.Contains(e.asteriskLogs(), "trunks through WireGuard waiting for their tunnel") {
			t.Error("Asterisk's entrypoint didn't say it held the trunk back")
		}
		docker(t, ctx, "start", agent)
		e.eventuallyOr(t, "the trunk back with its tunnel", 30*time.Second, endpointLoaded(tr), agent)
		outgoing(t, "wg-back", "0501234569")
	})

	t.Run("after Asterisk restarts, the tunnel follows it", func(t *testing.T) {
		restarts := docker(t, ctx, "inspect", "--format", "{{.RestartCount}}", agent)
		docker(t, ctx, "restart", astName)
		e.waitAsterisk()
		eventually(t, "Asterisk's ARI connection again", 30*time.Second, tracker.Connected)
		e.eventuallyOr(t, "the agent restarted into Asterisk's new namespace", 30*time.Second, func() bool {
			return docker(t, ctx, "inspect", "--format", "{{.RestartCount}}", agent) != restarts &&
				strings.Contains(docker(t, ctx, "inspect", "--format", "{{.State.Status}}", agent), "running")
		}, agent)
		e.eventuallyOr(t, "the trunk back after the restart", 30*time.Second, endpointLoaded(tr), agent)
		outgoing(t, "wg-restart", "0501234570")
		if !strings.Contains(e.logs(agent), "Asterisk's network is gone") {
			t.Errorf("the agent's log:\n%s", e.logs(agent))
		}
	})
}
