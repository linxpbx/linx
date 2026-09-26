package trunk

import "testing"

// Test keys generated once with `wg genkey` / the equivalent X25519 derivation;
// not used for anything real.
const (
	testPrivateKey = "qLgcA/ELBQAcmbijpgxcxhIGXziCqyfgvmHv1X63aUc=" // gitleaks:allow (test key)
	testPublicKey  = "X59Uv0OU7+T4zQ1YyFpUJ9eXc+AslF6qZ4TZYaXLYW8="
	testPeerPublic = "u719inAundyjsFWqCBDBO6R4+n58xrvlHm6vlZnVWxE="
	testPSK        = "LIWz1kXYhnTONwQ5Ct7xPT5UF+6XSNiaT/fZuxXJkTs="
)

func TestCheckPrivateKey(t *testing.T) {
	pub, err := checkPrivateKey(testPrivateKey)
	if err != nil {
		t.Fatalf("checkPrivateKey(%q) = %v", testPrivateKey, err)
	}
	if pub != testPublicKey {
		t.Errorf("checkPrivateKey(%q) = %q, want %q", testPrivateKey, pub, testPublicKey)
	}
	for _, bad := range []string{"", "not-base64!!", "dG9vc2hvcnQ="} {
		if _, err := checkPrivateKey(bad); err == nil {
			t.Errorf("checkPrivateKey(%q) succeeded, want an error", bad)
		}
	}
}

func TestCheckWireGuardKey(t *testing.T) {
	if err := checkWireGuardKey("peer_public_key", testPeerPublic); err != nil {
		t.Errorf("checkWireGuardKey(valid) = %v, want nil", err)
	}
	if err := checkWireGuardKey("peer_public_key", "short"); err == nil {
		t.Error("checkWireGuardKey(short) succeeded, want an error")
	}
}

func TestParseWireGuardConf(t *testing.T) {
	conf := `
[Interface]
PrivateKey = ` + testPrivateKey + `
Address = 10.6.0.2/32

[Peer]
PublicKey = ` + testPeerPublic + `
PresharedKey = ` + testPSK + `
Endpoint = vpn.example.com:51821
PersistentKeepalive = 25
AllowedIPs = 0.0.0.0/0
`
	f, err := parseWireGuardConf(conf)
	if err != nil {
		t.Fatalf("parseWireGuardConf: %v", err)
	}
	if f.PrivateKey != testPrivateKey || f.Address != "10.6.0.2/32" || f.PeerPublicKey != testPeerPublic ||
		f.PresharedKey != testPSK || f.PeerEndpointHost != "vpn.example.com" {
		t.Fatalf("parseWireGuardConf fields = %+v", f)
	}
	if f.PeerEndpointPort == nil || *f.PeerEndpointPort != 51821 {
		t.Errorf("PeerEndpointPort = %v, want 51821", f.PeerEndpointPort)
	}
	if f.PersistentKeepalive == nil || *f.PersistentKeepalive != 25 {
		t.Errorf("PersistentKeepalive = %v, want 25", f.PersistentKeepalive)
	}
}

func TestParseWireGuardConfIncomplete(t *testing.T) {
	if _, err := parseWireGuardConf("[Interface]\nAddress = 10.6.0.2/32\n"); err == nil {
		t.Error("parseWireGuardConf without a private key succeeded, want an error")
	}
}
