package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/enroll"
)

// The two endpoints a phone itself calls (docs/PHASE2.md §4). Nothing here
// reaches the database: every one of these is refused before that, which is
// the point — a caller that hasn't got a real ticket or a real certificate
// learns nothing and costs nothing.
func TestPhoneEndpointsRefuseStrangers(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		t.Fatal(err)
	}
	tokens, err := auth.NewTokens(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		t.Fatal(err)
	}
	ips, err := auth.NewClientIPResolver("")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerEnrollHandlers(mux, ips, &enroll.Service{Tokens: tokens}, slog.New(slog.DiscardHandler))

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	code := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		var p struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatalf("answer wasn't problem+json: %s", w.Body)
		}
		return p.Code
	}

	for _, tc := range []struct {
		name, path, body string
		status           int
		problem          string
	}{
		{"not JSON", enrollPath, "hello", http.StatusBadRequest, "request_invalid"},
		{"no ticket at all", enrollPath, `{"csr":"AQID"}`, http.StatusBadRequest, "enrollment_invalid"},
		{"a made-up token", enrollPath, `{"token":"not.a.token","csr":"AQID"}`, http.StatusBadRequest, "enrollment_invalid"},
		{"a certificate request that isn't base64", enrollPath, `{"token":"x","csr":"!!!"}`, http.StatusBadRequest, "csr_invalid"},
		{"a made-up certificate", deviceTokenPath, `{"certificate":"AQID","proof":"x"}`, http.StatusUnauthorized, "device_proof_invalid"},
		{"no certificate", deviceTokenPath, `{"proof":"x"}`, http.StatusUnauthorized, "device_proof_invalid"},
	} {
		w := post(tc.path, tc.body)
		if w.Code != tc.status || code(w) != tc.problem {
			t.Errorf("%s: %d %s, want %d %s", tc.name, w.Code, code(w), tc.status, tc.problem)
		}
	}

	// Guessing is stopped by the limit per address, not by anything on the
	// ticket: 10 tries a minute, and the ones after that wait.
	var last int
	for range 20 {
		last = post(enrollPath, `{"token":"not.a.token","csr":"AQID"}`).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("after 20 tries from one address: %d, want 429", last)
	}
}
