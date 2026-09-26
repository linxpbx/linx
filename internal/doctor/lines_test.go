package doctor

import (
	"context"
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/installer"
)

const ucmID = "11111111-1111-1111-1111-111111111111"

func linesJSON(t *testing.T, trunks ...map[string]any) string {
	t.Helper()
	if trunks == nil {
		trunks = []map[string]any{}
	}
	b, err := json.Marshal(map[string]any{"country": "AE", "trunks": trunks, "clashes": 0})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func ucmLine() map[string]any {
	return map[string]any{"id": ucmID, "name": "UCM", "kind": "lan_peer", "host": "192.168.1.5", "port": 5061,
		"transport": "tls", "media": "srtp", "enabled": true, "outbound": 1, "pinned": "", "status": "reachable", "detail": "ok"}
}

// addLines makes the fixture's one line (the UCM) healthy.
func addLines(t *testing.T, f *fixture) {
	f.runner[psqlCmd+linesQuery] = linesJSON(t, ucmLine())
	f.runner[astCLI+"pjsip show registrations"] = "No objects found.\n"
	f.runner[astCLI+"pjsip show contacts"] = "  Contact:  trunk-" + ucmID + "/sip b0e9390783 Avail         0.497\n"
}

func (f *fixture) lines() []Result { return Lines(context.Background(), f.env) }

func TestLines(t *testing.T) {
	f := phonesFixture(t)
	rs := f.lines()
	if worst(rs) != installer.OK {
		t.Fatalf("healthy line: %+v", rs)
	}
	want(t, rs, installer.OK, `Line "UCM" (192.168.1.5) works`)
	want(t, rs, installer.OK, "Emergency numbers can be called")
	want(t, rs, installer.OK, "United Arab Emirates")

	// Asterisk says it stopped answering: that, not the database's copy.
	f.runner[astCLI+"pjsip show contacts"] = "  Contact:  trunk-" + ucmID + "/sip b0e9390783 Unavail         nan\n"
	rs = f.lines()
	want(t, rs, installer.Fail, `Line "UCM" (192.168.1.5) isn't working`)
	want(t, rs, installer.Fail, "Every line for outgoing calls is down")

	// Unencrypted, a pinned certificate about to expire, not outgoing.
	soon := newCert(t, caTmpl("UCM6304", now.Add(10*day)), nil)
	line := ucmLine()
	line["transport"], line["media"], line["confirmed_by"], line["outbound"] = "tcp", "none", "user:admin@example.com", nil
	line["pinned"] = string(pemOf(soon))
	f = phonesFixture(t)
	f.runner[psqlCmd+linesQuery] = linesJSON(t, line)
	rs = f.lines()
	want(t, rs, installer.Warn, "isn't encrypted (user:admin@example.com confirmed it)")
	want(t, rs, installer.Warn, "pinned certificate expires on")
	want(t, rs, installer.Fail, "No line is set up for outgoing calls")

	// No lines at all.
	f.runner[psqlCmd+linesQuery] = linesJSON(t)
	rs = f.lines()
	want(t, rs, installer.Warn, "No phone lines to the outside yet")
	for _, r := range rs {
		if strings.Contains(r.Message, "outgoing calls") {
			t.Errorf("no lines, yet: %q", r.Message)
		}
	}
}

func TestLinesWireGuard(t *testing.T) {
	f := phonesFixture(t)
	withTunnels := func(tunnels ...map[string]any) {
		b, _ := json.Marshal(map[string]any{"country": "AE", "trunks": []map[string]any{ucmLine()}, "clashes": 0, "tunnels": tunnels})
		f.runner[psqlCmd+linesQuery] = string(b)
	}
	f.runner[inspect+"linx-wireguard"] = "running \n"
	f.env.Stat = func(p string) (fs.FileInfo, error) {
		if p == "/sys/module/wireguard" {
			return nil, nil
		}
		return nil, fs.ErrNotExist
	}
	withTunnels(map[string]any{"name": "Provider VPN", "status": "up", "detail": "Last handshake 20 seconds ago.", "used": true},
		map[string]any{"name": "Spare", "status": "down", "detail": "No handshake.", "used": false})
	rs := f.lines()
	want(t, rs, installer.OK, `WireGuard tunnel "Provider VPN" is up: Last handshake 20 seconds ago.`)
	want(t, rs, installer.Warn, `WireGuard tunnel "Spare" (no line uses it) is down`)

	withTunnels(map[string]any{"name": "Provider VPN", "status": "down", "detail": "No handshake with the provider for 4 minutes.", "used": true})
	delete(f.runner, inspect+"linx-wireguard")
	f.env.Stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	rs = f.lines()
	want(t, rs, installer.Fail, `WireGuard tunnel "Provider VPN" is down: No handshake with the provider for 4 minutes. Its lines can't work.`)
	want(t, rs, installer.Fail, "Linx's WireGuard service isn't running")
	want(t, rs, installer.Fail, "kernel hasn't loaded WireGuard")
}
