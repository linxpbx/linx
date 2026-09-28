package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/moved"
)

type fakeMovedStore struct{ move *moved.Move }

func (f *fakeMovedStore) RecordPlace(context.Context, moved.Place, time.Time, uuid.UUID) (*moved.Move, error) {
	return nil, nil
}
func (f *fakeMovedStore) CurrentMove(context.Context) (*moved.Move, error) {
	if f.move == nil || f.move.DoneAt != nil {
		return nil, nil
	}
	m := *f.move
	return &m, nil
}
func (f *fakeMovedStore) UpdateMove(_ context.Context, m moved.Move) error { f.move = &m; return nil }
func (f *fakeMovedStore) Facts(context.Context, uuid.UUID) (moved.Facts, error) {
	return moved.Facts{DeskPhones: 2, BackedUpSince: func(time.Time) bool { return false }}, nil
}

func TestMovedChecklistEndpoints(t *testing.T) {
	e := newBackupFileEnv(t)
	st := &fakeMovedStore{}
	e.apiServer.SetMoved(&moved.Service{Store: st})
	get := func() (int, map[string]any) {
		resp := e.do("GET", "/api/v1/moved-checklist", "", nil, false)
		defer resp.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	patch := func(body string) *http.Response {
		return e.do("PATCH", "/api/v1/moved-checklist", "application/json", strings.NewReader(body), true)
	}

	e.become(auth.RoleAdmin, 0)
	if code, body := get(); code != 200 || body["checklist"] != nil {
		t.Fatalf("none: %d %v", code, body)
	}
	if code := problemCode(t, patch(`{"hide":true}`)); code != "no_checklist" {
		t.Errorf("patch without one: %s", code)
	}
	st.move = &moved.Move{ID: uuid.New(), DetectedAt: time.Now().Add(-time.Hour),
		Before: moved.Place{ServerID: "a", Domain: "pbx.old.com", LANNetworks: []string{"192.168.1.0/24"}},
		After:  moved.Place{ServerID: "b", Domain: "example.com"}}
	code, body := get()
	c, _ := body["checklist"].(map[string]any)
	if code != 200 || c == nil || len(c["items"].([]any)) != 4 {
		t.Fatalf("checklist: %d %v", code, body)
	}
	if code := problemCode(t, patch(`{"ticks":{"backups":true}}`)); code != "unknown_item" {
		t.Errorf("tick an automatic item: %s", code)
	}
	resp := patch(`{"ticks":{"old_server":true},"hide":true}`)
	if resp.StatusCode != 200 || !st.move.Ticks["old_server"] || st.move.HiddenUntil == nil {
		t.Errorf("tick: %d %+v", resp.StatusCode, st.move)
	}
	// A reporter doesn't see it (no settings:read).
	e.become(auth.RoleReporter, 0)
	if code, _ := get(); code != http.StatusForbidden {
		t.Errorf("reporter: %d", code)
	}
	found := false
	for _, a := range e.store.audits {
		found = found || a.Action == "settings.moved_checklist"
	}
	if !found {
		t.Error("not audited")
	}
}
