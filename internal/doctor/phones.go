package doctor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/installer"
)

const asteriskContainer = "linx-asterisk"

const asteriskLogs = "Look at its log for the reason: sudo docker logs --tail 50 " + asteriskContainer

// Phones checks the phone system (docs/PBX.md §5): Asterisk answering, its
// database and control-plane connections, and that phones can reach it from
// the local network, over TLS, and from nowhere else.
func Phones(ctx context.Context, env Env, cfg installer.Config) []Result {
	var rs results
	service(ctx, env, &rs, asteriskContainer, "The phone system")
	lan := env.LAN()
	status, _, err := containerState(ctx, env.Runner, asteriskContainer)
	if err == nil && status == "running" {
		phoneDatabase(ctx, env, &rs)
		phoneControl(ctx, env, &rs)
		phoneTransports(ctx, env, &rs)
		phonePorts(ctx, env, &rs, lan)
		if lan.OK() {
			phoneCertificate(ctx, env, &rs, cfg, lan)
		}
	}
	no5060(ctx, env, &rs)
	firewall(ctx, env, &rs, lan)
	if lan.OK() {
		phoneDNS(ctx, env, &rs, cfg, lan)
	} else {
		rs.warn("This server isn't on a local network (its address is public), so phones can't connect yet.",
			"Nothing to do for now: connecting from anywhere arrives in a later Linx update. If this server is on a local network, run: "+
				"sudo linx setup --config "+installer.ConfigPath)
	}
	return rs
}

func asteriskCLI(ctx context.Context, env Env, cmd string) (string, error) {
	out, err := env.Runner.Run(ctx, nil, "docker", "exec", asteriskContainer, "asterisk", "-rx", cmd)
	return string(out), err
}

var odbcConnectionsRE = regexp.MustCompile(`Number of active connections: (\d+)`)

// ODBCConnected reports whether "odbc show asterisk" shows an open
// connection to the database.
func ODBCConnected(cliOutput string) bool {
	m := odbcConnectionsRE.FindStringSubmatch(cliOutput)
	return m != nil && m[1] != "0"
}

// phoneQuery checks the linx_asterisk role's grants (ADR-032): SELECT on the
// four realtime views, and nothing else anywhere.
const phoneQuery = `SELECT json_build_object(
	'views', (SELECT count(*) FROM unnest(ARRAY['ps_endpoints', 'ps_aors', 'ps_auths', 'linx_ring_targets']) v
		WHERE has_table_privilege('linx_asterisk', 'asterisk.' || v, 'SELECT')),
	'other', (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'v', 'm', 'p', 'f') AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		AND (has_table_privilege('linx_asterisk', c.oid, 'INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER')
			OR has_table_privilege('linx_asterisk', c.oid, 'SELECT')
			AND NOT (n.nspname = 'asterisk' AND c.relname IN ('ps_endpoints', 'ps_aors', 'ps_auths', 'linx_ring_targets')))))`

func phoneDatabase(ctx context.Context, env Env, rs *results) {
	out, err := asteriskCLI(ctx, env, "odbc show asterisk")
	if err != nil || !ODBCConnected(out) {
		rs.fail("The phone system can't read extensions and phones from the database, so no phone can sign in.",
			"Check the database is running (above), then: "+asteriskLogs)
		return
	}
	if status, _, err := containerState(ctx, env.Runner, postgresContainer); err != nil || status != "running" {
		return
	}
	b, err := env.Runner.Run(ctx, nil, "docker", psqlArgs(postgresContainer, phoneQuery)...)
	var st struct{ Views, Other int }
	if err == nil {
		err = json.Unmarshal(bytes.TrimSpace(b), &st)
	}
	switch {
	case err != nil:
		rs.fail("Can't check what the phone system may read in the database.",
			"The API service sets this up when it starts: sudo docker logs --tail 50 "+controlPlaneContainer)
	case st.Views != 4 || st.Other != 0:
		rs.fail("The phone system's database access isn't limited to what it needs (the phone settings, read-only).",
			"Restart the API service, which resets it: sudo docker restart "+controlPlaneContainer+
				". If this stays, the database was changed by hand: restore it from a backup.")
	default:
		rs.ok("The phone system reads the phone settings from the database, and nothing else.")
	}
}

// ARIConnected reports whether "ari show websocket sessions" lists Linx's
// outbound connection to the control plane as up.
func ARIConnected(cliOutput string) bool {
	for _, line := range strings.Split(cliOutput, "\n") {
		f := strings.Fields(line)
		if len(f) >= 4 && f[0] == asteriskconf.ARIApp && f[1] == "persistent" {
			return f[3] == "Up"
		}
	}
	return false
}

func phoneControl(ctx context.Context, env Env, rs *results) {
	out, err := asteriskCLI(ctx, env, "ari show websocket sessions")
	if err != nil || !ARIConnected(out) {
		rs.fail("The phone system isn't connected to the API service. Calls still work, but Linx can't see them "+
			"(no call events, no online status).",
			"Check the API service is running (above). The phone system reconnects by itself; if it doesn't within a minute: "+
				"sudo docker restart "+asteriskContainer)
		return
	}
	rs.ok("The phone system is connected to the API service.")
}

// PlainSIPTransports returns the SIP transports "pjsip show transports"
// lists that aren't TLS on port 5061 (there must be none: docs/PBX.md §2).
// ok is false if the output lists no transport at all.
func PlainSIPTransports(cliOutput string) (bad []string, ok bool) {
	for _, line := range strings.Split(cliOutput, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[0] != "Transport:" || strings.HasPrefix(f[1], "<") {
			continue
		}
		ok = true
		if f[2] != "tls" || !strings.HasSuffix(f[len(f)-1], ":"+strconv.Itoa(asteriskconf.SIPPort)) {
			bad = append(bad, f[1]+" ("+f[2]+" "+f[len(f)-1]+")")
		}
	}
	return bad, ok
}

func phoneTransports(ctx context.Context, env Env, rs *results) {
	out, err := asteriskCLI(ctx, env, "pjsip show transports")
	bad, ok := PlainSIPTransports(out)
	switch {
	case err != nil || !ok:
		rs.fail("The phone system isn't accepting phone connections (no SIP over TLS on port 5061).",
			"Usually the certificate couldn't be read. "+asteriskLogs)
	case len(bad) > 0:
		rs.fail("The phone system accepts connections without encryption: "+strings.Join(bad, ", ")+".",
			"Linx never sets this up; restart it to restore its settings: sudo docker restart "+asteriskContainer)
	default:
		rs.ok("The phone system only accepts encrypted phone connections (SIP over TLS, port 5061).")
	}
}

// phonePorts checks where Docker publishes the phone ports: on the LAN
// address setup detected, and nowhere else.
func phonePorts(ctx context.Context, env Env, rs *results, lan installer.LAN) {
	out, err := env.Runner.Run(ctx, nil, "docker", "port", asteriskContainer)
	if err != nil {
		rs.fail("Can't read which ports the phone system is using.", asteriskLogs)
		return
	}
	want := lan.BindAddress()
	var sip, rtp int
	var wrong []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		port, host, found := strings.Cut(line, " -> ")
		if !found {
			continue
		}
		ap, err := netip.ParseAddrPort(strings.TrimSpace(host))
		if err != nil || ap.Addr() != want {
			wrong = append(wrong, strings.TrimSpace(host))
			continue
		}
		switch {
		case port == fmt.Sprintf("%d/tcp", asteriskconf.SIPPort):
			sip++
		case strings.HasSuffix(port, "/udp"):
			rtp++
		}
	}
	switch {
	case len(wrong) > 0:
		rs.fail(fmt.Sprintf("The phone ports are open on %s, but this server's local network address is %s.", wrong[0], want),
			"If this server's address changed, run setup again so phones are set up for the new one: "+rerunSetup)
	case sip != 1 || rtp != asteriskconf.RTPEnd-asteriskconf.RTPStart+1:
		rs.fail("The phone ports aren't all open (phones need port 5061 and the audio ports).", rerunSetup)
	case lan.OK():
		rs.ok(fmt.Sprintf("The phone ports are open on this server's local network address (%s) only.", want))
	default:
		rs.ok("The phone ports are open on this server itself only.")
	}
}

// phoneCertificate connects to the phone port like a phone does and checks
// the certificate it gets is the one linx-certd issued, valid for sip.<domain>.
func phoneCertificate(ctx context.Context, env Env, rs *results, cfg installer.Config, lan installer.LAN) {
	pemData, err := copyFromContainer(ctx, env.Runner, certdContainer, fullchainPath)
	if err != nil {
		return // reported under Certificates
	}
	chain, err := parseCerts(pemData)
	if err != nil {
		return // reported under Certificates
	}
	roots := env.Roots
	if isStaging(chain[0]) {
		roots = env.StagingRoots
	}
	name := "sip." + cfg.Domain.Name
	addr := netip.AddrPortFrom(lan.Address, asteriskconf.SIPPort).String()
	leaf, err := env.TLSLeaf(ctx, addr, name, roots)
	switch {
	case err != nil:
		rs.fail("Couldn't make a secure connection to the phone system like a phone would ("+err.Error()+").",
			"Restart it so it reads the current certificate: sudo docker restart "+asteriskContainer+". If that doesn't help: "+asteriskLogs)
	case !bytes.Equal(leaf.Raw, chain[0].Raw):
		rs.fail("The phone system is still using an older certificate (a renewed one only takes effect after a restart).",
			"Restart it: sudo docker restart "+asteriskContainer)
	default:
		rs.ok("Phones get the current certificate for " + name + ".")
	}
}

// no5060 checks nothing on this server offers unencrypted SIP: no container
// publishes port 5060 and no host process listens on it.
func no5060(ctx context.Context, env Env, rs *results) {
	fix := "Linx never uses port 5060 (unencrypted SIP). Find what does, and stop it: sudo ss -lntup 'sport = :5060'; sudo docker ps"
	if out, err := env.Runner.Run(ctx, nil, "docker", "ps", "--format", "{{.Names}} {{.Ports}}"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, ":5060->") {
				name, _, _ := strings.Cut(line, " ")
				rs.fail("The container "+name+" opens port 5060 (unencrypted SIP) on this server.", fix)
				return
			}
		}
	}
	out, err := env.Runner.Run(ctx, nil, "ss", "-Hlntu")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 5 && strings.HasSuffix(f[4], ":5060") {
			rs.fail("Something on this server listens on port 5060 (unencrypted SIP).", fix)
			return
		}
	}
	rs.ok("Nothing on this server offers unencrypted SIP (port 5060).")
}

// firewall checks linx setup's nftables table is loaded, allows the LAN
// setup detected, and comes back after a restart.
func firewall(ctx context.Context, env Env, rs *results, lan installer.LAN) {
	out, err := env.Runner.Run(ctx, nil, "nft", "-j", "list", "set", "inet", installer.FirewallTable, "phone_networks")
	loaded, err := phoneNetworksSet(out, err)
	switch {
	case err != nil:
		rs.fail("The firewall rules that keep phones on your local network aren't loaded.",
			"Load them: sudo systemctl restart "+installer.FirewallUnit+". If that fails: "+rerunSetup)
		return
	case !slices.Equal(loaded, lan.Networks()):
		rs.fail(fmt.Sprintf("The firewall lets phones connect from %s, but this server's local network is %s.",
			describeNetworks(loaded), describeNetworks(lan.Networks())),
			"If this server moved to another network, run setup again: "+rerunSetup)
		return
	}
	en, _ := env.Runner.Run(ctx, nil, "systemctl", "is-enabled", installer.FirewallUnit)
	if strings.TrimSpace(string(en)) != "enabled" {
		rs.warn("The firewall rules for phones are loaded, but won't be after the server restarts.",
			"Turn them on at startup: sudo systemctl enable "+installer.FirewallUnit)
		return
	}
	if lan.OK() {
		rs.ok("The firewall only lets phones connect from your local network (" + lan.Network.String() + ").")
	} else {
		rs.ok("The firewall keeps the phone ports closed.")
	}
}

// phoneNetworksSet parses `nft -j list set`: elements are single addresses
// ("192.168.1.7") or {"prefix": {"addr", "len"}}.
func phoneNetworksSet(out []byte, err error) ([]netip.Prefix, error) {
	if err != nil {
		return nil, err
	}
	var doc struct {
		Nftables []struct {
			Set *struct {
				Elem []json.RawMessage `json:"elem"`
			} `json:"set"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	for _, o := range doc.Nftables {
		if o.Set == nil {
			continue
		}
		var nets []netip.Prefix
		for _, e := range o.Set.Elem {
			var s string
			var p struct {
				Prefix struct {
					Addr string
					Len  int
				}
			}
			switch {
			case json.Unmarshal(e, &s) == nil:
				a, err := netip.ParseAddr(s)
				if err != nil {
					return nil, err
				}
				nets = append(nets, netip.PrefixFrom(a, a.BitLen()))
			case json.Unmarshal(e, &p) == nil:
				a, err := netip.ParseAddr(p.Prefix.Addr)
				if err != nil {
					return nil, err
				}
				nets = append(nets, netip.PrefixFrom(a, p.Prefix.Len))
			default:
				return nil, fmt.Errorf("unexpected set element %s", e)
			}
		}
		return nets, nil
	}
	return nil, fmt.Errorf("no set in nft output")
}

func describeNetworks(nets []netip.Prefix) string {
	if len(nets) == 0 {
		return "nowhere"
	}
	return asteriskconf.FormatSIPNetworks(nets)
}

// phoneDNS checks phones can find the server: sip.<domain> must point at
// its LAN address (nothing creates that record yet; setup tells the owner).
func phoneDNS(ctx context.Context, env Env, rs *results, cfg installer.Config, lan installer.LAN) {
	name := "sip." + cfg.Domain.Name
	fix := fmt.Sprintf("At your DNS provider, add a record: %s, type A, value %s (at Cloudflare: \"DNS only\", not proxied). "+
		"Changes can take a few minutes to show up.", name, lan.Address)
	addrs, err := env.LookupIP(ctx, name)
	switch {
	case err != nil || len(addrs) == 0:
		rs.warn("Phones can't find this server yet: "+name+" doesn't exist.", fix)
	case !slices.Contains(addrs, lan.Address):
		rs.warn(fmt.Sprintf("Phones can't find this server: %s points to %s, not %s.", name, addrs[0], lan.Address), fix)
	default:
		rs.ok("Phones find this server at " + name + ".")
	}
}

// DialTLSLeaf is Env.TLSLeaf on a real network.
func DialTLSLeaf(ctx context.Context, addr, serverName string, roots *x509.CertPool) (*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12}}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.(*tls.Conn).ConnectionState().PeerCertificates[0], nil
}

// LookupIP is Env.LookupIP on a real network.
func LookupIP(ctx context.Context, host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	for i, a := range addrs {
		addrs[i] = a.Unmap()
	}
	return addrs, err
}
