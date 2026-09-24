package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"time"

	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/stepca"
)

// sipwsRenewer keeps the certificate for Asterisk's browser websocket
// (docs/WEB.md §2) current: issued from the internal CA like the ARI one,
// and written to dir (a memory-only volume Asterisk mounts read-only) in
// linx-certd's layout, which Asterisk's entrypoint watches. The control
// plane issues it so that Asterisk never holds a CA credential.
func sipwsRenewer(issuer stepca.Issuer, dir string, log *slog.Logger) *stepca.Renewer {
	store := certs.Store{Dir: dir}
	return &stepca.Renewer{
		Issuer:     issuer,
		CommonName: asteriskconf.DefaultSIPWSHost,
		DNSNames:   []string{asteriskconf.DefaultSIPWSHost},
		Log:        log,
		OnChange: func(c *tls.Certificate) {
			if err := deployCert(store, c, time.Now()); err != nil {
				log.Error("writing the phone websocket certificate for Asterisk", "err", err)
			}
		},
	}
}

// deployCert writes c as a new version in store and switches current to it.
func deployCert(store certs.Store, c *tls.Certificate, now time.Time) error {
	var chain []byte
	for _, der := range c.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	key, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		return fmt.Errorf("private key: %w", err)
	}
	leaf := c.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return err
		}
	}
	return store.Deploy(chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), certs.Meta{
		Names: leaf.DNSNames, Issuer: "linx internal CA", NotAfter: leaf.NotAfter, IssuedAt: now,
	})
}
