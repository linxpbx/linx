// Package asteriskconf renders Asterisk's *.conf files at container start
// (docs/PBX.md §2), from a small Config read from environment variables. It
// never edits a file by hand: every start renders a fresh set from scratch,
// like linx-certd's manager renders its own state.
package asteriskconf

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the Asterisk container's configuration.
type Config struct {
	// ConfDir is where *.conf files are rendered (a tmpfs mount: the
	// container's root filesystem is read-only).
	ConfDir string
	// VarDir is astvarlibdir/astdbdir/astkeydir: the asterisk-state volume,
	// which holds the local registrations database and generated keys.
	VarDir string
	// RunDir holds the console socket and PID file (a tmpfs mount).
	RunDir string
	// ScratchDir holds the spool and log directories (a tmpfs mount): spool
	// is unused this slice, and logs go to stdout, not files.
	ScratchDir string
	// CertsDir is linx-certd's certs volume (docs/ops/CERT_RELOAD.md):
	// CertsDir/current/{fullchain,privkey}.pem is always the deployed pair.
	CertsDir string
	// SIPPort is the PJSIP TLS transport's bind port.
	SIPPort int
	// DBHost, DBPort and DBName are Postgres's address (the same database
	// control-plane uses; ADR-032). Asterisk only ever reads the asterisk
	// schema's realtime views there, as the linx_asterisk role.
	DBHost, DBPort, DBName string
	// DBPasswordFile is the linx_asterisk role's password (docs/PBX.md §3;
	// Docker secret linx_asterisk_db_password), set by the control plane at
	// every startup so it always matches what's rendered here.
	DBPasswordFile string
	// DataDir is astdatadir: sound prompts, ARI's REST model and docs, shipped
	// in the image (read-only) rather than in the asterisk-state volume, so an
	// image update always brings its own copy.
	DataDir string
	// ARIURL is the control plane's ARI websocket, which Asterisk connects out
	// to (ARI outbound websocket, ADR-034). Asterisk has no ARI listener.
	ARIURL string
	// ARIPasswordFile is the password Asterisk presents to the control plane
	// (Docker secret linx_ari_password).
	ARIPasswordFile string
	// CARootFile is the internal CA's root certificate: Asterisk checks the
	// control plane's step-ca certificate against it.
	CARootFile string
	// SIPNetworks lists the networks phones may connect from, comma-separated
	// ("192.168.1.0/24"), or "none" (docs/PBX.md §6: LAN only this slice).
	// Everything else is refused before authentication.
	SIPNetworks string
	// SIPAddress is the host address phones reach Asterisk on (compose.yaml
	// publishes the SIP and audio ports there). Asterisk puts it in its SIP
	// headers and audio offers instead of its own container address. Empty,
	// or a loopback address, means phones talk to the container directly.
	SIPAddress string
	// SIPWSHost is the name the control plane reaches Asterisk's secure
	// websocket by (docs/WEB.md §2): a network alias compose.yaml gives
	// Asterisk on linx-sipws only, so it resolves to Asterisk's address on
	// that network, where the websocket listens. "none" turns the websocket
	// off.
	SIPWSHost string
	// SIPWSCertsDir holds the websocket's certificate for SIPWSHost, laid out
	// like CertsDir (current/{fullchain,privkey}.pem). The control plane
	// issues it from the internal CA and writes it there (a memory-only
	// volume); Asterisk never holds a CA credential.
	SIPWSCertsDir string
	// MediaHost is the name coturn reaches Asterisk's audio by (docs/WEB.md
	// §2): a network alias compose.yaml gives Asterisk on linx-media only.
	// Asterisk offers browsers that address for audio (the relay's side),
	// plus SIPAddress for browsers on the LAN, and nothing else. "none"
	// offers browsers only SIPAddress.
	MediaHost string
	// DefaultRouteAddr is the container's address on the network holding
	// its default route: the one Docker publishes the audio ports through.
	// Nil means reading /proc/net/route.
	DefaultRouteAddr func() (netip.Addr, error)
	// InterfaceAddrs lists the container's own addresses, which become
	// local_net (no address rewriting towards them). Nil means
	// net.InterfaceAddrs.
	InterfaceAddrs func() ([]net.Addr, error)
	// TrunksDir is where the control plane writes the trunks it renders
	// (internal/trunkconf, ADR-043): TrunksFile, which pjsip.conf includes,
	// and PinnedCAFile. A memory-only volume.
	TrunksDir string
	// SystemCAFile is the public CAs trunks' certificates are checked
	// against (the image's ca-certificates bundle), besides the pinned ones.
	SystemCAFile string
	// LookupHost resolves SIPWSHost and MediaHost. Nil means the system resolver, retried
	// for a few seconds while Docker's DNS catches up with a new container.
	LookupHost func(host string) ([]netip.Addr, error)
}

// Phone ports (docs/PBX.md §2). compose.yaml publishes exactly these, and
// linx setup's firewall opens them to the LAN only.
const (
	SIPPort  = 5061
	RTPStart = 10000
	RTPEnd   = 10199
)

// SIPWSPort is the secure websocket's port on linx-sipws. Nothing publishes
// it: only the control plane's /sip relay connects (docs/WEB.md §5).
const SIPWSPort = 8089

// SIPWSPath is the websocket's URL path (res_http_websocket's).
const SIPWSPath = "/ws"

// DefaultSIPWSHost is SIPWSHost's default: the name on the certificate the
// control plane issues for the websocket, and the alias compose.yaml gives
// Asterisk on linx-sipws.
const DefaultSIPWSHost = "linx-sipws"

// DefaultMediaHost is MediaHost's default: the alias compose.yaml gives
// Asterisk on linx-media, which coturn also resolves (allowed-peer-ip).
const DefaultMediaHost = "linx-asterisk-media"

// ConfigFromEnv reads the configuration from the environment, defaulting
// every path to the layout compose.yaml mounts.
func ConfigFromEnv(getenv func(string) string) Config {
	return Config{
		ConfDir:         envOr(getenv, "LINX_ASTERISK_CONF_DIR", "/etc/asterisk"),
		VarDir:          envOr(getenv, "LINX_ASTERISK_VAR_DIR", "/var/lib/asterisk"),
		RunDir:          envOr(getenv, "LINX_ASTERISK_RUN_DIR", "/var/run/asterisk"),
		ScratchDir:      envOr(getenv, "LINX_ASTERISK_SCRATCH_DIR", "/tmp/asterisk"),
		CertsDir:        envOr(getenv, "LINX_CERTS_DIR", "/var/lib/linx/certs"),
		SIPPort:         SIPPort,
		DBHost:          envOr(getenv, "LINX_DB_HOST", "postgres"),
		DBPort:          envOr(getenv, "LINX_DB_PORT", "5432"),
		DBName:          envOr(getenv, "LINX_DB_NAME", "linx"),
		DBPasswordFile:  envOr(getenv, "LINX_ASTERISK_DB_PASSWORD_FILE", "/run/secrets/linx_asterisk_db_password"),
		DataDir:         envOr(getenv, "LINX_ASTERISK_DATA_DIR", "/usr/share/asterisk"),
		ARIURL:          envOr(getenv, "LINX_ARI_URL", "wss://linx-ari:8089/ari"),
		ARIPasswordFile: envOr(getenv, "LINX_ARI_PASSWORD_FILE", "/run/secrets/linx_ari_password"),
		CARootFile:      envOr(getenv, "LINX_CA_ROOT_FILE", "/etc/linx/ca/root_ca.crt"),
		SIPNetworks:     strings.TrimSpace(getenv("LINX_SIP_NETWORKS")),
		SIPAddress:      strings.TrimSpace(getenv("LINX_SIP_ADDRESS")),
		SIPWSHost:       envOr(getenv, "LINX_SIPWS_HOST", DefaultSIPWSHost),
		SIPWSCertsDir:   envOr(getenv, "LINX_SIPWS_CERTS_DIR", "/var/lib/linx/sipws-certs"),
		MediaHost:       envOr(getenv, "LINX_MEDIA_HOST", DefaultMediaHost),
		TrunksDir:       envOr(getenv, "LINX_TRUNKS_DIR", "/var/lib/linx/trunks"),
		SystemCAFile:    envOr(getenv, "LINX_SYSTEM_CA_FILE", "/etc/ssl/certs/ca-certificates.crt"),
	}
}

// Names pjsip.conf defines that the control plane's pjsip_trunks.conf
// (internal/trunkconf) builds on.
const (
	// TLSTransport is the phones' TLS transport, which trunks over TLS use
	// too (docs/TRUNKS.md §6).
	TLSTransport = "transport-tls"
	// ACLName is the one SIP ACL: the trunk file adds its addresses to it
	// ("[phone-networks](+)"), because PJSIP makes a request pass every ACL
	// object separately (ADR-043).
	ACLName = "phone-networks"
	// TrunkTransportTemplate carries the address rewriting (natSettings)
	// for the trunk file's plain TCP/UDP transports (ADR-023).
	TrunkTransportTemplate = "linx-trunk-transport"
	// PlainTrunkPort is those transports' port: never 5060, never
	// published.
	PlainTrunkPort = 5062
	// TrunksFile and PinnedCAFile are the files in TrunksDir.
	TrunksFile   = "pjsip_trunks.conf"
	PinnedCAFile = "pinned-ca.pem"
)

// TrunkCAPath is the CA list the TLS transport checks providers against:
// the public CAs plus the pinned ones (WriteTrunkCA).
func (c Config) TrunkCAPath() string { return filepath.Join(c.ConfDir, "trunk-ca.pem") }

// WriteTrunkCA writes TrunkCAPath: SystemCAFile, then every certificate in
// TrunksDir's PinnedCAFile that parses (a broken one is skipped and
// reported: OpenSSL refuses a whole CA file it can't read, which would
// take the phones' TLS transport down with it). Replaced atomically:
// OpenSSL reads it again for every new connection to a provider.
func (c Config) WriteTrunkCA() (skipped int, err error) {
	system, err := os.ReadFile(c.SystemCAFile)
	if err != nil {
		return 0, fmt.Errorf("public CAs: %w", err)
	}
	out := append(bytes.TrimSpace(system), '\n')
	pinned, err := os.ReadFile(filepath.Join(c.TrunksDir, PinnedCAFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	for rest := pinned; ; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			if len(bytes.TrimSpace(rest)) > 0 {
				skipped++
			}
			break
		}
		if b.Type != "CERTIFICATE" {
			skipped++
			continue
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			skipped++
			continue
		}
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b.Bytes})...)
	}
	tmp := c.TrunkCAPath() + ".new"
	if err := os.WriteFile(tmp, out, 0o640); err != nil {
		return skipped, err
	}
	return skipped, os.Rename(tmp, c.TrunkCAPath())
}

// ARIApp is the ARI application the control plane serves: every event
// Asterisk sends over the outbound websocket is addressed to it.
const ARIApp = "linx"

// ARIUser is the username Asterisk presents to the control plane's ARI
// websocket, with the linx_ari_password secret.
const ARIUser = "asterisk"

// ODBCIniPath and ODBCSysIniDir are where Render puts unixODBC's config
// (ConfDir is the only writable directory this image has). The entrypoint
// points unixODBC at them with the ODBCINI and ODBCSYSINI environment
// variables before it execs asterisk.
func (c Config) ODBCIniPath() string   { return filepath.Join(c.ConfDir, "odbc.ini") }
func (c Config) ODBCSysIniDir() string { return c.ConfDir }

// OpenSSLConfPath is the OpenSSL configuration Render writes (openssl.cnf);
// the entrypoint points OPENSSL_CONF at it.
func (c Config) OpenSSLConfPath() string { return filepath.Join(c.ConfDir, "openssl.cnf") }

// odbcDriverPath is where the runtime image's psqlODBC package installs its
// driver, symlinked to this fixed, architecture-independent path by
// deploy/docker/asterisk.Dockerfile.
const odbcDriverPath = "/usr/lib/psqlodbcw.so"

func envOr(getenv func(string) string, key, fallback string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return fallback
}

// Render writes every *.conf file Asterisk needs into c.ConfDir, creating
// c.RunDir and c.ScratchDir's subdirectories along the way.
func (c Config) Render() error {
	for _, dir := range []string{c.ConfDir, c.RunDir, c.VarDir, filepath.Join(c.ScratchDir, "spool"), filepath.Join(c.ScratchDir, "log")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	dbPassword, err := readSecret(c.DBPasswordFile)
	if err != nil {
		return fmt.Errorf("asterisk database password: %w", err)
	}
	ariPassword, err := readSecret(c.ARIPasswordFile)
	if err != nil {
		return fmt.Errorf("ARI password: %w", err)
	}
	if !strings.HasPrefix(c.ARIURL, "wss://") {
		return fmt.Errorf("LINX_ARI_URL %q: must be wss:// (ARI never runs without TLS)", c.ARIURL)
	}
	nets, err := ParseSIPNetworks(c.SIPNetworks)
	if err != nil {
		return fmt.Errorf("LINX_SIP_NETWORKS: %w", err)
	}
	nat, err := c.natSettings()
	if err != nil {
		return err
	}
	ws, err := c.sipwsNetwork()
	if err != nil {
		return fmt.Errorf("LINX_SIPWS_HOST %q: %w", c.SIPWSHost, err)
	}
	ice, err := c.iceSettings()
	if err != nil {
		return err
	}

	files := map[string]string{
		"asterisk.conf":         c.asteriskConf(),
		"logger.conf":           loggerConf,
		"modules.conf":          modulesConf,
		"manager.conf":          managerConf,
		"http.conf":             c.httpConf(ws),
		"ari.conf":              ariConf(rand.Text()),
		"websocket_client.conf": c.websocketClientConf(ariPassword),
		"extensions.conf":       extensionsConf,
		"func_odbc.conf":        funcOdbcConf,
		"pjsip.conf":            c.pjsipConf(nets, nat, ws),
		"rtp.conf":              rtpConf(ice),
		"sorcery.conf":          sorceryConf,
		"extconfig.conf":        extconfigConf,
		"odbcinst.ini":          odbcinstIni,
		"odbc.ini":              c.odbcIni(),
		"res_odbc.conf":         c.resOdbcConf(dbPassword),
		"openssl.cnf":           opensslConf,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(c.ConfDir, name), []byte(content), 0o640); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	if _, err := c.WriteTrunkCA(); err != nil {
		return fmt.Errorf("trunk CA list: %w", err)
	}
	return nil
}

// readSecret reads a Docker secret for use as an Asterisk config value.
// Asterisk's config syntax has no quoting: ";" starts a comment and a line
// break ends the value, so a secret containing either would be silently
// truncated (or worse, inject config). The installer's secrets never do.
func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" || strings.ContainsAny(s, ";\r\n") {
		return "", fmt.Errorf("%s: empty, or contains characters Asterisk config can't hold", path)
	}
	return s, nil
}

func (c Config) asteriskConf() string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint at container start. Do not edit by
; hand: the next restart overwrites this file.

[directories]
astetcdir => %s
astmoddir => /usr/lib/asterisk/modules
astvarlibdir => %s
astdbdir => %s
astkeydir => %s
astdatadir => %s
astagidir => %s
astspooldir => %s
astrundir => %s
astlogdir => %s

[options]
verbose = 3
console = no
`, c.ConfDir, c.VarDir, c.VarDir, c.VarDir, c.DataDir, filepath.Join(c.DataDir, "agi-bin"),
		filepath.Join(c.ScratchDir, "spool"), c.RunDir, filepath.Join(c.ScratchDir, "log"))
}

// ParseSIPNetworks reads LINX_SIP_NETWORKS: comma-separated network
// prefixes, or "none". Each must be a network address ("192.168.1.0/24",
// not "192.168.1.7/24"), so what's configured is exactly what's allowed.
func ParseSIPNetworks(s string) ([]netip.Prefix, error) {
	switch s = strings.TrimSpace(s); s {
	case "":
		return nil, errors.New(`not set (a list of networks, or "none")`)
	case "none":
		return nil, nil
	}
	var nets []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		p, err := netip.ParsePrefix(strings.TrimSpace(f))
		if err != nil {
			return nil, err
		}
		if p != p.Masked() {
			return nil, fmt.Errorf("%s isn't a network address (did you mean %s?)", p, p.Masked())
		}
		nets = append(nets, p)
	}
	return nets, nil
}

// FormatSIPNetworks is ParseSIPNetworks' inverse.
func FormatSIPNetworks(nets []netip.Prefix) string {
	if len(nets) == 0 {
		return "none"
	}
	s := make([]string, len(nets))
	for i, p := range nets {
		s[i] = p.String()
	}
	return strings.Join(s, ",")
}

// natSettings are the TLS transport's address-rewriting options. Phones
// reach Asterisk through the host's published ports (Docker DNAT keeps
// their source address), so Asterisk must advertise the host's address in
// Contact headers and audio offers, not its container address. Its own
// container networks are local_net: towards them nothing is rewritten.
func (c Config) natSettings() (string, error) {
	if c.SIPAddress == "" {
		return "", nil
	}
	a, err := netip.ParseAddr(c.SIPAddress)
	if err != nil {
		return "", fmt.Errorf("LINX_SIP_ADDRESS: %w", err)
	}
	if a.IsLoopback() || a.IsUnspecified() {
		return "", nil // no LAN: no phone can connect, nothing to advertise
	}
	addrs := c.InterfaceAddrs
	if addrs == nil {
		addrs = net.InterfaceAddrs
	}
	list, err := addrs()
	if err != nil {
		return "", fmt.Errorf("container addresses: %w", err)
	}
	s := fmt.Sprintf("external_media_address=%s\nexternal_signaling_address=%s\n", a, a)
	seen := map[netip.Prefix]bool{}
	for _, ad := range list {
		n, ok := ad.(*net.IPNet)
		if !ok {
			continue
		}
		ip, _ := netip.AddrFromSlice(n.IP)
		ones, _ := n.Mask.Size()
		p := netip.PrefixFrom(ip.Unmap(), ones).Masked()
		if !p.Addr().Is4() || p.Addr().IsLoopback() || seen[p] {
			continue
		}
		seen[p] = true
		s += "local_net=" + p.String() + "\n"
	}
	return s, nil
}

// sipwsNetwork is the linx-sipws network: Asterisk's own address on it
// (SIPWSHost's), with its prefix. The zero Prefix means the websocket is off.
func (c Config) sipwsNetwork() (netip.Prefix, error) {
	if c.SIPWSHost == "none" {
		return netip.Prefix{}, nil
	}
	return c.ownNetwork(c.SIPWSHost)
}

// ownNetwork is the container's address, with its prefix, that host (an
// alias Docker gives this container on one network) resolves to.
func (c Config) ownNetwork(host string) (netip.Prefix, error) {
	lookup := c.LookupHost
	if lookup == nil {
		lookup = lookupWithRetry
	}
	ips, err := lookup(host)
	if err != nil {
		return netip.Prefix{}, err
	}
	addrs := c.InterfaceAddrs
	if addrs == nil {
		addrs = net.InterfaceAddrs
	}
	list, err := addrs()
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("container addresses: %w", err)
	}
	for _, a := range list {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, _ := netip.AddrFromSlice(n.IP)
		ones, _ := n.Mask.Size()
		for _, want := range ips {
			if ip.Unmap() == want.Unmap() {
				return netip.PrefixFrom(ip.Unmap(), ones), nil
			}
		}
	}
	return netip.Prefix{}, fmt.Errorf("resolves to %v, which isn't one of this container's addresses", ips)
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

// opensslConf is OpenSSL's configuration for every TLS connection Asterisk
// makes or accepts (phones on 5061, the ARI websocket to the control plane):
// TLS 1.2 at least. The transport's method=sslv23 means "whatever OpenSSL
// allows", so this file is what sets the floor. PJSIP's own tlsv1_2 method
// would allow exactly TLS 1.2 and refuse 1.3 (checked against the image),
// and PJSIP has no minimum-version setting.
const opensslConf = `# Rendered by linx-asterisk-entrypoint.
openssl_conf = openssl_init

[openssl_init]
ssl_conf = ssl_sect

[ssl_sect]
system_default = system_default_sect

[system_default_sect]
MinProtocol = TLSv1.2
CipherString = DEFAULT:@SECLEVEL=2
`

// pjsipConf is the TLS transport, the browsers' secure websocket transport
// (when ws is valid), and the ACL every incoming SIP request passes
// (res_pjsip_acl, before authentication): the allowed networks and
// linx-sipws (where only the control plane's relay connects from), and
// nothing else. No networks means every phone request is refused.
// user_agent replaces Asterisk's default, which names its exact version in
// every response.
func (c Config) pjsipConf(nets []netip.Prefix, nat string, ws netip.Prefix) string {
	acl := "deny=0.0.0.0/0.0.0.0\ndeny=::/0\n"
	for _, p := range nets {
		acl += "permit=" + p.String() + "\n"
	}
	wss := ""
	if ws.IsValid() {
		acl += "permit=" + ws.Masked().String() + "\n"
		// The websocket itself is http.conf's TLS listener, on linx-sipws
		// only: a websocket transport binds nothing. Its bind still
		// matters, though: Asterisk binds a call's audio to its transport's
		// address. It must be "any", so audio to the relay leaves from
		// Asterisk's linx-media address (coturn accepts nothing else) and
		// audio to a browser at home from its published LAN side; pinned to
		// the linx-sipws address, every reply went out with that address
		// and the relay dropped it. The port keeps "pjsip show transports"
		// (and linx doctor) showing 8089.
		wss = "\n[transport-wss]\ntype=transport\nprotocol=wss\nbind=0.0.0.0:" + strconv.Itoa(SIPWSPort) + "\n"
	}
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint. Phones' endpoints, AORs and auths
; come from the asterisk schema's realtime views over ODBC (docs/PBX.md §3;
; sorcery.conf, extconfig.conf, res_odbc.conf); trunks from the control
; plane's %[7]s, included at the end (docs/TRUNKS.md §4).

[global]
type=global
user_agent=Linx

[%[8]s]
type=transport
protocol=tls
bind=0.0.0.0:%[1]d
cert_file=%[2]s
priv_key_file=%[3]s
; TLS 1.2 and 1.3; the floor is openssl.cnf's MinProtocol.
method=sslv23
; Trunks over TLS connect out through this transport too (docs/TRUNKS.md
; §6, ADR-045): the provider's certificate must come from a public CA or
; one the admin pinned, and name the address Linx dialled. Phones only
; ever connect in, which these don't affect.
ca_list_file=%[9]s
verify_server=yes
%[4]s%[5]s
; Address rewriting for the trunk file's plain transports (ADR-023).
[%[10]s](!)
type=transport
%[4]s
[%[11]s]
type=acl
%[6]s
#tryinclude "%[12]s"
`, c.SIPPort, filepath.Join(c.CertsDir, "current", "fullchain.pem"), filepath.Join(c.CertsDir, "current", "privkey.pem"),
		nat, wss, acl, TrunksFile, TLSTransport, c.TrunkCAPath(), TrunkTransportTemplate, ACLName, filepath.Join(c.TrunksDir, TrunksFile))
}

// iceSettings are the addresses Asterisk offers browsers for audio (ICE
// host candidates, docs/WEB.md §2): its linx-media address, which only
// coturn reaches (browsers away from home, through the relay), and the
// LAN address the audio ports are published on (browsers at home, direct),
// which stands in for its address on the network Docker publishes them
// through. Every other address it has (linx-private, linx-sipws, ...) is
// left out: no browser could reach it, and offering it only tells the
// world how the server is laid out. No STUN or TURN of its own either:
// Asterisk never needs to find its public address.
func (c Config) iceSettings() (string, error) {
	var permit []netip.Addr
	var mapping string
	if c.MediaHost != "none" {
		media, err := c.ownNetwork(c.MediaHost)
		if err != nil {
			return "", fmt.Errorf("LINX_MEDIA_HOST %q: %w", c.MediaHost, err)
		}
		permit = append(permit, media.Addr())
	}
	if lan, err := netip.ParseAddr(c.SIPAddress); err == nil && !lan.IsLoopback() && !lan.IsUnspecified() {
		route := c.DefaultRouteAddr
		if route == nil {
			route = defaultRouteAddr
		}
		local, err := route()
		if err != nil {
			return "", fmt.Errorf("finding the address the audio ports are published through: %w", err)
		}
		permit = append(permit, lan)
		mapping = fmt.Sprintf("%s => %s\n", local, lan)
	}
	s := "icesupport=yes\n; Only the addresses below are offered to browsers.\nice_deny=0.0.0.0/0\nice_deny=::/0\n"
	for _, a := range permit {
		s += "ice_permit=" + netip.PrefixFrom(a, a.BitLen()).String() + "\n"
	}
	if mapping != "" {
		s += "\n[ice_host_candidates]\n" + mapping
	}
	return s, nil
}

// defaultRouteAddr reads the default route's interface from
// /proc/net/route and returns that interface's IPv4 address.
func defaultRouteAddr() (netip.Addr, error) {
	b, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return netip.Addr{}, err
	}
	name, err := DefaultRouteInterface(string(b))
	if err != nil {
		return netip.Addr{}, err
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return netip.Addr{}, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return netip.Addr{}, err
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap().Is4() {
				return ip.Unmap(), nil
			}
		}
	}
	return netip.Addr{}, fmt.Errorf("interface %s has no IPv4 address", name)
}

// DefaultRouteInterface returns the interface of the default route in a
// /proc/net/route file.
func DefaultRouteInterface(procNetRoute string) (string, error) {
	for _, line := range strings.Split(procNetRoute, "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 8 && f[1] == "00000000" && f[7] == "00000000" {
			return f[0], nil
		}
	}
	return "", errors.New("no default route")
}

// rtpConf pins audio to the port range compose.yaml publishes, and sets
// which addresses browsers are offered (ice, from iceSettings). strictrtp
// only accepts audio from the address a call's audio actually comes from.
func rtpConf(ice string) string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[general]
rtpstart=%d
rtpend=%d
strictrtp=yes
%s`, RTPStart, RTPEnd, ice)
}

// sorceryConf looks PJSIP's endpoint/auth/aor objects up in pjsip.conf
// first (trunks, from the included pjsip_trunks.conf, ADR-043), then in the
// realtime engine (phones and browser lines, docs/PBX.md §3, ADR-032).
// "ps_endpoints" etc. are realtime family names, resolved to the odbc DSN
// by extconfig.conf. Trunk names ("trunk-<uuid>") and device usernames
// ("d_…") never overlap.
const sorceryConf = `; Rendered by linx-asterisk-entrypoint.
[res_pjsip]
endpoint=config,pjsip.conf,criteria=type=endpoint
endpoint=realtime,ps_endpoints
auth=config,pjsip.conf,criteria=type=auth
auth=realtime,ps_auths
aor=config,pjsip.conf,criteria=type=aor
aor=realtime,ps_aors
`

// extconfigConf maps each realtime family sorcery.conf asks for to the
// "asterisk" ODBC DSN (res_odbc.conf) and the asterisk-schema view of the
// same name (migration 0005).
const extconfigConf = `; Rendered by linx-asterisk-entrypoint.
[settings]
ps_endpoints => odbc,asterisk,ps_endpoints
ps_auths => odbc,asterisk,ps_auths
ps_aors => odbc,asterisk,ps_aors
`

// odbcinstIni declares the PostgreSQL ODBC driver at the fixed path
// deploy/docker/asterisk.Dockerfile symlinks it to, so this file doesn't
// need to know the runtime image's architecture-specific library path.
const odbcinstIni = `; Rendered by linx-asterisk-entrypoint.
[PostgreSQL]
Description=PostgreSQL ODBC driver
Driver=` + odbcDriverPath + `
`

// odbcIni defines the "asterisk" DSN res_odbc.conf connects through: this
// server's Postgres, read-only, and scoped to the asterisk schema so an
// unqualified "SELECT * FROM ps_endpoints" (what res_config_odbc sends)
// finds the realtime views without Asterisk needing to know the schema
// name.
func (c Config) odbcIni() string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[asterisk]
Description=Linx phone system realtime data
Driver=PostgreSQL
Servername=%s
Port=%s
Database=%s
ReadOnly=Yes
ConnSettings=SET search_path TO asterisk
`, c.DBHost, c.DBPort, c.DBName)
}

// resOdbcConf is Asterisk's own side of the ODBC connection: the
// linx_asterisk role's credentials (its password is a Docker secret the
// control plane also reads, to keep this role's actual password in sync;
// docs/PBX.md §3) and pooling. No cache (ADR-032): every registration and
// call re-reads the database, so a revoked device stops at once.
func (c Config) resOdbcConf(dbPassword string) string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[asterisk]
dsn=asterisk
username=linx_asterisk
password=%s
max_connections=5
pre-connect=yes
sanitysql=SELECT 1
`, dbPassword)
}

// loggerConf sends every log line to the container's stdout: the read-only
// root filesystem has no writable log directory, and `docker logs` is where
// Linx expects every service's logs (control-plane and certd use slog to
// stdout the same way).
const loggerConf = `[general]
; Queues aren't built into this image yet (docs/PBX.md §2), so there's
; nothing to log.
queue_log = no

[logfiles]
/dev/stdout => notice,warning,error,verbose
`

// modulesConf loads exactly the modules the build's menuselect step chose
// (deploy/docker/asterisk.Dockerfile): autoload can't load anything else.
const modulesConf = `[modules]
autoload=yes
`

// managerConf keeps the Manager Interface off: no AMI, over the network or
// otherwise (docs/PBX.md §2, ADR-031). ARI is the integration point.
const managerConf = `[general]
enabled = no
`

// httpConf is Asterisk's built-in web server: never plain HTTP, and HTTPS
// only for the browsers' SIP websocket, on Asterisk's linx-sipws address
// (docs/WEB.md §2). ARI doesn't use it: ARI runs over the websocket Asterisk
// opens to the control plane, REST requests included. The certificate is
// the control plane's step-ca one for SIPWSHost; when it changes, the
// entrypoint's watcher reloads this module, which re-reads it. Until the
// first one arrives the HTTPS listener doesn't start. servername replaces
// "Asterisk/<version>" in the Server header. The ciphers only matter for
// TLS 1.2 (the floor is openssl.cnf's).
func (c Config) httpConf(ws netip.Prefix) string {
	if !ws.IsValid() {
		return "; Rendered by linx-asterisk-entrypoint.\n[general]\nenabled=no\n"
	}
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[general]
enabled=no
servername=Linx
sessionlimit=1000
tlsenable=yes
tlsbindaddr=%s
tlscertfile=%s
tlsprivatekey=%s
tlscipher=ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-RSA-CHACHA20-POLY1305
`, netip.AddrPortFrom(ws.Addr(), SIPWSPort), filepath.Join(c.SIPWSCertsDir, "current", "fullchain.pem"),
		filepath.Join(c.SIPWSCertsDir, "current", "privkey.pem"))
}

// ariConf serves the "linx" ARI app over the outbound websocket to the
// control plane, subscribed to every channel, bridge, endpoint and device
// state event (docs/PBX.md §4). REST requests arriving on that websocket act
// as a read-only local user: the control plane can look but not change calls
// in this slice. The local user's password is random per start and unused
// (http.conf is off, so there's nowhere to log in with it), but ari.conf
// requires one.
func ariConf(localPassword string) string {
	return `; Rendered by linx-asterisk-entrypoint.
[general]
enabled = yes
pretty = no
websocket_write_timeout = 1000

[linx-local]
type = user
read_only = yes
password_format = plain
password = ` + localPassword + `

[linx]
type = outbound_websocket
websocket_client_id = linx-control-plane
apps = ` + ARIApp + `
subscribe_all = yes
local_ari_user = linx-local
`
}

// websocketClientConf is the outbound connection itself: TLS only, the
// control plane's certificate checked against the internal CA's root and its
// hostname, and a password proving to the control plane that this is
// Asterisk. Persistent connections retry forever on their own.
func (c Config) websocketClientConf(password string) string {
	return fmt.Sprintf(`; Rendered by linx-asterisk-entrypoint.
[linx-control-plane]
type = websocket_client
uri = %s
protocols = ari
username = %s
password = %s
connection_type = persistent
connection_timeout = 3000
reconnect_interval = 2000
reconnect_attempts = 30
tls_enabled = yes
ca_list_file = %s
verify_server_cert = yes
verify_server_hostname = yes
`, c.ARIURL, ARIUser, password, c.CARootFile)
}

// funcOdbcConf defines the dialplan's database lookups, all through the
// asterisk schema (the linx_asterisk role can read nothing else):
//
// LINX_RING_TARGETS(number): a Dial() string ringing every enabled device
// of that extension ("PJSIP/d_a&PJSIP/d_b"), read from the
// linx_ring_targets view. No row at all means no such extension; one row
// with an empty string means the extension exists but has no devices. The
// dialplan tells the two apart with ${ODBCROWS}. Device usernames match
// ^d_[A-Za-z0-9]{8}$ (migration 0005), so nothing from the database can
// smuggle dial options into the string.
//
// LINX_OUTBOUND(endpoint,number): the outgoing-call decision (migration
// 0018's linx_outbound): reason, category, withhold (1/0), lines.
//
// LINX_INBOUND(endpoint,number): the extension a call from a trunk to one
// of its DIDs rings; no row if that trunk doesn't own the number.
const funcOdbcConf = `; Rendered by linx-asterisk-entrypoint.
[RING_TARGETS]
prefix = LINX
dsn = asterisk
readsql = SELECT coalesce(string_agg('PJSIP/' || aor, '&' ORDER BY aor), '') FROM linx_ring_targets WHERE number = '${SQL_ESC(${ARG1})}' HAVING count(*) > 0

[OUTBOUND]
prefix = LINX
dsn = asterisk
readsql = SELECT reason, category, CASE WHEN withhold THEN 1 ELSE 0 END, lines FROM linx_outbound('${SQL_ESC(${ARG1})}', '${SQL_ESC(${ARG2})}')

[INBOUND]
prefix = LINX
dsn = asterisk
readsql = SELECT * FROM linx_inbound('${SQL_ESC(${ARG1})}', '${SQL_ESC(${ARG2})}')
`

// extensionsConf is the whole dialplan (docs/PBX.md §4, docs/TRUNKS.md
// §4-§5, §9).
//
// linx-extensions is where phones and browser lines call from: *43 echo
// test, ring-all for extension numbers (2–6 digits, like the extension
// table's check), and everything else out through linx-outbound. A
// device's caller ID number is its extension (the ps_endpoints view sets
// it, and PJSIP ignores what the phone claims), so "calling yourself" is a
// plain comparison.
//
// linx-outbound asks the database (LINX_OUTBOUND) whether the caller may
// call the number and on which lines, then tries each line in turn: the
// next one only when a line is down or full (DIALSTATUS CHANUNAVAIL or
// CONGESTION, the latter e.g. a 503), never after the called person
// answered, was busy or refused. Every limit (2 outside calls at once per
// extension, each trunk's own call limit) skips emergency calls.
//
// linx-from-trunk is where trunks' calls arrive (their endpoints' context):
// it can reach that trunk's own DIDs and nothing else, so a call from a
// trunk can never go back out (ADR-048): nothing it can reach (linx-ring,
// linx-messages) leads to linx-outbound, which TestDialplanTrunkCalls
// checks. The number is the request's (IP
// peers) or else the To header's (providers that call the registered
// contact). The caller's name and number are untrusted: filtered to plain
// characters and shortened before anything else sees them. The call
// passes through linx-trunk-did with the DID that matched as its extension,
// which is how the control plane's call events learn it.
const extensionsConf = `; Rendered by linx-asterisk-entrypoint.
[general]
static = yes
writeprotect = yes
clearglobalvars = yes

[globals]

[linx-extensions]
exten => *43,1,Answer()
 same => n,Wait(0.5)
 same => n,Echo()
 same => n,Hangup()

exten => _XX,1,Goto(linx-local,${EXTEN},1)
exten => _XXX,1,Goto(linx-local,${EXTEN},1)
exten => _XXXX,1,Goto(linx-local,${EXTEN},1)
exten => _XXXXX,1,Goto(linx-local,${EXTEN},1)
exten => _XXXXXX,1,Goto(linx-local,${EXTEN},1)

; Anything else a phone can dial: an outside number, or nothing at all.
exten => _[0-9*#+].,1,Goto(linx-outbound,${EXTEN},1)
exten => _[0-9*#+],1,Goto(linx-outbound,${EXTEN},1)

; A short number from a phone: an extension, or else maybe an outside one
; (999 is 3 digits; extensions can't take such numbers, migration 0016).
[linx-local]
exten => _X.,1,Set(TARGETS=${LINX_RING_TARGETS(${EXTEN})})
 same => n,GotoIf($[${ODBCROWS} < 1]?linx-outbound,${EXTEN},1)
 same => n,GotoIf($["${CALLERID(num)}" = "${EXTEN}"]?linx-messages,not-available,1)
 same => n,Goto(linx-ring,${EXTEN},1)

; Rings an extension, for phones (through linx-local) and for trunks' calls
; to a DID (linx-trunk-call).
[linx-ring]
exten => _X.,1,Set(TARGETS=${LINX_RING_TARGETS(${EXTEN})})
 same => n,GotoIf($[${ODBCROWS} < 1]?linx-messages,not-in-use,1)
 same => n,GotoIf($["${TARGETS}" = ""]?linx-messages,not-available,1)
 same => n,Dial(${TARGETS},30)
 same => n,GotoIf($["${DIALSTATUS}" = "ANSWER"]?done)
 same => n,Goto(linx-messages,not-available,1)
 same => n(done),Hangup()

[linx-outbound]
exten => _[0-9*#+].,1,Set(ARRAY(REASON,CATEGORY,WITHHOLD,LINES)=${LINX_OUTBOUND(${CHANNEL(endpoint)},${EXTEN})})
 same => n,GotoIf($[${ODBCROWS} < 1]?linx-messages,no-lines,1)
 same => n,GotoIf($["${REASON}" = "invalid"]?linx-messages,not-in-use,1)
 same => n,GotoIf($["${REASON}" = "no_lines"]?linx-messages,no-lines,1)
 same => n,GotoIf($["${REASON}" != "allowed" & "${REASON}" != "emergency"]?linx-messages,not-permitted,1)
 same => n,GotoIf($["${CATEGORY}" = "emergency"]?lines)
 same => n,Set(GROUP(linx-out)=${CALLERID(num)})
 same => n,GotoIf($[${GROUP_COUNT(${CALLERID(num)}@linx-out)} > 2]?linx-messages,limit,1)
 same => n(lines),Set(I=0)
 same => n(next),Set(I=$[${I} + 1])
 same => n,Set(LINE=${CUT(LINES,&,${I})})
 same => n,GotoIf($["${LINE}" = ""]?linx-messages,no-lines,1)
 same => n,Set(TRUNK=${CUT(LINE,/,1)})
 same => n,GotoIf($["${CATEGORY}" = "emergency"]?dial)
 same => n,GotoIf($[${GROUP_COUNT(${TRUNK}@linx-trunk)} >= ${CUT(LINE,/,4)}]?next)
 same => n(dial),Set(GROUP(linx-trunk)=${TRUNK})
 same => n,Dial(PJSIP/${CUT(LINE,/,2)}@${TRUNK},120,b(linx-trunk-out^s^1(${CUT(LINE,/,3)},${WITHHOLD})))
 same => n,GotoIf($["${DIALSTATUS}" = "CHANUNAVAIL" | "${DIALSTATUS}" = "CONGESTION"]?next)
 same => n,Hangup()

exten => _[0-9*#+],1,Goto(linx-messages,not-in-use,1)

; On the outgoing channel, before it dials: the caller ID the called person
; sees (the caller's DID on that trunk, or the trunk's main number), and
; withheld if the caller's level says so. PJSIP sends an outgoing channel's
; connected line as From and P-Asserted-Identity; "i" keeps the change from
; being announced back to the caller's phone.
[linx-trunk-out]
exten => s,1,GotoIf($["${ARG1}" = ""]?withhold)
 same => n,Set(CONNECTEDLINE(num,i)=${ARG1})
 same => n(withhold),GotoIf($["${ARG2}" != "1"]?done)
 same => n,Set(CONNECTEDLINE(pres,i)=prohib)
 same => n(done),Return()

[linx-from-trunk]
exten => _[a-zA-Z0-9+*#].,1,Set(DIALLED=${EXTEN})
 same => n,Goto(linx-trunk-call,s,1)
exten => _[a-zA-Z0-9+*#],1,Set(DIALLED=${EXTEN})
 same => n,Goto(linx-trunk-call,s,1)

[linx-trunk-call]
exten => s,1,Set(CALLERID(name)=${FILTER(A-Za-z0-9 .,${CALLERID(name)}):0:40})
 same => n,Set(CALLERID(num)=${FILTER(0-9+,${CALLERID(num)}):0:20})
 same => n,Set(GROUP(linx-trunk)=${CHANNEL(endpoint)})
 same => n,Set(DID=${DIALLED})
 same => n,Set(TARGET=${LINX_INBOUND(${CHANNEL(endpoint)},${DID})})
 same => n,GotoIf($[${ODBCROWS} > 0]?found)
 same => n,Set(DID=${FILTER(0-9+,${PJSIP_PARSE_URI(${CHANNEL(pjsip,local_uri)},user)})})
 same => n,Set(TARGET=${LINX_INBOUND(${CHANNEL(endpoint)},${DID})})
 same => n,GotoIf($[${ODBCROWS} > 0]?found:linx-messages,not-in-use,1)
 same => n(found),GotoIf($["${TARGET}" = ""]?linx-messages,not-in-use,1)
 same => n,Set(DID=${FILTER(0-9+,${DID})})
 same => n,GotoIf($["${DID}" = ""]?linx-ring,${TARGET},1)
 same => n,Goto(linx-trunk-did,${DID},1)

; Only so the control plane's call events can tell which number was
; dialled: the channel passes through this context with the DID as its
; extension, then rings the DID's extension.
[linx-trunk-did]
exten => _[0-9+].,1,Goto(linx-ring,${TARGET},1)

[linx-messages]
exten => not-in-use,1,Answer()
 same => n,Wait(0.5)
 same => n,Playback(ss-noservice)
 same => n,Hangup()

exten => not-available,1,Answer()
 same => n,Wait(0.5)
 same => n,Playback(vm-nobodyavail)
 same => n,Hangup()

; The caller's permission level doesn't include this kind of number.
exten => not-permitted,1,Answer()
 same => n,Wait(0.5)
 same => n,Playback(im-sorry&feature-not-avail-line)
 same => n,Hangup()

; No line is set up, or every line is down or full.
exten => no-lines,1,Answer()
 same => n,Wait(0.5)
 same => n,Playback(all-circuits-busy-now&please-try-call-later)
 same => n,Hangup()

; This extension already has 2 outside calls.
exten => limit,1,Answer()
 same => n,Wait(0.5)
 same => n,Playback(simul-call-limit-reached)
 same => n,Hangup()
`
