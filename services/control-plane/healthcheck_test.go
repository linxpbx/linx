package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"linxpbx.com/linx/internal/certs"
)

func deployTestCert(t *testing.T, dir, name string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
	if err := (certs.Store{Dir: dir}).Deploy(chain, key, certs.Meta{Names: []string{name}, NotAfter: tmpl.NotAfter, IssuedAt: now}); err != nil {
		t.Fatal(err)
	}
}

func TestRunHealthcheck(t *testing.T) {
	dir := t.TempDir()
	deployTestCert(t, dir, "*.example.com")
	serving := &certs.ServingCert{Dir: dir}

	status := http.StatusOK
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer health.Close()
	https := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	https.TLS = &tls.Config{GetCertificate: serving.GetCertificate}
	https.StartTLS()
	defer https.Close()

	_, healthPort, _ := net.SplitHostPort(health.Listener.Addr().String())
	_, httpsPort, _ := net.SplitHostPort(https.Listener.Addr().String())
	env := func(k string) string {
		return map[string]string{"LINX_HEALTH_ADDR": "127.0.0.1:" + healthPort, "LINX_LISTEN_ADDR": ":" + httpsPort,
			"LINX_CERTS_DIR": dir, "LINX_DOMAIN": "example.com"}[k]
	}
	if code := runHealthcheck(env, nil); code != 0 {
		t.Errorf("healthy server: exit %d", code)
	}

	// certd renews: the server picks it up on the next handshake by itself.
	deployTestCert(t, dir, "*.example.com")
	if code := runHealthcheck(env, nil); code != 0 {
		t.Errorf("after a renewal: exit %d", code)
	}
	// A server still holding the old certificate is unhealthy.
	stale := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	old, _ := serving.Current()
	stale.TLS = &tls.Config{Certificates: []tls.Certificate{*old}}
	stale.StartTLS()
	defer stale.Close()
	deployTestCert(t, dir, "*.example.com")
	_, stalePort, _ := net.SplitHostPort(stale.Listener.Addr().String())
	staleEnv := func(k string) string {
		if k == "LINX_LISTEN_ADDR" {
			return ":" + stalePort
		}
		return env(k)
	}
	if code := runHealthcheck(staleEnv, nil); code != 1 {
		t.Errorf("serving a replaced certificate: exit %d", code)
	}

	status = http.StatusServiceUnavailable
	if code := runHealthcheck(env, nil); code != 1 {
		t.Errorf("unhealthy server: exit %d", code)
	}
	health.Close()
	if code := runHealthcheck(env, nil); code != 1 {
		t.Errorf("no server: exit %d", code)
	}
}
