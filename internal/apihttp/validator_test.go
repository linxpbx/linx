package apihttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const testSpecYAML = `
openapi: 3.1.0
info:
  title: test
  version: "1"
paths:
  /widgets:
    get:
      operationId: listWidgets
      parameters:
        - name: limit
          in: query
          schema:
            type: integer
            maximum: 10
      responses:
        "200":
          description: OK
`

func loadTestSpec(t *testing.T) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte(testSpecYAML))
	if err != nil {
		t.Fatalf("loading test spec: %v", err)
	}
	return spec
}

func TestValidatorAllowsMatchingRequest(t *testing.T) {
	handlerCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	h := Validator(loadTestSpec(t))(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/widgets?limit=5", nil))

	if !handlerCalled {
		t.Fatal("handler was not called for a valid request")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestValidatorRejectsUnknownPath(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called for an unmatched path")
	})

	h := Validator(loadTestSpec(t))(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if p.Code != "request_invalid" {
		t.Fatalf("code = %q, want request_invalid", p.Code)
	}
}

func TestValidatorRejectsOutOfRangeParameter(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called for an invalid parameter")
	})

	h := Validator(loadTestSpec(t))(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/widgets?limit=999", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
