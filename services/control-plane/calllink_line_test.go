package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"linxpbx.com/linx/internal/calllink"
)

// ringingResolver is Asterisk with one call between phones "a" and "b",
// ringing until answered is set.
type ringingResolver struct{ answered atomic.Bool }

func (r *ringingResolver) leg(user string) calllink.Leg {
	other := map[string]string{"a": "b", "b": "a"}[user]
	return calllink.Leg{Channel: "PJSIP/" + user + "-1", ChannelID: user + "-1", Peer: "PJSIP/" + other + "-1", PeerLinx: true}
}

func (r *ringingResolver) Resolve(_ context.Context, user, call string) (calllink.Leg, error) {
	if call != "call-1" || (user != "a" && user != "b") {
		return calllink.Leg{}, calllink.ErrNoCall
	}
	if !r.answered.Load() {
		return calllink.Leg{}, calllink.ErrNotAnswered
	}
	return r.leg(user), nil
}

func (r *ringingResolver) WaitAnswered(ctx context.Context, user, call string, _ time.Duration, _ func() error) (calllink.Leg, error) {
	for {
		leg, err := r.Resolve(ctx, user, call)
		if err != calllink.ErrNotAnswered {
			return leg, err
		}
		select {
		case <-ctx.Done():
			return calllink.Leg{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (r *ringingResolver) Alive(context.Context, string) bool { return true }

// Origin: "a" made the call and "b" is being rung.
func (r *ringingResolver) Origin(_ context.Context, user, call string) (string, string, error) {
	if call != "call-1" || (user != "a" && user != "b") {
		return "", "", calllink.ErrNoCall
	}
	return user + "-1", "a-1", nil
}

// phoneOnLine is one phone's line as the call links see it: what it was
// sent, one message at a time.
type phoneOnLine struct {
	links *lineLinks
	got   chan string
}

func newPhoneOnLine(ctx context.Context, r lineResolver, hub *calllink.Hub, user string) *phoneOnLine {
	p := &phoneOnLine{got: make(chan string, 64)}
	p.links = newLineLinks(ctx, r, hub, user, func(b []byte) error { p.got <- string(b); return nil })
	return p
}

func (p *phoneOnLine) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-p.got:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived on the line")
		return ""
	}
}

// Two phones open their links on their lines while the call rings: each is
// told to wait, both are paired at the answer, and what one sends the
// other gets, unchanged.
func TestCallLinksOnTheLine(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &ringingResolver{}
	hub := &calllink.Hub{}
	a, b := newPhoneOnLine(ctx, r, hub, "a"), newPhoneOnLine(ctx, r, hub, "b")

	a.links.received([]byte(`{"call":"call-1","open":true}`))
	b.links.received([]byte(`{"call":"call-1","open":true}`))
	for _, p := range []*phoneOnLine{a, b} {
		if s := p.next(t); s != `{"call":"call-1","msg":{"type":"wait"}}` {
			t.Fatalf("while ringing: %s", s)
		}
	}
	r.answered.Store(true)
	for _, p := range []*phoneOnLine{a, b} {
		var got struct {
			Call string               `json:"call"`
			Msg  calllink.PeerMessage `json:"msg"`
		}
		if s := p.next(t); json.Unmarshal([]byte(s), &got) != nil || got.Call != "call-1" || got.Msg.Type != "peer" || !got.Msg.Video {
			t.Fatalf("at the answer: %s", s)
		}
	}
	a.links.received([]byte(`{"call":"call-1","msg":{"type":"signal","data":{"description":{"type":"offer","sdp":"v=0"}}}}`))
	if s := b.next(t); s != `{"call":"call-1","msg":{"type":"signal","data":{"description":{"type":"offer","sdp":"v=0"}}}}` {
		t.Errorf("relayed: %s", s)
	}
	a.links.received([]byte(`{"call":"call-1","close":true}`))
	if s := b.next(t); !strings.Contains(s, `"peer-gone"`) {
		t.Errorf("after the other closed: %s", s)
	}
}

// A call that isn't the phone's own is closed at once, and nothing a phone
// sends on a link it hasn't opened goes anywhere.
func TestCallLinkOnTheLineRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &ringingResolver{}
	hub := &calllink.Hub{}
	c := newPhoneOnLine(ctx, r, hub, "c")
	c.links.received([]byte(`{"call":"call-1","open":true}`))
	if s := c.next(t); s != `{"call":"call-1","closed":"the call has ended"}` {
		t.Errorf("someone else's call: %s", s)
	}
	c.links.received([]byte(`{"call":"call-1","msg":{"type":"signal","data":{}}}`))
	c.links.received([]byte(`not json`))
	c.links.received([]byte(`{"call":"","open":true}`))
	select {
	case s := <-c.got:
		t.Errorf("sent %s", s)
	case <-time.After(200 * time.Millisecond):
	}
}

// A video call's picture set up while it rings: the caller is told to offer
// and its offer goes to the phone being rung, which holds its answer until
// its person answers; that answer, sent before Linx has seen the call
// answered, reaches the caller once the two are paired, and the pairing
// goes on from the exchange.
func TestPictureSetUpWhileRinging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &ringingResolver{}
	hub := &calllink.Hub{}
	a, b := newPhoneOnLine(ctx, r, hub, "a"), newPhoneOnLine(ctx, r, hub, "b")

	a.links.received([]byte(`{"call":"call-1","open":true,"early":true}`))
	if s := a.next(t); s != `{"call":"call-1","msg":{"type":"wait"}}` {
		t.Fatalf("caller, while ringing: %s", s)
	}
	var early struct {
		Msg calllink.EarlyMessage `json:"msg"`
	}
	if s := a.next(t); json.Unmarshal([]byte(s), &early) != nil || early.Msg.Type != "early" || !early.Msg.Offer || early.Msg.Session == 0 {
		t.Fatalf("caller told to offer: %s", s)
	}
	session := early.Msg.Session
	offer := `{"type":"signal","data":{"description":{"type":"offer","sdp":"v=0"},"session":1}}`
	a.links.received([]byte(`{"call":"call-1","msg":` + offer + `}`))

	// The phone being rung comes after the offer was made, and is given it.
	b.links.received([]byte(`{"call":"call-1","open":true,"early":true}`))
	if s := b.next(t); s != `{"call":"call-1","msg":{"type":"wait"}}` {
		t.Fatalf("rung, while ringing: %s", s)
	}
	early.Msg = calllink.EarlyMessage{}
	if s := b.next(t); json.Unmarshal([]byte(s), &early) != nil || early.Msg.Type != "early" || early.Msg.Offer || early.Msg.Session != session {
		t.Fatalf("rung, told of the exchange: %s", s)
	}
	if s := b.next(t); s != `{"call":"call-1","msg":{"type":"signal","data":{"description":{"type":"offer","sdp":"v=0"},"session":1}}}` {
		t.Fatalf("rung, given the offer: %s", s)
	}
	// Its person answers: the answer goes before Linx sees the call answered.
	b.links.received([]byte(`{"call":"call-1","msg":{"type":"signal","data":{"description":{"type":"answer","sdp":"v=1"}}}}`))
	b.links.received([]byte(`{"call":"call-1","msg":{"type":"signal","data":{"candidate":{"candidate":"c"}}}}`))
	r.answered.Store(true)
	for _, p := range []*phoneOnLine{a, b} {
		var got struct {
			Msg calllink.PeerMessage `json:"msg"`
		}
		if s := p.next(t); json.Unmarshal([]byte(s), &got) != nil || got.Msg.Type != "peer" || !got.Msg.Early || got.Msg.Session != session {
			t.Fatalf("at the answer: %s", s)
		}
	}
	if s := a.next(t); s != `{"call":"call-1","msg":{"type":"signal","data":{"description":{"type":"answer","sdp":"v=1"}}}}` {
		t.Fatalf("caller given the answer: %s", s)
	}
	if s := a.next(t); s != `{"call":"call-1","msg":{"type":"signal","data":{"candidate":{"candidate":"c"}}}}` {
		t.Fatalf("caller given the route after it: %s", s)
	}
}

// A phone that doesn't set its picture up while ringing (an older app) is
// paired afresh with one that did: the caller's exchange is not carried on,
// and nothing either said before the answer goes to the other.
func TestPictureWhileRingingNeedsBothEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &ringingResolver{}
	hub := &calllink.Hub{}
	a, b := newPhoneOnLine(ctx, r, hub, "a"), newPhoneOnLine(ctx, r, hub, "b")

	a.links.received([]byte(`{"call":"call-1","open":true,"early":true}`))
	a.next(t)
	a.next(t)
	a.links.received([]byte(`{"call":"call-1","msg":{"type":"signal","data":{"description":{"type":"offer","sdp":"v=0"}}}}`))
	b.links.received([]byte(`{"call":"call-1","open":true}`))
	if s := b.next(t); s != `{"call":"call-1","msg":{"type":"wait"}}` {
		t.Fatalf("older phone, while ringing: %s", s)
	}
	r.answered.Store(true)
	for _, p := range []*phoneOnLine{a, b} {
		var got struct {
			Msg calllink.PeerMessage `json:"msg"`
		}
		if s := p.next(t); json.Unmarshal([]byte(s), &got) != nil || got.Msg.Type != "peer" || got.Msg.Early {
			t.Fatalf("at the answer: %s", s)
		}
	}
	select {
	case s := <-b.got:
		t.Errorf("older phone sent %s", s)
	case <-time.After(200 * time.Millisecond):
	}
}
