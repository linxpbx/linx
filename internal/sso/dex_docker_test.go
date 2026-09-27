package sso

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"linxpbx.com/linx/internal/auth"
)

// dexImage is Dex (Apache-2.0, ADR-052), the OpenID Connect provider the
// test signs in through.
const dexImage = "ghcr.io/dexidp/dex:v2.45.1@sha256:8499afd690c437f52301efd2b05b2455da5bd2dfc20332cd697dc9937f808462"

const (
	dexContainer = "linx-ssotest-dex"
	dexEmail     = "sara@example.com"
	dexPassword  = "sara's dex password"
	dexSecret    = "linx-test-client-secret"
)

// TestDexDocker signs in through a real OpenID Connect provider (Dex in a
// container, serving HTTPS with a test CA): the authorization code flow
// with PKCE, state and nonce, the code exchanged with the client secret,
// and the ID token's signature, issuer, audience and nonce checked by the
// same Client production uses (docs/ADMIN.md §6, §10). Then the parts that
// must fail: a replayed code, a wrong PKCE verifier, a wrong nonce, and an
// issuer that doesn't match. make test-docker.
func TestDexDocker(t *testing.T) {
	if os.Getenv("LINX_DOCKER_TESTS") != "1" {
		t.Skip("set LINX_DOCKER_TESTS=1 (make test-docker)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	exec.Command("docker", "rm", "--force", dexContainer).Run()
	if os.Getenv("LINX_KEEP_DEX") == "" {
		t.Cleanup(func() { exec.Command("docker", "rm", "--force", dexContainer).Run() })
	}

	port := freePort(t)
	issuer := fmt.Sprintf("https://127.0.0.1:%d/dex", port)
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	roots := writeDexTLS(t, dir)
	hash, err := bcrypt.GenerateFromPassword([]byte(dexPassword), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	redirect := RedirectURI("linx.test")
	cfg := fmt.Sprintf(`issuer: %s
storage:
  type: memory
web:
  https: 0.0.0.0:5554
  tlsCert: /etc/dex/tls.crt
  tlsKey: /etc/dex/tls.key
oauth2:
  skipApprovalScreen: true
staticClients:
  - id: linx
    secret: %s
    name: Linx
    redirectURIs: ["%s"]
enablePasswordDB: true
staticPasswords:
  - email: %s
    hash: "%s"
    username: sara
    userID: 7b1e0bbf-3a2f-4f4d-9f0b-2f1a8b0c1d2e
`, issuer, dexSecret, redirect, dexEmail, hash)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "run", "-d", "--name", dexContainer,
		"-p", fmt.Sprintf("127.0.0.1:%d:5554", port), "-v", dir+":/etc/dex:ro",
		dexImage, "dex", "serve", "/etc/dex/config.yaml").CombinedOutput(); err != nil {
		t.Fatalf("docker run dex: %v\n%s", err, out)
	}

	// Loopback is never allowed by the production SSRF guard; the test
	// trusts only its own CA instead (the guard has its own tests).
	httpClient := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	waitForDex(t, ctx, httpClient, issuer)

	st := newFakeStore()
	client := &Client{Store: st, Sealer: testSealer(), HTTP: httpClient, RedirectURI: redirect, Now: time.Now}
	tenant, id := uuid.New(), uuid.New()
	secret, err := testSealer().Seal(SealID(id), []byte(dexSecret))
	if err != nil {
		t.Fatal(err)
	}
	st.providers[id] = Provider{ID: id, TenantID: tenant, Kind: KindOIDC, Name: "Dex", Issuer: issuer, ClientID: "linx",
		ClientSecretEnc: secret, Enabled: true, Shown: true, Version: 1}

	// A full sign-in.
	state, nonce, verifier := auth.NewSecret(), auth.NewSecret(), auth.NewSecret()
	authURL, prov, err := client.Start(ctx, tenant, id, state, nonce, verifier)
	if err != nil || prov.Name != "Dex" {
		t.Fatalf("Start: %v %v", prov, err)
	}
	code := dexLogin(t, ctx, roots, authURL, redirect, state)
	gotProv, identity, err := client.Finish(ctx, tenant, id, code, verifier, nonce)
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if gotProv.ID != id || identity.Subject == "" || identity.Email != dexEmail || !identity.EmailVerified {
		t.Fatalf("identity = %+v from %+v", identity, gotProv)
	}

	// A code works once.
	if _, _, err := client.Finish(ctx, tenant, id, code, verifier, nonce); err == nil {
		t.Fatal("a code was accepted twice")
	}

	// The PKCE verifier must match the challenge sent at the start.
	state, nonce, verifier = auth.NewSecret(), auth.NewSecret(), auth.NewSecret()
	authURL, _, _ = client.Start(ctx, tenant, id, state, nonce, verifier)
	code = dexLogin(t, ctx, roots, authURL, redirect, state)
	if _, _, err := client.Finish(ctx, tenant, id, code, auth.NewSecret(), nonce); err == nil {
		t.Fatal("a wrong PKCE verifier was accepted")
	}

	// The ID token's nonce must be this flow's.
	state, nonce, verifier = auth.NewSecret(), auth.NewSecret(), auth.NewSecret()
	authURL, _, _ = client.Start(ctx, tenant, id, state, nonce, verifier)
	code = dexLogin(t, ctx, roots, authURL, redirect, state)
	if _, _, err := client.Finish(ctx, tenant, id, code, verifier, auth.NewSecret()); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("wrong nonce: %v", err)
	}

	// An issuer that names itself differently is refused at discovery.
	if _, err := client.Discover(ctx, strings.Replace(issuer, "127.0.0.1", "localhost", 1)); err == nil {
		t.Fatal("an issuer mismatch was accepted")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// writeDexTLS writes a test CA's certificate for 127.0.0.1 into dir as
// tls.crt/tls.key, and returns the CA as a pool.
func writeDexTLS(t *testing.T, dir string) *x509.CertPool {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Linx SSO test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	// Readable by Dex's non-root user; a throwaway key in a temp dir.
	if err := os.WriteFile(filepath.Join(dir, "tls.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o644); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool
}

func waitForDex(t *testing.T, ctx context.Context, c *http.Client, issuer string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := c.Get(issuer + "/.well-known/openid-configuration")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			out, _ := exec.Command("docker", "logs", "--tail", "30", dexContainer).CombinedOutput()
			t.Fatalf("Dex didn't start: %v\n%s", err, out)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// dexLogin is the person in the browser: follow the provider's pages, type
// the email and password, and stop at the redirect back to Linx, returning
// its code.
func dexLogin(t *testing.T, ctx context.Context, roots *x509.CertPool, authURL, redirect, state string) string {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	back, _ := url.Parse(redirect)
	var final *url.URL
	c := &http.Client{Jar: jar, Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(r *http.Request, _ []*http.Request) error {
			if r.URL.Host == back.Host {
				final = r.URL
				return http.ErrUseLastResponse
			}
			return nil
		}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, authURL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("opening the provider's sign-in page: %v", err)
	}
	resp.Body.Close()
	loginPage := resp.Request.URL
	if final == nil {
		form := url.Values{"login": {dexEmail}, "password": {dexPassword}}
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, loginPage.String(), strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err = c.Do(req)
		if err != nil {
			t.Fatalf("signing in at the provider: %v", err)
		}
		resp.Body.Close()
	}
	if final == nil {
		t.Fatalf("the provider didn't send the browser back (last page %s, status %d)", resp.Request.URL, resp.StatusCode)
	}
	if final.Path != CallbackPath || final.Query().Get("state") != state || final.Query().Get("code") == "" {
		t.Fatalf("redirect back = %s", final)
	}
	return final.Query().Get("code")
}
