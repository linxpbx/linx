package trunkcert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/trunkprobe"
)

func TestNewForAddress(t *testing.T) {
	c, err := New("192.168.1.60", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(pair.Certificate[0])
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("192.168.1.60")) || len(cert.DNSNames) != 0 {
		t.Errorf("names: %v %v", cert.IPAddresses, cert.DNSNames)
	}
	if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("not usable as a pin (OpenSSL needs a CA)")
	}
	if c.Fingerprint != trunkprobe.Fingerprint(cert) {
		t.Errorf("fingerprint %s, trunkprobe says %s", c.Fingerprint, trunkprobe.Fingerprint(cert))
	}
	if d := time.Until(c.NotAfter); d < 3*364*24*time.Hour {
		t.Errorf("lasts %v", d)
	}
}

// The connection test (which checks exactly as Asterisk does) accepts a
// phone system serving it, once pinned, at that address and no other.
func TestPinnedPassesTheConnectionTest(t *testing.T) {
	c, err := New("127.0.0.1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, _ := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, _ := conn.Read(buf)
				req := string(buf[:n])
				var h []string
				for _, l := range strings.Split(req, "\r\n")[1:] {
					for _, k := range []string{"Via:", "From:", "To:", "Call-ID:", "CSeq:"} {
						if strings.HasPrefix(l, k) {
							h = append(h, l)
						}
					}
				}
				conn.Write([]byte("SIP/2.0 200 OK\r\n" + strings.Join(h, "\r\n") + "\r\nContent-Length: 0\r\n\r\n"))
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	p := &trunkprobe.Prober{Roots: x509.NewCertPool(), Timeout: 2 * time.Second,
		Lookup: func(context.Context, string) ([]netip.Addr, error) { return nil, nil }}

	r := p.Run(context.Background(), trunkprobe.Target{Host: "127.0.0.1", Port: port, Transport: trunkprobe.TLS, Pinned: string(c.CertPEM)})
	if r.Steps[2].Name != "certificate" || r.Steps[2].Result != trunkprobe.OK {
		t.Fatalf("pinned: %+v", r.Steps)
	}
	r = p.Run(context.Background(), trunkprobe.Target{Host: "127.0.0.1", Port: port, Transport: trunkprobe.TLS})
	if r.Steps[2].Result != trunkprobe.Failed {
		t.Fatalf("not pinned but accepted: %+v", r.Steps)
	}
}

func TestNewRefuses(t *testing.T) {
	for _, a := range []string{"", "fe80::1", "not a host", "*.example.com", "a..b"} {
		if _, err := New(a, time.Now()); err == nil {
			t.Errorf("%q accepted", a)
		}
	}
	c, err := New("ucm.home.arpa.", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pair, _ := tls.X509KeyPair(c.CertPEM, c.KeyPEM)
	cert, _ := x509.ParseCertificate(pair.Certificate[0])
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "ucm.home.arpa" {
		t.Errorf("names: %v", cert.DNSNames)
	}
}
