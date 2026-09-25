package webapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandler(t *testing.T) {
	files := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>Linx</title>")},
		"assets/app-1a2b.js":   {Data: []byte("console.log(1)")},
		"manifest.webmanifest": {Data: []byte("{}")},
	}
	h := Headers(Handler(files))
	for _, tc := range []struct {
		method, path string
		status       int
		body, cache  string
	}{
		{"GET", "/", 200, "<title>Linx</title>", "no-cache"},
		{"GET", "/index.html", 200, "<title>Linx</title>", "no-cache"},
		{"GET", "/team", 200, "<title>Linx</title>", "no-cache"},
		{"GET", "/setup/abc123", 200, "<title>Linx</title>", "no-cache"},
		{"GET", "/assets/app-1a2b.js", 200, "console.log(1)", "public, max-age=31536000, immutable"},
		{"GET", "/manifest.webmanifest", 200, "{}", "no-cache"},
		{"GET", "/assets/app-old.js", 404, "", ""},
		{"GET", "/favicon.ico", 404, "", ""},
		{"GET", "/../../etc/passwd", 200, "<title>Linx</title>", "no-cache"}, // cleaned to /etc/passwd: not a file, so the app
		{"POST", "/", 405, "", ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Errorf("%s %s: status %d, want %d", tc.method, tc.path, rec.Code, tc.status)
			continue
		}
		if tc.body != "" && !strings.Contains(rec.Body.String(), tc.body) {
			t.Errorf("%s %s: body %q", tc.method, tc.path, rec.Body.String())
		}
		if tc.cache != "" && rec.Header().Get("Cache-Control") != tc.cache {
			t.Errorf("%s %s: Cache-Control %q, want %q", tc.method, tc.path, rec.Header().Get("Cache-Control"), tc.cache)
		}
		for _, name := range []string{"Strict-Transport-Security", "Content-Security-Policy", "Permissions-Policy", "X-Content-Type-Options"} {
			if rec.Header().Get(name) == "" {
				t.Errorf("%s %s: no %s header", tc.method, tc.path, name)
			}
		}
	}
}

func TestPoliciesStayStrict(t *testing.T) {
	for _, bad := range []string{"'unsafe-inline'", "'unsafe-eval'", "*", "http:", "https:"} {
		if strings.Contains(ContentSecurityPolicy, bad) {
			t.Errorf("CSP allows %s", bad)
		}
	}
	if !strings.Contains(PermissionsPolicy, "microphone=(self)") || !strings.Contains(PermissionsPolicy, "camera=()") {
		t.Errorf("Permissions-Policy: %s", PermissionsPolicy)
	}
}

func TestNoBuild(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
}
