package certs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"

	"linxpbx.com/linx/internal/dnsapi"
)

// memAPI is a DNS company in memory: record sets by "name type".
type memAPI struct {
	mu     sync.Mutex
	sets   map[string][]string
	writes []string
	fail   error
}

func (m *memAPI) Get(_ context.Context, zone, name, typ string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	return slices.Clone(m.sets[name+" "+typ]), nil
}

func (m *memAPI) Set(_ context.Context, zone, name, typ string, values []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, "set "+name+" "+typ+" "+strings.Join(values, ","))
	m.sets[name+" "+typ] = slices.Clone(values)
	return nil
}

func (m *memAPI) Delete(_ context.Context, zone, name, typ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, "delete "+name+" "+typ)
	delete(m.sets, name+" "+typ)
	return nil
}

func porkbunKey(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(`{"api_key":"pk1_keykeykey","secret_api_key":"sk1_secretsecret"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func memClient(m *memAPI, state *DNSState) *RecordsClient {
	return &RecordsClient{
		State: state,
		NewAPI: func(_ context.Context, provider string, k dnsapi.Key) (dnsapi.API, error) {
			if provider != dnsapi.Porkbun || k["api_key"] != "pk1_keykeykey" {
				return nil, errors.New("wrong key")
			}
			return m, nil
		},
		Zone: func(string) (string, error) { return "example.com", nil },
	}
}

func TestPointRecordsCompany(t *testing.T) {
	m := &memAPI{sets: map[string][]string{
		"@ A":        {"198.51.100.1"},     // the domain's website: someone else's
		"turn CNAME": {"home.example.net"}, // an alias: left alone
		"sip A":      {"203.0.113.9"},      // added by hand from the card, at the right address
		"admin A":    {"192.0.2.1"},        // someone else's, not the domain itself
	}}
	state := &DNSState{Path: filepath.Join(t.TempDir(), "dns.json")}
	c := memClient(m, state)
	cfg := Config{Domain: "example.com", Provider: dnsapi.Porkbun, TokenFile: porkbunKey(t)}
	ip := netip.MustParseAddr("203.0.113.9")

	res, err := c.PointRecords(context.Background(), cfg, []string{"@", "turn", "sip", "admin", "provision"}, ip)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"left as is: someone else made it (it points at 198.51.100.1)", "left as is: it's an alias", "already points at 203.0.113.9 (Porkbun)",
		"changed to 203.0.113.9 (Porkbun)", "created, pointing at 203.0.113.9 (Porkbun)"}
	for i, r := range res {
		if !strings.HasPrefix(r.Outcome, want[i]) {
			t.Errorf("%s: %q, want %s…", r.Name, r.Outcome, want[i])
		}
	}
	if got := strings.Join(m.writes, "; "); got != "set admin A 203.0.113.9; set provision A 203.0.113.9" {
		t.Errorf("writes = %s", got)
	}
	if got := state.owned(); len(got) != 3 || got["sip.example.com"] != "203.0.113.9" || got["example.com"] != "" {
		t.Errorf("owned = %v", got)
	}

	// The follower, later, at a new address: Linx's records follow; one
	// someone changed since is theirs now.
	m.sets["admin A"] = []string{"192.0.2.77"}
	m.writes = nil
	again := memClient(m, &DNSState{Path: state.Path}) // read back from the file
	again.OwnOnly = true
	ip2 := netip.MustParseAddr("198.51.100.4")
	res, err = again.PointRecords(context.Background(), cfg, []string{"sip", "admin", "provision"}, ip2)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.writes, "; "); got != "set sip A 198.51.100.4; set provision A 198.51.100.4" {
		t.Errorf("follower writes = %s (%+v)", got, res)
	}
	if !strings.HasPrefix(res[1].Outcome, "left as is: someone else made it") {
		t.Errorf("admin: %+v", res[1])
	}

	m.fail = errors.New("Porkbun: Invalid API key")
	if _, err := again.PointRecords(context.Background(), cfg, []string{"sip"}, ip); err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("company's refusal: %v", err)
	}
	cfg.TokenFile = tokenFile(t) // a bare token: not how a Porkbun key is kept
	if _, err := again.PointRecords(context.Background(), cfg, []string{"sip"}, ip); err == nil || strings.Contains(err.Error(), "test-token") {
		t.Errorf("bare token: %v", err)
	}
}

func TestCheckTokenCompany(t *testing.T) {
	m := &memAPI{sets: map[string][]string{}}
	c := memClient(m, nil)
	key, _ := os.ReadFile(porkbunKey(t))
	if err := c.CheckToken(context.Background(), dnsapi.Porkbun, "example.com", string(key)); err != nil {
		t.Fatal(err)
	}
	m.fail = errors.New("Porkbun: Invalid API key")
	err := c.CheckToken(context.Background(), dnsapi.Porkbun, "example.com", string(key))
	if !errors.Is(err, ErrTokenRefused) || !strings.Contains(err.Error(), "Porkbun didn't let this key read example.com's records (Porkbun: Invalid API key)") {
		t.Errorf("refused: %v", err)
	}
	if err := c.CheckToken(context.Background(), dnsapi.Porkbun, "example.com", "just-a-token"); !errors.Is(err, ErrTokenRefused) {
		t.Errorf("malformed key: %v", err)
	}
	if err := c.CheckToken(context.Background(), dnsapi.Porkbun, "example.com", `{"api_key":"other-key-xx","secret_api_key":"sk1_secretsecret"}`); !errors.Is(err, ErrTokenRefused) {
		t.Errorf("key the company refuses at once: %v", err)
	}
}

func TestDNSChallenge(t *testing.T) {
	old := dnsapi.FindZone
	dnsapi.FindZone = func(string) (string, error) { return "example.com", nil }
	defer func() { dnsapi.FindZone = old }()
	m := &memAPI{sets: map[string][]string{}}
	d := &dnsChallenge{api: m}
	// A wildcard certificate: two values at one name (ACME names both
	// pbx.example.com), added one by one
	// and taken away one by one.
	if err := d.Present("pbx.example.com", "", "key-auth-1"); err != nil {
		t.Fatal(err)
	}
	if err := d.Present("pbx.example.com", "", "key-auth-2"); err != nil {
		t.Fatal(err)
	}
	v1, v2 := dns01.GetChallengeInfo("pbx.example.com", "key-auth-1").Value, dns01.GetChallengeInfo("pbx.example.com", "key-auth-2").Value
	if got := m.sets["_acme-challenge.pbx TXT"]; !slices.Equal(got, []string{v1, v2}) {
		t.Fatalf("both values: %v", got)
	}
	if err := d.CleanUp("pbx.example.com", "", "key-auth-1"); err != nil {
		t.Fatal(err)
	}
	if got := m.sets["_acme-challenge.pbx TXT"]; !slices.Equal(got, []string{v2}) {
		t.Fatalf("one left: %v", got)
	}
	if err := d.CleanUp("pbx.example.com", "", "key-auth-2"); err != nil {
		t.Fatal(err)
	}
	if _, left := m.sets["_acme-challenge.pbx TXT"]; left || m.writes[len(m.writes)-1] != "delete _acme-challenge.pbx TXT" {
		t.Errorf("record not removed: %v", m.writes)
	}
	if to, _ := d.Timeout(); to != propagationTimeout {
		t.Errorf("timeout %v", to)
	}
}

func TestDNSStatus(t *testing.T) {
	m := &memAPI{sets: map[string][]string{}}
	state := &DNSState{Path: filepath.Join(t.TempDir(), "dns.json")}
	trace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ip=203.0.113.9\n") }))
	defer trace.Close()
	c := memClient(m, state)
	c.HTTP, c.TraceURL, c.OwnOnly = http.DefaultClient, trace.URL, true
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	cfg := Config{Domain: "example.com", Provider: dnsapi.Porkbun, TokenFile: porkbunKey(t)}
	f := &Follower{Client: c, Config: cfg, Hosts: []string{"turn"}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
	if err := f.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	get := func(f *Follower) DNSStatus {
		rec := httptest.NewRecorder()
		DNSStatusHandler(cfg, f).ServeHTTP(rec, httptest.NewRequest("GET", "/dns", nil))
		var st DNSStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := get(f)
	if !st.Following || st.Company != dnsapi.Porkbun || st.Address != "203.0.113.9" || st.Previous != "" || !st.Changed.Equal(now) || !st.Checked.Equal(now) {
		t.Errorf("status %+v", st)
	}
	m.fail = errors.New("Porkbun: Invalid API key")
	now = now.Add(reconfirmEvery)
	_ = f.Check(context.Background())
	if st := get(f); st.Failures != 1 || st.Error != "Porkbun: Invalid API key" {
		t.Errorf("after a failure %+v", st)
	}
	if st := get(nil); st.Following || st.Company != "" {
		t.Errorf("not following: %+v", st)
	}
	// The address moves: kept across a restart.
	if err := state.Followed("198.51.100.4", now); err != nil {
		t.Fatal(err)
	}
	if ch := (&DNSState{Path: state.Path}).LastChange(); ch.Address != "198.51.100.4" || ch.Previous != "203.0.113.9" || !ch.Changed.Equal(now) {
		t.Errorf("after restart %+v", ch)
	}
	if fi, err := os.Stat(state.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("state file %v %v", fi, err)
	}
}
