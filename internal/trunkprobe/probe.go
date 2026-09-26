// Package trunkprobe tests a trunk's connection before it's saved, and on
// request after (docs/TRUNKS.md §6, §10: `linx trunk add|test`,
// POST /trunks/{id}/test): the provider's address, the connection, its
// TLS certificate (checked exactly as Asterisk will: public CAs, or the
// certificate the admin pinned, and always the name), that it speaks SIP,
// and, for a trunk Linx signs in to, that it accepts the login. Every step
// is reported in plain words. Certificate checking is never switched off:
// a certificate that fails is reported (with its fingerprint, to pin if
// the admin recognises it) from the failed handshake itself.
//
// The login check sends a REGISTER with no Contact, which asks the
// provider for its current registrations: it proves the password without
// changing where the provider sends calls.
package trunkprobe

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Transports and trust, as internal/trunk names them.
const (
	TLS = "tls"
	TCP = "tcp"
	UDP = "udp"
)

// Target is what's tested.
type Target struct {
	Host      string
	Port      int
	Transport string // tls, tcp or udp
	// Pinned is the admin's pinned PEM (ADR-045); empty: public CAs only.
	Pinned string
	// Registers: Linx signs in to it (a "registration" trunk).
	Registers bool
	Username  string
	Password  string
	// SRTP: its calls' audio must be encrypted.
	SRTP bool
	// WireGuard: reached through a tunnel, which only Asterisk's network
	// has (docs/TRUNKS.md §7): the control plane can't test the line
	// itself, only report the tunnel (TunnelName, TunnelState, one of
	// wgconf's states, and TunnelDetail).
	WireGuard                             bool
	TunnelName, TunnelState, TunnelDetail string
}

// Step results.
const (
	OK      = "ok"
	Warning = "warning"
	Failed  = "failed"
	Skipped = "skipped"
)

// Step is one check.
type Step struct {
	Name   string `json:"name"`   // tunnel, address, connection, certificate, sip, login, audio_encryption, tls_offered
	Result string `json:"result"` // ok, warning, failed, skipped
	Words  string `json:"words"`  // plain words
}

// Certificate is one certificate the provider presented.
type Certificate struct {
	Subject    string    `json:"subject"`
	Issuer     string    `json:"issuer"`
	Names      []string  `json:"names"`
	NotAfter   time.Time `json:"not_after"`
	SHA256     string    `json:"sha256"` // AB:CD:... as providers print it
	SelfSigned bool      `json:"self_signed"`
	PEM        string    `json:"pem"`
}

// Result is a whole test.
type Result struct {
	// OK: nothing failed (warnings allowed).
	OK    bool   `json:"ok"`
	Steps []Step `json:"steps"`
	// Certificates the provider presented, leaf first (TLS only).
	Certificates []Certificate `json:"certificates,omitempty"`
	// Untrusted: the certificate failed only because nothing Linx trusts
	// signed it; pinning Certificates' last entry would let it pass.
	Untrusted bool `json:"untrusted,omitempty"`
}

// Prober runs tests.
type Prober struct {
	// Lookup resolves a host to IPv4 addresses (as Asterisk's trunks
	// file uses). Nil: the system resolver.
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dialer connects. Nil: net.Dialer.
	Dialer func(ctx context.Context, network, addr string) (net.Conn, error)
	// Roots are the public CAs. Nil: the system's.
	Roots *x509.CertPool
	// Refuse, if set, returns why an address mustn't be tested (Linx's own
	// container networks, loopback), or "".
	Refuse func(netip.Addr) string
	// Timeout bounds each network step (default 5 s).
	Timeout time.Duration
	Now     func() time.Time
}

func (p *Prober) timeout() time.Duration {
	if p.Timeout > 0 {
		return p.Timeout
	}
	return 5 * time.Second
}

func (p *Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// RefuseOwn refuses addresses a trunk can never legitimately be at:
// loopback, unspecified, multicast and link-local ones, and own (Linx's
// container networks), so the test can't be used to look around inside
// Linx.
func RefuseOwn(own []netip.Prefix) func(netip.Addr) string {
	return func(a netip.Addr) string {
		switch {
		case a.IsLoopback(), a.IsUnspecified(), a.IsMulticast(), a.IsLinkLocalUnicast(), a.IsLinkLocalMulticast():
			return "that's not an address a phone line can be at"
		}
		for _, n := range own {
			if n.Contains(a) {
				return "that's one of Linx's own internal networks"
			}
		}
		return ""
	}
}

type run struct {
	res Result
}

func (r *run) add(name, result, words string) {
	r.res.Steps = append(r.res.Steps, Step{Name: name, Result: result, Words: words})
}

func (r *run) done() Result {
	r.res.OK = true
	for _, s := range r.res.Steps {
		if s.Result == Failed {
			r.res.OK = false
		}
	}
	return r.res
}

// skipRest marks the steps that couldn't run after a failure.
func (r *run) skipRest(t Target, from string) {
	names := []string{"connection", "certificate", "sip", "login"}
	started := false
	for _, n := range names {
		if n == from {
			started = true
		}
		if !started || (n == "certificate" && t.Transport != TLS) || (n == "connection" && t.Transport == UDP) ||
			(n == "login" && !t.Registers) {
			continue
		}
		r.add(n, Skipped, "Not checked: an earlier step failed.")
	}
}

func defaultPort(transport string) int {
	if transport == TLS {
		return 5061
	}
	return 5060
}

// Run tests t.
func (p *Prober) Run(ctx context.Context, t Target) Result {
	r := &run{}
	if t.WireGuard {
		words := fmt.Sprintf("WireGuard tunnel %q: %s", t.TunnelName, t.TunnelDetail)
		switch t.TunnelState {
		case "up":
			r.add("tunnel", OK, words)
		case "down":
			r.add("tunnel", Failed, words)
		default:
			r.add("tunnel", Warning, words)
		}
		r.add("address", Skipped, "This line is only reachable through its tunnel, from the phone system: its connection and login show in its status (linx trunk list) within a minute.")
		return r.done()
	}
	port := t.Port
	if port == 0 {
		port = defaultPort(t.Transport)
	}

	// Address.
	addr, ok := p.address(ctx, r, t.Host)
	if !ok {
		r.skipRest(t, "connection")
		return r.done()
	}
	hostport := net.JoinHostPort(addr.String(), strconv.Itoa(port))

	// Connection and certificate.
	conn, ok := p.connect(ctx, r, t, hostport)
	if !ok {
		p.offeredTLS(ctx, r, t, addr)
		return r.done()
	}
	defer conn.Close()

	// SIP.
	deadline := time.Now().Add(p.timeout())
	viaTransport := strings.ToUpper(t.Transport)
	target := t.Host
	if port != defaultPort(t.Transport) {
		target = net.JoinHostPort(t.Host, strconv.Itoa(port))
	}
	uri := "sip:" + target
	if t.Transport != UDP {
		uri += ";transport=" + t.Transport
	}
	local := conn.LocalAddr().String()
	opts := request{Method: "OPTIONS", URI: uri, Transport: viaTransport, From: "<sip:linx@" + local + ">", To: "<" + uri + ">",
		CallID: randomToken(), CSeq: 1, Extra: []string{"Accept: application/sdp"}}
	resp, err := exchange(conn, t.Transport == UDP, opts, deadline)
	if err != nil {
		r.add("sip", Failed, "It accepted the connection but didn't answer a SIP question (OPTIONS) within "+
			p.timeout().String()+". Check the port and transport.")
		if t.Registers {
			r.add("login", Skipped, "Not checked: an earlier step failed.")
		}
		p.audio(r, t, response{})
		p.offeredTLS(ctx, r, t, addr)
		return r.done()
	}
	r.add("sip", OK, fmt.Sprintf("It answers SIP (%d %s).", resp.Status, resp.Reason))

	// Login.
	if t.Registers {
		p.login(r, conn, t, target, viaTransport, deadline)
	} else {
		r.add("login", Skipped, "Linx doesn't sign in to this line: it's recognised by its address.")
	}
	p.audio(r, t, resp)
	p.offeredTLS(ctx, r, t, addr)
	return r.done()
}

func (p *Prober) address(ctx context.Context, r *run, host string) (netip.Addr, bool) {
	var addr netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addr = a.Unmap()
	} else {
		lookup := p.Lookup
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
			}
		}
		lctx, cancel := context.WithTimeout(ctx, p.timeout())
		addrs, err := lookup(lctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			r.add("address", Failed, fmt.Sprintf("%s doesn't resolve to an address. Check the spelling, or use its IP address.", host))
			return netip.Addr{}, false
		}
		addr = addrs[0].Unmap()
	}
	if p.Refuse != nil {
		if why := p.Refuse(addr); why != "" {
			r.add("address", Failed, fmt.Sprintf("%s is at %s: %s.", host, addr, why))
			return netip.Addr{}, false
		}
	}
	if addr.String() == host {
		r.add("address", OK, "Using the address "+host+".")
	} else {
		r.add("address", OK, fmt.Sprintf("%s is at %s.", host, addr))
	}
	return addr, true
}

func (p *Prober) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	if p.Dialer != nil {
		return p.Dialer(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// connect opens the connection (and TLS), adding its steps.
func (p *Prober) connect(ctx context.Context, r *run, t Target, hostport string) (net.Conn, bool) {
	if t.Transport == UDP {
		conn, err := p.dial(ctx, "udp", hostport)
		if err != nil {
			r.add("sip", Failed, "Linx can't send to "+hostport+": "+err.Error())
			return nil, false
		}
		return conn, true
	}
	raw, err := p.dial(ctx, "tcp", hostport)
	if err != nil {
		words := fmt.Sprintf("Nothing answers on %s (TCP): ", hostport)
		var ne net.Error
		switch {
		case errors.As(err, &ne) && ne.Timeout():
			words += fmt.Sprintf("no reply within %s. A firewall may be in the way, or it's the wrong address.", p.timeout())
		case strings.Contains(err.Error(), "refused"):
			words += "the connection was refused. Check the port."
		default:
			words += err.Error() + "."
		}
		r.add("connection", Failed, words)
		r.skipRest(t, "certificate")
		return nil, false
	}
	r.add("connection", OK, "It accepts connections on "+hostport+".")
	if t.Transport != TLS {
		return raw, true
	}
	conn, ok := p.handshake(ctx, r, t, raw)
	if !ok {
		raw.Close()
		r.skipRest(t, "sip")
		return nil, false
	}
	return conn, true
}

// handshake checks the provider's certificate exactly as Asterisk will
// (docs/TRUNKS.md §6): public CAs plus what the admin pinned, and the name.
func (p *Prober) handshake(ctx context.Context, r *run, t Target, raw net.Conn) (net.Conn, bool) {
	roots := p.Roots
	if roots == nil {
		sys, err := x509.SystemCertPool()
		if err != nil {
			sys = x509.NewCertPool()
		}
		roots = sys
	}
	pinned := strings.TrimSpace(t.Pinned) != ""
	if pinned {
		roots = roots.Clone()
		if !roots.AppendCertsFromPEM([]byte(t.Pinned)) {
			r.add("certificate", Failed, "The pinned certificate can't be read. Paste it again (PEM, starting -----BEGIN CERTIFICATE-----).")
			return nil, false
		}
	}
	conn := tls.Client(raw, &tls.Config{ServerName: t.Host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	hctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	err := conn.HandshakeContext(hctx)
	if err == nil {
		certs := conn.ConnectionState().PeerCertificates
		r.res.Certificates = describe(certs)
		how := "signed by a certificate authority Linx trusts"
		if pinned && !publiclyTrusted(certs, p.Roots, t.Host) {
			how = "the one you pinned"
		}
		words := fmt.Sprintf("Encrypted (TLS). Its certificate is for %s and is %s.", t.Host, how)
		result := OK
		if left := certs[0].NotAfter.Sub(p.now()); left < 30*24*time.Hour {
			result = Warning
			words += fmt.Sprintf(" It expires in %d days (%s): ask for a new one.", int(left.Hours()/24), certs[0].NotAfter.Format("2 Jan 2006"))
		}
		r.add("certificate", result, words)
		return conn, true
	}

	var verr *tls.CertificateVerificationError
	if !errors.As(err, &verr) {
		r.add("certificate", Failed, "It doesn't speak TLS on this port ("+err.Error()+"). Check the port, or whether it needs TLS turned on.")
		return nil, false
	}
	certs := verr.UnverifiedCertificates
	r.res.Certificates = describe(certs)
	var unknown x509.UnknownAuthorityError
	var host x509.HostnameError
	var invalid x509.CertificateInvalidError
	switch {
	case errors.As(verr.Err, &host):
		r.add("certificate", Failed, fmt.Sprintf("Its certificate is for %s, not %s. Use the name its certificate is for as the address, or put a certificate for %s on it.",
			strings.Join(names(certs[0]), ", "), t.Host, t.Host))
	case errors.As(verr.Err, &invalid) && invalid.Reason == x509.Expired:
		r.add("certificate", Failed, fmt.Sprintf("Its certificate expired or isn't valid yet (valid %s to %s). It needs a new one.",
			certs[0].NotBefore.Format("2 Jan 2006"), certs[0].NotAfter.Format("2 Jan 2006")))
	case errors.As(verr.Err, &unknown):
		r.res.Untrusted = true
		top := r.res.Certificates[len(r.res.Certificates)-1]
		words := "Its certificate isn't signed by a certificate authority Linx trusts"
		if pinned {
			words += ", nor by the certificate you pinned"
		}
		words += fmt.Sprintf(". If this is your own phone system or your provider gave you its certificate, compare this fingerprint with it and pin it: SHA-256 %s.", top.SHA256)
		if !top.SelfSigned {
			words += " That certificate was itself signed by \"" + top.Issuer + "\": pinning that one instead is better, if the provider gives it to you."
		}
		r.add("certificate", Failed, words)
	default:
		r.add("certificate", Failed, "Its certificate was refused: "+verr.Err.Error()+".")
	}
	return nil, false
}

// publiclyTrusted reports whether certs verify for host against the
// public CAs alone (so a pin wasn't needed).
func publiclyTrusted(certs []*x509.Certificate, roots *x509.CertPool, host string) bool {
	if roots == nil {
		sys, err := x509.SystemCertPool()
		if err != nil {
			return false
		}
		roots = sys
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	_, err := certs[0].Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: inter})
	return err == nil
}

func names(c *x509.Certificate) []string {
	var out []string
	out = append(out, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	if len(out) == 0 && c.Subject.CommonName != "" {
		out = append(out, c.Subject.CommonName)
	}
	if len(out) == 0 {
		out = append(out, "no name at all")
	}
	return out
}

// Fingerprint is a certificate's SHA-256 as providers print it.
func Fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	var b strings.Builder
	for i, x := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02X", x)
	}
	return b.String()
}

func describe(certs []*x509.Certificate) []Certificate {
	out := make([]Certificate, 0, len(certs))
	for _, c := range certs {
		out = append(out, Certificate{
			Subject: c.Subject.String(), Issuer: c.Issuer.String(), Names: names(c), NotAfter: c.NotAfter,
			SHA256: Fingerprint(c), SelfSigned: c.CheckSignatureFrom(c) == nil && c.Subject.String() == c.Issuer.String(),
			PEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})),
		})
	}
	return out
}

// login proves the password with a REGISTER that has no Contact (a query:
// nothing about where calls go changes).
func (p *Prober) login(r *run, conn net.Conn, t Target, target, via string, deadline time.Time) {
	if t.Username == "" {
		r.add("login", Failed, "This line needs a login, and none is set.")
		return
	}
	uri := "sip:" + target
	if t.Transport != UDP {
		uri += ";transport=" + t.Transport
	}
	aor := "<sip:" + t.Username + "@" + t.Host + ">"
	q := request{Method: "REGISTER", URI: uri, Transport: via, From: aor, To: aor, CallID: randomToken(), CSeq: 1}
	resp, err := exchange(conn, t.Transport == UDP, q, deadline)
	for attempt := 0; err == nil && attempt < 2 && (resp.Status == 401 || resp.Status == 407); attempt++ {
		h, name := resp.header("www-authenticate"), "Authorization"
		if resp.Status == 407 {
			h, name = resp.header("proxy-authenticate"), "Proxy-Authorization"
		}
		c, ok := parseChallenge(h)
		if !ok {
			r.add("login", Failed, "It asked for a login in a way Linx doesn't understand.")
			return
		}
		if attempt == 1 && !strings.Contains(strings.ToLower(h), "stale=true") {
			break
		}
		auth, aerr := c.authorization("REGISTER", uri, t.Username, t.Password)
		if aerr != nil {
			r.add("login", Failed, "It "+aerr.Error()+".")
			return
		}
		q.CSeq++
		q.Extra = []string{name + ": " + auth}
		resp, err = exchange(conn, t.Transport == UDP, q, deadline)
	}
	switch {
	case err != nil:
		r.add("login", Failed, "It didn't answer Linx's sign-in in time.")
	case resp.Status/100 == 2:
		r.add("login", OK, "It accepted Linx's login ("+t.Username+").")
	case resp.Status == 401 || resp.Status == 407 || resp.Status == 403:
		r.add("login", Failed, fmt.Sprintf("It refused the login %q (%d %s). Check the login and password.", t.Username, resp.Status, resp.Reason))
	default:
		r.add("login", Failed, fmt.Sprintf("It refused Linx's sign-in: %d %s.", resp.Status, resp.Reason))
	}
}

// audio reports what's known about the audio's encryption without placing
// a call.
func (p *Prober) audio(r *run, t Target, opts response) {
	if !t.SRTP {
		r.add("audio_encryption", Warning, "Calls' audio on this line isn't encrypted: it can be listened to on the way.")
		return
	}
	switch {
	case strings.Contains(opts.Body, "RTP/SAVP"):
		r.add("audio_encryption", OK, "It offers encrypted audio (SRTP).")
	case t.Transport != TLS:
		r.add("audio_encryption", Warning, "Encrypted audio needs TLS to exchange its keys; without TLS, calls will fail. Use TLS, or turn audio encryption off.")
	default:
		r.add("audio_encryption", Skipped, "Checked on the first call: Linx only accepts calls on this line with encrypted audio (SRTP).")
	}
}

// offeredTLS, for a line set up without TLS, checks whether the provider
// offers TLS on 5061 after all (ADR-023: encrypted first).
func (p *Prober) offeredTLS(ctx context.Context, r *run, t Target, addr netip.Addr) {
	if t.Transport == TLS {
		return
	}
	raw, err := p.dial(ctx, "tcp", net.JoinHostPort(addr.String(), "5061"))
	if err != nil {
		return
	}
	defer raw.Close()
	roots := p.Roots
	if roots == nil {
		if sys, err := x509.SystemCertPool(); err == nil {
			roots = sys
		}
	}
	conn := tls.Client(raw, &tls.Config{ServerName: t.Host, RootCAs: roots, MinVersion: tls.VersionTLS12})
	hctx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()
	if err := conn.HandshakeContext(hctx); err != nil {
		var verr *tls.CertificateVerificationError
		if errors.As(err, &verr) {
			r.add("tls_offered", Warning, "It also answers encrypted (TLS on port 5061), with a certificate Linx doesn't trust yet. Try TLS with that certificate pinned before settling for no encryption.")
		}
		return
	}
	r.add("tls_offered", Warning, "It also answers encrypted (TLS on port 5061) with a trusted certificate: switch this line to TLS.")
}
