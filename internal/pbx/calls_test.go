package pbx

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/ari"
)

type fakeCallStore struct {
	mu      sync.Mutex
	devices map[string]Device // by SIP username
	numbers map[string]string
	events  []struct {
		Type string
		Data map[string]any
	}
}

func newFakeCallStore() *fakeCallStore {
	return &fakeCallStore{devices: map[string]Device{}, numbers: map[string]string{}}
}

func (f *fakeCallStore) add(user, number, name string) Device {
	d := Device{ID: uuid.New(), TenantID: uuid.New(), SIPUsername: user, Name: name, Enabled: true}
	f.devices[user], f.numbers[user] = d, number
	return d
}

func (f *fakeCallStore) DeviceBySIPUsername(_ context.Context, u string) (Device, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[u]
	if !ok {
		return d, "", ErrNotFound
	}
	return d, f.numbers[u], nil
}

func (f *fakeCallStore) SetDeviceOnline(_ context.Context, u string, online bool, from *netip.Addr, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[u]
	if !ok {
		return false, ErrNotFound
	}
	changed := d.Online != online
	d.Online = online
	if online {
		d.LastRegisteredAt, d.LastRegisteredFrom = &at, from
	}
	f.devices[u] = d
	return changed, nil
}

func (f *fakeCallStore) InsertCallEvent(_ context.Context, _ uuid.UUID, typ string, data any, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, struct {
		Type string
		Data map[string]any
	}{typ, data.(map[string]any)})
	return nil
}

func (f *fakeCallStore) types() []string {
	var out []string
	for _, e := range f.events {
		out = append(out, e.Type)
	}
	return out
}

var t0 = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func at(sec int) ari.Time { return ari.Time{Time: t0.Add(time.Duration(sec) * time.Second)} }

func ch(id, user, state, context, exten, app string) *ari.Channel {
	return &ari.Channel{ID: id, Name: "PJSIP/" + user + "-0000000" + id, State: state,
		Dialplan: ari.Dialplan{Context: context, Exten: exten, AppName: app}, CreationTime: at(0)}
}

func newTracker() (*CallTracker, *fakeCallStore) {
	st := newFakeCallStore()
	st.add("d_alice001", "101", "Alice's phone")
	st.add("d_bob00002", "102", "Bob's phone")
	return &CallTracker{Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		calls: map[string]*call{}, legs: map[string]string{}}, st
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestAnsweredCall replays the event order Asterisk sent for a real
// 101 → 102 call (internal/calltest).
func TestAnsweredCall(t *testing.T) {
	tr, st := newTracker()
	ctx := context.Background()
	caller := ch("1", "d_alice001", "Ring", "linx-extensions", "102", "")
	tr.handle(ctx, ari.Event{Type: "ChannelCreated", Timestamp: at(0), Channel: caller})
	if calls := tr.ActiveCalls(); len(calls) != 1 || calls[0].State != CallRinging || calls[0].From.Extension != "101" || calls[0].To != "102" {
		t.Fatalf("after start: %+v", calls)
	}
	leg := ch("2", "d_bob00002", "Down", "linx-extensions", "s", "")
	tr.handle(ctx, ari.Event{Type: "ChannelCreated", Timestamp: at(0), Channel: leg}) // not a new call
	caller.Dialplan = ari.Dialplan{Context: "linx-ring", Exten: "102", AppName: "Dial"}
	tr.handle(ctx, ari.Event{Type: "Dial", Timestamp: at(0), Caller: caller, Peer: leg})
	tr.handle(ctx, ari.Event{Type: "Dial", Timestamp: at(0), Caller: caller, Peer: leg, DialStatus: "RINGING"})
	tr.handle(ctx, ari.Event{Type: "Dial", Timestamp: at(3), Caller: caller, Peer: leg, DialStatus: "ANSWER"})
	caller.State = "Up"
	tr.handle(ctx, ari.Event{Type: "ChannelStateChange", Timestamp: at(3), Channel: caller})
	calls := tr.ActiveCalls()
	if len(calls) != 1 || calls[0].State != CallAnswered || calls[0].AnsweredBy == nil || calls[0].AnsweredBy.Extension != "102" {
		t.Fatalf("after answer: %+v", calls)
	}
	tr.handle(ctx, ari.Event{Type: "ChannelDestroyed", Timestamp: at(10), Channel: caller})
	tr.handle(ctx, ari.Event{Type: "ChannelDestroyed", Timestamp: at(10), Channel: leg})

	if got := st.types(); !equal(got, []string{"call.started", "call.answered", "call.ended"}) {
		t.Fatalf("events %v", got)
	}
	ended := st.events[2].Data
	if ended["outcome"] != OutcomeAnswered || ended["duration_seconds"] != 7 || ended["id"] != st.events[0].Data["id"] {
		t.Errorf("call.ended = %v", ended)
	}
	if len(tr.ActiveCalls()) != 0 || len(tr.legs) != 0 {
		t.Errorf("leftovers: %v %v", tr.ActiveCalls(), tr.legs)
	}
}

func TestCallOutcomes(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, to string
		steps    func(tr *CallTracker, caller *ari.Channel)
		want     string
		state    string
	}{
		{"echo test", "*43", func(tr *CallTracker, c *ari.Channel) {
			c.State = "Up"
			tr.handle(ctx, ari.Event{Type: "ChannelStateChange", Channel: c})
		}, OutcomeEchoTest, CallSystem},
		{"no such number", "555", func(tr *CallTracker, c *ari.Channel) {
			c.State, c.Dialplan = "Up", ari.Dialplan{Context: "linx-messages", Exten: "not-in-use"}
			tr.handle(ctx, ari.Event{Type: "ChannelDialplan", Channel: c})
		}, OutcomeNotInUse, CallSystem},
		{"nothing to ring", "103", func(tr *CallTracker, c *ari.Channel) {
			c.State, c.Dialplan = "Up", ari.Dialplan{Context: "linx-messages", Exten: "not-available"}
			tr.handle(ctx, ari.Event{Type: "ChannelDialplan", Channel: c})
		}, OutcomeNotAvailable, CallSystem},
		{"nobody answers", "102", func(tr *CallTracker, c *ari.Channel) {
			leg := ch("9", "d_bob00002", "Down", "linx-extensions", "s", "")
			tr.handle(ctx, ari.Event{Type: "Dial", Caller: c, Peer: leg})
			tr.handle(ctx, ari.Event{Type: "Dial", Caller: c, Peer: leg, DialStatus: "NOANSWER"})
		}, OutcomeMissed, CallRinging},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, st := newTracker()
			caller := ch("1", "d_alice001", "Ring", "linx-extensions", tt.to, "")
			tr.handle(ctx, ari.Event{Type: "ChannelCreated", Timestamp: at(0), Channel: caller})
			tt.steps(tr, caller)
			if calls := tr.ActiveCalls(); len(calls) != 1 || calls[0].State != tt.state {
				t.Errorf("state: %+v, want %s", calls, tt.state)
			}
			tr.handle(ctx, ari.Event{Type: "ChannelDestroyed", Timestamp: at(5), Channel: caller})
			want := []string{"call.started", "call.ended"}
			if tt.want == OutcomeMissed {
				want = append(want, "call.missed")
			}
			if got := st.types(); !equal(got, want) {
				t.Fatalf("events %v, want %v", got, want)
			}
			if got := st.events[1].Data["outcome"]; got != tt.want {
				t.Errorf("outcome %v, want %s", got, tt.want)
			}
		})
	}
}

func TestUnknownDevicesIgnored(t *testing.T) {
	tr, st := newTracker()
	ctx := context.Background()
	tr.handle(ctx, ari.Event{Type: "ChannelCreated", Channel: ch("1", "d_nobody00", "Ring", "linx-extensions", "102", "")})
	tr.handle(ctx, ari.Event{Type: "ContactStatusChange", Endpoint: &ari.Endpoint{Technology: "PJSIP", Resource: "d_nobody00"},
		ContactInfo: &ari.ContactInfo{ContactStatus: "NonQualified", URI: "sip:x@192.0.2.1:5061"}})
	if len(tr.ActiveCalls()) != 0 || len(st.events) != 0 {
		t.Fatalf("calls %v events %v", tr.ActiveCalls(), st.events)
	}
}

func TestContactStatus(t *testing.T) {
	tr, st := newTracker()
	ctx := context.Background()
	contact := func(status string) {
		tr.handle(ctx, ari.Event{Type: "ContactStatusChange", Timestamp: at(1),
			Endpoint:    &ari.Endpoint{Technology: "PJSIP", Resource: "d_bob00002"},
			ContactInfo: &ari.ContactInfo{ContactStatus: status, URI: "sip:d_bob00002@172.18.0.4:5060;transport=TLS"}})
	}
	online := func() bool { return st.devices["d_bob00002"].Online }

	contact("NonQualified")
	d := st.devices["d_bob00002"]
	if !d.Online || d.LastRegisteredFrom == nil || d.LastRegisteredFrom.String() != "172.18.0.4" || !d.LastRegisteredAt.Equal(at(1).Time) {
		t.Fatalf("after sign-in: %+v", d)
	}
	contact("Reachable")
	if !online() {
		t.Error("Reachable should stay online")
	}
	contact("Unreachable")
	if online() {
		t.Error("Unreachable should be offline")
	}
	contact("Reachable")
	contact("Removed")
	if online() {
		t.Error("Removed should be offline")
	}
	contact("SomethingNew")
	if online() {
		t.Error("an unknown status must not change anything")
	}
}

func TestContactAddr(t *testing.T) {
	for uri, want := range map[string]string{
		"sip:d_x@192.0.2.1:5061;transport=TLS": "192.0.2.1",
		"sip:d_x@192.0.2.1":                    "192.0.2.1",
		"sip:d_x@[2001:db8::1]:5061":           "2001:db8::1",
		"sip:d_x@phone.example.com:5061":       "",
		"garbage":                              "",
	} {
		got := ""
		if a := contactAddr(uri); a != nil {
			got = a.String()
		}
		if got != want {
			t.Errorf("contactAddr(%q) = %q, want %q", uri, got, want)
		}
	}
}
