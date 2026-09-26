// Package wgconf is what passes between the control plane and linx-wireguard
// (docs/TRUNKS.md §7, ADR-046): the tunnels the control plane renders from
// its WireGuard profiles (with their private keys, into a memory-only
// volume only the agent also mounts), and the state the agent writes back
// (each tunnel's last handshake). Both sides import it; it depends on
// nothing else in Linx.
package wgconf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The files, each in its own volume.
const (
	// ConfigFile is written by the control plane, read by the agent.
	ConfigFile = "wireguard.json"
	// StatusFile is written by the agent, read by the control plane.
	StatusFile = "wireguard-status.json"
)

// Routing inside Asterisk's network namespace. A trunk's addresses are
// routed into its tunnel from their own table, which every packet except
// the tunnels' own (marked FirewallMark) looks up first: so a provider
// whose SIP server has the same address as its WireGuard endpoint still
// works, the tunnel's encrypted packets taking the normal route.
const (
	FirewallMark = 0x4c58 // "LX"
	RouteTable   = 0x4c58
	RulePriority = 10000 // before the main table (32766)
)

// DefaultKeepalive is the keep-alive a profile without one gets, in
// seconds. WireGuard only shakes hands when there's traffic; without this a
// quiet tunnel would look down after DownAfter.
const DefaultKeepalive = 25

// DownAfter: a tunnel with no handshake for this long is down, and its
// trunks with it (docs/TRUNKS.md §7). WireGuard shakes hands every two
// minutes while traffic flows.
const DownAfter = 3 * time.Minute

// InterfaceName is the network interface a profile's tunnel gets: stable
// for the profile, within Linux's 15 characters, and recognisably Linx's
// (the agent removes every "linx-wg" interface no profile names).
func InterfaceName(profile uuid.UUID) string {
	sum := sha256.Sum256(profile[:])
	return InterfacePrefix + hex.EncodeToString(sum[:4])
}

// InterfacePrefix starts every tunnel interface's name.
const InterfacePrefix = "linx-wg"

// Config is every tunnel the agent should have up.
type Config struct {
	Tunnels []Tunnel `json:"tunnels"`
}

// Tunnel is one WireGuard profile, ready to apply.
type Tunnel struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Interface string    `json:"interface"`
	// PrivateKey and PresharedKey are base64, as WireGuard writes them.
	PrivateKey    string `json:"private_key"`
	PresharedKey  string `json:"preshared_key,omitempty"`
	PeerPublicKey string `json:"peer_public_key"`
	// Address is this end's tunnel address (IPv4, added as a /32).
	Address netip.Addr `json:"address"`
	// Endpoint is the provider's WireGuard address, resolved by the
	// control plane. Invalid while it doesn't resolve.
	Endpoint  netip.AddrPort `json:"endpoint"`
	Keepalive int            `json:"keepalive"`
	// AllowedIPs are its trunks' addresses and nothing else: the split
	// tunnel (ADR-024). Each is routed into the tunnel as a /32.
	AllowedIPs []netip.Addr `json:"allowed_ips"`
}

// Profile is what Render needs of a WireGuard profile, its keys opened.
type Profile struct {
	ID            uuid.UUID
	Name          string
	Address       string
	PrivateKey    string
	PresharedKey  string
	PeerPublicKey string
	// Endpoint is the peer's resolved address and port (invalid if its
	// name doesn't resolve).
	Endpoint  netip.AddrPort
	Keepalive *int
}

// Render builds the agent's config: every profile gets a tunnel (so its
// handshake shows before any trunk uses it), carrying exactly the
// addresses allowed gives it. A profile that can't be used is left out,
// with a problem saying why.
func Render(profiles []Profile, allowed map[uuid.UUID][]netip.Addr) (Config, []string) {
	var problems []string
	cfg := Config{Tunnels: []Tunnel{}}
	profiles = slices.Clone(profiles)
	slices.SortFunc(profiles, func(a, b Profile) int { return strings.Compare(a.ID.String(), b.ID.String()) })
	for _, p := range profiles {
		addr, err := ParseTunnelAddress(p.Address)
		if err != nil {
			problems = append(problems, fmt.Sprintf("WireGuard profile %s (%s) left out: %v", p.ID, p.Name, err))
			continue
		}
		if p.PrivateKey == "" || p.PeerPublicKey == "" {
			problems = append(problems, fmt.Sprintf("WireGuard profile %s (%s) left out: no keys", p.ID, p.Name))
			continue
		}
		if !p.Endpoint.IsValid() {
			problems = append(problems, fmt.Sprintf("WireGuard profile %s (%s): its endpoint doesn't resolve; the tunnel waits until it does", p.ID, p.Name))
		}
		keepalive := DefaultKeepalive
		if p.Keepalive != nil && *p.Keepalive > 0 {
			keepalive = *p.Keepalive
		}
		ips := slices.Clone(allowed[p.ID])
		slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })
		ips = slices.Compact(ips)
		if ips == nil {
			ips = []netip.Addr{}
		}
		cfg.Tunnels = append(cfg.Tunnels, Tunnel{
			ID: p.ID, Name: p.Name, Interface: InterfaceName(p.ID),
			PrivateKey: p.PrivateKey, PresharedKey: p.PresharedKey, PeerPublicKey: p.PeerPublicKey,
			Address: addr, Endpoint: p.Endpoint, Keepalive: keepalive, AllowedIPs: ips,
		})
	}
	return cfg, problems
}

// ParseTunnelAddress reads a profile's Address (wg-quick's form: one or
// more comma-separated addresses, each optionally with a prefix length)
// and returns its IPv4 address: Asterisk talks to trunks over IPv4 only.
func ParseTunnelAddress(s string) (netip.Addr, error) {
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var a netip.Addr
		if p, err := netip.ParsePrefix(part); err == nil {
			a = p.Addr()
		} else if a, err = netip.ParseAddr(part); err != nil {
			return netip.Addr{}, fmt.Errorf("%q isn't an address", part)
		}
		if a.Is4() {
			if a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a.IsLinkLocalUnicast() {
				return netip.Addr{}, fmt.Errorf("%s can't be a tunnel address", a)
			}
			return a, nil
		}
	}
	return netip.Addr{}, errors.New("it has no IPv4 address")
}

// Status is what the agent last found.
type Status struct {
	WrittenAt time.Time `json:"written_at"`
	// Error is a problem with the agent as a whole (it can't read its
	// config, say).
	Error string `json:"error,omitempty"`
	// Tunnels by profile id.
	Tunnels map[string]TunnelStatus `json:"tunnels"`
}

// TunnelStatus is one tunnel's state.
type TunnelStatus struct {
	Interface string `json:"interface"`
	// UpSince is when the agent (re)applied the tunnel: until DownAfter
	// has passed since, a tunnel with no handshake is still connecting.
	UpSince       time.Time  `json:"up_since"`
	LastHandshake *time.Time `json:"last_handshake,omitempty"`
	RxBytes       int64      `json:"rx_bytes"`
	TxBytes       int64      `json:"tx_bytes"`
	// Error: the tunnel couldn't be applied.
	Error string `json:"error,omitempty"`
	// Skipped are allowed addresses the agent left out, and why.
	Skipped []string `json:"skipped,omitempty"`
}

// Tunnel states.
const (
	StateUp         = "up"
	StateConnecting = "connecting"
	StateDown       = "down"
	StateUnknown    = "unknown"
)

// staleAfter: an older status file means the agent isn't running.
const staleAfter = time.Minute

// Decide is tunnel id's state and why, in plain words, from the agent's
// report (fileErr: it couldn't be read).
func Decide(file Status, fileErr error, id uuid.UUID, now time.Time) (string, string) {
	if fileErr != nil || now.Sub(file.WrittenAt) > staleAfter {
		return StateUnknown, "Linx's WireGuard service isn't reporting (is linx-wireguard running?)."
	}
	if file.Error != "" {
		return StateDown, file.Error
	}
	s, ok := file.Tunnels[id.String()]
	if !ok {
		return StateUnknown, "The tunnel isn't set up yet."
	}
	if s.Error != "" {
		return StateDown, s.Error
	}
	if s.LastHandshake != nil && now.Sub(*s.LastHandshake) <= DownAfter {
		return StateUp, fmt.Sprintf("Last handshake %s ago.", ago(now.Sub(*s.LastHandshake)))
	}
	if now.Sub(s.UpSince) <= DownAfter {
		return StateConnecting, "Waiting for the first handshake with the provider."
	}
	if s.LastHandshake == nil {
		return StateDown, fmt.Sprintf("No handshake with the provider in %s: check the keys and the provider's address.", ago(now.Sub(s.UpSince)))
	}
	return StateDown, fmt.Sprintf("No handshake with the provider for %s.", ago(now.Sub(*s.LastHandshake)))
}

func ago(d time.Duration) string {
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
}

// Encode is c as the agent reads it: deterministic, so an unchanged
// config writes nothing.
func (c Config) Encode() []byte {
	b, _ := json.MarshalIndent(c, "", "  ")
	return append(b, '\n')
}

// WriteConfig puts c in dir (owner-only: it holds private keys),
// atomically, reporting whether it changed.
func WriteConfig(dir string, c Config) (bool, error) {
	return writeFile(filepath.Join(dir, ConfigFile), c.Encode(), 0o600)
}

// ReadConfig reads the config in dir.
func ReadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, ConfigFile))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", ConfigFile, err)
	}
	return c, nil
}

// WriteStatus puts s in dir atomically. States only, no keys: readable by
// all.
func WriteStatus(dir string, s Status) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	_, err = writeFile(filepath.Join(dir, StatusFile), append(b, '\n'), 0o644)
	return err
}

// ReadStatus reads the agent's report in dir.
func ReadStatus(dir string) (Status, error) {
	var s Status
	b, err := os.ReadFile(filepath.Join(dir, StatusFile))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", StatusFile, err)
	}
	return s, nil
}

func writeFile(path string, data []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return false, err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return false, err
	}
	return true, os.Rename(tmp, path)
}
