// Package turnconf renders linx-coturn's configuration (ADR-039, docs/WEB.md
// §2): the relay Linx's devices use when they can't reach the server
// directly. It relays to Linx's own addresses on linx-media only —
// Asterisk's audio, the video server for group calls (ADR-088), and its own,
// where another device's allocation is (ADR-087) — and refuses every other
// peer (the LAN, the internet, other containers), so a relay credential is
// good for calls and nothing else.
package turnconf

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/livekit"
	"linxpbx.com/linx/internal/turn"
)

// Ports inside the container (compose.yaml and the front doors map them;
// docs/WEB.md §3).
const (
	ListenPort = 3478 // TURN over UDP (and plain TCP, unused)
	TLSPort    = 5349 // TURN over TLS
	// RelayMin and RelayMax are the relay's own ports on linx-media, one per
	// allocation; nothing outside the server reaches them.
	RelayMin = 49160
	RelayMax = 49999
)

// Quotas (docs/THREAT_MODEL.md, docs/WEB.md §5): coturn can't see who is
// behind a front door, so limits per person and overall do the work.
const (
	// UserQuota is how many allocations one person may hold at once (the
	// part of the username after the colon: their id). Each phone or
	// browser on a call holds two (UDP and TLS) and two more for every
	// route search during it, so 10 was reached by one person's iPhone and
	// iPad on a video call (2026-10-12).
	UserQuota = 20
	// TotalQuota is how many allocations the server holds at once.
	TotalQuota = 300
	// MaxBPS is each allocation's bandwidth cap, bytes a second each way.
	//
	// It has to hold a 1:1 video call, not just the voice: 600 kbit/s of
	// picture (ADR-079) and 24 kbit/s of Opus is 78 kB/s of payload, about
	// 90 kB/s with RTP, SRTP, UDP and TURN's own headers, and more than that
	// for a moment whenever a keyframe goes out or the link recovers. 64 kB/s
	// was sized for audio alone in Phase 1C and silently throttled every
	// relayed video call: the owner's picture froze within seconds of coming
	// on, on a network where the relay was the only way through (2026-10-05).
	// This is twice what a video call needs and still nowhere near enough to
	// use the relay for moving data about, which is what the cap is for.
	// Raised from 160 kB/s on 2026-10-12 so a relayed picture can reach 720p
	// (1.5 Mbit/s) when the person turns HD on; at 160 it stopped at 540p.
	MaxBPS = 240000
)

// Config is linx-coturn's configuration.
type Config struct {
	// ConfPath is where Render writes turnserver.conf (a tmpfs: the root
	// filesystem is read-only, and the file holds the shared secret).
	ConfPath   string
	SecretFile string
	// Realm is this server's domain.
	Realm string
	// CertsDir is linx-certd's certs volume (docs/ops/CERT_RELOAD.md).
	CertsDir string
	// PeerHost is Asterisk's alias on linx-media: a peer that must be there.
	PeerHost string
	// VideoHost is the video server's alias there (ADR-088): a peer too,
	// when it's running.
	VideoHost string
	// LookupHost and InterfaceAddrs are for tests; nil means the system's.
	LookupHost     func(host string) ([]netip.Addr, error)
	InterfaceAddrs func() ([]net.Addr, error)
}

// ConfigFromEnv reads the configuration, defaulting to compose.yaml's
// layout.
func ConfigFromEnv(getenv func(string) string) Config {
	or := func(k, def string) string {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
		return def
	}
	return Config{
		ConfPath:   or("LINX_TURN_CONF", "/tmp/linx-coturn/turnserver.conf"),
		SecretFile: turn.SecretPathFromEnv(getenv),
		Realm:      strings.TrimSpace(getenv("LINX_DOMAIN")),
		CertsDir:   or("LINX_CERTS_DIR", "/var/lib/linx/certs"),
		PeerHost:   or("LINX_MEDIA_HOST", asteriskconf.DefaultMediaHost),
		VideoHost:  or("LINX_LIVEKIT_MEDIA_HOST", livekit.DefaultMediaHost),
	}
}

// Addresses are the two ends of the relay inside the server.
type Addresses struct {
	// Peer is Asterisk's linx-media address; Relay is coturn's own there;
	// Video is the video server's, if it was found.
	Peer, Relay, Video netip.Addr
}

// Resolve finds Asterisk's linx-media address and this container's own
// address on the same network.
func (c Config) Resolve() (Addresses, error) {
	lookup := c.LookupHost
	if lookup == nil {
		lookup = lookupWithRetry
	}
	ips, err := lookup(c.PeerHost)
	if err != nil {
		return Addresses{}, fmt.Errorf("finding the phone system (%s): %w", c.PeerHost, err)
	}
	var peer netip.Addr
	for _, ip := range ips {
		if ip.Unmap().Is4() {
			peer = ip.Unmap()
			break
		}
	}
	if !peer.IsValid() {
		return Addresses{}, fmt.Errorf("%s has no IPv4 address", c.PeerHost)
	}
	addrs := c.InterfaceAddrs
	if addrs == nil {
		addrs = net.InterfaceAddrs
	}
	list, err := addrs()
	if err != nil {
		return Addresses{}, err
	}
	for _, a := range list {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, _ := netip.AddrFromSlice(n.IP)
		ones, _ := n.Mask.Size()
		if p := netip.PrefixFrom(ip.Unmap(), ones); p.Addr().Is4() && p.Contains(peer) && p.Addr() != peer {
			return Addresses{Peer: peer, Relay: p.Addr(), Video: c.video(p)}, nil
		}
	}
	return Addresses{}, fmt.Errorf("this container isn't on the same network as %s (%s)", c.PeerHost, peer)
}

// video finds the video server on the relay's network, once: unlike
// Asterisk it isn't needed for calls, so a server without it still relays
// them.
func (c Config) video(network netip.Prefix) netip.Addr {
	if c.VideoHost == "" {
		return netip.Addr{}
	}
	lookup := c.LookupHost
	if lookup == nil {
		lookup = lookupOnce
	}
	ips, err := lookup(c.VideoHost)
	if err != nil {
		return netip.Addr{}
	}
	for _, ip := range ips {
		if a := ip.Unmap(); a.Is4() && network.Contains(a) {
			return a
		}
	}
	return netip.Addr{}
}

func lookupOnce(host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

func lookupWithRetry(host string) ([]netip.Addr, error) {
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		cancel()
		if err == nil || attempt == 10 {
			return ips, err
		}
		time.Sleep(time.Second)
	}
}

// Render writes turnserver.conf for addrs.
func (c Config) Render(addrs Addresses) error {
	if c.Realm == "" {
		return errors.New("LINX_DOMAIN isn't set")
	}
	secret, err := turn.LoadSecret(c.SecretFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.ConfPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.ConfPath, []byte(c.conf(addrs, string(secret))), 0o600)
}

func videoPeer(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return "allowed-peer-ip=" + a.String() + "\n"
}

// conf is turnserver.conf. Every option is explained in coturn's own
// turnserver.conf; the ones that matter for safety are commented here.
func (c Config) conf(a Addresses, secret string) string {
	return fmt.Sprintf(`# Rendered by linx-coturn-entrypoint at start. Do not edit: the next start
# overwrites it.
listening-port=%d
tls-listening-port=%d
# Relay endpoints on linx-media only, where Asterisk is.
relay-ip=%s
min-port=%d
max-port=%d

# Short-lived credentials from the control plane (TURN REST API, HMAC-SHA1
# with this shared secret); no user database.
use-auth-secret
static-auth-secret=%s
realm=%s
fingerprint
stale-nonce=600

# TLS on the TLS port with linx-certd's certificate (TLS 1.2 and later is
# coturn's default). The entrypoint sends SIGUSR2 when it's renewed.
cert=%s
pkey=%s

# The only peers anyone may relay to are Asterisk's audio and the relay's
# own addresses, where another Linx device's allocation is: two devices
# that can only reach the relay send each other their picture through it
# (ADR-087), and TURN's permissions keep anyone from sending to an
# allocation that didn't ask. Allowed wins over denied in coturn, so this
# is "everything is denied, except these".
denied-peer-ip=0.0.0.0-255.255.255.255
denied-peer-ip=::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff
allowed-peer-ip=%s
allowed-peer-ip=%s
%sno-multicast-peers
no-tcp-relay
# Its admin console stays off: coturn 4.18 starts one only with --cli.

user-quota=%d
total-quota=%d
max-bps=%d

log-file=stdout
simple-log
pidfile=%s
`, ListenPort, TLSPort, a.Relay, RelayMin, RelayMax, secret, c.Realm,
		filepath.Join(c.CertsDir, "current", "fullchain.pem"), filepath.Join(c.CertsDir, "current", "privkey.pem"),
		a.Peer, a.Relay, videoPeer(a.Video), UserQuota, TotalQuota, MaxBPS, filepath.Join(filepath.Dir(c.ConfPath), "turnserver.pid"))
}
