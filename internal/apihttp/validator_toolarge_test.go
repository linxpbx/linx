package apihttp

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestBodyTooLarge(t *testing.T) {
	// Wrapped, as a well-behaved error chain carries it.
	wrapped := fmt.Errorf("reading failed: %w", &http.MaxBytesError{Limit: MaxBodyBytes})
	if !bodyTooLarge(wrapped) {
		t.Error("didn't recognise a wrapped MaxBytesError")
	}
	// Stringified, as kin-openapi surfaced it live:
	// "security requirements failed: reading failed: http: request body too large".
	stringified := errors.New("security requirements failed: reading failed: http: request body too large")
	if !bodyTooLarge(stringified) {
		t.Error("didn't recognise the stringified body-too-large error")
	}
	// An ordinary validation error is not this.
	if bodyTooLarge(errors.New("request body has an error: doesn't match schema")) {
		t.Error("an ordinary validation error looked like body-too-large")
	}
}
