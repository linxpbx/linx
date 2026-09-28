package api

import (
	"context"
	"testing"
	"time"

	"linxpbx.com/linx/internal/ops"
)

type snapOps struct {
	snap    ops.Snapshot
	checked bool
}

func (s *snapOps) Snapshot() ops.Snapshot                                   { return s.snap }
func (s *snapOps) Refresh(context.Context) error                            { s.checked = true; return nil }
func (s *snapOps) Logs(context.Context, string, int) ([]ops.LogLine, error) { return nil, nil }
func (s *snapOps) Restart(context.Context, string) (string, error)          { return "", nil }

func TestOpsStatus(t *testing.T) {
	started := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)
	o := &snapOps{snap: ops.Snapshot{Connected: true, CheckedAt: started, Containers: []ops.Container{
		{Service: "control-plane", State: "running", Health: "healthy", StartedAt: started},
		{Service: "asterisk", State: "running", Health: "starting", StartedAt: started},
		{Service: "coturn", State: "exited"},
		{Service: "postgres", State: "exited"},
		{Service: "sni", State: ops.StateMissing},
		{Service: "wireguard", State: "running"},
	}}}
	expiry := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	s := testServer(t)
	s.SetOps(o, nil, func() time.Time { return expiry })
	out := SystemStatus{Services: map[string]SystemStatusServices{"control-plane": "ok", "database": "ok"}}
	s.opsStatus(context.Background(), true, &out)

	if !o.checked {
		t.Error("check=true didn't ask the helper to check")
	}
	if !out.Helper.Connected || out.Helper.CheckedAt == nil || out.Certificate == nil || !out.Certificate.ExpiresAt.Equal(expiry) {
		t.Errorf("helper/certificate = %+v %+v", out.Helper, out.Certificate)
	}
	want := map[string]SystemStatusServices{
		"control-plane": "ok", "database": "ok", "asterisk": "degraded", "coturn": "down", "wireguard": "ok",
	}
	if len(out.Services) != len(want) {
		t.Errorf("services = %v, want %v (a missing optional one isn't listed; the database answered)", out.Services, want)
	}
	for k, v := range want {
		if out.Services[k] != v {
			t.Errorf("services[%s] = %q, want %q", k, out.Services[k], v)
		}
	}
	if len(out.Containers) != 6 || out.Containers[1].Label != "Phone system" || out.Containers[1].State != ServiceHealthStateStarting ||
		!out.Containers[1].CanRestart || out.Containers[3].CanRestart || !out.Containers[4].Optional {
		t.Errorf("containers = %+v", out.Containers)
	}
}

func TestOpsStatusWithoutHelper(t *testing.T) {
	out := SystemStatus{Services: map[string]SystemStatusServices{}}
	testServer(t).opsStatus(context.Background(), false, &out)
	if out.Helper.Connected || out.Containers == nil || len(out.Containers) != 0 || out.Certificate != nil {
		t.Errorf("status = %+v", out)
	}
}
