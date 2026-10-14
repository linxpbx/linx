package calllink

import (
	"encoding/json"
	"strings"
	"testing"
)

func got(t *testing.T, l *Listener) string {
	t.Helper()
	select {
	case b, ok := <-l.Out:
		if !ok {
			t.Fatal("listener closed")
		}
		return string(b)
	default:
		t.Fatal("nothing given")
		return ""
	}
}

// A device rung before its caller's exchange is open is told when it opens;
// one rung later is given everything said so far; the answer ends it for
// all of them; and the two ends of the exchange are paired as its session.
func TestEarlyExchange(t *testing.T) {
	var h Hub
	first := h.EarlyListen("caller-1")
	select {
	case b := <-first.Out:
		t.Fatalf("before the caller: %s", b)
	default:
	}
	session := h.EarlyCaller("caller-1")
	if s := got(t, first); !strings.Contains(s, `"early"`) || h.Session(first) != session {
		t.Fatalf("told of the exchange: %s", s)
	}
	if !h.EarlySend("caller-1", json.RawMessage(`{"sdp":"x"}`)) {
		t.Fatal("offer refused")
	}
	if s := got(t, first); s != `{"type":"signal","data":{"sdp":"x"}}` {
		t.Fatalf("given the offer: %s", s)
	}
	second := h.EarlyListen("caller-1")
	if got(t, second) == "" || got(t, second) != `{"type":"signal","data":{"sdp":"x"}}` {
		t.Fatal("a later device isn't given what was said")
	}
	if h.EarlySend("someone-else", json.RawMessage(`{}`)) {
		t.Fatal("an exchange nobody opened took a message")
	}

	h.EarlyEnd("caller-1")
	for _, l := range []*Listener{first, second} {
		if _, ok := <-l.Out; ok {
			t.Fatal("still open after the answer")
		}
	}
	h.EarlyLeave(first) // after the end, harmless

	a := h.JoinEarly(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_b-2", PeerLinx: true}, session)
	b := h.JoinEarly(Leg{Channel: "PJSIP/d_b-2", Peer: "PJSIP/d_a-1", PeerLinx: true}, h.Session(first))
	for _, m := range []*Member{a, b} {
		if p := next(t, m); p["early"] != true || p["session"] != float64(session) {
			t.Fatalf("paired: %v", p)
		}
	}
}

// What a caller may leave waiting is limited.
func TestEarlyBacklogIsLimited(t *testing.T) {
	var h Hub
	h.EarlyCaller("c")
	n := 0
	for h.EarlySend("c", json.RawMessage(`{"candidate":"`+strings.Repeat("x", 100)+`"}`)) {
		n++
		if n > earlyBacklog {
			break
		}
	}
	if n != earlyBacklog {
		t.Fatalf("kept %d messages", n)
	}
}
