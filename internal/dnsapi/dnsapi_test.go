package dnsapi

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestCompaniesAreTheTen(t *testing.T) {
	want := []string{Cloudflare, DuckDNS, Route53, GoDaddy, Namecheap, Porkbun, DigitalOcean, Hetzner, DeSEC, OVH}
	if !slices.Equal(IDs(), want) {
		t.Fatalf("IDs() = %v, want %v", IDs(), want)
	}
	for _, c := range Companies {
		if c.TTL <= 0 || c.Where == "" || len(c.Fields) == 0 || c.Name == "" {
			t.Errorf("%s: incomplete %+v", c.ID, c)
		}
		if b, ok := ByName(c.Name); !ok || b.ID != c.ID {
			t.Errorf("ByName(%q) = %v, %v", c.Name, b.ID, ok)
		}
	}
}

// The web forms read the list from this file (web/src/lib/dnsCompanies.ts).
func TestCompaniesFixture(t *testing.T) {
	const fixture = "../../web/src/lib/dns-companies.json"
	want, err := json.MarshalIndent(Companies, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if os.Getenv("UPDATE_FIXTURES") != "" {
		if err := os.WriteFile(fixture, want, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(fixture)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("%s is stale (%v): run UPDATE_FIXTURES=1 go test ./internal/dnsapi -run TestCompaniesFixture", fixture, err)
	}
}

func TestKeyEncodeDecode(t *testing.T) {
	cf, _ := Find(Cloudflare)
	s, err := cf.Encode(Key{"token": "  abcdefghijklmnopqrstuvwxyz0123456789  "})
	if err != nil || s != "abcdefghijklmnopqrstuvwxyz0123456789" {
		t.Fatalf("one field is the value itself, as tokens always were: %q, %v", s, err)
	}
	if k, err := cf.Decode(s + "\n"); err != nil || k["token"] != s {
		t.Fatalf("Decode = %v, %v", k, err)
	}

	pb, _ := Find(Porkbun)
	s, err = pb.Encode(Key{"secret_api_key": "sk1_secretsecret", "api_key": "pk1_keykeykey"})
	if err != nil || s != `{"api_key":"pk1_keykeykey","secret_api_key":"sk1_secretsecret"}` {
		t.Fatalf("two fields are JSON, keys sorted: %q, %v", s, err)
	}
	if k, err := pb.Decode(s); err != nil || k["api_key"] != "pk1_keykeykey" {
		t.Fatalf("Decode = %v, %v", k, err)
	}
	if _, err := pb.Decode("pk1_keykeykey"); err == nil || strings.Contains(err.Error(), "pk1_") {
		t.Errorf("a bare token for Porkbun: %v (never echoing the secret)", err)
	}
}

func TestKeyCheck(t *testing.T) {
	ovh, _ := Find(OVH)
	good := Key{"endpoint": "ovh-eu", "application_key": "ak", "application_secret": "secretsecret", "consumer_key": "consumerkey"}
	if bad := ovh.Check(good); bad != nil {
		t.Fatalf("good OVH key: %v", bad)
	}
	for name, tc := range map[string]struct {
		k     Key
		field string
	}{
		"missing":       {Key{"endpoint": "ovh-eu", "application_key": "ak", "application_secret": "secretsecret"}, "consumer_key"},
		"not an option": {Key{"endpoint": "ovh-xx", "application_key": "ak", "application_secret": "secretsecret", "consumer_key": "consumerkey"}, "endpoint"},
		"space inside":  {Key{"endpoint": "ovh-eu", "application_key": "a k", "application_secret": "secretsecret", "consumer_key": "consumerkey"}, "application_key"},
		"newline":       {Key{"endpoint": "ovh-eu", "application_key": "ak", "application_secret": "secret\nsecret", "consumer_key": "consumerkey"}, "application_secret"},
		"short secret":  {Key{"endpoint": "ovh-eu", "application_key": "ak", "application_secret": "short", "consumer_key": "consumerkey"}, "application_secret"},
		"extra field":   {Key{"endpoint": "ovh-eu", "application_key": "ak", "application_secret": "secretsecret", "consumer_key": "consumerkey", "token": "x"}, "token"},
	} {
		if bad := ovh.Check(tc.k); bad[tc.field] == "" {
			t.Errorf("%s: Check = %v, want a problem with %s", name, bad, tc.field)
		}
	}
}

func TestRelative(t *testing.T) {
	for _, tc := range [][3]string{
		{"example.com", "example.com", "@"},
		{"pbx.example.com", "example.com", "pbx"},
		{"_acme-challenge.pbx.example.com.", "example.com", "_acme-challenge.pbx"},
		{"Turn.Example.com", "example.com.", "turn"},
	} {
		if got := Relative(tc[0], tc[1]); got != tc[2] {
			t.Errorf("Relative(%q, %q) = %q, want %q", tc[0], tc[1], got, tc[2])
		}
	}
}
