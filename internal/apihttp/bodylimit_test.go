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

func TestLimitBodyRejectsOverLimitWhileReading(t *testing.T) {
	var readErr error
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	})

	rec := httptest.NewRecorder()
	body := bytes.Repeat([]byte("a"), MaxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(bytes.NewReader(body)))
	req.ContentLength = -1 // unknown length (e.g. chunked): no up-front 413
	LimitBody(next).ServeHTTP(rec, req)

	if readErr == nil {
		t.Fatal("reading an over-limit body with no declared length should fail")
	}
}

func TestLimitBodyRejectsDeclaredOversizeUpFront(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true })

	rec := httptest.NewRecorder()
	// A body whose declared Content-Length is over the cap: turned away
	// with 413 before the handler runs, so nothing reads it and the caller
	// learns the real reason (not a 401 from a later body read).
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(bytes.Repeat([]byte("a"), MaxBodyBytes+1)))
	LimitBody(next).ServeHTTP(rec, req)

	if reached {
		t.Fatal("the handler ran for an over-limit request")
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over-limit request got %d, want 413", rec.Code)
	}
}
