package asteriskconf

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWireGuardTrunks(t *testing.T) {
	dir, conf := t.TempDir(), t.TempDir()
	c := Config{TrunksDir: dir, ConfDir: conf}
	tunnel := netip.MustParseAddr("10.6.0.2")
	write := func(iface, header, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, WireGuardTrunkFile(iface)), []byte(header+"\n"+body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("linx-wga", WireGuardHeaderLine(tunnel, []netip.Addr{netip.MustParseAddr("10.6.0.1"), netip.MustParseAddr("10.6.0.5")}), "[trunk-a]\n")
	write("linx-wgb", WireGuardHeaderLine(netip.MustParseAddr("10.7.0.2"), []netip.Addr{netip.MustParseAddr("10.7.0.1")}), "[trunk-b]\n")
	write("linx-wgc", "; no header", "[trunk-c]\n")

	// The kernel routes 10.6.0.1 and .5 into tunnel a; tunnel b isn't up,
	// so 10.7.0.1 would leave by the normal route.
	routes := map[string]string{"10.6.0.1": "10.6.0.2", "10.6.0.5": "10.6.0.2", "10.7.0.1": "172.20.0.3"}
	source := func(dst netip.Addr) (netip.Addr, error) {
		if s, ok := routes[dst.String()]; ok {
			return netip.MustParseAddr(s), nil
		}
		return netip.Addr{}, errors.New("unreachable")
	}
	content, held, err := c.WireGuardTrunks(source)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "[trunk-a]") || strings.Contains(string(content), "[trunk-b]") || strings.Contains(string(content), "[trunk-c]") {
		t.Errorf("content:\n%s", content)
	}
	if len(held) != 2 || !strings.Contains(held[0], "10.7.0.1 isn't routed into the tunnel yet") || !strings.Contains(held[1], "no header") {
		t.Errorf("held %q", held)
	}
	if changed, err := c.WriteWireGuardTrunks(content); err != nil || !changed {
		t.Fatalf("write: %v %v", changed, err)
	}
	if changed, _ := c.WriteWireGuardTrunks(content); changed {
		t.Error("the same content counted as a change")
	}

	// Tunnel b comes up.
	routes["10.7.0.1"] = "10.7.0.2"
	content, held, _ = c.WireGuardTrunks(source)
	if !strings.Contains(string(content), "[trunk-b]") || len(held) != 1 {
		t.Errorf("after tunnel b came up: held %q\n%s", held, content)
	}
}

func TestPJSIPIncludesWireGuardTrunks(t *testing.T) {
	out := Config{SIPPort: 5061, ConfDir: "/etc/asterisk", TrunksDir: "/var/lib/linx/trunks"}.pjsipConf(nil, "", netip.Prefix{})
	trunks := strings.Index(out, `#tryinclude "/var/lib/linx/trunks/pjsip_trunks.conf"`)
	wg := strings.Index(out, `#tryinclude "/etc/asterisk/pjsip_wireguard.conf"`)
	if trunks < 0 || wg < trunks {
		t.Errorf("the tunnels' trunks must be included after the trunk file (which defines their transports):\n%s", out)
	}
	// Only a template: nothing listens until a trunk needs it.
	if !strings.Contains(out, "[linx-wg-transport-tls](!)\ntype=transport\nprotocol=tls\n") || strings.Contains(out, "5064") {
		t.Errorf("WireGuard TLS template:\n%s", out)
	}
	// It has no address rewriting.
	_, sec, _ := strings.Cut(out, "[linx-wg-transport-tls](!)")
	sec, _, _ = strings.Cut(sec, "\n\n")
	if strings.Contains(sec, "external_") || strings.Contains(sec, "local_net") {
		t.Errorf("the WireGuard TLS transport rewrites addresses:\n%s", sec)
	}
}
