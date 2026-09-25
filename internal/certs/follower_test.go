package certs

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestFollower(t *testing.T) {
	public := "203.0.113.9"
	trace := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "ip="+public+"\n")
	}))
	defer trace.Close()
	var writes, reads atomic.Int32
	content := ""
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok := func(v any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "result": v}) }
		switch {
		case r.URL.Path == "/zones":
			ok([]map[string]string{{"id": "z"}})
		case r.Method == "GET":
			reads.Add(1)
			if content == "" {
				ok([]any{})
			} else {
				ok([]map[string]any{{"id": "r", "type": "A", "name": r.URL.Query().Get("name"), "content": content}})
			}
		default:
			writes.Add(1)
			var rec cfRecord
			json.NewDecoder(r.Body).Decode(&rec)
			content = rec.Content
			ok(rec)
		}
	}))
	defer cf.Close()

	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	f := &Follower{
		Client: &RecordsClient{HTTP: http.DefaultClient, CloudflareAPI: cf.URL, TraceURL: trace.URL},
		Config: Config{Domain: "lab.example.com", Provider: ProviderCloudflare, TokenFile: tokenFile(t)},
		Hosts:  []string{"meet"},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    func() time.Time { return now },
	}
	ctx := context.Background()
	if err := f.Check(ctx); err != nil || writes.Load() != 1 || content != public {
		t.Fatalf("first check: %v, %d writes, record %q", err, writes.Load(), content)
	}
	// Nothing changed: not even a DNS read.
	r0 := reads.Load()
	now = now.Add(followEvery)
	if err := f.Check(ctx); err != nil || reads.Load() != r0 {
		t.Fatalf("unchanged address touched DNS: %v, %d reads", err, reads.Load()-r0)
	}
	// The home's address changes: the record follows.
	public = "198.51.100.4"
	now = now.Add(followEvery)
	if err := f.Check(ctx); err != nil || content != public || writes.Load() != 2 {
		t.Fatalf("after the address changed: %v, record %q, %d writes", err, content, writes.Load())
	}
	// Six hours on, the records are read again (and left alone).
	now = now.Add(reconfirmEvery)
	r0 = reads.Load()
	if err := f.Check(ctx); err != nil || reads.Load() == r0 || writes.Load() != 2 {
		t.Fatalf("reconfirm: %v, %d reads, %d writes", err, reads.Load()-r0, writes.Load())
	}
	if s := f.Snapshot(); s.Address.String() != public || s.Failures != 0 {
		t.Errorf("stats %+v", s)
	}

	// Home only: a fixed address; the public one is never asked for.
	fixed := &Follower{Client: &RecordsClient{HTTP: http.DefaultClient, CloudflareAPI: cf.URL, TraceURL: "http://127.0.0.1:1"},
		Config: f.Config, Hosts: []string{"meet"}, Fixed: netip.MustParseAddr("192.168.1.20"), Log: f.Log}
	if err := fixed.Check(ctx); err != nil || content != "192.168.1.20" {
		t.Fatalf("fixed address: %v, record %q", err, content)
	}
}
