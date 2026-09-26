// Package trunkcert makes the certificate a phone system on the LAN (like
// the Grandstream UCM6304) serves to Linx over TLS (docs/TRUNKS.md §6,
// ADR-045): `linx trunk cert ADDRESS` writes it and its key for the admin to
// upload to that system, and `linx trunk add --pin` pins the certificate.
// Linx never keeps the key.
//
// Asterisk checks that a trunk's certificate names the exact address it
// dials (an IP address entry for an address, never a wildcard, RFC 5922
// §7.2), which LAN phone systems' built-in certificates usually don't.
package trunkcert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"time"
)

// Validity is how long a certificate lasts. Linx's doctor warns 30 days
// before a pinned certificate expires; making a new one is the same
// command again.
const Validity = 3 * 365 * 24 * time.Hour

// hostPattern is a DNS name (internal/trunk's rule for a trunk's host).
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62})(\.[A-Za-z0-9]([A-Za-z0-9-]{0,62}))*$`)

// Certificate is a made certificate: both PEM files and the certificate's
// SHA-256 fingerprint, written as `linx trunk add` shows it.
type Certificate struct {
	CertPEM, KeyPEM []byte
	Fingerprint     string
	NotAfter        time.Time
}

// New makes a self-signed certificate naming exactly address (an IPv4
// address or a DNS name). RSA 2048, since phone systems' TLS stacks don't
// all take elliptic-curve keys. It is its own CA (basic constraints CA,
// allowed to sign certificates), which is what OpenSSL needs to trust it as
// a pin; nothing else holds its key, so it can vouch only for itself.
func New(address string, now time.Time) (Certificate, error) {
	address = strings.TrimSuffix(strings.TrimSpace(address), ".")
	tmpl := &x509.Certificate{
		Subject:               pkix.Name{CommonName: address, Organization: []string{"Linx phone line"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(Validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	if a, err := netip.ParseAddr(address); err == nil {
		if !a.Is4() {
			return Certificate{}, errors.New("give an IPv4 address or a name: phone lines use IPv4")
		}
		tmpl.IPAddresses = []net.IP{a.AsSlice()}
	} else if len(address) <= 253 && hostPattern.MatchString(address) {
		tmpl.DNSNames = []string{address}
	} else {
		return Certificate{}, fmt.Errorf("%q isn't an IPv4 address or a name", address)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return Certificate{}, err
	}
	tmpl.SerialNumber = serial
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return Certificate{}, err
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Certificate{}, err
	}
	return Certificate{
		CertPEM:     pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:      pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		Fingerprint: Fingerprint(der),
		NotAfter:    tmpl.NotAfter,
	}, nil
}

// Fingerprint is a certificate's SHA-256, "AB:CD:…", as trunkprobe writes it.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
