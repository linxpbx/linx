package install

import (
	"crypto/tls"
	"crypto/x509"
	"net/netip"
	"regexp"
	"testing"
	"time"
)

func TestFirstPageCert(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	addrs := []netip.Addr{netip.MustParseAddr("192.168.1.20"), netip.MustParseAddr("203.0.113.5")}
	certPEM, keyPEM, err := NewFirstPageCert(addrs, now)
	if err != nil {
		t.Fatal(err)
	}
	if !FirstPageCertUsable(certPEM, keyPEM, addrs, now) {
		t.Fatal("a new one isn't usable")
	}
	fp, err := Fingerprint(certPEM)
	if err != nil || !regexp.MustCompile(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`).MatchString(fp) {
		t.Errorf("fingerprint %q %v", fp, err)
	}
	// It verifies as itself for each address (what a browser does once
	// told to trust it), and for nothing else.
	pair, _ := tls.X509KeyPair(certPEM, keyPEM)
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	for _, a := range addrs {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: a.String(), CurrentTime: now}); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "10.0.0.1", CurrentTime: now}); err == nil {
		t.Error("verified for another address")
	}

	other, _, _ := NewFirstPageCert(addrs, now)
	for name, ok := range map[string]bool{
		"another address":   FirstPageCertUsable(certPEM, keyPEM, append(addrs, netip.MustParseAddr("198.51.100.1")), now),
		"nearly expired":    FirstPageCertUsable(certPEM, keyPEM, addrs, now.Add(85*24*time.Hour)),
		"key doesn't fit":   FirstPageCertUsable(other, keyPEM, addrs, now),
		"not a certificate": FirstPageCertUsable([]byte("x"), keyPEM, addrs, now),
	} {
		if ok {
			t.Errorf("%s: usable", name)
		}
	}
	if _, _, err := NewFirstPageCert(nil, now); err == nil {
		t.Error("made one for no address")
	}
}
