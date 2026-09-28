package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/ops"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// fakeOps stands in for internal/ops.Hub.
type fakeOps struct {
	mu        sync.Mutex
	err       error
	lines     []ops.LogLine
	asked     []string
	refreshed bool
}

func (f *fakeOps) Snapshot() ops.Snapshot {
	return ops.Snapshot{Connected: f.err == nil, CheckedAt: time.Now(), Containers: []ops.Container{
		{Service: "asterisk", State: "running", Health: "unhealthy", Restarts: 2, StartedAt: time.Now()},
		{Service: "sni", State: ops.StateMissing},
	}}
}

func (f *fakeOps) Refresh(context.Context) error {
	f.mu.Lock()
	f.refreshed = true
	f.mu.Unlock()
	return f.err
}

func (f *fakeOps) Logs(_ context.Context, service string, lines int) ([]ops.LogLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, "logs "+service)
	return f.lines, f.err
}

func (f *fakeOps) Restart(_ context.Context, service string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, "restart "+service)
	if f.err != nil {
		return "", f.err
	}
	return "restarted", nil
}

func TestServiceLogAndRestart(t *testing.T) {
	e := newTestEnv(t)
	_, admin := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "all")
	_, reporter := e.newCredential(auth.TypeAPIKey, auth.RoleReporter, "all")
	e.ops.lines = []ops.LogLine{{Time: time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC), Text: "Asterisk Ready."}, {Text: "no time"}}

	t.Run("log", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/system/services/asterisk/log?lines=50", admin, nil)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		var got controlplaneapi.ServiceLog
		r.json(t, &got)
		if got.Service != "asterisk" || len(got.Lines) != 2 || got.Lines[0].Text != "Asterisk Ready." || got.Lines[1].Time != nil {
			t.Errorf("log = %+v", got)
		}
	})
	t.Run("more than 500 lines is refused", func(t *testing.T) {
		if r := e.do(http.MethodGet, "/api/v1/system/services/asterisk/log?lines=501", admin, nil); r.status != http.StatusBadRequest {
			t.Errorf("status %d", r.status)
		}
	})
	t.Run("reporters can't read logs or restart", func(t *testing.T) {
		for _, r := range []response{
			e.do(http.MethodGet, "/api/v1/system/services/asterisk/log", reporter, nil),
			e.do(http.MethodPost, "/api/v1/system/services/asterisk/restart", reporter, nil),
		} {
			if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
				t.Errorf("status %d: %s", r.status, r.body)
			}
		}
	})
	t.Run("restart is audited", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/system/services/asterisk/restart", admin, nil)
		if r.status != http.StatusOK {
			t.Fatalf("status %d: %s", r.status, r.body)
		}
		var got controlplaneapi.ServiceRestart
		r.json(t, &got)
		if got.Result != "restarted" {
			t.Errorf("result = %q", got.Result)
		}
		if !slices.Contains(e.store.auditActions(), "system.service_restart") {
			t.Error("not audited")
		}
	})
	t.Run("unknown service", func(t *testing.T) {
		r := e.do(http.MethodPost, "/api/v1/system/services/portainer/restart", admin, nil)
		if r.status != http.StatusNotFound || r.problemCode(t) != "unknown_service" {
			t.Errorf("status %d: %s", r.status, r.body)
		}
		if slices.Contains(e.ops.asked, "restart portainer") {
			t.Error("asked the helper anyway")
		}
	})
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ops.ErrNotConnected, http.StatusServiceUnavailable, "helper_unavailable"},
		{ops.ErrTooSoon, http.StatusTooManyRequests, "restarted_recently"},
		{ops.ErrNotAllowed, http.StatusConflict, "not_restartable"},
		{ops.ErrBusy, http.StatusServiceUnavailable, "helper_busy"},
		{errors.New("docker couldn't restart it"), http.StatusBadGateway, "helper_failed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			e.ops.err = tc.err
			defer func() { e.ops.err = nil }()
			r := e.do(http.MethodPost, "/api/v1/system/services/certd/restart", admin, nil)
			if r.status != tc.status || r.problemCode(t) != tc.code {
				t.Errorf("status %d: %s", r.status, r.body)
			}
		})
	}
	t.Run("a failed restart is audited as failed", func(t *testing.T) {
		var failed bool
		for _, a := range e.store.audits {
			if a.Action == "system.service_restart" && a.Result == auth.ResultFailed && strings.Contains(a.Target, "certd") {
				failed = true
			}
		}
		if !failed {
			t.Error("no failed entry")
		}
	})
}

// The whole link: agent ↔ ops-bridge ↔ socket ↔ hub.
func TestOpsBridge(t *testing.T) {
	dir, err := os.MkdirTemp("", "ops")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "ops.sock")
	if code := runOpsBridge(sock, strings.NewReader(""), io.Discard, io.Discard); code != 1 {
		t.Errorf("no socket: exit %d, want 1", code)
	}

	ln, err := ops.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(sock); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v, %v", fi.Mode().Perm(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := &ops.Hub{}
	go hub.Serve(ctx, ln)

	// The agent's side of the bridge's stdin/stdout.
	toBridge, agentOut := io.Pipe()
	agentIn, fromBridge := io.Pipe()
	bridged := make(chan int, 1)
	go func() { bridged <- runOpsBridge(sock, toBridge, fromBridge, io.Discard); fromBridge.Close() }()
	agent := &ops.Agent{Exec: func(_ context.Context, _ string, args ...string) ([]byte, []byte, error) {
		if args[0] == "logs" {
			return []byte("2026-09-28T09:00:00Z hello\n"), nil, nil
		}
		return []byte("[]"), nil, nil
	}}
	served := make(chan struct{})
	go func() {
		_ = agent.Serve(ctx, struct {
			io.Reader
			io.Writer
		}{agentIn, agentOut})
		close(served)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for !hub.Snapshot().Connected || hub.Snapshot().CheckedAt.IsZero() {
		if time.Now().After(deadline) {
			t.Fatal("the agent never reported through the bridge")
		}
		time.Sleep(5 * time.Millisecond)
	}
	lines, err := hub.Logs(ctx, "asterisk", 10)
	if err != nil || len(lines) != 1 || lines[0].Text != "hello" {
		t.Fatalf("Logs = %+v, %v", lines, err)
	}
	// The agent going away ends the bridge and the hub's connection.
	agentOut.Close()
	select {
	case code := <-bridged:
		if code != 0 {
			t.Errorf("bridge exit %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the bridge didn't end")
	}
	for hub.Snapshot().Connected {
		if time.Now().After(deadline.Add(3 * time.Second)) {
			t.Fatal("hub still connected")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-served
}
