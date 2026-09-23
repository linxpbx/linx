package apihttp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLimitBodyAllowsUnderLimit(t *testing.T) {
	var readErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small body"))
	LimitBody(next).ServeHTTP(rec, req)

	if readErr != nil {
		t.Fatalf("reading an under-limit body failed: %v", readErr)
	}
}

func TestLimitBodyRejectsOverLimit(t *testing.T) {
	var readErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	rec := httptest.NewRecorder()
	body := bytes.Repeat([]byte("a"), MaxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	LimitBody(next).ServeHTTP(rec, req)

	if readErr == nil {
		t.Fatal("reading an over-limit body should fail")
	}
}
