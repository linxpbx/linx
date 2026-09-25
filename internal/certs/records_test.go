package certs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func tokenFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeCloudflare is the part of Cloudflare's API PointRecords uses.
type fakeCloudflare struct {
	mu      sync.Mutex
	records map[string]cfRecord // by name
	writes  []string
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "result": v}) }
	if r.Header.Get("Authorization") != "Bearer test-token" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]string{{"message": "Invalid API token"}}})
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/zones":
		if r.URL.Query().Get("name") == "example.com" {
			ok([]map[string]string{{"id": "zone1"}})
		} else {
			ok([]any{})
		}
	case r.Method == "GET" && r.URL.Path == "/zones/zone1/dns_records":
		var out []cfRecord
		if rec, found := f.records[r.URL.Query().Get("name")]; found {
			out = append(out, rec)
		}
		ok(out)
	case r.Method == "POST" || r.Method == "PUT":
		b, _ := io.ReadAll(r.Body)
		var rec cfRecord
		json.Unmarshal(b, &rec)
		if strings.Contains(string(b), `"proxied":true`) {
			http.Error(w, "proxied", http.StatusBadRequest)
			return
		}
		rec.ID = "rec-" + rec.Name
		f.records[rec.Name] = rec
		f.writes = append(f.writes, r.Method+" "+rec.Name)
		ok(rec)
	default:
		http.NotFound(w, r)
	}
}

func TestPointRecordsCloudflare(t *testing.T) {
	cf := &fakeCloudflare{records: map[string]cfRecord{
		"api.pbx.example.com":  {ID: "a1", Type: "A", Name: "api.pbx.example.com", Content: "198.51.100.1"},
		"turn.pbx.example.com": {ID: "c1", Type: "CNAME", Name: "turn.pbx.example.com", Content: "home.example.net"},
		"sip.pbx.example.com":  {ID: "a2", Type: "A", Name: "sip.pbx.example.com", Content: "203.0.113.9"},
	}}
	srv := httptest.NewServer(cf)
	defer srv.Close()
	c := &RecordsClient{HTTP: srv.Client(), CloudflareAPI: srv.URL}
	cfg := Config{Domain: "pbx.example.com", Provider: ProviderCloudflare, TokenFile: tokenFile(t)}
	ip := netip.MustParseAddr("203.0.113.9")

	res, err := c.PointRecords(context.Background(), cfg, []string{"meet", "api", "turn", "sip"}, ip)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"created", "changed", "left as is", "already points"}
	for i, r := range res {
		if !strings.HasPrefix(r.Outcome, want[i]) {
			t.Errorf("%s: %q, want %s…", r.Name, r.Outcome, want[i])
		}
	}
	if got := strings.Join(cf.writes, ","); got != "POST meet.pbx.example.com,PUT api.pbx.example.com" {
		t.Errorf("writes = %s", got)
	}

	cfg.TokenFile = filepath.Join(t.TempDir(), "missing")
	if _, err := c.PointRecords(context.Background(), cfg, []string{"meet"}, ip); err == nil {
		t.Error("no error without a token")
	}
}

func TestPointRecordsDuckDNS(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.RawQuery
		if r.URL.Query().Get("token") != "test-token" {
			io.WriteString(w, "KO")
			return
		}
		io.WriteString(w, "OK")
	}))
	defer srv.Close()
	c := &RecordsClient{HTTP: srv.Client(), DuckDNSAPI: srv.URL}
	cfg := Config{Domain: "lab.myhome.duckdns.org", Provider: ProviderDuckDNS, TokenFile: tokenFile(t)}
	res, err := c.PointRecords(context.Background(), cfg, []string{"meet", "turn"}, netip.MustParseAddr("203.0.113.9"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "domains=myhome") || !strings.Contains(got, "ip=203.0.113.9") || len(res) != 2 {
		t.Errorf("query %q, results %v", got, res)
	}
}

func TestPublicIPv4(t *testing.T) {
	body := "fl=1\nh=1.1.1.1\nip=203.0.113.44\nts=1\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer srv.Close()
	c := &RecordsClient{HTTP: srv.Client(), TraceURL: srv.URL}
	a, err := c.PublicIPv4(context.Background())
	if err != nil || a.String() != "203.0.113.44" {
		t.Fatalf("%v, %v", a, err)
	}
	body = "ip=192.168.1.20\n"
	if _, err := c.PublicIPv4(context.Background()); err == nil {
		t.Error("a private address accepted as public")
	}
}
