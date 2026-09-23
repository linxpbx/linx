package apihttp

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteProblem(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteProblem(rec, http.StatusBadRequest, "scope_missing", "The key is missing the extensions:write scope.")

	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if p.Code != "scope_missing" || p.Status != http.StatusBadRequest || p.Title != "Bad Request" {
		t.Fatalf("unexpected body: %+v", p)
	}
}

func TestResponseErrorHandler(t *testing.T) {
	var logged strings.Builder
	log := slog.New(slog.NewTextHandler(&logged, nil))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	ResponseErrorHandler(log)(rec, req, errClaimTest)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if p.Code != "internal" {
		t.Fatalf("code = %q, want internal", p.Code)
	}
	if strings.Contains(p.Detail, errClaimTest.Error()) {
		t.Fatalf("detail leaked the underlying error: %q", p.Detail)
	}
	if !strings.Contains(logged.String(), errClaimTest.Error()) {
		t.Fatalf("underlying error not logged: %q", logged.String())
	}
}

var errClaimTest = errTest("marshal failed: unexpected end of JSON")

type errTest string

func (e errTest) Error() string { return string(e) }
