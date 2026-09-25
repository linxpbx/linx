package doctor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/installer"
	"linxpbx.com/linx/internal/turn"
)

const sniContainer = installer.SNIContainerName

// FrontDoor checks the way in from the internet (docs/WEB.md §3), as far as
// this server can see it: each public name reaches Linx through the front
// door (with Linx's own certificate where it passes TLS through; for an
// HTTP-only proxy, Linx's health answer through the proxy's own trusted
// certificate), the relay works over TLS through it, coturn answers on UDP,
// the names are in DNS, and only the front door may reach the web port. The
// router's forwards themselves can't be seen from inside.
func FrontDoor(ctx context.Context, env Env, cfg installer.Config) []Result {
	var rs results
	lan := env.LAN()
	fd := cfg.FrontDoor
	settings := installer.FrontDoorFor(cfg, lan)
	var entry netip.Addr
	var door string
	switch fd.Kind {
	case installer.FrontDoorPangolin, installer.FrontDoorNginx, installer.FrontDoorHTTPProxy:
		entry, _ = netip.ParseAddr(fd.ProxyAddress)
		door = map[string]string{installer.FrontDoorPangolin: "Pangolin", installer.FrontDoorNginx: "nginx/HAProxy",
			installer.FrontDoorHTTPProxy: "your proxy"}[fd.Kind] + " (" + fd.ProxyAddress + ")"
	case installer.FrontDoorLinx443, installer.FrontDoorHomeOnly:
		service(ctx, env, &rs, sniContainer, "Linx's port 443 router")
		entry = settings.SNIAddress
		if entry.IsUnspecified() {
			entry = netip.AddrFrom4([4]byte{127, 0, 0, 1})
		}
		door = "Linx's own port 443"
	default:
		rs.warn("Linx has no web address and calls from outside your home aren't set up (no front door chosen).",
			"When you want them, run: sudo linx setup, and pick what sits in front of Linx.")
		return rs
	}

	leaf, roots := deployedLeaf(ctx, env)
	if leaf == nil {
		return rs // reported under Certificates
	}
	fix := frontDoorFix(fd.Kind)
	addr := netip.AddrPortFrom(entry, installer.PublicPort).String()
	web := installer.PublicHosts
	turnAddr := addr
	if fd.Kind == installer.FrontDoorHTTPProxy {
		// The proxy decrypts the web names with its own certificate: ask
		// Linx's health through it instead. The relay has its own port.
		web = []string{"meet", "api"}
		for _, h := range web {
			name := h + "." + cfg.Domain.Name
			status, body, err := env.HTTPSGet(ctx, addr, name, "/healthz", nil)
			switch {
			case err != nil:
				rs.fail(fmt.Sprintf("%s doesn't reach Linx through %s (%v).", name, door, err), fix)
			case status != 200 || !strings.Contains(string(body), `"linx-control-plane"`):
				rs.fail(fmt.Sprintf("%s through %s doesn't answer as Linx (status %d).", name, door, status), fix)
			default:
				rs.ok(fmt.Sprintf("%s reaches Linx through %s.", name, door))
			}
		}
		web = []string{"turn"}
		turnAddr = netip.AddrPortFrom(settings.WebAddress, installer.TURNTLSPort).String()
	}
	for _, h := range web {
		name := h + "." + cfg.Domain.Name
		a := addr
		if h == "turn" {
			a = turnAddr
		}
		got, err := env.TLSLeaf(ctx, a, name, roots)
		switch {
		case err != nil:
			rs.fail(fmt.Sprintf("%s doesn't reach Linx through %s (%v).", name, door, err), fix)
		case !bytes.Equal(got.Raw, leaf.Raw):
			rs.fail(fmt.Sprintf("%s through %s answers with another certificate, not Linx's: it isn't passed through to Linx.", name, door), fix)
		default:
			rs.ok(fmt.Sprintf("%s reaches Linx through %s, with Linx's own certificate.", name, door))
		}
	}
	relayOverTLS(ctx, env, &rs, turnAddr, "turn."+cfg.Domain.Name, roots, door, fix)
	if fd.Kind != installer.FrontDoorHomeOnly {
		relayUDP(ctx, env, &rs, settings, fd.Kind)
	}
	publicDNS(ctx, env, &rs, cfg, settings.DNSAddress)
	if installer.NeedsProxyAddress(fd.Kind) {
		frontDoorFirewall(ctx, env, &rs, settings.WebClients, door)
	}
	return rs
}

func frontDoorFix(kind string) string {
	if f := installer.StepsFile(kind); f != "" {
		return "Check it has Linx's settings: " + f + ". Then run linx doctor again."
	}
	return "Look at the port 443 router's log: sudo docker logs --tail 50 " + sniContainer + ". If that doesn't help: " + rerunSetup
}

// deployedLeaf is linx-certd's current certificate and the roots it
// verifies against (Let's Encrypt's test roots for a test certificate).
func deployedLeaf(ctx context.Context, env Env) (*x509.Certificate, *x509.CertPool) {
	pemData, err := copyFromContainer(ctx, env.Runner, certdContainer, fullchainPath)
	if err != nil {
		return nil, nil
	}
	chain, err := parseCerts(pemData)
	if err != nil {
		return nil, nil
	}
	if isStaging(chain[0]) {
		return chain[0], env.StagingRoots
	}
	return chain[0], env.Roots
}

// relayOverTLS allocates a relay address over TLS on 443 through the front
// door, as a browser on a network that allows only web traffic does.
func relayOverTLS(ctx context.Context, env Env, rs *results, addr, name string, roots *x509.CertPool, door, fix string) {
	secret, err := env.ReadFile(installer.TURNSecretPath)
	if err != nil {
		return // reported under Secrets
	}
	user := strconv.FormatInt(env.Now().Add(5*time.Minute).Unix(), 10) + ":" + uuid.Nil.String()
	err = env.TURNOverTLS(ctx, addr, name, roots, user, turn.Password([]byte(strings.TrimSpace(string(secret))), user))
	if err != nil {
		rs.fail(fmt.Sprintf("The call relay doesn't work over TLS through %s (%v): calls from networks that allow only web traffic would have no audio.", door, err),
			fix+" If the name is reached but relaying fails: "+relayLogs)
		return
	}
	rs.ok("The call relay works over TLS on port 443 through " + door + " (for networks that allow only web traffic).")
}

// relayUDP checks coturn answers where the router's UDP forward lands.
func relayUDP(ctx context.Context, env Env, rs *results, s installer.FrontDoorSettings, kind string) {
	a := s.TURNUDPAddress
	if a.IsUnspecified() {
		a = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	}
	addr := netip.AddrPortFrom(a, uint16(s.TURNUDPPort)).String()
	if err := env.STUNPing(ctx, addr); err != nil {
		rs.fail("The call relay doesn't answer on UDP port "+strconv.Itoa(s.TURNUDPPort)+" here ("+err.Error()+").", relayLogs)
		return
	}
	who := "your router's"
	if kind == installer.FrontDoorLinx443 && !a.IsPrivate() && !a.IsLoopback() {
		who = "the"
	}
	rs.ok(fmt.Sprintf("The call relay answers on UDP port %d (%s). Calls get their smoothest audio once %s forward of UDP %d lands here; Linx can't see the router from inside.",
		s.TURNUDPPort, a, who, s.TURNUDPPort))
}

// publicDNS checks the public names resolve to one address: a public one,
// or home (home-only).
func publicDNS(ctx context.Context, env Env, rs *results, cfg installer.Config, home string) {
	var addrs []netip.Addr
	var missing []string
	for _, h := range installer.PublicHosts {
		name := h + "." + cfg.Domain.Name
		as, err := env.LookupIP(ctx, name)
		if err != nil || len(as) == 0 {
			missing = append(missing, name)
			continue
		}
		for _, a := range as {
			if !slices.Contains(addrs, a) {
				addrs = append(addrs, a)
			}
		}
	}
	records := "Point them at your public address: sudo docker compose --file " + installer.StackDir + "/compose.yaml run --rm certd -records " +
		strings.Join(installer.PublicHosts, ",")
	if home != "" {
		records = strings.Replace(records, "your public address", "this server", 1) + " -address " + home
	}
	switch {
	case len(missing) > 0:
		rs.fail("These names aren't in DNS yet: "+strings.Join(missing, ", ")+".", records+" (new records can take a few minutes to appear).")
	case len(addrs) > 1:
		rs.warn(fmt.Sprintf("The public names point at different addresses (%v).", addrs), records)
	case home != "" && addrs[0].String() != home:
		rs.fail(fmt.Sprintf("meet., api. and turn.%s point at %s, not this server (%s).", cfg.Domain.Name, addrs[0], home), records)
	case home != "":
		rs.ok(fmt.Sprintf("meet., api. and turn.%s point at this server (%s), for use at home.", cfg.Domain.Name, home))
	case addrs[0].IsPrivate() || addrs[0].IsLoopback():
		rs.warn(fmt.Sprintf("The public names point at %s, a home-network address: they work at home only.", addrs[0]), records)
	default:
		rs.ok(fmt.Sprintf("meet., api. and turn.%s point at %s.", cfg.Domain.Name, addrs[0]))
	}
}

// frontDoorFirewall checks only the front door may reach the web port.
func frontDoorFirewall(ctx context.Context, env Env, rs *results, want []netip.Addr, door string) {
	out, err := env.Runner.Run(ctx, nil, "nft", "-j", "list", "set", "inet", installer.FirewallTable, "front_door")
	loaded, err := phoneNetworksSet(out, err)
	var wantP []netip.Prefix
	for _, a := range want {
		wantP = append(wantP, netip.PrefixFrom(a, a.BitLen()))
	}
	if err != nil || !slices.Equal(loaded, wantP) {
		rs.fail("The firewall doesn't limit Linx's web port to "+door+".", rerunSetup)
		return
	}
	rs.ok("Only " + door + " can reach Linx's web port on this network.")
}

// TURNOverTLS is Env.TURNOverTLS on a real network: a TLS connection to
// addr for serverName (checked against roots), then a relay allocation.
func TURNOverTLS(ctx context.Context, addr, serverName string, roots *x509.CertPool, user, pass string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12}}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	}
	cl := &turn.Client{Conn: c, Stream: true, User: user, Pass: pass}
	return cl.Allocate()
}

// HTTPSGet is Env.HTTPSGet on a real network: GET path over TLS to addr
// for serverName, the certificate checked against roots (nil: the
// system's), and the status and first 64 KB of the body.
func HTTPSGet(ctx context.Context, addr, serverName, path string, roots *x509.CertPool) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := tls.Dialer{Config: &tls.Config{ServerName: serverName, RootCAs: roots, MinVersion: tls.VersionTLS12}}
	client := &http.Client{Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) },
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+serverName+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, b, nil
}

// STUNPing is Env.STUNPing on a real network.
func STUNPing(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return turn.Ping(ctx, addr)
}
