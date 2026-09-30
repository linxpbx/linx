package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/help"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// A small set of guides, one per audience, each with a word only it uses.
func testHelpLibrary(t *testing.T) *help.Library {
	t.Helper()
	guide := func(title, audience, section, body string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte("---\ntitle: " + title + "\naudience: " + audience + "\nsection: " + section +
			"\nkeywords: [k]\nscreens: []\n---\n# " + title + "\n\n" + body + "\n")}
	}
	lib, err := help.Open(fstest.MapFS{
		"signing-in.md":               guide("Signing in", "public", "everyday", "Type your **password**. Anyone may read aardvark.\n\n![Sign in](screen:signin)"),
		"whats-new.md":                guide("What's new", "everyone", "whats-new", "## This release\n\nZanzibar."),
		"team-guide.md":               guide("The Team list", "everyone", "everyday", "## Presence\n\nPeople see zanzibar here.\n\n![Team](screen:team)"),
		"admin-guide.md":              guide("Phone lines", "admin", "admin", "Only admins read quokka.\n\n![Lines](screen:lines)"),
		"system-guide.md":             guide("Installing", "system_admin", "install", "Only system admins read wombat."),
		"pictures/light-signin.webp":  {Data: []byte("light signin")},
		"pictures/dark-signin.webp":   {Data: []byte("dark signin")},
		"pictures/light-team.webp":    {Data: []byte("light team")},
		"pictures/dark-team.webp":     {Data: []byte("dark team")},
		"pictures/light-lines.webp":   {Data: []byte("light lines")},
		"pictures/dark-lines.webp":    {Data: []byte("dark lines")},
		"pictures/light-unused.webp":  {Data: []byte("no guide uses this")},
		"running-guide-not-guide.txt": {Data: []byte("not a guide")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

type helpEnv struct {
	*testEnv
	srv *httptest.Server
}

func newHelpEnv(t *testing.T) *helpEnv {
	env := newTestEnv(t)
	mux := http.NewServeMux()
	registerHelpHandlers(mux, env.authn, testHelpLibrary(t))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &helpEnv{env, srv}
}

// session stores a session for a person with role and returns its cookie.
func (e *helpEnv) session(role string, verified bool) *http.Cookie {
	e.t.Helper()
	raw := uuid.NewString()
	now := time.Now()
	s := auth.UserSession{ID: uuid.Must(uuid.NewV7()), TenantID: e.store.tenant, UserID: uuid.Must(uuid.NewV7()), Role: role,
		TokenHash: auth.HashSecret(raw), CSRFHash: auth.HashSecret("csrf"), MFAVerified: verified,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour), IdleExpiresAt: now.Add(time.Hour), LastSeenAt: now}
	if err := e.store.CreateSession(e.t.Context(), s); err != nil {
		e.t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: raw}
}

func (e *helpEnv) get(path string, cookie *http.Cookie, header ...string) (int, []byte, http.Header) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func (e *helpEnv) guideNames(cookie *http.Cookie) []string {
	e.t.Helper()
	status, body, _ := e.get("/api/v1/help/guides", cookie)
	if status != http.StatusOK {
		e.t.Fatalf("guide list: %d %s", status, body)
	}
	var list controlplaneapi.HelpGuideList
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&list); err != nil {
		e.t.Fatalf("guide list doesn't match the spec: %v", err)
	}
	var names []string
	for _, g := range list.Guides {
		names = append(names, g.Name)
	}
	return names
}

func (e *helpEnv) searchGuides(q string, cookie *http.Cookie) []string {
	e.t.Helper()
	status, body, _ := e.get("/api/v1/help/search?q="+q, cookie)
	if status != http.StatusOK {
		e.t.Fatalf("search %q: %d %s", q, status, body)
	}
	var out controlplaneapi.HelpSearchResults
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		e.t.Fatalf("search results don't match the spec: %v", err)
	}
	var names []string
	for _, r := range out.Results {
		names = append(names, r.Guide)
	}
	return names
}

// Each kind of caller reads exactly its guides, pictures and search
// results (docs/HELP.md §6), and everything else is the same 404.
func TestHelpWhoReadsWhat(t *testing.T) {
	e := newHelpEnv(t)
	_, apiKey := e.newCredential(auth.TypeAPIKey, auth.RoleSystemAdmin, "all")
	_, missing, _ := e.get("/api/v1/help/guides/no-such-guide", nil)

	type caller struct {
		name   string
		cookie *http.Cookie
		header []string
	}
	anyone := []string{"signing-in"}
	everyone := []string{"whats-new", "signing-in", "team-guide"}
	admins := append(append([]string{}, everyone...), "admin-guide")
	for _, tc := range []struct {
		caller
		guides []string
	}{
		{caller{name: "no session"}, anyone},
		{caller{name: "pending second step", cookie: e.session(auth.RoleSystemAdmin, false)}, anyone},
		{caller{name: "unknown cookie", cookie: &http.Cookie{Name: auth.SessionCookieName, Value: "stale"}}, anyone},
		{caller{name: "API key", header: []string{"Authorization", "Bearer " + apiKey}}, anyone},
		{caller{name: "person", cookie: e.session(auth.RoleUser, true)}, everyone},
		{caller{name: "read-only admin", cookie: e.session(auth.RoleReporter, true)}, admins},
		{caller{name: "admin", cookie: e.session(auth.RoleAdmin, true)}, admins},
		{caller{name: "system admin", cookie: e.session(auth.RoleSystemAdmin, true)}, append(append([]string{}, admins...), "system-guide")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := e.guideNames(tc.cookie)
			for _, name := range []string{"signing-in", "whats-new", "team-guide", "admin-guide", "system-guide"} {
				want := contains(tc.guides, name)
				if contains(list, name) != want {
					t.Errorf("guide list %v: %s listed = %v", list, name, !want)
				}
				status, body, _ := e.get("/api/v1/help/guides/"+name, tc.cookie, tc.header...)
				switch {
				case want && status != http.StatusOK:
					t.Errorf("%s: %d %s", name, status, body)
				case !want && (status != http.StatusNotFound || !bytes.Equal(body, missing)):
					t.Errorf("%s: %d %s, want the same 404 as a guide that doesn't exist", name, status, body)
				}
			}
			for word, guide := range map[string]string{"aardvark": "signing-in", "zanzibar": "team-guide", "quokka": "admin-guide", "wombat": "system-guide"} {
				found := e.searchGuides(word, tc.cookie)
				if contains(tc.guides, guide) != contains(found, guide) || (!contains(tc.guides, guide) && len(found) != 0) {
					t.Errorf("search %q found %v", word, found)
				}
			}
			for file, guide := range map[string]string{"light-signin.webp": "signing-in", "dark-team.webp": "team-guide", "light-lines.webp": "admin-guide"} {
				status, body, _ := e.get("/api/v1/help/pictures/"+file, tc.cookie, tc.header...)
				want := contains(tc.guides, guide)
				if (status == http.StatusOK) != want || (!want && !bytes.Equal(body, missing)) {
					t.Errorf("picture %s: %d", file, status)
				}
			}
		})
	}
}

// Altered names never reach a guide or picture the caller may not read,
// and a picture no guide uses is never served (§6).
func TestHelpAlteredAddresses(t *testing.T) {
	e := newHelpEnv(t)
	person := e.session(auth.RoleUser, true)
	for _, path := range []string{
		"/api/v1/help/guides/Team-guide", "/api/v1/help/guides/team-guide/", "/api/v1/help/guides/team-guide.md",
		"/api/v1/help/guides/team-guide?name=signing-in", "/api/v1/help/guides/team%2Dguide", "/api/v1/help/guides/..%2Fteam-guide",
		"/api/v1/help/guides/%2e%2e%2fteam-guide", "/api/v1/help/guides/signing-in%2F..%2Fteam-guide", "/api/v1/help/guides/signing-in/../team-guide",
		"/api/v1/help/guides/team-guide%00", "/api/v1/help/guides/" + strings.Repeat("a", 65), "/api/v1/help/guides/",
		"/api/v1/help/pictures/light-team.webp", "/api/v1/help/pictures/light-team.png", "/api/v1/help/pictures/..%2Flight-team.webp",
		"/api/v1/help/pictures/%2e%2e%2fteam-guide.md", "/api/v1/help/pictures/light-unused.webp", "/api/v1/help/search?q=zanzibar&role=admin",
	} {
		status, body, _ := e.get(path, nil)
		if status == http.StatusOK && !strings.Contains(path, "search") || strings.Contains(string(body), "zanzibar") || strings.Contains(string(body), "light team") {
			t.Errorf("no session: %s gave %d %s", path, status, body)
		}
	}
	for _, path := range []string{"/api/v1/help/pictures/light-lines.webp", "/api/v1/help/pictures/light-unused.webp",
		"/api/v1/help/guides/admin-guide", "/api/v1/help/guides/Admin-guide"} {
		if status, body, _ := e.get(path, person); status != http.StatusNotFound {
			t.Errorf("person: %s gave %d %s", path, status, body)
		}
	}
}

func TestHelpGuideAndPicture(t *testing.T) {
	e := newHelpEnv(t)
	status, body, h := e.get("/api/v1/help/guides/signing-in", nil)
	if status != http.StatusOK || h.Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s %v", status, body, h)
	}
	var g controlplaneapi.HelpGuide
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&g); err != nil {
		t.Fatalf("the guide doesn't match the spec: %v", err)
	}
	if g.Title != "Signing in" || len(g.Blocks) != 3 || g.Blocks[2].Type != "picture" || *g.Blocks[2].Picture != "signin" {
		t.Errorf("guide: %s", body)
	}

	status, body, h = e.get("/api/v1/help/pictures/dark-signin.webp", nil)
	if status != http.StatusOK || string(body) != "dark signin" || h.Get("Content-Type") != "image/webp" || h.Get("ETag") == "" {
		t.Fatalf("picture: %d %q %v", status, body, h)
	}
	if status, _, _ := e.get("/api/v1/help/pictures/dark-signin.webp", nil, "If-None-Match", h.Get("ETag")); status != http.StatusNotModified {
		t.Errorf("again with its ETag: %d", status)
	}

	if status, _, _ := e.get("/api/v1/help/search?q="+strings.Repeat("a", help.MaxQuestionLen+1), nil); status != http.StatusBadRequest {
		t.Errorf("too long a question: %d", status)
	}
	if got := e.searchGuides("", nil); len(got) != 0 {
		t.Errorf("empty question found %v", got)
	}
}

// A stale cookie isn't a failed sign-in: reading Help after a session ends
// can't lock the address out of signing in.
func TestHelpStaleCookieIsNotAFailure(t *testing.T) {
	e := newHelpEnv(t)
	stale := &http.Cookie{Name: auth.SessionCookieName, Value: "ended"}
	for range auth.FailedAuthPerMinute + 5 {
		e.get("/api/v1/help/guides/signing-in", stale)
	}
	if e.authn.Failures.Exhausted(auth.IPKey(netip.MustParseAddr("127.0.0.1")), time.Now()) {
		t.Fatal("reading Help with a stale cookie used up the address's sign-in attempts")
	}
}

func TestHelpLimitsRequestsWithoutASession(t *testing.T) {
	e := newHelpEnv(t)
	limited := false
	for range helpPublicBurst + 10 {
		if status, _, _ := e.get("/api/v1/help/guides", nil); status == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("no limit on requests without a session")
	}
	if status, _, _ := e.get("/api/v1/help/guides", e.session(auth.RoleUser, true)); status != http.StatusOK {
		t.Errorf("a signed-in person was limited with the address: %d", status)
	}
}

func TestHelpRole(t *testing.T) {
	ctx := func(p auth.Principal) *http.Request {
		return httptest.NewRequest(http.MethodGet, "/", nil).WithContext(auth.WithPrincipal(t.Context(), p))
	}
	for _, tc := range []struct {
		p    auth.Principal
		want string
	}{
		{auth.Principal{Type: auth.TypeUser, Role: auth.RoleAdmin}, auth.RoleAdmin},
		{auth.Principal{Type: auth.TypeUser, Role: auth.RoleAdmin, AdminNetworkRestricted: true}, auth.RoleUser},
		{auth.Principal{Type: auth.TypeUser, Role: auth.RoleAdmin, Pending: true}, ""},
		{auth.Principal{Type: auth.TypeAPIKey, Role: auth.RoleSystemAdmin}, ""},
		{auth.Principal{Type: auth.TypeSystem, Role: auth.RoleSystemAdmin}, ""},
	} {
		if got := helpRole(ctx(tc.p).Context()); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.p, got, tc.want)
		}
	}
	if got := helpRole(t.Context()); got != "" {
		t.Errorf("no principal: %q", got)
	}
}
