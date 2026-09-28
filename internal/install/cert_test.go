package install

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"linxpbx.com/linx/internal/certs"
)

type fakeCert struct {
	mu                      sync.Mutex
	mode                    string
	setup                   bool
	dns                     []string
	prepareErr              error
	stagingErr, realErr     error
	prepared, staging, real int
	token                   string
	records, tokenCerts     int
}

func (f *fakeCert) Plan(_ context.Context, a Answers, fa Facts) (CertView, error) {
	c := CertView{Mode: f.mode, Domain: a.Domain, FrontDoor: a.FrontDoor}
	if f.mode == CertPort443 {
		for _, h := range []string{"meet", "turn"} {
			c.AddRecords = append(c.AddRecords, Record{Type: "A", Name: h + "." + a.Domain, Value: fa.PublicAddress})
		}
	}
	if f.setup {
		c.Setup = &DoorSetup{Files: []SetupFile{{Title: "Pangolin", Text: "tcp: …"}}}
	}
	return c, nil
}
func (f *fakeCert) Prepare(context.Context, CertView) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared++
	return f.prepareErr
}
func (f *fakeCert) Lookup(context.Context, string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dns, nil
}
func (f *fakeCert) Obtain(_ context.Context, staging bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if staging {
		f.staging++
		return f.stagingErr
	}
	f.real++
	return f.realErr
}
func (f *fakeCert) SaveToken(_ context.Context, _ CertView, token string) (string, error) {
	if len(token) < 20 {
		return "That doesn't look like a DNS token.", nil
	}
	f.mu.Lock()
	f.token = token
	f.mu.Unlock()
	return "", nil
}
func (f *fakeCert) Records(context.Context) error {
	f.mu.Lock()
	f.records++
	f.mu.Unlock()
	return nil
}
func (f *fakeCert) ObtainWithToken(context.Context) error {
	f.mu.Lock()
	f.tokenCerts++
	f.mu.Unlock()
	return nil
}
func (f *fakeCert) set(fn func(*fakeCert)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

// certRig is a rig whose host gets certificates from fake.
func certRig(t *testing.T, fake *fakeCert) (*rig, string) {
	t.Helper()
	r := newRig(t)
	r.host.Cert, r.host.Poll = fake, 5*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go r.host.runCert(ctx)
	cookie := r.claim(t)
	body := `{"where":"rented","front_door":"linx-443","domain":"example.com","name":"Owner","email":"o@example.com","agreed_to_terms":true}`
	if rec := r.do("POST", "/install/api/check", cookie, body, jsonFromPage...); rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body.String())
	}
	return r, cookie
}

func (r *rig) cert() CertView {
	c := r.host.State().View.Cert
	if c == nil {
		return CertView{}
	}
	return *c
}

func TestCertificateThroughPort443(t *testing.T) {
	fake := &fakeCert{mode: CertPort443, dns: []string{"198.51.100.7"}}
	r, cookie := certRig(t, fake)

	waitFor(t, func() bool { return r.cert().DNS.State == DNSWrong })
	if c := r.cert(); c.Prepare.State != StageOK || c.AddRecords[1].Value != "203.0.113.5" || c.Reach.State != "" || !c.DNS.CheckedAt.After(time.Time{}) {
		t.Fatalf("waiting for DNS: %+v", c)
	}
	// The page sees it too.
	waitFor(t, func() bool { v, _ := r.srv.Snapshot(); return v.Cert != nil && len(v.Cert.DNS.Names) == 2 })
	var ps pageState
	rec := r.do("GET", "/install/api/state", cookie, "")
	if json.Unmarshal(rec.Body.Bytes(), &ps) != nil || ps.Cert == nil || ps.Cert.DNS.Names[1].Seen[0] != "198.51.100.7" {
		t.Errorf("page state: %s", rec.Body.String())
	}
	// A second check can't change the answers under the certificate.
	body := `{"where":"rented","front_door":"linx-443","domain":"other.example","name":"Owner","email":"o@example.com","agreed_to_terms":true}`
	if rec := r.do("POST", "/install/api/check", cookie, body, jsonFromPage...); rec.Code == http.StatusOK || r.host.State().View.Accepted.Domain != "example.com" {
		t.Errorf("second check: %d", rec.Code)
	}

	// Staging fails: nothing more is asked of Let's Encrypt until "Try again".
	fake.set(func(f *fakeCert) {
		f.dns = []string{"203.0.113.5"}
		f.stagingErr = &certs.BootstrapError{Kind: certs.ProblemConnection, Detail: "203.0.113.5: Timeout during connect"}
	})
	waitFor(t, func() bool { return r.cert().Reach.State == StageFailed })
	if c := r.cert(); c.Reach.Kind != certs.ProblemConnection || c.Certificate.State != "" {
		t.Errorf("after staging failed: %+v", c)
	}
	time.Sleep(50 * time.Millisecond)
	fake.mu.Lock()
	tries := fake.staging
	fake.mu.Unlock()
	if tries != 1 {
		t.Errorf("staging asked %d times without Try again", tries)
	}
	if h := r.do("POST", "/install/api/handoff", cookie, "", jsonFromPage...); h.Code != http.StatusConflict {
		t.Errorf("handoff before the certificate: %d", h.Code)
	}

	fake.set(func(f *fakeCert) { f.stagingErr = nil })
	if rec := r.do("POST", "/install/api/retry", cookie, "", "Origin", "http://203.0.113.5:6464"); rec.Code != http.StatusForbidden {
		t.Errorf("retry without JSON: %d", rec.Code)
	}
	if rec := r.do("POST", "/install/api/retry", cookie, "{}", jsonFromPage...); rec.Code != http.StatusNoContent {
		t.Fatalf("retry: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { c := r.cert(); return c.Ready() })
	c := r.cert()
	fake.mu.Lock()
	real := fake.real
	fake.mu.Unlock()
	if c.Reach.State != StageOK || c.SecureURL != "https://meet.example.com" || real != 1 {
		t.Errorf("ready: %+v", c)
	}
	texts := ""
	for _, p := range r.host.State().Progress {
		texts += p.Text + "\n"
	}
	for _, want := range []string{"Waiting for meet.example.com and turn.example.com to point at 203.0.113.5", "meet.example.com and turn.example.com point at this server",
		"Let's Encrypt reached this server on port 443", "Certificate ready: https://meet.example.com"} {
		if !strings.Contains(texts, want) {
			t.Errorf("progress lacks %q:\n%s", want, texts)
		}
	}
}

func TestFrontDoorStepsFirst(t *testing.T) {
	fake := &fakeCert{mode: CertPort443, setup: true, dns: []string{"203.0.113.5"}}
	r, cookie := certRig(t, fake)
	waitFor(t, func() bool { return r.cert().DNS.State == DNSOK })
	time.Sleep(30 * time.Millisecond)
	if r.cert().Reach.State != "" {
		t.Fatal("Let's Encrypt asked before the front door was set up")
	}
	if rec := r.do("POST", "/install/api/door-ready", cookie, "{}", jsonFromPage...); rec.Code != http.StatusNoContent {
		t.Fatalf("door ready: %d", rec.Code)
	}
	waitFor(t, func() bool { c := r.cert(); return c.Ready() })
}

func TestPrepareFailureWaitsForRetry(t *testing.T) {
	fake := &fakeCert{mode: CertPort443, prepareErr: errors.New("port 443 is already allocated")}
	r, cookie := certRig(t, fake)
	waitFor(t, func() bool { return r.cert().Prepare.State == StageFailed })
	if !strings.Contains(r.cert().Prepare.Detail, "already allocated") {
		t.Errorf("detail: %+v", r.cert().Prepare)
	}
	fake.set(func(f *fakeCert) { f.prepareErr = nil; f.dns = []string{"203.0.113.5"} })
	r.do("POST", "/install/api/retry", cookie, "{}", jsonFromPage...)
	waitFor(t, func() bool { c := r.cert(); return c.Ready() })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.prepared != 2 {
		t.Errorf("prepared %d times", fake.prepared)
	}
}

func TestTokenFallback(t *testing.T) {
	fake := &fakeCert{mode: CertToken}
	r, cookie := certRig(t, fake)
	waitFor(t, func() bool { return r.cert().Prepare.State == StageOK })
	rec := r.do("POST", "/install/api/token", cookie, `{"token":"short"}`, jsonFromPage...)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"step":"token"`) {
		t.Errorf("short token: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.do("POST", "/install/api/token", cookie, `{"token":"x","more":1}`, jsonFromPage...); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
	token := strings.Repeat("t", 40)
	if rec := r.do("POST", "/install/api/token", cookie, `{"token":"`+token+`"}`, jsonFromPage...); rec.Code != http.StatusNoContent {
		t.Fatalf("token: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { c := r.cert(); return c.Ready() })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if c := r.cert(); !c.TokenSaved || c.Records.State != StageOK || fake.token != token || fake.tokenCerts != 1 {
		t.Errorf("after the token: %+v", c)
	}
	if strings.Contains(string(mustJSON(r.host.State())), token) {
		t.Error("the token is in the install state")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// secureDo is a request to the HTTPS port.
func (r *rig) secureDo(method, host, path, cookie, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.9:50000"
	req.TLS = &tls.ConnectionState{}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: SecureCookieName, Value: cookie})
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.srv.SecureHandler().ServeHTTP(rec, req)
	return rec
}

func TestHandoffToTheSecurePage(t *testing.T) {
	fake := &fakeCert{mode: CertPort443, dns: []string{"203.0.113.5"}}
	r, cookie := certRig(t, fake)
	const meet = "meet.example.com"
	secureJSON := []string{"Content-Type", "application/json", "Origin", "https://" + meet}

	// Before it's ready, the secure side serves nothing but the handoff page.
	waitFor(t, func() bool { v, _ := r.srv.Snapshot(); return v.Cert != nil })
	if rec := r.secureDo("GET", meet, "/install/api/state", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("state without a session: %d", rec.Code)
	}
	if rec := r.secureDo("GET", "api.example.com", "/install/continue", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("another name: %d", rec.Code)
	}
	if rec := r.secureDo("GET", meet, "/install/continue", "", ""); rec.Code != http.StatusOK || rec.Header().Get("Strict-Transport-Security") == "" {
		t.Errorf("handoff page: %d %v", rec.Code, rec.Header())
	}
	if rec := r.secureDo("GET", meet, "/install/api/ping", "", ""); rec.Code != http.StatusNoContent {
		t.Errorf("ping: %d", rec.Code)
	}
	waitFor(t, func() bool { c := r.cert(); return c.Ready() })
	waitFor(t, func() bool { v, _ := r.srv.Snapshot(); return v.Cert.Ready() })

	rec := r.do("POST", "/install/api/handoff", cookie, "{}", jsonFromPage...)
	var h struct{ Handoff string }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &h) != nil || !ValidSecret(h.Handoff) {
		t.Fatalf("handoff: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(string(mustJSON(r.host.State())), h.Handoff) {
		t.Error("the handoff itself is in the install state")
	}
	redeem := func(handoff string, headers ...string) *httptest.ResponseRecorder {
		return r.secureDo("POST", meet, "/install/api/redeem", "", `{"handoff":"`+handoff+`"}`, headers...)
	}
	if rec := redeem(h.Handoff, "Content-Type", "application/json", "Origin", "http://"+meet); rec.Code != http.StatusForbidden {
		t.Errorf("redeem from the plain origin: %d", rec.Code)
	}
	if rec := redeem(NewSecret(), secureJSON...); rec.Code != http.StatusConflict {
		t.Errorf("wrong handoff: %d", rec.Code)
	}
	// Two minutes, once.
	r.clk.add(HandoffLifetime)
	if rec := redeem(h.Handoff, secureJSON...); rec.Code != http.StatusConflict {
		t.Errorf("expired handoff: %d", rec.Code)
	}
	rec = r.do("POST", "/install/api/handoff", cookie, "{}", jsonFromPage...)
	_ = json.Unmarshal(rec.Body.Bytes(), &h)
	rec = redeem(h.Handoff, secureJSON...)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("redeem: %d %s", rec.Code, rec.Body.String())
	}
	var secure string
	for _, c := range rec.Result().Cookies() {
		if c.Name == SecureCookieName && c.Secure && c.HttpOnly && c.SameSite == http.SameSiteStrictMode && c.Path == "/" {
			secure = c.Value
		}
	}
	if secure == "" {
		t.Fatalf("no secure cookie: %v", rec.Result().Cookies())
	}
	if rec := redeem(h.Handoff, secureJSON...); rec.Code != http.StatusConflict {
		t.Errorf("handoff used twice: %d", rec.Code)
	}
	waitFor(t, func() bool { return r.host.State().View.Secure })

	// The plain page's session is over; the secure page's works.
	if rec := r.do("GET", "/install/api/state", cookie, ""); rec.Code != http.StatusNotFound {
		t.Errorf("plain session after the move: %d", rec.Code)
	}
	rec = r.secureDo("GET", meet, "/install/api/state", secure, "")
	var ps pageState
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ps) != nil || !ps.Secure || ps.ExpiresIn != int(LinkLifetime.Seconds()) {
		t.Errorf("secure state: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.secureDo("GET", meet, "/install", secure, ""); rec.Code != http.StatusOK {
		t.Errorf("secure page: %d", rec.Code)
	}
	// The plain page's cookie on the secure side, or the other way round: no.
	if rec := r.secureDo("GET", meet, "/install/api/state", cookie, ""); rec.Code != http.StatusNotFound {
		t.Errorf("plain cookie on the secure side: %d", rec.Code)
	}
}

func TestRestartRedoesWhatWasRunning(t *testing.T) {
	c := &CertView{Mode: CertPort443, Prepare: Stage{State: StageOK}, Reach: Stage{State: StageOK}, Certificate: Stage{State: StageRunning}}
	resetCert(c)
	if c.Prepare.State != "" || c.Reach.State != StageOK || c.Certificate.State != "" {
		t.Errorf("%+v", c)
	}
	ready := &CertView{Certificate: Stage{State: StageOK}, Prepare: Stage{State: StageOK}}
	resetCert(ready)
	if ready.Prepare.State != StageOK {
		t.Error("a finished certificate page was reset")
	}
}

func TestChallengeTLS(t *testing.T) {
	dir := t.TempDir()
	if err := (certs.ChallengeWriter{Dir: dir}).Present("meet.example.com", "t", "key-auth"); err != nil {
		t.Fatal(err)
	}
	ch := certs.Challenges{Dir: dir}
	conf := TLSConfig(ch, &certs.ServingCert{Dir: t.TempDir()})
	if c, err := conf.GetCertificate(&tls.ClientHelloInfo{ServerName: "meet.example.com", SupportedProtos: []string{"acme-tls/1"}}); err != nil || c == nil {
		t.Errorf("challenge: %v", err)
	}
	if _, err := conf.GetCertificate(&tls.ClientHelloInfo{ServerName: "meet.example.com", SupportedProtos: []string{"h2", "http/1.1"}}); err == nil {
		t.Error("a browser got the challenge certificate")
	}
	only := ChallengeTLSConfig(ch)
	if _, err := only.GetCertificate(&tls.ClientHelloInfo{ServerName: "meet.example.com"}); err == nil {
		t.Error("the challenge-only port answered a non-challenge")
	}
}
