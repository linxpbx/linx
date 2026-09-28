package install

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type rig struct {
	srv   *Server
	host  *Host
	clk   *clock
	http  http.Handler
	check func(Answers) []FieldError
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{clk: &clock{t: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}}
	r.check = func(Answers) []FieldError { return nil }
	r.srv = &Server{Now: r.clk.now, Web: fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>Linx</title>")},
		"assets/app-1.js": {Data: []byte("app")},
		"favicon.svg":     {Data: []byte("<svg/>")},
	}}
	r.host = &Host{
		Path:  filepath.Join(t.TempDir(), "state.json"),
		Now:   r.clk.now,
		Facts: func(context.Context) Facts { return Facts{Where: WhereRented, PublicAddress: "203.0.113.5"} },
		Check: func(_ context.Context, a Answers) (string, []FieldError, error) {
			if errs := r.check(a); len(errs) > 0 {
				return "", errs, nil
			}
			return "Domain: " + a.Domain, nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := r.host.Start(ctx); err != nil {
		t.Fatal(err)
	}
	hostSide, srvSide := net.Pipe()
	go r.srv.handleBridge(srvSide)
	go func() { _ = r.host.Serve(ctx, hostSide) }()
	waitFor(t, func() bool { _, ok := r.srv.Snapshot(); v, _ := r.srv.Snapshot(); return ok && v.LinkHash != "" })
	r.http = r.srv.Handler()
	return r
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func (r *rig) do(method, path, cookie, body string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://203.0.113.5:6464"+path, strings.NewReader(body))
	req.RemoteAddr = "198.51.100.9:50000"
	req.Header.Set("User-Agent", "Mozilla/5.0 Chrome/140.0 Safari/537.36")
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.http.ServeHTTP(rec, req)
	return rec
}

func (r *rig) claim(t *testing.T) string {
	t.Helper()
	rec := r.do("GET", "/install/"+r.host.State().Secret, "", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/install" {
		t.Fatalf("claim: %d %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie not HttpOnly/Strict: %+v", c)
			}
			return c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

var jsonFromPage = []string{"Content-Type", "application/json", "Origin", "http://203.0.113.5:6464"}

func TestOnlyTheLinkAnswers(t *testing.T) {
	r := newRig(t)
	unusable := r.do("GET", "/install/"+NewSecret(), "", "")
	if unusable.Code != http.StatusNotFound || !strings.Contains(unusable.Body.String(), "This link can't be used") {
		t.Fatalf("wrong link: %d %s", unusable.Code, unusable.Body.String())
	}
	for _, p := range []string{"/", "/install", "/install/api/state", "/assets/app-1.js", "/api/v1/me", "/install/short"} {
		rec := r.do("GET", p, "", "")
		if rec.Code != http.StatusNotFound || rec.Body.String() != unusable.Body.String() {
			t.Errorf("GET %s without a session: %d", p, rec.Code)
		}
		if rec.Header().Get("Strict-Transport-Security") != "" {
			t.Errorf("GET %s: HSTS over plain HTTP", p)
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
			t.Errorf("GET %s: CSP %q", p, rec.Header().Get("Content-Security-Policy"))
		}
	}
	if r.host.State().Secret == "" {
		t.Error("a wrong link used up the real one")
	}
}

func TestClaimOnceThenPages(t *testing.T) {
	r := newRig(t)
	secret := r.host.State().Secret
	cookie := r.claim(t)

	st := r.host.State()
	if st.Secret != "" || st.View.LinkHash != "" || !Matches(cookie, st.View.SessionHash) || st.Browser != "Chrome" || st.Address != "198.51.100.9" {
		t.Errorf("host state after claim: %+v", st)
	}
	if len(st.Progress) != 1 || st.Progress[0].Text != "Link opened (Chrome, 198.51.100.9)" {
		t.Errorf("progress: %+v", st.Progress)
	}
	// Used: another browser (no cookie) gets the same page as a wrong link.
	if rec := r.do("GET", "/install/"+secret, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("second claim: %d", rec.Code)
	}
	// The same browser, following the link again, lands on the page.
	if rec := r.do("GET", "/install/"+secret, cookie, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("link again with the session: %d", rec.Code)
	}
	for p, want := range map[string]string{"/install": "<title>Linx</title>", "/assets/app-1.js": "app", "/favicon.svg": "<svg/>"} {
		if rec := r.do("GET", p, cookie, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s: %d %q", p, rec.Code, rec.Body.String())
		}
	}
	if rec := r.do("GET", "/team", cookie, ""); rec.Code != http.StatusNotFound {
		t.Errorf("another app page: %d", rec.Code)
	}
	rec := r.do("GET", "/install/api/state", cookie, "")
	var ps pageState
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ps) != nil || ps.Facts.PublicAddress != "203.0.113.5" || !ps.Connected {
		t.Errorf("state: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("state is cacheable")
	}
}

func TestDraftAndCheck(t *testing.T) {
	r := newRig(t)
	cookie := r.claim(t)

	if rec := r.do("PUT", "/install/api/draft", cookie, `{"step":"domain"}`, "Content-Type", "application/json"); rec.Code != http.StatusForbidden {
		t.Errorf("draft without Origin: %d", rec.Code)
	}
	if rec := r.do("PUT", "/install/api/draft", cookie, `{"step":"domain"}`, "Content-Type", "application/json", "Origin", "http://evil.example"); rec.Code != http.StatusForbidden {
		t.Errorf("draft from another origin: %d", rec.Code)
	}
	if rec := r.do("PUT", "/install/api/draft", cookie, `["x"]`, jsonFromPage...); rec.Code != http.StatusBadRequest {
		t.Errorf("draft not an object: %d", rec.Code)
	}
	if rec := r.do("PUT", "/install/api/draft", cookie, `{"step":"domain"}`, jsonFromPage...); rec.Code != http.StatusNoContent {
		t.Errorf("draft: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return string(r.host.State().View.Draft) == `{"step":"domain"}` })

	r.check = func(a Answers) []FieldError {
		if a.Domain == "co.uk" {
			return []FieldError{{Step: StepDomain, Field: "domain", Message: "co.uk is shared by everyone. Use your own domain."}}
		}
		return nil
	}
	body := `{"where":"rented","front_door":"linx-443","domain":"co.uk","name":"Owner","email":"o@example.com","agreed_to_terms":true}`
	rec := r.do("POST", "/install/api/check", cookie, body, jsonFromPage...)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "shared by everyone") {
		t.Errorf("refused check: %d %s", rec.Code, rec.Body.String())
	}
	if rec := r.do("POST", "/install/api/check", cookie, `{"domain":"x","surprise":1}`, jsonFromPage...); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
	rec = r.do("POST", "/install/api/check", cookie, strings.Replace(body, "co.uk", "example.com", 1), jsonFromPage...)
	if rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body.String())
	}
	st := r.host.State()
	if st.View.Accepted == nil || st.View.Accepted.Domain != "example.com" || st.Progress[len(st.Progress)-1].Text != "Domain: example.com" {
		t.Errorf("host after check: %+v", st)
	}
}

func TestExpiry(t *testing.T) {
	r := newRig(t)
	cookie := r.claim(t)
	r.clk.add(LinkLifetime)
	if rec := r.do("GET", "/install/api/state", cookie, ""); rec.Code != http.StatusNotFound {
		t.Errorf("session after the hour: %d", rec.Code)
	}
	ended := make(chan string, 1)
	r.host.OnEnd = func(_ context.Context, reason string) { ended <- reason }
	r.host.End(context.Background(), EndedExpired)
	if got := <-ended; got != EndedExpired {
		t.Errorf("OnEnd(%q)", got)
	}
	st := r.host.State()
	if st.View.Ended != EndedExpired || !st.Progress[len(st.Progress)-1].Failed {
		t.Errorf("state: %+v", st)
	}
	// And a new link isn't claimable after the hour either.
	r2 := newRig(t)
	r2.clk.add(LinkLifetime)
	if rec := r2.do("GET", "/install/"+r2.host.State().Secret, "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("claim after the hour: %d", rec.Code)
	}
}

func TestHostChecksTheSecretItself(t *testing.T) {
	r := newRig(t)
	ok := r.host.claim(Message{Secret: NewSecret(), SessionHash: Hash(NewSecret())})
	if ok || r.host.State().Secret == "" {
		t.Error("host accepted a claim for a secret that isn't its link's")
	}
	if r.host.claim(Message{Secret: r.host.State().Secret, SessionHash: "not a hash"}) {
		t.Error("host accepted a bad session hash")
	}
}

func TestStartKeepsALiveLink(t *testing.T) {
	r := newRig(t)
	secret := r.host.State().Secret
	h2 := &Host{Path: r.host.Path, Now: r.clk.now}
	if err := h2.Start(context.Background()); err != nil || h2.State().Secret != secret {
		t.Errorf("restarted service made a new link: %v", err)
	}
	r.clk.add(LinkLifetime)
	h3 := &Host{Path: r.host.Path, Now: r.clk.now}
	if err := h3.Start(context.Background()); err != nil || h3.State().Secret == secret || h3.State().Secret == "" {
		t.Errorf("expired link not replaced: %v", err)
	}
}

func TestRateLimit(t *testing.T) {
	r := newRig(t)
	r.srv.Burst = 3
	for i := range 3 {
		if rec := r.do("GET", "/x", "", ""); rec.Code != http.StatusNotFound {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if rec := r.do("GET", "/install/"+r.host.State().Secret, "", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("over the limit: %d", rec.Code)
	}
}

func TestBrowserName(t *testing.T) {
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 Edg/140.0": "Edge",
		"Mozilla/5.0 (X11; Linux x86_64; rv:143.0) Gecko/20100101 Firefox/143.0":                "Firefox",
		"Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/26.0 Safari/605.1.15":             "Safari",
		"curl/8.5": "a browser",
	} {
		if got := BrowserName(ua); got != want {
			t.Errorf("%s: %q, want %q", ua, got, want)
		}
	}
}

func TestPageUsesTokenColours(t *testing.T) {
	tokens, err := os.ReadFile("../../web/src/styles/tokens.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, hex := range regexp.MustCompile(`#[0-9A-Fa-f]{6}`).FindAllString(pageStyle, -1) {
		if !strings.Contains(strings.ToUpper(string(tokens)), strings.ToUpper(hex)) {
			t.Errorf("%s isn't a design token colour", hex)
		}
	}
}

func TestStartingBeforeTheHostSpeaks(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest("GET", "/install/"+NewSecret(), nil)
	req.RemoteAddr = "198.51.100.9:50000"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "Setup is starting") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
}
