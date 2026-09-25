package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// fakeTeamStore lists one member per person whose status was set, plus
// anything in members.
type fakeTeamStore struct {
	mu       sync.Mutex
	members  []pbx.TeamMember
	presence map[uuid.UUID]string
}

func (f *fakeTeamStore) TeamMembers(context.Context, uuid.UUID) ([]pbx.TeamMember, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pbx.TeamMember(nil), f.members...), nil
}

func (f *fakeTeamStore) SetPresence(_ context.Context, _, user uuid.UUID, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presence[user] = p
	for i := range f.members {
		f.members[i].Presence = p
	}
	return nil
}

func putJSON(t *testing.T, env *testEnv, path string, body any, cookies []*http.Cookie, csrf, bearer string) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPut, env.srv.URL+path, bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if csrf != "" {
		req.Header.Set(auth.CSRFHeaderName, csrf)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (e *testEnv) dialTeam(cookies []*http.Cookie, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	for _, c := range cookies {
		h.Add("Cookie", c.Name+"="+c.Value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.srv.URL, "http")+controlplaneapi.TeamLivePath,
		&websocket.DialOptions{HTTPHeader: h, Subprotocols: []string{controlplaneapi.TeamSubprotocol}})
}

type teamListBody struct {
	Items []struct {
		Extension string `json:"extension"`
		Name      string `json:"name"`
		Status    string `json:"status"`
	} `json:"items"`
}

func TestTeamList(t *testing.T) {
	e := newTestEnv(t)
	e.team.members = []pbx.TeamMember{
		{Extension: "102", Name: "Omar", Online: false, Presence: pbx.PresenceAvailable},
		{Extension: "101", Name: "Aisha", Online: true, Presence: pbx.PresenceAvailable},
	}
	e.calls.calls = []pbx.ActiveCall{{From: pbx.CallParty{Extension: "103"}, To: "101", State: pbx.CallRinging, StartedAt: time.Now()}}
	_, reader := e.newCredential(auth.TypeAPIKey, auth.RoleUser, "team:read")
	_, other := e.newCredential(auth.TypeAPIKey, auth.RoleUser, "extensions:read")

	if r := e.do(http.MethodGet, "/api/v1/team", other, nil); r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
		t.Errorf("without team:read: %d", r.status)
	}
	r := e.do(http.MethodGet, "/api/v1/team", reader, nil)
	if r.status != http.StatusOK {
		t.Fatalf("GET /team: %d %s", r.status, r.body)
	}
	var body teamListBody
	r.json(t, &body)
	if len(body.Items) != 2 || body.Items[0].Name != "Aisha" || body.Items[0].Status != pbx.TeamRinging ||
		body.Items[1].Status != pbx.TeamOffline {
		t.Errorf("GET /team = %+v", body)
	}
}

func TestSetMyPresence(t *testing.T) {
	e := newTestEnv(t)
	ext := e.newExtension("101")
	uid, cookies, csrf := signedInPerson(t, e, "rana@example.com", &ext.ID)

	resp := putJSON(t, e, "/api/v1/me/presence", map[string]string{"presence": "dnd"}, cookies, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || e.team.presence[uid] != pbx.PresenceDND {
		t.Fatalf("set dnd: %d, stored %q", resp.StatusCode, e.team.presence[uid])
	}
	resp = putJSON(t, e, "/api/v1/me/presence", map[string]string{"presence": "busy"}, cookies, csrf, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown status: %d", resp.StatusCode)
	}
	resp = putJSON(t, e, "/api/v1/me/presence", map[string]string{"presence": "away"}, cookies, "", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("without the CSRF header: %d", resp.StatusCode)
	}
	_, key := e.newCredential(auth.TypeAPIKey, auth.RoleUser, "team:read")
	resp = putJSON(t, e, "/api/v1/me/presence", map[string]string{"presence": "away"}, nil, "", key)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an API key has no status of its own: %d", resp.StatusCode)
	}

	// GET /me tells the page its own extension number.
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/me", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	me := e.send(req)
	var body struct {
		Extension string `json:"extension"`
	}
	me.json(t, &body)
	if body.Extension != "101" {
		t.Errorf("GET /me extension = %q", body.Extension)
	}
}

func TestTeamLive(t *testing.T) {
	e := newTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go e.hub.Run(ctx)
	ext := e.newExtension("101")
	e.team.members = []pbx.TeamMember{{Extension: "101", Name: "Rana Haddad", Online: true, Presence: pbx.PresenceAvailable}}
	_, cookies, csrf := signedInPerson(t, e, "rana@example.com", &ext.ID)
	origin := "https://" + strings.TrimPrefix(e.srv.URL, "http://")

	if _, resp, err := e.dialTeam(cookies, "https://evil.example.com"); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("another page's origin: %v", err)
	}
	_, key := e.newCredential(auth.TypeAPIKey, auth.RoleUser, "team:read")
	h := http.Header{"Authorization": {"Bearer " + key}, "Origin": {origin}}
	if _, resp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(e.srv.URL, "http")+controlplaneapi.TeamLivePath,
		&websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an API key: %v", err)
	}

	c, _, err := e.dialTeam(cookies, origin)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read := func() teamListBody {
		t.Helper()
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, b, err := c.Read(rctx)
		if err != nil {
			t.Fatal(err)
		}
		var body teamListBody
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if first := read(); len(first.Items) != 1 || first.Items[0].Status != pbx.TeamAvailable {
		t.Fatalf("first message: %+v", first)
	}
	resp := putJSON(t, e, "/api/v1/me/presence", map[string]string{"presence": "away"}, cookies, csrf, "")
	resp.Body.Close()
	if next := read(); next.Items[0].Status != pbx.TeamAway {
		t.Fatalf("after setting away: %+v", next)
	}

	// One session can hold only so many open at once.
	for i := 1; i < teamPerSession; i++ {
		more, _, err := e.dialTeam(cookies, origin)
		if err != nil {
			t.Fatalf("list %d: %v", i+1, err)
		}
		defer more.CloseNow()
	}
	if _, resp, err := e.dialTeam(cookies, origin); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("one list too many: %v", err)
	}
}
