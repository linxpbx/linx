package install

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// The first page's own certificate (docs/INSTALL.md §14 item 1, owner
// decision 2026-09-28): port 6464 is HTTPS with a temporary self-signed
// certificate for the server's addresses. The browser warns once; the
// terminal prints the certificate's fingerprint so the owner can check it's
// really their server before clicking past. linx setup makes it on the host
// and keeps it (FirstPageTLSDir) so the browser's exception survives a
// restart or a new link; install mode reads it from FirstPageTLSMount.

const (
	// FirstPageTLSDir is where linx setup keeps it on the host (root; the
	// key readable by the Linx services' group only).
	FirstPageTLSDir = "/etc/linx/install-tls"
	// FirstPageTLSMount is where install.yaml mounts that folder.
	FirstPageTLSMount = "/run/linx-install-tls"
	FirstPageCertFile = "cert.pem"
	FirstPageKeyFile  = "key.pem"
	// firstPageLifetime is how long one lasts; a new one is made when it
	// has less than firstPageRenew left.
	firstPageLifetime = 90 * 24 * time.Hour
	firstPageRenew    = 7 * 24 * time.Hour
)

// NewFirstPageCert makes the first page's certificate and key (PEM) for
// addrs.
func NewFirstPageCert(addrs []netip.Addr, now time.Time) (certPEM, keyPEM []byte, err error) {
	if len(addrs) == 0 {
		return nil, nil, errors.New("no address to make the installer's certificate for")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Linx installer (temporary)"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(firstPageLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, a := range addrs {
		tmpl.IPAddresses = append(tmpl.IPAddresses, net.IP(a.AsSlice()))
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	kder, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kder}), nil
}

func parseFirstPageCert(certPEM []byte) (*x509.Certificate, error) {
	b, _ := pem.Decode(certPEM)
	if b == nil || b.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(b.Bytes)
}

// Fingerprint is a certificate's SHA-256 fingerprint the way browsers show
// it: 32 pairs of hex digits separated by colons.
func Fingerprint(certPEM []byte) (string, error) {
	c, err := parseFirstPageCert(certPEM)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c.Raw)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":"), nil
}

// FirstPageCertUsable reports whether a kept certificate and key still do:
// they match, it lasts another week, and it names every one of addrs.
func FirstPageCertUsable(certPEM, keyPEM []byte, addrs []netip.Addr, now time.Time) bool {
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return false
	}
	c, err := parseFirstPageCert(certPEM)
	if err != nil || now.Before(c.NotBefore) || c.NotAfter.Sub(now) < firstPageRenew {
		return false
	}
	for _, a := range addrs {
		if !slices.ContainsFunc(c.IPAddresses, func(ip net.IP) bool { return ip.Equal(net.IP(a.AsSlice())) }) {
			return false
		}
	}
	return true
}
