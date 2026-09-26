package trunk

import (
	"encoding/base64"
	"net"
	"strconv"
	"strings"

	"golang.org/x/crypto/curve25519"
)

// checkWireGuardKey reports whether key looks like a WireGuard key: 32
// bytes, base64-encoded.
func checkWireGuardKey(field, key string) error {
	raw, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 {
		return invalid(field+"_invalid", "That isn't a WireGuard key (32 bytes, base64).")
	}
	return nil
}

// checkPrivateKey validates key and returns the public key it derives
// (docs/TRUNKS.md §7): WireGuard keys are Curve25519, so the public key is
// the private scalar multiplied by the curve's base point.
func checkPrivateKey(key string) (string, error) {
	if key == "" {
		return "", invalid("private_key_required", "Give the tunnel's private key, or paste a whole configuration file.")
	}
	if err := checkWireGuardKey("private_key", key); err != nil {
		return "", err
	}
	raw, _ := base64.StdEncoding.DecodeString(key)
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", invalid("private_key_invalid", "That isn't a WireGuard private key.")
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// parseWireGuardConf reads a wg-quick configuration file ([Interface] and
// [Peer] sections) into the fields a profile needs. AllowedIPs isn't used:
// a profile's tunnel is split (ADR-024) to exactly its trunks' addresses,
// computed when it's rendered, not from what was imported; it's kept only
// to tell the admin (splitNote).
func parseWireGuardConf(text string) (WireGuardFields, error) {
	var f WireGuardFields
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(value)
		switch section {
		case "interface":
			switch key {
			case "privatekey":
				f.PrivateKey = value
			case "address":
				f.Address = value
			}
		case "peer":
			switch key {
			case "publickey":
				f.PeerPublicKey = value
			case "presharedkey":
				f.PresharedKey = value
			case "endpoint":
				host, portStr, err := net.SplitHostPort(value)
				if err != nil {
					return f, invalid("config_invalid", "The Endpoint line must be host:port.")
				}
				port, err := strconv.Atoi(portStr)
				if err != nil {
					return f, invalid("config_invalid", "The Endpoint line must be host:port.")
				}
				f.PeerEndpointHost, f.PeerEndpointPort = host, &port
			case "allowedips":
				f.allowedIPs = value
			case "persistentkeepalive":
				if n, err := strconv.Atoi(value); err == nil {
					f.PersistentKeepalive = &n
				}
			}
		}
	}
	if f.PrivateKey == "" || f.Address == "" || f.PeerPublicKey == "" || f.PeerEndpointHost == "" {
		return f, invalid("config_invalid", "That configuration is missing PrivateKey, Address, the peer's PublicKey or Endpoint.")
	}
	return f, nil
}

// splitNote tells the admin what became of an imported AllowedIPs
// (docs/TRUNKS.md §7): only the trunks' addresses go through the tunnel.
func splitNote(allowedIPs string) string {
	for _, p := range strings.Split(allowedIPs, ",") {
		if p := strings.TrimSpace(p); p == "0.0.0.0/0" || p == "::/0" {
			return "This configuration sent all traffic through the tunnel (AllowedIPs = " + strings.TrimSpace(allowedIPs) +
				"). Linx sends only the addresses of the phone lines using this profile through it; everything else keeps using this server's own connection."
		}
	}
	if strings.TrimSpace(allowedIPs) == "" {
		return ""
	}
	return "Linx sends only the addresses of the phone lines using this profile through the tunnel; the configuration's AllowedIPs (" +
		strings.TrimSpace(allowedIPs) + ") aren't used."
}
