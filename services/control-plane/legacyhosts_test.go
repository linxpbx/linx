package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyHostsRedirect(t *testing.T) {
	h := legacyHosts("pbx.example.com", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for host, want := range map[string]string{
		"meet.pbx.example.com":     "https://pbx.example.com/setup/abc?x=1",
		"API.pbx.example.com:8443": "https://pbx.example.com/setup/abc?x=1",
		"pbx.example.com":          "",
		"turn.pbx.example.com":     "",
		"meet.other.example":       "",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "https://"+host+"/setup/abc?x=1", nil)
		h.ServeHTTP(rec, req)
		if want == "" {
			if rec.Code != http.StatusTeapot {
				t.Errorf("%s: %d", host, rec.Code)
			}
			continue
		}
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d %s", host, rec.Code, rec.Header().Get("Location"))
		}
	}
}
