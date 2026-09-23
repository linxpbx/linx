package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAPIHandlerEndToEnd(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := newAPIHandler(log)
	if err != nil {
		t.Fatalf("newAPIHandler: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/v1/", handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("openapi.json is served and valid JSON", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/v1/openapi.json")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var doc map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if doc["openapi"] != "3.1.0" {
			t.Fatalf("openapi field = %v, want 3.1.0", doc["openapi"])
		}
	})

	t.Run("me returns the temporary system principal", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/v1/me")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var me struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&me); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if me.ID != "system" || me.Type != "system" {
			t.Fatalf("unexpected principal: %+v", me)
		}
	})

	t.Run("event-types paginates", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/v1/event-types?limit=3")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var list struct {
			Items      []map[string]string `json:"items"`
			NextCursor *string             `json:"next_cursor"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if len(list.Items) != 3 || list.NextCursor == nil {
			t.Fatalf("unexpected page: %+v", list)
		}
	})

	t.Run("an unmatched path is a problem+json 404, not the stdlib default", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/v1/does-not-exist")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
			t.Fatalf("Content-Type = %q, want application/problem+json", ct)
		}
	})

	t.Run("an out-of-range limit is rejected before the handler runs", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/api/v1/event-types?limit=99999")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", resp.StatusCode)
		}
	})
}
