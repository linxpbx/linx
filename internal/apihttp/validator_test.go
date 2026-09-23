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
  /secret:
    get:
      operationId: getSecret
      security:
        - bearer: [secrets:read]
      responses:
        "200":
          description: OK
components:
  securitySchemes:
    bearer:
      type: http
      scheme: bearer
`

func allowAll(*http.Request, []string) error { return nil }

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

	h := Validator(loadTestSpec(t), allowAll)(next)
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

	h := Validator(loadTestSpec(t), allowAll)(next)
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

	h := Validator(loadTestSpec(t), allowAll)(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/widgets?limit=999", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestValidatorEnforcesSecurityScopes(t *testing.T) {
	var gotScopes []string
	authorize := func(r *http.Request, scopes []string) error {
		gotScopes = scopes
		if r.Header.Get("X-Allow") == "" {
			return &Error{Status: http.StatusForbidden, Code: "scope_missing", Detail: "no"}
		}
		return nil
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Validator(loadTestSpec(t), authorize)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/secret", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	var p Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil || p.Code != "scope_missing" {
		t.Fatalf("problem = %+v (%v), want code scope_missing", p, err)
	}
	if len(gotScopes) != 1 || gotScopes[0] != "secrets:read" {
		t.Fatalf("authorize got scopes %v, want [secrets:read]", gotScopes)
	}

	req := httptest.NewRequest(http.MethodGet, "/secret", nil)
	req.Header.Set("X-Allow", "1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed request status = %d, want 200", rec.Code)
	}
}

func TestWriteErrorChallengesOn401(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, &Error{Status: http.StatusUnauthorized, Code: "auth_required", Detail: "x"})
	if got := rec.Header().Get("WWW-Authenticate"); got == "" {
		t.Fatal("401 without WWW-Authenticate")
	}
}
