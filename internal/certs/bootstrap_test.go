package certs

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/go-acme/lego/v4/acme"
)

func TestBootstrapFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	b, err := BootstrapFromEnv(env(map[string]string{"LINX_DOMAIN": " Example.COM ", "LINX_ACME_EMAIL": "o@example.com"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(b.Names()); got != "[example.com turn.example.com]" {
		t.Errorf("names: %s", got)
	}
	if b.ChallengeDir == "" || b.Directory != "" {
		t.Errorf("defaults: %+v", b)
	}
	for _, bad := range []map[string]string{
		{"LINX_ACME_EMAIL": "o@example.com"},
		{"LINX_DOMAIN": "example.com"},
		{"LINX_DOMAIN": "example.com", "LINX_ACME_EMAIL": "o@example.com", "LINX_ACME_TEST_DIRECTORY": "http://pebble/dir"},
	} {
		if _, err := BootstrapFromEnv(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestChallengeWriterAndChallenges(t *testing.T) {
	dir := t.TempDir()
	w, c := ChallengeWriter{Dir: dir}, Challenges{Dir: dir}
	if _, err := c.Certificate("meet.example.com"); !errors.Is(err, ErrNoChallenge) {
		t.Fatalf("before: %v", err)
	}
	if err := w.Present("meet.example.com", "token", "key-authorization"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(ChallengeFile(dir, "meet.example.com")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", fi, err)
	}
	cert, err := c.Certificate("MEET.example.com.")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range leaf.Extensions {
		found = found || e.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31})
	}
	if !found || leaf.DNSNames[0] != "meet.example.com" {
		t.Errorf("not an acme-tls/1 certificate for the name: %v", leaf.DNSNames)
	}
	for _, bad := range []string{"../meet.example.com", "api.example.com", ""} {
		if _, err := c.Certificate(bad); err == nil {
			t.Errorf("%q answered", bad)
		}
	}
	if err := w.CleanUp("meet.example.com", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := w.CleanUp("meet.example.com", "", ""); err != nil {
		t.Errorf("second clean-up: %v", err)
	}
	if _, err := c.Certificate("meet.example.com"); err == nil {
		t.Error("still answers after clean-up")
	}
	if err := w.Present("../x", "t", "k"); err == nil {
		t.Error("wrote a path")
	}
}

func TestIsChallenge(t *testing.T) {
	for protos, want := range map[string]bool{"acme-tls/1": true, "h2,acme-tls/1": false, "": false, "h2": false} {
		var ps []string
		if protos != "" {
			ps = splitComma(protos)
		}
		if got := IsChallenge(&tls.ClientHelloInfo{SupportedProtos: ps}); got != want {
			t.Errorf("%q: %v", protos, got)
		}
	}
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := range len(s) + 1 {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}

func TestClassify(t *testing.T) {
	sub := func(typ, detail string) error {
		return fmt.Errorf("error: one or more domains had a problem:\n[meet.example.com] %w", &acme.ProblemDetails{
			Type: "urn:ietf:params:acme:error:unauthorized", Detail: "outer",
			SubProblems: []acme.SubProblem{{Type: "urn:ietf:params:acme:error:" + typ, Detail: detail}},
		})
	}
	for _, tc := range []struct {
		err  error
		kind string
	}{
		{sub("connection", "203.0.113.5: Timeout during connect (likely firewall problem)"), ProblemConnection},
		{sub("dns", "DNS problem: NXDOMAIN looking up A for meet.example.com"), ProblemDNS},
		{sub("unauthorized", "Incorrect validation certificate for tls-alpn-01 challenge"), ProblemWrongAnswer},
		{sub("tls", "remote error: tls: no application protocol"), ProblemWrongAnswer},
		{&acme.ProblemDetails{Type: "urn:ietf:params:acme:error:rateLimited", Detail: "too many failed authorizations"}, ProblemRateLimited},
		{errors.New("acme: error: 400 :: urn:ietf:params:acme:error:connection :: 203.0.113.5: Connection refused, url: x"), ProblemConnection},
		{errors.New("dial tcp: lookup acme-v02.api.letsencrypt.org: no such host"), ProblemOther},
	} {
		if got := Classify(tc.err); got.Kind != tc.kind || got.Detail == "" {
			t.Errorf("%v: %+v, want %s", tc.err, got, tc.kind)
		}
	}
	if got := Classify(errors.New("acme: error: 400 :: urn:ietf:params:acme:error:connection :: 203.0.113.5: Connection refused, url: x")); got.Detail != "203.0.113.5: Connection refused" {
		t.Errorf("detail from text: %q", got.Detail)
	}
}

// Renewing without a DNS token (docs/INSTALL.md §5): the names Let's
// Encrypt can reach on port 443, real certificates only, no token needed.
func TestALPNConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := ConfigFromEnv(env(map[string]string{"LINX_DOMAIN": "pbx.example.com", "LINX_DNS_PROVIDER": "cloudflare",
		"LINX_ACME_EMAIL": "o@example.com", "LINX_ACME_STAGING": "false", "LINX_CERT_CHALLENGE": "tls-alpn-01", "LINX_DNS_TOKEN_FILE": " "}))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(c.Names()); got != "[pbx.example.com turn.pbx.example.com]" {
		t.Errorf("names: %s", got)
	}
	if ALPNIssuer(c).ID() != IssuerLE {
		t.Error("issuer")
	}
	for _, bad := range []map[string]string{
		{"LINX_CERT_CHALLENGE": "tls-alpn-01", "LINX_ACME_STAGING": "true"},
		{"LINX_CERT_CHALLENGE": "http-01", "LINX_ACME_STAGING": "false"},
	} {
		bad["LINX_DOMAIN"], bad["LINX_DNS_PROVIDER"], bad["LINX_ACME_EMAIL"] = "pbx.example.com", "cloudflare", "o@example.com"
		if _, err := ConfigFromEnv(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
