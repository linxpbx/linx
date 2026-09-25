package turnconf

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	os.WriteFile(secret, []byte(strings.Repeat("s3", 20)+"\n"), 0o600)
	return Config{
		ConfPath: filepath.Join(dir, "run", "turnserver.conf"), SecretFile: secret, Realm: "example.com",
		CertsDir: "/var/lib/linx/certs", PeerHost: "linx-asterisk-media",
		LookupHost: func(h string) ([]netip.Addr, error) {
			if h != "linx-asterisk-media" {
				return nil, errors.New("no such host")
			}
			return []netip.Addr{netip.MustParseAddr("172.23.0.2")}, nil
		},
		InterfaceAddrs: func() ([]net.Addr, error) {
			return []net.Addr{
				&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
				&net.IPNet{IP: net.ParseIP("172.20.0.7"), Mask: net.CIDRMask(16, 32)},
				&net.IPNet{IP: net.ParseIP("172.23.0.3"), Mask: net.CIDRMask(28, 32)},
			}, nil
		},
	}
}

func TestResolveAndRender(t *testing.T) {
	c := testConfig(t)
	a, err := c.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if a.Peer != netip.MustParseAddr("172.23.0.2") || a.Relay != netip.MustParseAddr("172.23.0.3") {
		t.Fatalf("%+v", a)
	}
	if err := c.Render(a); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.ConfPath)
	conf := string(b)
	for _, want := range []string{
		"\nrelay-ip=172.23.0.3\n", "\nallowed-peer-ip=172.23.0.2\n",
		"\ndenied-peer-ip=0.0.0.0-255.255.255.255\n", "\ndenied-peer-ip=::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff\n",
		"\nuse-auth-secret\n", "\nstatic-auth-secret=" + strings.Repeat("s3", 20) + "\n", "\nrealm=example.com\n",
		"\ncert=/var/lib/linx/certs/current/fullchain.pem\n", "\nno-tcp-relay\n", "\nno-multicast-peers\n",
		"\nuser-quota=10\n", "\nlistening-port=3478\n", "\ntls-listening-port=5349\n",
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("turnserver.conf lacks %q:\n%s", want, conf)
		}
	}
	if fi, _ := os.Stat(c.ConfPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v: it holds the secret", fi.Mode())
	}
}

func TestResolveRefuses(t *testing.T) {
	c := testConfig(t)
	c.PeerHost = "elsewhere"
	if _, err := c.Resolve(); err == nil {
		t.Error("resolved an unknown host")
	}
	c = testConfig(t)
	c.InterfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("172.20.0.7"), Mask: net.CIDRMask(16, 32)}}, nil
	}
	if _, err := c.Resolve(); err == nil {
		t.Error("resolved with no address on the phone system's network")
	}
	c = testConfig(t)
	c.Realm = ""
	if err := c.Render(Addresses{}); err == nil {
		t.Error("rendered without a domain")
	}
	c = testConfig(t)
	os.WriteFile(c.SecretFile, []byte("short"), 0o600)
	if err := c.Render(Addresses{}); err == nil {
		t.Error("rendered with a weak secret")
	}
}

// TestHealthy runs the health check against a fake coturn: a STUN answerer
// and a TLS listener serving one certificate, then another.
func TestHealthy(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := udp.ReadFrom(buf)
			if err != nil {
				return
			}
			if n >= 20 {
				resp := append([]byte(nil), buf[:20]...)
				binary.BigEndian.PutUint16(resp[0:], stunBindingSuccess)
				udp.WriteTo(resp, from)
			}
		}
	}()

	dir := t.TempDir()
	served := newCert(t, "*.example.com")
	deploy := func(c tls.Certificate) {
		v := filepath.Join(dir, "v"+c.Leaf.SerialNumber.String())
		os.MkdirAll(v, 0o755)
		os.WriteFile(filepath.Join(v, "fullchain.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate[0]}), 0o644)
		os.Remove(filepath.Join(dir, "current"))
		os.Symlink(filepath.Base(v), filepath.Join(dir, "current"))
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{served}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deploy(served)
	if err := stunPing(ctx, udp.LocalAddr().String()); err != nil {
		t.Errorf("STUN: %v", err)
	}
	if err := servesCurrent(ctx, ln.Addr().String(), "turn.example.com", dir); err != nil {
		t.Errorf("current certificate: %v", err)
	}
	deploy(newCert(t, "*.example.com")) // renewed, but not reloaded
	if err := servesCurrent(ctx, ln.Addr().String(), "turn.example.com", dir); err == nil {
		t.Error("healthy while serving an old certificate")
	}
	closed, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := closed.LocalAddr().String()
	closed.Close()
	short, cancel2 := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel2()
	if err := stunPing(short, addr); err == nil {
		t.Error("STUN answered by nothing")
	}
}

var serial int64

func newCert(t *testing.T, name string) tls.Certificate {
	serial++
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}
