package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/registration"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigFromEnv(t *testing.T) {
	base := map[string]string{"LINX_DOMAIN": "PBX.Example.com", "LINX_DNS_PROVIDER": "cloudflare"}
	c, err := ConfigFromEnv(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.Domain != "pbx.example.com" || !c.Staging || !c.Wildcard || c.CertsDir != "/var/lib/linx/certs" ||
		c.TokenFile != "/run/secrets/linx_dns_token" {
		t.Errorf("defaults: %+v", c)
	}

	tests := []struct {
		name string
		set  map[string]string
		want string // substring of the error, "" for ok
	}{
		{"production needs email", map[string]string{"LINX_ACME_STAGING": "false"}, "LINX_ACME_EMAIL"},
		{"production with email", map[string]string{"LINX_ACME_STAGING": "false", "LINX_ACME_EMAIL": "a@example.com"}, ""},
		{"bad email", map[string]string{"LINX_ACME_EMAIL": "nope"}, "not an email"},
		{"bad bool", map[string]string{"LINX_CERT_WILDCARD": "maybe"}, "LINX_CERT_WILDCARD"},
		{"wildcard domain", map[string]string{"LINX_DOMAIN": "*.example.com"}, "LINX_DOMAIN"},
		{"single label", map[string]string{"LINX_DOMAIN": "localhost"}, "LINX_DOMAIN"},
		{"unknown provider", map[string]string{"LINX_DNS_PROVIDER": "desec"}, "LINX_DNS_PROVIDER"},
		{"duckdns suffix", map[string]string{"LINX_DNS_PROVIDER": "duckdns"}, "duckdns.org"},
		{"duckdns ok", map[string]string{"LINX_DNS_PROVIDER": "duckdns", "LINX_DOMAIN": "mypbx.duckdns.org"}, ""},
		{"zone token not cloudflare", map[string]string{"LINX_DNS_PROVIDER": "duckdns", "LINX_DOMAIN": "x.duckdns.org", "LINX_DNS_ZONE_TOKEN_FILE": "/x"}, "only used with Cloudflare"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]string{}
			for k, v := range base {
				m[k] = v
			}
			for k, v := range tt.set {
				m[k] = v
			}
			_, err := ConfigFromEnv(env(m))
			if tt.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
				t.Fatalf("error %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

func TestNames(t *testing.T) {
	c := Config{Domain: "pbx.example.com", Wildcard: true}
	if got := c.Names(); len(got) != 1 || got[0] != "*.pbx.example.com" {
		t.Errorf("wildcard names = %v", got)
	}
	c.Wildcard = false
	got := c.Names()
	if len(got) != len(Hostnames) || got[0] != "admin.pbx.example.com" {
		t.Errorf("named = %v", got)
	}
	for _, n := range got {
		if strings.HasPrefix(n, "tunnel.") {
			t.Error("tunnel. is reserved and must not be issued yet")
		}
	}
}

func TestReadSecretNeverLeaks(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tok")
	if err := os.WriteFile(p, []byte("  s3cret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := readSecret(p); err != nil || s != "s3cret-token" {
		t.Fatalf("readSecret = %q, %v", s, err)
	}
	_ = os.WriteFile(p, []byte("\n"), 0o600)
	if _, err := readSecret(p); err == nil {
		t.Error("empty secret accepted")
	}
}

// selfSigned returns a PEM chain and key valid until notAfter.
func selfSigned(t *testing.T, name string, notAfter time.Time) (chain, key []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
}

func TestStoreDeploy(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if d, err := s.Current(); d != nil || err != nil {
		t.Fatalf("empty store: %v, %v", d, err)
	}
	if err := s.Deploy([]byte("junk"), nil, Meta{IssuedAt: time.Now()}); err == nil {
		t.Fatal("deployed a non-certificate")
	}

	start := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	for i := range 4 {
		exp := start.AddDate(0, 3, i)
		chain, key := selfSigned(t, "*.pbx.example.com", exp)
		m := Meta{Names: []string{"*.pbx.example.com"}, Issuer: IssuerLEStaging, Staging: true, NotAfter: exp, IssuedAt: start.Add(time.Duration(i) * time.Hour)}
		if err := s.Deploy(chain, key, m); err != nil {
			t.Fatal(err)
		}
		d, err := s.Current()
		if err != nil {
			t.Fatal(err)
		}
		if !d.Leaf.NotAfter.Equal(exp) || d.Meta.Issuer != IssuerLEStaging {
			t.Fatalf("deploy %d: current = %+v", i, d.Meta)
		}
	}

	entries, _ := os.ReadDir(s.Dir)
	var versions int
	for _, e := range entries {
		if isVersion(e.Name()) {
			versions++
		}
		if e.Name() == CurrentLink+".tmp" {
			t.Error("temporary symlink left behind")
		}
	}
	if versions != keepVersions {
		t.Errorf("kept %d versions, want %d", versions, keepVersions)
	}
	fi, err := os.Stat(filepath.Join(s.Dir, CurrentLink, PrivkeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o007 != 0 {
		t.Errorf("private key is world-accessible: %v", fi.Mode())
	}
}

type fakeIssuer struct {
	id    string
	err   error
	chain []byte
	key   []byte
	calls int
}

func (f *fakeIssuer) ID() string { return f.id }
func (f *fakeIssuer) Obtain(context.Context, []string) (*Issued, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return &Issued{Chain: f.chain, Key: f.key}, nil
}

func testManager(t *testing.T, staging bool, now time.Time, issuers ...Issuer) *Manager {
	return &Manager{
		Config:  Config{Domain: "pbx.example.com", Wildcard: true, Staging: staging},
		Store:   Store{Dir: t.TempDir()},
		Issuers: issuers,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		now:     func() time.Time { return now },
	}
}

func TestManagerFallbackAndRenewal(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	chain, key := selfSigned(t, "*.pbx.example.com", now.AddDate(0, 0, 90))
	le := &fakeIssuer{id: IssuerLE, err: errors.New("rate limited")}
	zs := &fakeIssuer{id: IssuerZeroSSL, chain: chain, key: key}
	m := testManager(t, false, now, le, zs)
	var deployed []Meta
	m.OnDeploy = func(meta Meta) { deployed = append(deployed, meta) }

	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if le.calls != 1 || zs.calls != 1 || len(deployed) != 1 || deployed[0].Issuer != IssuerZeroSSL {
		t.Fatalf("fallback: le=%d zs=%d deployed=%v", le.calls, zs.calls, deployed)
	}

	// Fresh certificate: nothing to do.
	if err := m.Check(context.Background()); err != nil || zs.calls != 1 {
		t.Fatalf("renewed a fresh certificate (calls=%d, err=%v)", zs.calls, err)
	}

	// 29 days left: renew, and Let's Encrypt is tried first again.
	m.now = func() time.Time { return now.AddDate(0, 0, 61) }
	le.err, le.chain, le.key = nil, chain, key
	if err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if le.calls != 2 || zs.calls != 1 || m.Snapshot().Issuer != IssuerLE {
		t.Fatalf("renewal: le=%d zs=%d stats=%+v", le.calls, zs.calls, m.Snapshot())
	}
}

func TestManagerAllFail(t *testing.T) {
	now := time.Now()
	a := &fakeIssuer{id: IssuerLE, err: errors.New("down")}
	b := &fakeIssuer{id: IssuerZeroSSL, err: errors.New("also down")}
	m := testManager(t, false, now, a, b)
	err := m.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "letsencrypt: down") || !strings.Contains(err.Error(), "zerossl: also down") {
		t.Fatalf("err = %v", err)
	}
	if m.Snapshot().Failures != 1 {
		t.Errorf("failures = %d", m.Snapshot().Failures)
	}
}

func TestRenewReason(t *testing.T) {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	m := testManager(t, true, now)
	fresh := &Deployed{
		Meta: Meta{Names: []string{"*.pbx.example.com"}, Staging: true},
		Leaf: &x509.Certificate{NotAfter: now.AddDate(0, 0, 60)},
	}
	if r := m.renewReason(fresh); r != "" {
		t.Errorf("fresh: %q", r)
	}
	if r := m.renewReason(nil); r == "" {
		t.Error("missing certificate not renewed")
	}
	moved := *fresh
	moved.Meta.Names = []string{"*.other.example.com"}
	if r := m.renewReason(&moved); r != "hostnames changed" {
		t.Errorf("names: %q", r)
	}
	m.Config.Staging = false
	if r := m.renewReason(fresh); !strings.Contains(r, "trusted") {
		t.Errorf("staging→production: %q", r)
	}
	m.Config.Staging = true
	old := *fresh
	old.Leaf = &x509.Certificate{NotAfter: now.AddDate(0, 0, 30)}
	if r := m.renewReason(&old); r == "" {
		t.Error("30 days left not renewed")
	}
}

func TestBackoff(t *testing.T) {
	want := map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 20: retryMax, 1000: retryMax}
	for n, w := range want {
		if got := Backoff(n); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, w)
		}
	}
	for range 100 {
		if j := jitter(time.Hour); j < 54*time.Minute || j > 66*time.Minute {
			t.Fatalf("jitter out of ±10%%: %v", j)
		}
	}
}

func TestZeroSSLEAB(t *testing.T) {
	var gotEmail string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotEmail = r.PostForm.Get("email")
		if gotEmail == "denied@example.com" {
			_, _ = io.WriteString(w, `{"success":false}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"eab_kid":"kid1","eab_hmac_key":"hmac1"}`)
	}))
	defer srv.Close()

	kid, hmac, err := zeroSSLEAB(context.Background(), srv.Client(), srv.URL, "a@example.com")
	if err != nil || kid != "kid1" || hmac != "hmac1" || gotEmail != "a@example.com" {
		t.Fatalf("got %q %q %v (email %q)", kid, hmac, err, gotEmail)
	}
	if _, _, err := zeroSSLEAB(context.Background(), srv.Client(), srv.URL, "denied@example.com"); err == nil {
		t.Error("refusal accepted")
	}
}

func TestAccountPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "acct")
	a, err := loadAccount(dir, "a@example.com")
	if err != nil || a.Registration != nil || a.key == nil {
		t.Fatalf("new account: %+v, %v", a, err)
	}
	a.Registration = &registration.Resource{URI: "https://ca.example/acct/1"}
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	b, err := loadAccount(dir, "a@example.com")
	if err != nil || b.Registration == nil || b.Registration.URI != "https://ca.example/acct/1" {
		t.Fatalf("reload: %+v, %v", b, err)
	}
	if !a.key.(*ecdsa.PrivateKey).Equal(b.key) {
		t.Error("account key changed on reload")
	}
	c, err := loadAccount(dir, "new@example.com")
	if err != nil || c.Registration != nil {
		t.Errorf("email change should re-register: %+v, %v", c, err)
	}
	fi, _ := os.Stat(filepath.Join(dir, "key.pem"))
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("account key mode %v", fi.Mode())
	}
}

func TestMetrics(t *testing.T) {
	m := testManager(t, true, time.Now())
	m.stats = Stats{NotAfter: time.Unix(1790000000, 0), Issuer: IssuerLEStaging, Failures: 2}
	rec := httptest.NewRecorder()
	MetricsHandler(m).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`linx_cert_expiry_timestamp_seconds{names="*.pbx.example.com",issuer="letsencrypt-staging"} 1790000000`,
		"linx_cert_renewal_failures_total 2",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "last_renewal") {
		t.Error("last renewal reported before any renewal")
	}
}
