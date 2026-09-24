package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"linxpbx.com/linx/internal/certs"
)

func testTLSCert(t *testing.T, serial int64) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "linx-sipws"},
		DNSNames: []string{"linx-sipws"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestDeployCert(t *testing.T) {
	dir := t.TempDir()
	store := certs.Store{Dir: dir}
	now := time.Now()
	for serial := int64(1); serial <= 3; serial++ {
		if err := deployCert(store, testTLSCert(t, serial), now.Add(time.Duration(serial)*time.Second)); err != nil {
			t.Fatal(err)
		}
		// What Asterisk loads: current/{fullchain,privkey}.pem, a matching pair.
		cur := filepath.Join(dir, certs.CurrentLink)
		pair, err := tls.LoadX509KeyPair(filepath.Join(cur, certs.FullchainFile), filepath.Join(cur, certs.PrivkeyFile))
		if err != nil {
			t.Fatal(err)
		}
		leaf, _ := x509.ParseCertificate(pair.Certificate[0])
		if leaf.SerialNumber.Int64() != serial || leaf.DNSNames[0] != "linx-sipws" {
			t.Fatalf("current is serial %v %v, want %d", leaf.SerialNumber, leaf.DNSNames, serial)
		}
		// The key is readable by the group (Asterisk's, through the volume's
		// setgid directory), never by others.
		st, err := os.Stat(filepath.Join(cur, certs.PrivkeyFile))
		if err != nil || st.Mode().Perm() != 0o640 {
			t.Fatalf("privkey.pem mode %v, %v", st.Mode().Perm(), err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 { // two versions and current
		t.Errorf("old versions not pruned: %d entries", len(entries))
	}
}
