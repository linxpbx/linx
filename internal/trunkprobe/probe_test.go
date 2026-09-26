package trunkprobe

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  string
}

func newCA(t *testing.T) testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test phone system CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return testCA{cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (ca testCA) leaf(t *testing.T, notAfter time.Time, names ...string) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter, DNSNames: names,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, ca.cert.Raw}, PrivateKey: key}
}

// provider is a fake SIP provider: OPTIONS → 200 (with an SRTP offer if
// sdp), REGISTER → 401 then 200 with the right password, 403 otherwise.
type provider struct {
	user, password string
	sdp            bool
	registered     []string // the Contact headers REGISTERs carried
}

func (p *provider) reply(req string) string {
	head, _, _ := strings.Cut(req, "\r\n\r\n")
	lines := strings.Split(head, "\r\n")
	method := strings.Fields(lines[0])[0]
	h := map[string]string{}
	for _, l := range lines[1:] {
		k, v, _ := strings.Cut(l, ":")
		h[strings.ToLower(k)] = strings.TrimSpace(v)
	}
	status, extra, body := "200 OK", "", ""
	switch method {
	case "OPTIONS":
		if p.sdp {
			body = "v=0\r\nm=audio 0 RTP/SAVP 8\r\n"
		}
	case "REGISTER":
		p.registered = append(p.registered, h["contact"])
		auth := h["authorization"]
		switch {
		case auth == "":
			status, extra = "401 Unauthorized", "WWW-Authenticate: Digest realm=\"test\", nonce=\"n0nce\", qop=\"auth\"\r\n"
		case !p.validAuth(auth):
			status = "403 Forbidden"
		}
	}
	return fmt.Sprintf("SIP/2.0 100 Trying\r\nVia: %s\r\nCall-ID: %s\r\nCSeq: %s\r\nContent-Length: 0\r\n\r\n", h["via"], h["call-id"], h["cseq"]) +
		fmt.Sprintf("SIP/2.0 %s\r\nVia: %s\r\nCall-ID: %s\r\nCSeq: %s\r\n%sContent-Length: %d\r\n\r\n%s", status, h["via"], h["call-id"], h["cseq"], extra, len(body), body)
}

func (p *provider) validAuth(auth string) bool {
	params := map[string]string{}
	for _, part := range splitParams(strings.TrimPrefix(auth, "Digest ")) {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		params[k] = strings.Trim(v, `"`)
	}
	sum := func(s string) string { x := md5.Sum([]byte(s)); return hex.EncodeToString(x[:]) }
	ha1 := sum(params["username"] + ":test:" + p.password)
	ha2 := sum("REGISTER:" + params["uri"])
	want := sum(ha1 + ":n0nce:" + params["nc"] + ":" + params["cnonce"] + ":auth:" + ha2)
	return params["username"] == p.user && params["response"] == want
}

func (p *provider) serveStream(t *testing.T, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			r := bufio.NewReader(c)
			for {
				var msg strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					msg.WriteString(line)
					if line == "\r\n" {
						break
					}
				}
				if _, err := c.Write([]byte(p.reply(msg.String()))); err != nil {
					return
				}
			}
		}()
	}
}

func (p *provider) serveUDP(pc net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		// A datagram per response.
		for _, resp := range strings.SplitAfter(p.reply(string(buf[:n])), "\r\n\r\n") {
			if resp != "" {
				_, _ = pc.WriteTo([]byte(resp), from)
			}
		}
	}
}

func listenTLS(t *testing.T, p *provider, cert tls.Certificate) int {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go p.serveStream(t, ln)
	return ln.Addr().(*net.TCPAddr).Port
}

func newProber() *Prober {
	return &Prober{
		Roots:   x509.NewCertPool(), // no public CA knows the test ones
		Timeout: 2 * time.Second,
		Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			if strings.HasSuffix(host, ".test") {
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			}
			return nil, fmt.Errorf("no such host")
		},
	}
}

func results(r Result) string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Name+"="+s.Result)
	}
	return strings.Join(out, " ")
}

func TestProbeTLS(t *testing.T) {
	ca := newCA(t)
	p := &provider{user: "linx", password: "s3cret;x"}
	port := listenTLS(t, p, ca.leaf(t, time.Now().Add(200*24*time.Hour), "sip.test"))
	ctx := context.Background()
	target := Target{Host: "sip.test", Port: port, Transport: TLS, Pinned: ca.pem, Registers: true,
		Username: "linx", Password: "s3cret;x", SRTP: true}

	t.Run("everything works", func(t *testing.T) {
		p.registered = nil
		r := newProber().Run(ctx, target)
		if want := "address=ok connection=ok certificate=ok sip=ok login=ok audio_encryption=skipped"; results(r) != want || !r.OK {
			t.Fatalf("got %s (ok %v), want %s\n%+v", results(r), r.OK, want, r.Steps)
		}
		if !strings.Contains(r.Steps[2].Words, "the one you pinned") || len(r.Certificates) != 2 {
			t.Errorf("certificate step: %q, %d certificates", r.Steps[2].Words, len(r.Certificates))
		}
		// The login check never changes where the provider sends calls.
		for _, c := range p.registered {
			if c != "" {
				t.Errorf("a REGISTER carried a Contact: %q", c)
			}
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		bad := target
		bad.Password = "nope"
		r := newProber().Run(ctx, bad)
		if r.OK || !strings.Contains(results(r), "login=failed") || !strings.Contains(r.Steps[4].Words, "403") {
			t.Fatalf("got %s: %+v", results(r), r.Steps)
		}
	})

	t.Run("not pinned: untrusted, with the certificate to pin", func(t *testing.T) {
		unpinned := target
		unpinned.Pinned = ""
		r := newProber().Run(ctx, unpinned)
		if want := "address=ok connection=ok certificate=failed sip=skipped login=skipped"; r.OK || !r.Untrusted || results(r) != want {
			t.Fatalf("got %s untrusted=%v", results(r), r.Untrusted)
		}
		top := r.Certificates[len(r.Certificates)-1]
		if top.SHA256 != Fingerprint(ca.cert) || !top.SelfSigned || !strings.Contains(r.Steps[2].Words, top.SHA256) {
			t.Errorf("certificate to pin: %+v\n%s", top, r.Steps[2].Words)
		}
	})

	t.Run("wrong name", func(t *testing.T) {
		wrong := target
		wrong.Host = "other.test"
		r := newProber().Run(ctx, wrong)
		if r.OK || r.Untrusted || !strings.Contains(r.Steps[2].Words, "is for sip.test, not other.test") {
			t.Fatalf("got %s: %+v", results(r), r.Steps)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		closed := target
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		closed.Port = ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		r := newProber().Run(ctx, closed)
		if want := "address=ok connection=failed certificate=skipped sip=skipped login=skipped"; results(r) != want {
			t.Fatalf("got %s, want %s", results(r), want)
		}
	})

	t.Run("doesn't resolve", func(t *testing.T) {
		r := newProber().Run(ctx, Target{Host: "sip.example.invalid", Transport: TLS})
		if !strings.HasPrefix(results(r), "address=failed") || r.OK {
			t.Fatalf("got %s", results(r))
		}
	})

	t.Run("Linx's own networks refused", func(t *testing.T) {
		pr := newProber()
		pr.Refuse = RefuseOwn([]netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")})
		r := pr.Run(ctx, target)
		if !strings.HasPrefix(results(r), "address=failed") || !strings.Contains(r.Steps[0].Words, "not an address a phone line can be at") {
			t.Fatalf("loopback: %s %+v", results(r), r.Steps)
		}
		if why := pr.Refuse(netip.MustParseAddr("172.18.0.5")); why == "" {
			t.Error("an own network was allowed")
		}
		if why := pr.Refuse(netip.MustParseAddr("192.168.1.10")); why != "" {
			t.Errorf("a LAN address was refused: %s", why)
		}
	})

	t.Run("expiring soon", func(t *testing.T) {
		soon := &provider{}
		port := listenTLS(t, soon, ca.leaf(t, time.Now().Add(10*24*time.Hour), "sip.test"))
		r := newProber().Run(ctx, Target{Host: "sip.test", Port: port, Transport: TLS, Pinned: ca.pem, SRTP: true})
		if !r.OK || r.Steps[2].Result != Warning || !strings.Contains(r.Steps[2].Words, "expires in") {
			t.Fatalf("got %s: %+v", results(r), r.Steps)
		}
	})
}

func TestProbePlain(t *testing.T) {
	ctx := context.Background()
	p := &provider{sdp: true}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go p.serveUDP(pc)
	port := pc.LocalAddr().(*net.UDPAddr).Port

	r := newProber().Run(ctx, Target{Host: "127.0.0.1", Port: port, Transport: UDP})
	if want := "address=ok sip=ok login=skipped audio_encryption=warning"; results(r) != want || !r.OK {
		t.Fatalf("got %s, want %s: %+v", results(r), want, r.Steps)
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go p.serveStream(t, ln)
	tcpPort := ln.Addr().(*net.TCPAddr).Port
	r = newProber().Run(ctx, Target{Host: "127.0.0.1", Port: tcpPort, Transport: TCP, SRTP: true})
	if want := "address=ok connection=ok sip=ok login=skipped audio_encryption=ok"; results(r) != want {
		t.Fatalf("got %s, want %s: %+v", results(r), want, r.Steps)
	}

	r = newProber().Run(ctx, Target{Host: "10.6.0.1", Transport: UDP, WireGuard: true, TunnelName: "VPN", TunnelState: "up", TunnelDetail: "Last handshake 20 seconds ago."})
	if results(r) != "tunnel=ok address=skipped" || !r.OK || !strings.Contains(r.Steps[0].Words, `"VPN": Last handshake`) {
		t.Fatalf("WireGuard: %s %+v", results(r), r.Steps)
	}
	r = newProber().Run(ctx, Target{Host: "10.6.0.1", Transport: UDP, WireGuard: true, TunnelState: "down"})
	if results(r) != "tunnel=failed address=skipped" || r.OK {
		t.Fatalf("WireGuard down: %s", results(r))
	}
}

func TestParse(t *testing.T) {
	r, err := parse("SIP/2.0 200 OK\r\ni: abc\r\nCSeq: 1 OPTIONS\r\nl: 0\r\n\r\n")
	if err != nil || r.Status != 200 || r.header("call-id") != "abc" || r.header("content-length") != "0" {
		t.Fatalf("parse = %+v, %v", r, err)
	}
	if _, err := parse("HTTP/1.1 200 OK\r\n\r\n"); err == nil {
		t.Error("an HTTP answer parsed as SIP")
	}
	c, ok := parseChallenge(`Digest realm="a, b", nonce="n", algorithm=SHA-256, qop="auth,auth-int", opaque="o"`)
	if !ok || c.realm != "a, b" || c.algorithm != "SHA-256" || c.qop != "auth" || c.opaque != "o" {
		t.Fatalf("challenge = %+v", c)
	}
	if _, err := (challenge{nonce: "n", algorithm: "SHA-512-256"}).authorization("REGISTER", "sip:x", "u", "p"); err == nil {
		t.Error("an unknown algorithm was answered")
	}
}
