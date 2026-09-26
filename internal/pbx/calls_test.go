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

func (f *fakeCallStore) TrunkTenant(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	if id == testTrunk {
		return testTenant, nil
	}
	return uuid.Nil, ErrNotFound
}

func (f *fakeCallStore) Country(context.Context) (string, error) { return "AE", nil }

var (
	testTrunk  = uuid.MustParse("7d0c0e3a-0000-4000-8000-000000000001")
	testTenant = uuid.MustParse("7d0c0e3a-0000-4000-8000-0000000000aa")
)

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
		{"not allowed to call that", "00442079460000", func(tr *CallTracker, c *ari.Channel) {
			c.State, c.Dialplan = "Up", ari.Dialplan{Context: "linx-messages", Exten: "not-permitted"}
			tr.handle(ctx, ari.Event{Type: "ChannelDialplan", Channel: c})
		}, OutcomeNotPermitted, CallSystem},
		{"no outside line", "0501234567", func(tr *CallTracker, c *ari.Channel) {
			c.State, c.Dialplan = "Up", ari.Dialplan{Context: "linx-messages", Exten: "no-lines"}
			tr.handle(ctx, ari.Event{Type: "ChannelDialplan", Channel: c})
		}, OutcomeNoLines, CallSystem},
		{"too many outside calls", "0501234567", func(tr *CallTracker, c *ari.Channel) {
			c.State, c.Dialplan = "Up", ari.Dialplan{Context: "linx-messages", Exten: "limit"}
			tr.handle(ctx, ari.Event{Type: "ChannelDialplan", Channel: c})
		}, OutcomeLimitReached, CallSystem},
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

type fakeWatcher struct{ started, ended []OutsideCall }

func (w *fakeWatcher) OutsideCallStarted(_ context.Context, c OutsideCall) {
	w.started = append(w.started, c)
}
func (w *fakeWatcher) OutsideCallEnded(_ context.Context, c OutsideCall) {
	w.ended = append(w.ended, c)
}

func trunkLeg(id string) *ari.Channel {
	return &ari.Channel{ID: id, Name: "PJSIP/trunk-" + testTrunk.String() + "-0000000" + id, State: "Down",
		Dialplan: ari.Dialplan{Context: "linx-outbound", Exten: "s"}}
}

func TestOutboundCall(t *testing.T) {
	ctx := context.Background()
	tr, st := newTracker()
	w := &fakeWatcher{}
	tr.Watch = w
	caller := ch("1", "d_alice001", "Ring", "linx-extensions", "050 123 4567", "")
	caller.Dialplan.Exten = "0501234567"
	tr.handle(ctx, ari.Event{Type: "ChannelCreated", Timestamp: at(0), Channel: caller})
	if calls := tr.ActiveCalls(); len(calls) != 1 || calls[0].Direction != DirectionOutbound || calls[0].TrunkID != nil {
		t.Fatalf("after start: %+v", calls)
	}
	leg := trunkLeg("2")
	tr.handle(ctx, ari.Event{Type: "Dial", Timestamp: at(1), Caller: caller, Peer: leg})
	tr.handle(ctx, ari.Event{Type: "Dial", Timestamp: at(4), Caller: caller, Peer: leg, DialStatus: "ANSWER"})
	if progress := tr.OutsideCallsInProgress(); len(progress) != 1 || !progress[0].WentOut || progress[0].AnsweredAt == nil {
		t.Fatalf("in progress: %+v", progress)
	}
	tr.handle(ctx, ari.Event{Type: "ChannelDestroyed", Timestamp: at(64), Channel: caller})

	if got := st.types(); !equal(got, []string{"call.started", "call.answered", "call.ended"}) {
		t.Fatalf("events %v", got)
	}
	started, ended := st.events[0].Data, st.events[2].Data
	if started["direction"] != DirectionOutbound || started["outside_number"] != "+971501234567" || started["trunk_id"] != nil {
		t.Errorf("call.started = %v", started)
	}
	if ended["trunk_id"] != testTrunk || ended["duration_seconds"] != 60 || ended["outcome"] != OutcomeAnswered {
		t.Errorf("call.ended = %v", ended)
	}
	if by := ended["answered_by"].(*CallParty); by.Number != "+971501234567" || by.DeviceID != nil {
		t.Errorf("answered_by = %+v", by)
	}
	if len(w.started) != 1 || w.started[0].Result.Category != "mobile" || w.started[0].WentOut {
		t.Errorf("watcher started: %+v", w.started)
	}
	if len(w.ended) != 1 || !w.ended[0].WentOut || w.ended[0].TalkSeconds != 60 || *w.ended[0].TrunkID != testTrunk ||
		w.ended[0].Extension != "101" || w.ended[0].Number != "+971501234567" {
		t.Errorf("watcher ended: %+v", w.ended)
	}
}

func TestEmergencyAndInternalCalls(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		to, direction string
		watched       bool
	}{
		{"999", DirectionOutbound, true},
		{"102", DirectionInternal, false},
		{"*43", DirectionInternal, false},
		{"12345", DirectionInternal, false}, // no such extension: "not in use"
	} {
		tr, st := newTracker()
		w := &fakeWatcher{}
		tr.Watch = w
		tr.handle(ctx, ari.Event{Type: "ChannelCreated", Timestamp: at(0), Channel: ch("1", "d_alice001", "Ring", "linx-extensions", tc.to, "")})
		if d := st.events[0].Data["direction"]; d != tc.direction {
			t.Errorf("%s: direction %v, want %s", tc.to, d, tc.direction)
		}
		if (len(w.started) == 1) != tc.watched {
			t.Errorf("%s: watcher heard %d calls", tc.to, len(w.started))
		}
		if tc.watched && w.started[0].Result.Category != "emergency" {
			t.Errorf("%s: category %q", tc.to, w.started[0].Result.Category)
		}
	}
}

func TestInboundCall(t *testing.T) {
	ctx := context.Background()
	tr, st := newTracker()
	w := &fakeWatcher{}
	tr.Watch = w
	caller := &ari.Channel{ID: "1", Name: "PJSIP/trunk-" + testTrunk.String() + "-00000001", State: "Ring",
		Caller:   ari.CallerID{Name: `Bob "B" <script>`, Number: "050 123 4567"},
		Dialplan: ari.Dialplan{Context: "linx-from-trunk", Exten: "linxuser"}, CreationTime: at(0)}
	tr.handle(ctx, ari.Event{Type: "ChannelCreated", Timestamp: at(0), Channel: caller})
	caller.ChannelVars = map[string]string{DIDVariable: "+97142000100"}
	caller.Dialplan = ari.Dialplan{Context: "linx-ring", Exten: "102", AppName: "Dial"}
	tr.handle(ctx, ari.Event{Type: "ChannelDialplan", Timestamp: at(1), Channel: caller})
	calls := tr.ActiveCalls()
	if len(calls) != 1 || calls[0].Direction != DirectionInbound || calls[0].To != "+97142000100" || calls[0].Ringing != "102" ||
		calls[0].From.Number != "+971501234567" || calls[0].From.Name != "Bob B script" || *calls[0].TrunkID != testTrunk {
		t.Fatalf("after start: %+v", calls)
	}
	leg := ch("2", "d_bob00002", "Down", "linx-ring", "s", "")
	tr.handle(ctx, ari.Event{Type: "Dial", Timestamp: at(2), Caller: caller, Peer: leg, DialStatus: "ANSWER"})
	tr.handle(ctx, ari.Event{Type: "ChannelDestroyed", Timestamp: at(9), Channel: caller})
	if got := st.types(); !equal(got, []string{"call.started", "call.answered", "call.ended"}) {
		t.Fatalf("events %v", got)
	}
	ended := st.events[2].Data
	if ended["direction"] != DirectionInbound || ended["outside_number"] != "+971501234567" || ended["to"] != "+97142000100" ||
		ended["answered_by"].(*CallParty).Extension != "102" {
		t.Errorf("call.ended = %v", ended)
	}
	if len(w.started)+len(w.ended) != 0 {
		t.Error("the watcher heard about an inbound call")
	}
}
