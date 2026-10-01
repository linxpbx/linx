package certs

import (
	"context"
	"encoding/json"
	"errors"
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
	case r.Method == "PATCH":
		b, _ := io.ReadAll(r.Body)
		var patch cfRecord
		json.Unmarshal(b, &patch)
		for name, rec := range f.records {
			if "/zones/zone1/dns_records/"+rec.ID == r.URL.Path {
				rec.Comment = patch.Comment
				f.records[name] = rec
				f.writes = append(f.writes, "PATCH "+name)
			}
		}
		ok(nil)
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
	want := []string{"created", "changed", "left as is", "already points at 203.0.113.9; Linx keeps it right"}
	for i, r := range res {
		if !strings.HasPrefix(r.Outcome, want[i]) {
			t.Errorf("%s: %q, want %s…", r.Name, r.Outcome, want[i])
		}
	}
	// sip. was added by hand at the right address: marked as Linx's.
	if got := strings.Join(cf.writes, ","); got != "POST meet.pbx.example.com,PUT api.pbx.example.com,PATCH sip.pbx.example.com" {
		t.Errorf("writes = %s", got)
	}
	if c := cf.records["sip.pbx.example.com"].Comment; !strings.HasPrefix(c, recordComment) {
		t.Errorf("sip comment %q", c)
	}

	// The background follower leaves a record it didn't make alone.
	cf.records["api.pbx.example.com"] = cfRecord{ID: "a1", Type: "A", Name: "api.pbx.example.com", Content: "198.51.100.1"}
	cf.writes = nil
	c.OwnOnly = true
	res, err = c.PointRecords(context.Background(), cfg, []string{"api", "meet"}, netip.MustParseAddr("192.0.2.44"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res[0].Outcome, "left as is: someone else") || !strings.HasPrefix(res[1].Outcome, "changed") {
		t.Errorf("own only: %+v", res)
	}
	if got := strings.Join(cf.writes, ","); got != "PUT meet.pbx.example.com" {
		t.Errorf("own only: writes = %s", got)
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

// The base domain may be someone's website: setup creates its record when
// there's none, and keeps it up to date once it's Linx's, but never
// replaces an address someone else set there.
func TestPointRecordsApex(t *testing.T) {
	cf := &fakeCloudflare{records: map[string]cfRecord{
		"example.com": {ID: "w1", Type: "A", Name: "example.com", Content: "198.51.100.80"},
	}}
	srv := httptest.NewServer(cf)
	defer srv.Close()
	c := &RecordsClient{HTTP: srv.Client(), CloudflareAPI: srv.URL} // setup: not OwnOnly
	ip := netip.MustParseAddr("203.0.113.9")

	cfg := Config{Domain: "example.com", Provider: ProviderCloudflare, TokenFile: tokenFile(t)}
	res, err := c.PointRecords(context.Background(), cfg, []string{"@", "turn"}, ip)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Name != "example.com" || !strings.HasPrefix(res[0].Outcome, "left as is: someone else") || !strings.HasPrefix(res[1].Outcome, "created") {
		t.Errorf("website at the apex: %+v", res)
	}
	if got := strings.Join(cf.writes, ","); got != "POST turn.example.com" {
		t.Errorf("writes = %s", got)
	}

	cf.writes = nil
	cfg.Domain = "pbx.example.com"
	if _, err := c.PointRecords(context.Background(), cfg, []string{"@"}, ip); err != nil {
		t.Fatal(err)
	}
	res, err = c.PointRecords(context.Background(), cfg, []string{"@"}, netip.MustParseAddr("203.0.113.10"))
	if err != nil || !strings.HasPrefix(res[0].Outcome, "changed") {
		t.Errorf("Linx's own apex record: %+v %v", res, err)
	}
	if got := strings.Join(cf.writes, ","); got != "POST pbx.example.com,PUT pbx.example.com" {
		t.Errorf("writes = %s", got)
	}
}

func TestCheckToken(t *testing.T) {
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]string{{"message": "Invalid access token"}}})
			return
		}
		if r.Method != http.MethodGet {
			t.Errorf("CheckToken changed something: %s %s", r.Method, r.URL)
		}
		res := any([]any{})
		if r.URL.Path == "/zones" && r.URL.Query().Get("name") == "example.com" {
			res = []map[string]string{{"id": "z"}}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": res})
	}))
	defer cf.Close()
	c := &RecordsClient{HTTP: http.DefaultClient, CloudflareAPI: cf.URL}
	ctx := context.Background()
	if err := c.CheckToken(ctx, ProviderCloudflare, "pbx.example.com", "good"); err != nil {
		t.Errorf("good token: %v", err)
	}
	if err := c.CheckToken(ctx, ProviderCloudflare, "pbx.example.com", "bad"); !errors.Is(err, ErrTokenRefused) {
		t.Errorf("bad token: %v", err)
	}
	if err := c.CheckToken(ctx, ProviderCloudflare, "pbx.other.org", "good"); !errors.Is(err, ErrTokenRefused) {
		t.Errorf("other zone: %v", err)
	}
	if err := c.CheckToken(ctx, ProviderDuckDNS, "me.duckdns.org", "x"); err != nil {
		t.Errorf("DuckDNS: %v", err)
	}
}
