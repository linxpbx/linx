package reach

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/turn"
)

func TestLinks(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	proxy := netip.MustParseAddr("192.168.1.20")
	home := netip.MustParseAddr("94.200.1.10")
	l := &Links{Domain: "example.com", Now: func() time.Time { return now },
		Proxies:  func(a netip.Addr) bool { return a == proxy },
		PublicIP: func(context.Context) (netip.Addr, error) { return home, nil },
		TURN:     &turn.Issuer{Secret: []byte("s"), URLs: []string{"turns:turn.example.com:443?transport=tcp"}}}
	ctx := context.Background()

	k := l.New()
	if len(k.Code) != codeLen || strings.ContainsAny(k.Code, "01OIL") || l.URL(k) != "https://example.com/reach/"+k.Code || k.State != Waiting {
		t.Fatalf("link %+v %s", k, l.URL(k))
	}
	if _, err := l.Claim(ctx, "WRONGCODE2", netip.MustParseAddr("5.194.33.12")); err != ErrNoLink {
		t.Errorf("a wrong code: %v", err)
	}
	c, err := l.Claim(ctx, k.Code, netip.MustParseAddr("5.194.33.12"))
	if err != nil || c.Domain != "example.com" || !strings.HasSuffix(c.TURN.Username, ":linx-reach-"+k.ID.String()) ||
		c.TURN.Password != turn.Password([]byte("s"), c.TURN.Username) || !c.TURN.ExpiresAt.Equal(now.Add(RelayTTL)) {
		t.Fatalf("claim %+v %v", c, err)
	}
	if _, err := l.Claim(ctx, k.Code, netip.MustParseAddr("5.194.33.12")); err != ErrNoLink {
		t.Error("a link worked twice")
	}
	got, ok := l.Wait(ctx, k.ID, 0)
	if !ok || got.State != Reached || got.Address != "5.194.33.12" || got.Seen != SeenOutside || got.Version != 1 {
		t.Errorf("after the claim %+v", got)
	}
	// The relay test: once, and the waiting page hears of it.
	done := make(chan Link)
	go func() { k2, _ := l.Wait(ctx, k.ID, 1); done <- k2 }()
	time.Sleep(20 * time.Millisecond)
	if err := l.ReportRelay(k.Code, true, ""); err != nil {
		t.Fatal(err)
	}
	if k2 := <-done; k2.Relay == nil || !k2.Relay.OK || k2.Version != 2 {
		t.Errorf("relay %+v", k2)
	}
	if l.ReportRelay(k.Code, false, "") != ErrNoLink {
		t.Error("relay reported twice")
	}

	// What the address says.
	for addr, want := range map[string]string{"192.168.1.20": SeenProxy, "192.168.1.55": SeenLocal, "94.200.1.10": SeenHome} {
		k := l.New()
		if _, err := l.Claim(ctx, k.Code, netip.MustParseAddr(addr)); err != nil {
			t.Fatal(err)
		}
		if got, _ := l.Wait(ctx, k.ID, 0); got.Seen != want {
			t.Errorf("%s: seen %q, want %q", addr, got.Seen, want)
		}
	}

	// Ten minutes, then it can't be used.
	old := l.New()
	now = now.Add(LinkTTL)
	if _, err := l.Claim(ctx, old.Code, netip.MustParseAddr("5.194.33.12")); err != ErrNoLink {
		t.Error("an old link worked")
	}
	if got, _ := l.Wait(ctx, old.ID, 0); got.State != Expired {
		t.Errorf("old link %+v", got)
	}
	// A late relay report is refused.
	if l.ReportRelay(k.Code, true, "") != ErrNoLink {
		t.Error("late relay report accepted")
	}
	// Memory stays bounded.
	for range 3 * maxLinks {
		l.New()
	}
	if len(l.links) > maxLinks {
		t.Errorf("%d links kept", len(l.links))
	}
}

// A certificate chain: an issuer and a leaf for names.
func chain(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test issuer"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	ca, _ = x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: key}
}

// serveTLS answers TLS with cert on a local port.
func serveTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); time.Sleep(50 * time.Millisecond); c.Close() }()
		}
	}()
	return ln.Addr().String()
}

func TestChecker(t *testing.T) {
	linx := chain(t, "example.com", "turn.example.com")
	other := chain(t, "example.com")
	public := netip.MustParseAddr("94.200.1.10")
	good, bad := serveTLS(t, linx), serveTLS(t, other)
	route := map[string]string{"192.168.1.20:443": good}
	dns := map[string][]string{"example.com": {"94.200.1.10"}, "turn.example.com": {"94.200.1.10"}}
	c := &Checker{Domain: "example.com", Door: "proxy", Proxy: "192.168.1.20", TURNSecret: []byte("s"),
		Leaf:     func() (*tls.Certificate, error) { return &linx, nil },
		Lookup:   func(_ context.Context, name string) ([]string, error) { return dns[name], nil },
		PublicIP: func(context.Context) (netip.Addr, error) { return public, nil },
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if to, ok := route[addr]; ok {
				return (&net.Dialer{}).DialContext(ctx, network, to)
			}
			return nil, errors.New("connection refused")
		},
		Now: time.Now}
	texts := func(ls []Line) string {
		var b strings.Builder
		for _, l := range ls {
			b.WriteString(l.State + " " + l.Text + " | " + l.Meaning + " | " + l.Fix + "\n")
		}
		return b.String()
	}
	ctx := context.Background()

	// Names right, Linx's certificate through the door; the relay isn't a
	// real TURN server here, and the router can't reach its own address.
	got := texts(c.Run(ctx))
	for _, want := range []string{
		"ok example.com points to 94.200.1.10 (your home's address)",
		"ok turn.example.com points to 94.200.1.10",
		"ok example.com answers with Linx's certificate",
		"fail turn.example.com doesn't answer",
		"Calls from outside will have no audio. | steps",
		"info Your router can't reach its own address from inside, so this server can't test the rest. | Use your phone below.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}

	// It reaches its own public address: that's checked too.
	route["94.200.1.10:443"] = good
	if got := texts(c.Run(ctx)); !strings.Contains(got, "ok example.com answers at your public address (94.200.1.10)") {
		t.Errorf("hairpin:\n%s", got)
	}

	// A front door that unlocks the traffic, a wrong record, a missing one.
	route["192.168.1.20:443"] = bad
	dns["example.com"] = []string{"203.0.113.9"}
	delete(dns, "turn.example.com")
	got = texts(c.Run(ctx))
	for _, want := range []string{
		"fail example.com points to 203.0.113.9, not 94.200.1.10 (your home's address). | People outside are sent somewhere else. | records",
		"fail turn.example.com isn't in DNS yet.",
		"fail example.com answers with another certificate, not Linx's.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "public address") || strings.Contains(got, "Use your phone") {
		t.Errorf("tried the public address with the door broken:\n%s", got)
	}

	// Home only: the names point at this server, and nothing outside is tried.
	h := *c
	h.Door, h.Proxy, h.SNI, h.Home = "home-only", "", "sni:443", netip.MustParseAddr("192.168.1.212")
	route["sni:443"] = good
	dns["example.com"], dns["turn.example.com"] = []string{"192.168.1.212"}, []string{"192.168.1.212"}
	got = texts(h.Run(ctx))
	if !strings.Contains(got, "ok example.com points to 192.168.1.212 (this server, at home)") ||
		!strings.Contains(got, "ok example.com answers with Linx's certificate") || strings.Contains(got, "phone") {
		t.Errorf("home only:\n%s", got)
	}

	// Another public port: linx-sni still answers inside, the public
	// address is tried on that port, and the port itself is a warning.
	pp := *c
	pp.Door, pp.Proxy, pp.SNI, pp.Port = "public-port", "", "sni:443", 8443
	dns["example.com"], dns["turn.example.com"] = []string{"94.200.1.10"}, []string{"94.200.1.10"}
	route["94.200.1.10:443"], route["94.200.1.10:8443"] = bad, good
	got = texts(pp.Run(ctx))
	for _, want := range []string{
		"ok example.com answers with Linx's certificate",
		"turn.example.com doesn't answer",
		"ok example.com answers at your public address (94.200.1.10 port 8443)",
		"warn People open Linx at port 8443, not the standard 443.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("public port: missing %q in\n%s", want, got)
		}
	}

	// Nothing set up yet.
	n := *c
	n.Door = "none"
	if ls := n.Run(ctx); len(ls) != 1 || ls[0].State != Warn {
		t.Errorf("no front door: %+v", ls)
	}
}
