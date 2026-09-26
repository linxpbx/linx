package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

type fakeCalls struct {
	connected bool
	calls     []pbx.ActiveCall
}

func (f *fakeCalls) ActiveCalls() []pbx.ActiveCall { return f.calls }
func (f *fakeCalls) Connected() bool               { return f.connected }

func TestActiveCalls(t *testing.T) {
	e := newTestEnv(t)
	_, reader := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "calls:read")
	_, other := e.newCredential(auth.TypeAPIKey, auth.RoleAdmin, "extensions:read")

	t.Run("needs calls:read", func(t *testing.T) {
		r := e.do(http.MethodGet, "/api/v1/calls/active", other, nil)
		if r.status != http.StatusForbidden || r.problemCode(t) != "scope_missing" {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})

	t.Run("empty list, engine not connected", func(t *testing.T) {
		e.calls.connected = false
		r := e.do(http.MethodGet, "/api/v1/calls/active", reader, nil)
		var out controlplaneapi.ActiveCallList
		r.json(t, &out)
		if r.status != http.StatusOK || out.Items == nil || len(out.Items) != 0 || out.PhoneEngineConnected {
			t.Fatalf("got %d %s", r.status, r.body)
		}
	})

	t.Run("an answered call", func(t *testing.T) {
		e.calls.connected = true
		start := time.Now().UTC().Truncate(time.Second)
		answered := start.Add(5 * time.Second)
		aliceID, bobID := uuid.New(), uuid.New()
		alice := pbx.CallParty{Extension: "101", DeviceID: &aliceID, Name: "Alice's phone"}
		bob := pbx.CallParty{Extension: "102", DeviceID: &bobID, Name: "Bob's phone"}
		id := uuid.New()
		e.calls.calls = []pbx.ActiveCall{{ID: id, Direction: pbx.DirectionInternal, From: alice, To: "102", Ringing: "102",
			State: pbx.CallAnswered, StartedAt: start, AnsweredAt: &answered, AnsweredBy: &bob}}
		r := e.do(http.MethodGet, "/api/v1/calls/active", reader, nil)
		var out controlplaneapi.ActiveCallList
		r.json(t, &out)
		if r.status != http.StatusOK || len(out.Items) != 1 || !out.PhoneEngineConnected {
			t.Fatalf("got %d %s", r.status, r.body)
		}
		c := out.Items[0]
		if c.Id != id || c.From.Extension == nil || *c.From.Extension != "101" || c.To != "102" || c.State != controlplaneapi.ActiveCallStateAnswered ||
			c.Direction != controlplaneapi.Internal || c.AnsweredBy == nil || *c.AnsweredBy.DeviceId != bobID ||
			!c.AnsweredAt.Equal(answered) || c.From.Number != nil || c.TrunkId != nil {
			t.Fatalf("unexpected call %+v", c)
		}
	})

	t.Run("a call from outside", func(t *testing.T) {
		trunkID := uuid.New()
		e.calls.calls = []pbx.ActiveCall{{ID: uuid.New(), Direction: pbx.DirectionInbound, TrunkID: &trunkID,
			From: pbx.CallParty{Number: "+971501234567"}, To: "+97142000100", Ringing: "101", State: pbx.CallRinging, StartedAt: time.Now()}}
		r := e.do(http.MethodGet, "/api/v1/calls/active", reader, nil)
		var out controlplaneapi.ActiveCallList
		r.json(t, &out)
		c := out.Items[0]
		if c.Direction != controlplaneapi.Inbound || c.TrunkId == nil || *c.TrunkId != trunkID ||
			c.From.Number == nil || *c.From.Number != "+971501234567" || c.From.Extension != nil || c.Ringing == nil || *c.Ringing != "101" {
			t.Fatalf("unexpected call %s", r.body)
		}
	})
}
