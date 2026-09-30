package main

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/reach"
	"linxpbx.com/linx/internal/turn"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

func TestReach(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, reporter := e.newCredential(auth.TypeAPIKey, auth.RoleReporter, "all")
	links := &reach.Links{Domain: "linx.example.com", Now: time.Now, Proxies: func(netip.Addr) bool { return false },
		TURN: &turn.Issuer{Secret: []byte("s"), URLs: []string{"turns:turn.linx.example.com:443?transport=tcp"}}}
	e.apiServer.SetReach(func(context.Context) []reach.Line {
		return []reach.Line{{State: reach.OK, Text: "linx.example.com answers with Linx's certificate"},
			{State: reach.Fail, Text: "turn.linx.example.com doesn't answer.", Meaning: "Calls from outside will have no audio.", Fix: reach.FixSteps}}
	}, links)

	t.Run("from this server", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/system/reach-check", admin, nil)
		var got controlplaneapi.ReachCheck
		r.json(t, &got)
		if r.status != http.StatusOK || len(got.Lines) != 2 || got.Lines[1].State != "fail" || got.Lines[1].Fix == nil || *got.Lines[1].Fix != "steps" {
			t.Errorf("status %d: %s", r.status, r.body)
		}
		if r := e.do(http.MethodPost, "/api/v1/system/reach-check", reporter, nil); r.status == http.StatusOK {
			t.Errorf("a reporter ran it: %d", r.status)
		}
	})

	t.Run("from a phone", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/system/reach-links", admin, nil)
		var link controlplaneapi.ReachLink
		r.json(t, &link)
		if r.status != http.StatusCreated || link.State != "waiting" || !strings.HasPrefix(link.Url, "https://linx.example.com/reach/") {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		code := strings.TrimPrefix(link.Url, "https://linx.example.com/reach/")
		path := "/api/v1/system/reach-links/" + link.Id.String()

		// The admin's page waits; the phone opens the link meanwhile.
		got := make(chan response)
		go func() { got <- e.do(http.MethodGet, path+"?after=0", admin, nil) }()
		time.Sleep(50 * time.Millisecond)
		c := e.do(http.MethodPost, "/api/v1/reach/"+code, "", nil)
		var claim controlplaneapi.ReachClaim
		c.json(t, &claim)
		if c.status != http.StatusOK || claim.Domain != "linx.example.com" || claim.Address != "127.0.0.1" ||
			!strings.Contains(claim.Turn.Username, ":linx-reach-") || len(claim.Turn.Urls) != 1 {
			t.Fatalf("claim %d: %s", c.status, c.body)
		}
		w := <-got
		var after controlplaneapi.ReachLink
		w.json(t, &after)
		if after.State != "reached" || after.Address == nil || *after.Address != "127.0.0.1" || after.Seen == nil || *after.Seen != "local" {
			t.Errorf("waiting page got %s", w.body)
		}
		// Once only; the relay report once too.
		if c := e.do(http.MethodPost, "/api/v1/reach/"+code, "", nil); c.status != http.StatusNotFound || c.problemCode(t) != "reach_link_invalid" {
			t.Errorf("second claim %d: %s", c.status, c.body)
		}
		if r := e.do(http.MethodPost, "/api/v1/reach/"+code+"/relay", "", map[string]any{"ok": true}); r.status != http.StatusNoContent {
			t.Errorf("relay %d: %s", r.status, r.body)
		}
		if r := e.do(http.MethodPost, "/api/v1/reach/"+code+"/relay", "", map[string]any{"ok": false}); r.status != http.StatusNotFound {
			t.Errorf("relay twice %d", r.status)
		}
		var done controlplaneapi.ReachLink
		e.do(http.MethodGet, path, admin, nil).json(t, &done)
		if done.Relay == nil || !done.Relay.Ok {
			t.Errorf("relay not recorded: %+v", done)
		}
	})

	t.Run("guessing codes is limited", func(t *testing.T) {
		last := 0
		for range 8 {
			last = e.do(http.MethodPost, "/api/v1/reach/GUESS23456", "", nil).status
		}
		if last != http.StatusTooManyRequests {
			t.Errorf("status %d after 8 guesses", last)
		}
	})
}

func TestDNSRecords(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, reporter := e.newCredential(auth.TypeAPIKey, auth.RoleReporter, "all")
	e.apiServer.SetDNSRecords(func(context.Context) reach.Records {
		return reach.Records{Domain: "linx.example.com", Zone: "example.com", NameServers: []string{"ada.ns.cloudflare.com"}, Company: "Cloudflare",
			Records:   []reach.Record{{Use: reach.UseWeb, Name: "linx.example.com", Type: "A", Value: "94.200.1.10", State: reach.RecordOK, Seen: []string{"94.200.1.10"}}},
			CheckedAt: time.Now()}
	})
	r := e.do(http.MethodGet, "/api/v1/system/dns-records", admin, nil)
	var got controlplaneapi.DnsRecords
	r.json(t, &got)
	if r.status != http.StatusOK || got.Company != "Cloudflare" || len(got.Records) != 1 || got.Records[0].State != "ok" || got.Records[0].Use != "web" {
		t.Errorf("status %d: %s", r.status, r.body)
	}
	if r := e.do(http.MethodGet, "/api/v1/system/dns-records", reporter, nil); r.status == http.StatusOK {
		t.Errorf("a reporter saw them: %d", r.status)
	}
}

func TestKeptRecords(t *testing.T) {
	got := keptRecords("@,turn,sip=192.168.1.212")
	if !got[reach.UseWeb] || !got[reach.UseTURN] || !got[reach.UseSIP] || len(keptRecords("")) != 0 {
		t.Errorf("kept %v", got)
	}
}
