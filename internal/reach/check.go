// Package reach is "Check it" (docs/SIMPLER.md §2.3, docs/ui/SCREENS_PHASE1F.md
// §2): can people reach Linx from outside? From this server, in plain
// words: the names point here at the domain's own name servers, Linx's own
// certificate answers through the front door, and the call relay works
// through it. From outside, a one-time link opened on a phone with Wi-Fi
// off shows the address Linx saw (which also proves the front door tells
// Linx who's visiting) and tests the relay from there (Links).
//
// Everything runs in the control plane, on request only: nothing runs
// while nobody asks, and nothing needs the host.
package reach

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"linxpbx.com/linx/internal/turn"
)

// Line states.
const (
	OK   = "ok"
	Fail = "fail"
	Warn = "warn"
	Info = "info"
)

// Fixes a failed line points at.
const (
	FixRecords = "records" // the DNS records
	FixSteps   = "steps"   // the front door's steps (the front-door card)
	FixRouter  = "router"  // the router's forward
)

// Line is one check in plain words: what it found, what a failure means for
// people, and which fix helps.
type Line struct {
	State   string `json:"state"`
	Text    string `json:"text"`
	Meaning string `json:"meaning,omitempty"`
	Fix     string `json:"fix,omitempty"`
}

// Front doors, as the control plane's LINX_FRONT_DOOR names them.
const (
	doorLinx443   = "linx-443"
	doorHomeOnly  = "home-only"
	doorHTTPProxy = "http-proxy"
)

// port is the public port people use.
func (c *Checker) port() int {
	if c.Port == 0 {
		return 443
	}
	return c.Port
}

// Checker runs the checks from this server.
type Checker struct {
	Domain string
	// Door is LINX_FRONT_DOOR; Proxy is the front door's address for a
	// proxy (LINX_TRUSTED_PROXIES), "" for Linx's own port 443 router.
	Door, Proxy string
	// SNI is Linx's own port 443 router ("sni:443"), for linx-443 and
	// home-only.
	SNI string
	// Home is this server's home-network address (home-only's names
	// point there); may be unset elsewhere.
	Home netip.Addr
	// TURNTLS is the relay's own TLS port for a proxy that decrypts
	// ("192.168.1.212:5349"); the relay goes through the front door otherwise.
	TURNTLS string
	// Port is the public port people use (LINX_PUBLIC_PORT): 0 or 443,
	// or another one (public-port; linx-sni still answers on SNI inside).
	Port int

	// Leaf is the certificate Linx serves now, with its chain.
	Leaf func() (*tls.Certificate, error)
	// TURNSecret makes a short relay credential for the relay check.
	TURNSecret []byte
	// Lookup asks the domain's own name servers (dnscheck.Resolver.LookupA).
	Lookup func(ctx context.Context, name string) ([]string, error)
	// NameServers finds the domain's zone and name servers
	// (dnscheck.Resolver.NameServers).
	NameServers func(ctx context.Context, name string) (zone string, servers []string, err error)
	// Kept are the records Linx keeps right itself, by use (UseWeb…), from
	// LINX_DNS_RECORDS.
	Kept map[string]bool
	// Automatic, if set, asks linx-certd how it keeps them right (nil when
	// it doesn't, or didn't answer).
	Automatic func(ctx context.Context) *Automatic
	// PublicIP is this network's public address (publicip.Lookup).
	PublicIP func(ctx context.Context) (netip.Addr, error)
	// Dial connects (net.Dialer.DialContext; tests replace it).
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// SystemRoots checks a decrypting proxy's own certificate (nil: the system's).
	SystemRoots *x509.CertPool
	Now         func() time.Time
}

// Timeout bounds each network check.
const Timeout = 8 * time.Second

// Run runs every check, the independent ones at once, and returns them in
// the order the panel shows them.
func (c *Checker) Run(ctx context.Context) []Line {
	turnName := "turn." + c.Domain
	if c.Door == "" || c.Door == "none" {
		return []Line{{State: Warn, Text: "Nothing is set up to let people in from outside.",
			Meaning: "Linx works on this network only.", Fix: FixSteps}}
	}
	cert, err := c.Leaf()
	if err != nil || len(cert.Certificate) == 0 {
		return []Line{{State: Fail, Text: "Linx has no certificate yet.", Meaning: "Nobody can open Linx until it has one."}}
	}
	leaf, roots, err := chainOf(cert)
	if err != nil {
		return []Line{{State: Fail, Text: "Linx's certificate can't be read (" + err.Error() + ")."}}
	}

	var (
		wg                      sync.WaitGroup
		dnsLines                []Line
		public                  netip.Addr
		publicErr               error
		webLine, turnLine, hair Line
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		public, publicErr = c.PublicIP(ctx)
	}()
	go func() {
		defer wg.Done()
		webLine = c.web(ctx, leaf, roots)
	}()
	go func() {
		defer wg.Done()
		turnLine = c.relay(ctx, turnName, roots)
	}()
	wg.Wait()

	home := c.Door == doorHomeOnly
	want := public
	if home {
		want = c.Home
	}
	dnsLines = c.names(ctx, want, publicErr, home)

	lines := append(dnsLines, webLine, turnLine)
	if !home && webLine.State == OK && publicErr == nil {
		// Through the front door works: now the way people outside come
		// in, this network's public address. Many home routers can't reach
		// their own public address from inside ("hairpin"), so a failure
		// here only sends the owner to the phone.
		hair = c.hairpin(ctx, public, leaf, roots)
		lines = append(lines, hair)
	}
	if c.port() != 443 {
		// Never a failure: it works, with what it trades away
		// (docs/SIMPLER.md §2.5 items 5, 6 and 8).
		lines = append(lines, Line{State: Warn, Text: fmt.Sprintf("People open Linx at port %d, not the standard 443.", c.port()),
			Meaning: "Networks that only allow standard web traffic (some hotels, workplaces, public Wi-Fi) may block calls, " +
				"or even this page. A front door on port 443 avoids that.", Fix: FixSteps})
	}
	return lines
}

// chainOf is the serving certificate's leaf and a pool of the chain's
// issuers: a connection checked against it gets exactly Linx's chain (test
// certificates included), and a proxy's own certificate fails the check.
func chainOf(cert *tls.Certificate) (*x509.Certificate, *x509.CertPool, error) {
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	roots := x509.NewCertPool()
	for _, der := range cert.Certificate[1:] {
		if c, err := x509.ParseCertificate(der); err == nil {
			roots.AddCert(c)
		}
	}
	if len(cert.Certificate) == 1 {
		roots.AddCert(leaf)
	}
	return leaf, roots, nil
}

// names checks each public name at the domain's own name servers.
func (c *Checker) names(ctx context.Context, want netip.Addr, wantErr error, home bool) []Line {
	var lines []Line
	for _, name := range []string{c.Domain, "turn." + c.Domain} {
		cctx, cancel := context.WithTimeout(ctx, Timeout)
		got, err := c.Lookup(cctx, name)
		cancel()
		where := "your home's address"
		if home {
			where = "this server, at home"
		}
		switch {
		case err != nil:
			lines = append(lines, Line{State: Warn, Text: "Linx couldn't ask your DNS company about " + name + " just now.",
				Meaning: "Try again in a minute."})
		case len(got) == 0:
			lines = append(lines, Line{State: Fail, Text: name + " isn't in DNS yet.",
				Meaning: "Nobody outside can find Linx by that name.", Fix: FixRecords})
		case wantErr != nil:
			lines = append(lines, Line{State: Info, Text: name + " points to " + strings.Join(got, ", ") + ".",
				Meaning: "Linx couldn't find this network's public address to compare."})
		case len(got) == 1 && got[0] == want.String():
			lines = append(lines, Line{State: OK, Text: fmt.Sprintf("%s points to %s (%s)", name, got[0], where)})
		default:
			lines = append(lines, Line{State: Fail, Text: fmt.Sprintf("%s points to %s, not %s (%s).", name, strings.Join(got, ", "), want, where),
				Meaning: "People outside are sent somewhere else.", Fix: FixRecords})
		}
	}
	return lines
}

// entry is where the front door takes connections on this network.
func (c *Checker) entry() string {
	if c.Proxy != "" {
		return net.JoinHostPort(c.Proxy, "443")
	}
	return c.SNI
}

// doorWords names the front door in a line.
func (c *Checker) doorWords() string {
	switch {
	case c.Proxy != "":
		return "your front door (" + c.Proxy + ")"
	case c.Door == doorHomeOnly:
		return "Linx's port 443 at home"
	case c.port() != 443:
		return fmt.Sprintf("Linx's public port %d", c.port())
	}
	return "Linx's port 443"
}

// web checks the domain answers as Linx through the front door.
func (c *Checker) web(ctx context.Context, leaf *x509.Certificate, roots *x509.CertPool) Line {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	if c.Door == doorHTTPProxy {
		// The proxy unlocks the traffic with its own certificate: ask for
		// Linx's health through it instead.
		status, err := c.httpsGet(ctx, c.entry(), c.Domain, "/healthz")
		switch {
		case err != nil:
			return Line{State: Fail, Text: c.Domain + " doesn't answer through " + c.doorWords() + ".",
				Meaning: "Nobody outside can open Linx.", Fix: FixSteps}
		case status != http.StatusOK:
			return Line{State: Fail, Text: fmt.Sprintf("%s through %s doesn't answer as Linx (status %d).", c.Domain, c.doorWords(), status),
				Meaning: "Nobody outside can open Linx.", Fix: FixSteps}
		}
		return Line{State: OK, Text: c.Domain + " reaches Linx through " + c.doorWords()}
	}
	got, err := c.leafAt(ctx, c.entry(), c.Domain, roots)
	switch {
	case err != nil && isOtherCert(err):
		return Line{State: Fail, Text: c.Domain + " answers with another certificate, not Linx's.",
			Meaning: "Your front door unlocks the traffic instead of passing it through: nobody outside can open Linx.", Fix: FixSteps}
	case err != nil:
		return Line{State: Fail, Text: c.Domain + " doesn't answer through " + c.doorWords() + ".",
			Meaning: "Nobody outside can open Linx.", Fix: FixSteps}
	case !got.Equal(leaf):
		return Line{State: Fail, Text: c.Domain + " answers with another certificate, not Linx's.",
			Meaning: "Your front door sends it somewhere else: nobody outside can open Linx.", Fix: FixSteps}
	}
	return Line{State: OK, Text: c.Domain + " answers with Linx's certificate"}
}

// relay allocates a relay address over TLS, as a browser on a network
// that allows only web traffic does.
func (c *Checker) relay(ctx context.Context, name string, roots *x509.CertPool) Line {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	addr, port := c.entry(), strconv.Itoa(c.port())
	if c.Door == doorHTTPProxy {
		addr, port = c.TURNTLS, "5349"
	}
	user := strconv.FormatInt(c.Now().Add(2*time.Minute).Unix(), 10) + ":linx-reach-check"
	err := c.turnOverTLS(ctx, addr, name, roots, user, turn.Password(c.TURNSecret, user))
	if err != nil {
		how := "Your front door isn't sending " + name + " to port 5349."
		if c.Door == doorHTTPProxy {
			how = "Your router isn't sending TCP port 5349 to this server."
		} else if c.Proxy == "" {
			how = "Linx's port 443 router isn't passing it on."
		}
		return Line{State: Fail, Text: name + " doesn't answer (" + short(err) + ").",
			Meaning: how + " Calls from outside will have no audio.", Fix: FixSteps}
	}
	return Line{State: OK, Text: name + " relays call audio over port " + port}
}

// hairpin tries the public address itself.
func (c *Checker) hairpin(ctx context.Context, public netip.Addr, leaf *x509.Certificate, roots *x509.CertPool) Line {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	got, err := c.leafAt(ctx, netip.AddrPortFrom(public, uint16(c.port())).String(), c.Domain, roots)
	if err == nil && got.Equal(leaf) {
		at := public.String()
		if c.port() != 443 {
			at += " port " + strconv.Itoa(c.port())
		}
		return Line{State: OK, Text: fmt.Sprintf("%s answers at your public address (%s)", c.Domain, at)}
	}
	return Line{State: Info, Text: "Your router can't reach its own address from inside, so this server can't test the rest.",
		Meaning: "Use your phone below."}
}

// leafAt connects to addr for name, checked against roots, and returns the
// certificate that answered.
func (c *Checker) leafAt(ctx context.Context, addr, name string, roots *x509.CertPool) (*x509.Certificate, error) {
	conn, err := c.tlsDial(ctx, addr, name, roots)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0], nil
}

func (c *Checker) tlsDial(ctx context.Context, addr, name string, roots *x509.CertPool) (*tls.Conn, error) {
	raw, err := c.Dial(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, &tls.Config{ServerName: name, RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

func (c *Checker) turnOverTLS(ctx context.Context, addr, name string, roots *x509.CertPool, user, pass string) error {
	conn, err := c.tlsDial(ctx, addr, name, roots)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	cl := &turn.Client{Conn: conn, Stream: true, User: user, Pass: pass}
	return cl.Allocate()
}

func (c *Checker) httpsGet(ctx context.Context, addr, name, path string) (int, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext:     func(ctx context.Context, network, _ string) (net.Conn, error) { return c.Dial(ctx, network, addr) },
		TLSClientConfig: &tls.Config{ServerName: name, RootCAs: c.SystemRoots, MinVersion: tls.VersionTLS12},
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+name+path, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// isOtherCert reports a certificate that isn't Linx's (it doesn't chain to
// Linx's issuers, or isn't for the name).
func isOtherCert(err error) bool {
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var verify *tls.CertificateVerificationError
	return errors.As(err, &unknown) || errors.As(err, &host) || errors.As(err, &verify)
}

// short is an error without Go's long prefixes.
func short(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i < len(s)-2 {
		s = s[i+2:]
	}
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}
