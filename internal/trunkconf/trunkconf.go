// Package trunkconf renders trunks for Asterisk (docs/TRUNKS.md §4,
// ADR-043): the control plane opens each trunk's sealed password and writes
// pjsip_trunks.conf (endpoints, AORs, auths, registrations, identify rules,
// and the trunks' addresses added to the SIP ACL) plus the certificates
// admins pinned (ADR-045) into a memory-only volume only Asterisk also
// mounts. Asterisk's pjsip.conf includes the file, and its entrypoint
// reloads PJSIP when either file changes (internal/asteriskconf).
package trunkconf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/dbsecret"
	"linxpbx.com/linx/internal/trunk"
	"linxpbx.com/linx/internal/wgconf"
)

// Input is everything a render needs.
type Input struct {
	// Trunks are every trunk; disabled ones are left out of the file.
	Trunks []trunk.Trunk
	// Passwords are the trunks' opened provider passwords, by trunk id.
	Passwords map[uuid.UUID]string
	// Addresses are what each trunk's host resolves to (an IPv4 host is
	// its own address). They go into the ACL, and into identify rules for
	// trunks that call in from their own addresses.
	Addresses map[string][]netip.Addr
	// Tunnels are every WireGuard profile, keys opened and endpoint
	// resolved (docs/TRUNKS.md §7).
	Tunnels []wgconf.Profile
}

// Files is a render: what Write puts in the directory Asterisk reads.
type Files struct {
	// PJSIP is pjsip_trunks.conf.
	PJSIP []byte
	// PinnedCA is every enabled trunk's pinned certificates (PEM), which
	// the entrypoint adds to the public CAs the TLS transport checks
	// providers against.
	PinnedCA []byte
	// WireGuardTrunks are the trunks through each tunnel, by file name
	// (asteriskconf.WireGuardTrunkFile): Asterisk's entrypoint includes a
	// tunnel's file only while its addresses route into the tunnel.
	WireGuardTrunks map[string][]byte
	// WireGuard is linx-wireguard's config: every profile's tunnel,
	// carrying exactly its trunks' addresses (the split tunnel, ADR-024).
	WireGuard wgconf.Config
}

// Render writes the files for in. A trunk that can't be rendered safely
// is left out, with a problem saying why (the API refuses such values;
// this is the second line of defence).
func Render(in Input) (Files, []string) {
	var problems []string
	trunks := slices.Clone(in.Trunks)
	sort.Slice(trunks, func(i, j int) bool { return trunks[i].ID.String() < trunks[j].ID.String() })

	var b bytes.Buffer
	b.WriteString(`; Rendered by the Linx control plane (internal/trunkconf) from the trunks in
; its database; included by pjsip.conf. Do not edit: the next change
; overwrites it. Holds provider passwords: memory only, Asterisk's group only.
`)
	var body bytes.Buffer
	var pinned [][]byte
	seenPin := map[[32]byte]bool{}
	var permit []netip.Addr
	plain := map[string]bool{}
	wgPlain := map[string]bool{}
	profiles := map[uuid.UUID]bool{}
	for _, p := range in.Tunnels {
		profiles[p.ID] = true
	}
	wgBodies := map[uuid.UUID]*bytes.Buffer{}
	allowed := map[uuid.UUID][]netip.Addr{}
	for _, t := range trunks {
		if !t.Enabled {
			continue
		}
		if err := check(t, in.Passwords[t.ID]); err != nil {
			problems = append(problems, fmt.Sprintf("trunk %s (%s) left out: %v", t.ID, t.Name, err))
			continue
		}
		addrs := in.Addresses[t.Host]
		if a, err := netip.ParseAddr(t.Host); err == nil {
			addrs = []netip.Addr{a}
		}
		if len(addrs) == 0 {
			problems = append(problems, fmt.Sprintf("trunk %s (%s): %s doesn't resolve; calls from it are refused until it does", t.ID, t.Name, t.Host))
		}
		if t.CertTrust == trunk.CertPinned && t.Transport == trunk.TransportTLS {
			certs, _ := trunk.ParsePinnedCertificates(t.PinnedCertificate)
			for _, c := range certs {
				sum := sha256.Sum256(c.Raw)
				if !seenPin[sum] {
					seenPin[sum] = true
					pinned = append(pinned, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
				}
			}
		}
		if id := t.WireGuardProfileID; id != nil {
			// Through a tunnel: only by address (the tunnel carries only
			// addresses known in advance), and only in the tunnel's own
			// file.
			if !profiles[*id] {
				problems = append(problems, fmt.Sprintf("trunk %s (%s) left out: its WireGuard profile can't be used", t.ID, t.Name))
				continue
			}
			if _, err := netip.ParseAddr(t.Host); err != nil {
				problems = append(problems, fmt.Sprintf("trunk %s (%s) left out: through a WireGuard tunnel, its address must be an IPv4 address", t.ID, t.Name))
				continue
			}
			wgPlain[t.Transport] = true
			if wgBodies[*id] == nil {
				wgBodies[*id] = &bytes.Buffer{}
			}
			permit = append(permit, addrs...)
			allowed[*id] = append(allowed[*id], addrs...)
			writeTrunk(wgBodies[*id], t, in.Passwords[t.ID], addrs)
			continue
		}
		if t.Transport != trunk.TransportTLS {
			plain[t.Transport] = true
		}
		permit = append(permit, addrs...)
		writeTrunk(&body, t, in.Passwords[t.ID], addrs)
	}

	for _, proto := range []string{trunk.TransportTCP, trunk.TransportUDP} {
		if plain[proto] {
			fmt.Fprintf(&b, "\n; For trunks whose provider can't encrypt (ADR-023): never published, and\n; used by nothing else.\n[%s](%s)\nprotocol=%s\nbind=0.0.0.0:%d\n",
				TransportName(proto), asteriskconf.TrunkTransportTemplate, proto, asteriskconf.PlainTrunkPort)
		}
	}
	if wgPlain[trunk.TransportTLS] {
		fmt.Fprintf(&b, "\n; For trunks over TLS through a WireGuard tunnel (docs/TRUNKS.md §7): no\n; address rewriting (it isn't the LAN). Never published.\n[%s](%s)\nbind=0.0.0.0:%d\n",
			asteriskconf.WireGuardTLSTransport, asteriskconf.WireGuardTLSTemplate, asteriskconf.WireGuardTLSPort)
	}
	for _, proto := range []string{trunk.TransportTCP, trunk.TransportUDP} {
		if wgPlain[proto] {
			fmt.Fprintf(&b, "\n; For trunks through a WireGuard tunnel (docs/TRUNKS.md §7): the tunnel\n; encrypts; no address rewriting (it isn't the LAN). Never published.\n[%s]\ntype=transport\nprotocol=%s\nbind=0.0.0.0:%d\n",
				wireGuardTransport(proto), proto, asteriskconf.WireGuardPlainPort)
		}
	}
	b.Write(body.Bytes())

	slices.SortFunc(permit, func(a, b netip.Addr) int { return a.Compare(b) })
	permit = slices.Compact(permit)
	if len(permit) > 0 {
		fmt.Fprintf(&b, "\n; The trunks' addresses join the phones' ACL: PJSIP checks every request\n; against every ACL object, so there must be only one (ADR-043).\n[%s](+)\n", asteriskconf.ACLName)
		for _, a := range permit {
			fmt.Fprintf(&b, "permit=%s\n", netip.PrefixFrom(a, a.BitLen()))
		}
	}
	wg, wgProblems := wgconf.Render(in.Tunnels, allowed)
	problems = append(problems, wgProblems...)
	files := Files{PJSIP: b.Bytes(), PinnedCA: bytes.Join(pinned, nil), WireGuardTrunks: map[string][]byte{}, WireGuard: wg}
	for _, tun := range wg.Tunnels {
		body := wgBodies[tun.ID]
		if body == nil {
			continue
		}
		var f bytes.Buffer
		f.WriteString(asteriskconf.WireGuardHeaderLine(tun.Address, tun.AllowedIPs) + "\n")
		fmt.Fprintf(&f, "; Rendered by the Linx control plane (internal/trunkconf): the trunks through\n; WireGuard profile %s. Holds provider passwords.\n", tun.ID)
		f.Write(body.Bytes())
		files.WireGuardTrunks[asteriskconf.WireGuardTrunkFile(tun.Interface)] = f.Bytes()
	}
	for id := range wgBodies {
		if !slices.ContainsFunc(wg.Tunnels, func(t wgconf.Tunnel) bool { return t.ID == id }) {
			problems = append(problems, fmt.Sprintf("the trunks through WireGuard profile %s are left out: the profile can't be used", id))
		}
	}
	return files, problems
}

// wireGuardTransport is the transport of trunks through a tunnel.
func wireGuardTransport(transport string) string {
	if transport == trunk.TransportTLS {
		return asteriskconf.WireGuardTLSTransport
	}
	return "transport-wg-" + transport
}

// EndpointTransport is the PJSIP transport t uses.
func EndpointTransport(t trunk.Trunk) string {
	if t.WireGuardProfileID != nil {
		return wireGuardTransport(t.Transport)
	}
	return TransportName(t.Transport)
}

// TransportName is the PJSIP transport trunks on a transport use: TLS
// shares the phones' transport-tls (its CA list is the public CAs plus the
// pinned ones, and it checks the provider's name, ADR-045); TCP and UDP get
// transports of their own, rendered only while a trunk needs them.
func TransportName(transport string) string {
	if transport == trunk.TransportTLS {
		return asteriskconf.TLSTransport
	}
	return "transport-trunk-" + transport
}

// check refuses what can't go into Asterisk's config as is.
func check(t trunk.Trunk, password string) error {
	if t.ID == uuid.Nil {
		return errors.New("no id")
	}
	for _, v := range []struct{ what, s string }{{"host", t.Host}, {"login", t.Username}, {"caller ID", t.CallerIDNumber}} {
		if strings.ContainsAny(v.s, ";,[]\\\"<>@:/ \t\r\n") {
			return fmt.Errorf("its %s has characters Asterisk's config can't hold", v.what)
		}
	}
	if trunk.CheckPassword(password) != nil {
		return errors.New("its password has characters Asterisk's config can't hold")
	}
	if t.Port < 1 || t.Port > 65535 {
		return errors.New("bad port")
	}
	for _, c := range t.Codecs {
		if !slices.Contains(trunk.Codecs, c) {
			return fmt.Errorf("unknown codec %q", c)
		}
	}
	if !slices.Contains(trunk.Transports, t.Transport) || !slices.Contains(trunk.MediaEncryptions, t.MediaEncryption) {
		return errors.New("unknown transport or media encryption")
	}
	if t.Kind == trunk.KindRegistration && (t.Username == "" || password == "") {
		return errors.New("a trunk that signs in needs a login and password")
	}
	if t.CertTrust == trunk.CertPinned && t.Transport == trunk.TransportTLS {
		if _, err := trunk.ParsePinnedCertificates(t.PinnedCertificate); err != nil {
			return fmt.Errorf("pinned certificate: %w", err)
		}
	}
	return nil
}

// configValue escapes ";" (a comment otherwise); check already refused
// everything else Asterisk's config can't hold.
func configValue(s string) string { return strings.ReplaceAll(s, ";", `\;`) }

func writeTrunk(b *bytes.Buffer, t trunk.Trunk, password string, addrs []netip.Addr) {
	id := t.Endpoint()
	transport := EndpointTransport(t)
	// Asterisk decides at start which transports its DNS resolver may use,
	// and a trunk's plain TCP/UDP transport only exists once the first
	// such trunk is added: a name would never resolve over it. An address
	// needs no resolver, so plain trunks get the one Linx resolved (kept
	// current by the renderer). TLS trunks keep the name: the provider's
	// certificate is checked against it.
	target := t.Host
	if t.Transport != trunk.TransportTLS && len(addrs) > 0 {
		target = addrs[0].String()
	}
	uri := fmt.Sprintf("sip:%s:%d;transport=%s", target, t.Port, t.Transport)
	hasAuth := t.Username != "" || password != ""

	fmt.Fprintf(b, "\n; ---- trunk %s (kind %s)\n", t.ID, t.Kind)
	if t.Unencrypted() {
		fmt.Fprintf(b, "; Unencrypted (ADR-023), confirmed by the admin.\n")
	}
	fmt.Fprintf(b, "[%s]\ntype=aor\ncontact=%s\n", id, uri)
	// Keep-alive checks: a trunk that stops answering is marked unreachable
	// and outgoing calls go straight to the next line.
	b.WriteString("qualify_frequency=60\nqualify_timeout=5\n")

	if hasAuth {
		fmt.Fprintf(b, "\n[%s]\ntype=auth\nauth_type=userpass\nusername=%s\npassword=%s\n", id, t.Username, configValue(password))
	}

	media := "sdes"
	if t.MediaEncryption == trunk.MediaNone {
		media = "no"
	}
	fmt.Fprintf(b, "\n[%s]\ntype=endpoint\ntransport=%s\n", id, transport)
	// linx-from-trunk reaches this trunk's own DIDs and nothing else: no
	// dialling out from a call that came in (ADR-048).
	b.WriteString("context=linx-from-trunk\n")
	fmt.Fprintf(b, "disallow=all\nallow=%s\naors=%s\n", strings.Join(t.Codecs, ","), id)
	if hasAuth {
		fmt.Fprintf(b, "outbound_auth=%s\n", id)
	}
	fmt.Fprintf(b, "media_encryption=%s\nmedia_encryption_optimistic=no\n", media)
	fmt.Fprintf(b, "from_domain=%s\n", t.Host)
	// Only a request from its own addresses (or, for a trunk Linx signs in
	// to, on its registration's line) is this trunk: never a name someone
	// puts in a From header, since trunks don't challenge.
	b.WriteString("identify_by=ip\n")
	b.WriteString(`direct_media=no
rtp_symmetric=yes
force_rport=yes
rewrite_contact=yes
dtmf_mode=rfc4733
send_pai=yes
send_rpid=no
trust_id_inbound=no
trust_id_outbound=yes
allow_subscribe=no
`)

	if t.Kind == trunk.KindRegistration {
		fmt.Fprintf(b, "\n[%s]\ntype=registration\ntransport=%s\noutbound_auth=%s\n", id, transport, id)
		fmt.Fprintf(b, "server_uri=%s\nclient_uri=sip:%s@%s\ncontact_user=%s\n", uri, t.Username, t.Host, t.Username)
		// line: the provider's calls to Linx carry this registration's
		// line parameter, which identifies the trunk.
		fmt.Fprintf(b, "expiration=300\nretry_interval=30\nforbidden_retry_interval=300\nfatal_retry_interval=300\nmax_retries=1000000\nline=yes\nendpoint=%s\n", id)
	} else if len(addrs) > 0 {
		fmt.Fprintf(b, "\n[%s]\ntype=identify\nendpoint=%s\n", id, id)
		for _, a := range addrs {
			fmt.Fprintf(b, "match=%s\n", a)
		}
	}
}

// Write puts files in dir, each replaced atomically (written aside, then
// renamed), the certificates first so a reload triggered by the new
// pjsip_trunks.conf always sees the certificates it needs. It reports
// whether anything changed.
func Write(dir string, f Files) (bool, error) {
	changed := false
	type file struct {
		name string
		data []byte
	}
	files := []file{{asteriskconf.PinnedCAFile, f.PinnedCA}}
	for _, name := range slices.Sorted(maps.Keys(f.WireGuardTrunks)) {
		files = append(files, file{name, f.WireGuardTrunks[name]})
	}
	files = append(files, file{asteriskconf.TrunksFile, f.PJSIP})
	// Tunnels no longer there.
	old, _ := filepath.Glob(filepath.Join(dir, asteriskconf.WireGuardTrunkFile("*")))
	for _, path := range old {
		if _, keep := f.WireGuardTrunks[filepath.Base(path)]; !keep {
			if err := os.Remove(path); err != nil {
				return changed, err
			}
			changed = true
		}
	}
	for _, w := range files {
		path := filepath.Join(dir, w.name)
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, w.data) {
			continue
		}
		tmp := path + ".new"
		if err := os.WriteFile(tmp, w.data, 0o640); err != nil {
			return changed, err
		}
		// WriteFile keeps an existing file's mode; make sure.
		if err := os.Chmod(tmp, 0o640); err != nil {
			return changed, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

// Store is the database access the renderer needs.
type Store interface {
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
	ListTrunks(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]trunk.Trunk, error)
	ListWireGuardProfiles(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]trunk.WireGuardProfile, error)
}

// Renderer keeps Dir's files in step with the database: at start, when
// told something changed, and every Interval (a provider's address can
// change without anyone touching Linx).
type Renderer struct {
	Store  Store
	Sealer *dbsecret.Sealer
	Dir    string
	// WireGuardDir is where linx-wireguard reads its tunnels
	// (internal/wgconf); empty: not written.
	WireGuardDir string
	// Lookup resolves a trunk's host. Nil: the system resolver.
	Lookup   func(ctx context.Context, host string) ([]netip.Addr, error)
	Interval time.Duration
	Log      *slog.Logger

	changed chan struct{}
	once    sync.Once
	// last are the addresses each host last resolved to: kept while its
	// DNS fails, so a DNS hiccup doesn't lock a provider out.
	last map[string][]netip.Addr
}

func (r *Renderer) init() {
	r.once.Do(func() {
		r.changed = make(chan struct{}, 1)
		r.last = map[string][]netip.Addr{}
	})
}

// Changed asks for a render as soon as possible (trunk.Service's OnChange).
func (r *Renderer) Changed() {
	r.init()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

// Run renders until ctx ends.
func (r *Renderer) Run(ctx context.Context) {
	r.init()
	interval := r.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		if _, err := r.RenderOnce(ctx); err != nil && ctx.Err() == nil {
			r.Log.Error("rendering trunks for Asterisk", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-r.changed:
		case <-tick.C:
		}
	}
}

// RenderOnce renders and writes the files, reporting whether they changed.
func (r *Renderer) RenderOnce(ctx context.Context) (bool, error) {
	r.init()
	tenant, err := r.Store.DefaultTenant(ctx)
	if err != nil {
		return false, err
	}
	in := Input{Passwords: map[uuid.UUID]string{}, Addresses: map[string][]netip.Addr{}}
	var before *uuid.UUID
	for {
		page, err := r.Store.ListTrunks(ctx, tenant, before, 100)
		if err != nil {
			return false, err
		}
		for _, t := range page {
			if !t.Enabled {
				continue
			}
			pw, err := trunk.OpenPassword(r.Sealer, t)
			if err != nil {
				r.Log.Error("opening a trunk's password; it's left out", "trunk", t.ID, "err", err)
				continue
			}
			in.Trunks = append(in.Trunks, t)
			in.Passwords[t.ID] = pw
			if _, done := in.Addresses[t.Host]; !done {
				in.Addresses[t.Host] = r.resolve(ctx, t.Host)
			}
		}
		if len(page) < 100 {
			break
		}
		before = &page[len(page)-1].ID
	}
	before = nil
	for {
		page, err := r.Store.ListWireGuardProfiles(ctx, tenant, before, 100)
		if err != nil {
			return false, err
		}
		for _, w := range page {
			priv, psk, err := trunk.OpenWireGuardKeys(r.Sealer, w)
			if err != nil {
				r.Log.Error("opening a WireGuard profile's keys; it's left out", "profile", w.ID, "err", err)
				continue
			}
			p := wgconf.Profile{ID: w.ID, Name: w.Name, Address: w.Address, PrivateKey: priv, PresharedKey: psk,
				PeerPublicKey: w.PeerPublicKey, Keepalive: w.PersistentKeepalive}
			if addrs := r.resolve(ctx, w.PeerEndpointHost); len(addrs) > 0 {
				p.Endpoint = netip.AddrPortFrom(addrs[0], uint16(w.PeerEndpointPort))
			}
			in.Tunnels = append(in.Tunnels, p)
		}
		if len(page) < 100 {
			break
		}
		before = &page[len(page)-1].ID
	}
	files, problems := Render(in)
	for _, p := range problems {
		r.Log.Warn(p)
	}
	// The tunnels first: a trunk through one only loads once its addresses
	// route into it.
	if r.WireGuardDir != "" {
		wgChanged, err := wgconf.WriteConfig(r.WireGuardDir, files.WireGuard)
		if err != nil {
			return false, fmt.Errorf("writing the WireGuard tunnels: %w", err)
		}
		if wgChanged {
			r.Log.Info("WireGuard tunnels rendered", "tunnels", len(files.WireGuard.Tunnels))
		}
	}
	changed, err := Write(r.Dir, files)
	if changed {
		r.Log.Info("trunks rendered for Asterisk", "trunks", len(in.Trunks))
	}
	return changed, err
}

func (r *Renderer) resolve(ctx context.Context, host string) []netip.Addr {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}
	}
	lookup := r.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		if prev := r.last[host]; len(prev) > 0 {
			r.Log.Warn("a trunk's address doesn't resolve; keeping the last one", "host", host, "err", err, "addresses", prev)
			return prev
		}
		return nil
	}
	for i := range addrs {
		addrs[i] = addrs[i].Unmap()
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	r.last[host] = addrs
	return addrs
}
