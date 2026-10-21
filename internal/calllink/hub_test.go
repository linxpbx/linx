package calllink

import (
	"encoding/json"
	"testing"
	"time"
)

func next(t *testing.T, m *Member) map[string]any {
	t.Helper()
	select {
	case b, ok := <-m.Out:
		if !ok {
			t.Fatal("link closed")
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatal(err)
		}
		return v
	default:
		t.Fatal("nothing sent")
		return nil
	}
}

func nothing(t *testing.T, m *Member) {
	t.Helper()
	select {
	case b := <-m.Out:
		t.Fatalf("unexpected %s", b)
	default:
	}
}

func TestPairsTheTwoEndsOfOneCall(t *testing.T) {
	var h Hub
	a := h.Join(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_b-2", PeerLinx: true})
	nothing(t, a)
	// Someone else's call doesn't pair with it.
	c := h.Join(Leg{Channel: "PJSIP/d_c-3", Peer: "PJSIP/d_a-9", PeerLinx: true})
	nothing(t, a)
	nothing(t, c)
	b := h.Join(Leg{Channel: "PJSIP/d_b-2", Peer: "PJSIP/d_a-1", PeerLinx: true})
	pa, pb := next(t, a), next(t, b)
	if pa["type"] != "peer" || pa["video"] != true || pb["video"] != true {
		t.Fatalf("peer messages %v %v", pa, pb)
	}
	if (pa["polite"] == true) == (pb["polite"] == true) {
		t.Fatalf("both ends polite or neither: %v %v", pa, pb)
	}

	if !h.Relay(a, json.RawMessage(`{"sdp":"x"}`)) {
		t.Fatal("relay refused")
	}
	if s := next(t, b); s["type"] != "signal" || s["data"].(map[string]any)["sdp"] != "x" {
		t.Fatalf("signal %v", s)
	}
	if h.Relay(c, json.RawMessage(`{}`)) {
		t.Fatal("an unpaired link relayed")
	}

	h.Leave(a)
	if g := next(t, b); g["type"] != "peer-gone" {
		t.Fatalf("gone %v", g)
	}
	if _, ok := <-a.Out; ok {
		t.Fatal("left link still open")
	}
}

func TestADeskPhoneIsNeverAPeer(t *testing.T) {
	var h Hub
	a := h.Join(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_desk-2"})
	if p := next(t, a); p["type"] != "peer" || p["video"] != false {
		t.Fatalf("peer %v", p)
	}
}

func TestBothEndsAreToldWhenTheCallWasAnswered(t *testing.T) {
	var h Hub
	at := time.Now().Add(-3 * time.Second)
	a := h.Join(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_b-2", PeerLinx: true, AnsweredAt: at})
	b := h.Join(Leg{Channel: "PJSIP/d_b-2", Peer: "PJSIP/d_a-1", PeerLinx: true, AnsweredAt: at})
	for _, p := range []map[string]any{next(t, a), next(t, b)} {
		ago, ok := p["answered_ms_ago"].(float64)
		if !ok || ago < 3000 || ago > 4000 {
			t.Fatalf("answered_ms_ago %v, want about 3000", p["answered_ms_ago"])
		}
	}
	// A desk phone's other end is told too; a moment not known isn't sent.
	d := h.Join(Leg{Channel: "PJSIP/d_c-3", Peer: "PJSIP/d_desk-4", AnsweredAt: at})
	if p := next(t, d); p["answered_ms_ago"] == nil {
		t.Fatalf("peer %v", p)
	}
	e := h.Join(Leg{Channel: "PJSIP/d_e-5", Peer: "PJSIP/d_desk-6"})
	if p := next(t, e); p["answered_ms_ago"] != nil {
		t.Fatalf("peer %v", p)
	}
}

func TestAReconnectReplacesTheOldLinkAndPairsAgain(t *testing.T) {
	var h Hub
	a := h.Join(Leg{Channel: "A", Peer: "B", PeerLinx: true})
	b := h.Join(Leg{Channel: "B", Peer: "A", PeerLinx: true})
	first := next(t, a)
	if fb := next(t, b); first["session"] == nil || fb["session"] != first["session"] {
		t.Fatalf("the two ends of one pairing have different sessions: %v %v", first, fb)
	}
	a2 := h.Join(Leg{Channel: "A", Peer: "B", PeerLinx: true})
	if _, ok := <-a.Out; ok {
		// drain the one queued before close, if any
		for range a.Out {
		}
	}
	if g := next(t, b); g["type"] != "peer-gone" {
		t.Fatalf("b got %v", g)
	}
	if p := next(t, b); p["type"] != "peer" {
		t.Fatalf("b re-paired %v", p)
	}
	p2 := next(t, a2)
	if p2["type"] != "peer" {
		t.Fatalf("a2 %v", p2)
	}
	// The same two ends, moments apart: the pairing goes on, so neither
	// device throws away the picture it already has (ADR-096). A pairing
	// that is really new gets a new number, and what was said on the old
	// one is dropped by the devices (ADR-087, docs/VIDEO_CALLS.md §10) —
	// TestAPairingIsForgottenAfterAWhile covers that.
	if p2["resumed"] != true || p2["session"] != first["session"] {
		t.Fatalf("a device that came straight back didn't go on: %v, was %v", p2, first)
	}
}

func TestASlowDeviceIsLetGo(t *testing.T) {
	var h Hub
	a := h.Join(Leg{Channel: "A", Peer: "B", PeerLinx: true})
	b := h.Join(Leg{Channel: "B", Peer: "A", PeerLinx: true})
	next(t, a)
	for range Outbox + 1 {
		h.Relay(a, json.RawMessage(`{}`))
	}
	n := 0
	for range b.Out {
		n++
	}
	if n != Outbox {
		t.Fatalf("queued %d, want %d then closed", n, Outbox)
	}
	if g := next(t, a); g["type"] != "peer-gone" {
		t.Fatalf("a got %v", g)
	}
}

// A link that drops and comes straight back is the same pairing: both ends
// keep the picture they have instead of building another, which on a poor
// network cost the owner's calls several seconds of frozen picture each
// time (China, 2026-10-21).
func TestALinkThatComesStraightBackGoesOn(t *testing.T) {
	var h Hub
	a := h.Join(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_b-2", PeerLinx: true})
	b := h.Join(Leg{Channel: "PJSIP/d_b-2", Peer: "PJSIP/d_a-1", PeerLinx: true})
	first := next(t, a)
	next(t, b)
	if first["resumed"] != nil {
		t.Fatalf("a first pairing said it was resumed: %v", first)
	}
	session := first["session"]
	if session == nil {
		t.Fatal("a pairing with no session")
	}

	// A's link drops. B is told, and holds its picture.
	h.Leave(a)
	if g := next(t, b); g["type"] != "peer-gone" {
		t.Fatalf("gone %v", g)
	}

	// And comes back: the same two ends, so the same pairing.
	again := h.Join(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_b-2", PeerLinx: true})
	back, told := next(t, again), next(t, b)
	for _, p := range []map[string]any{back, told} {
		if p["resumed"] != true {
			t.Fatalf("a link that came straight back started afresh: %v", p)
		}
		if p["session"] != session {
			t.Fatalf("session %v, was %v", p["session"], session)
		}
	}
	// The polite side is still decided the same way round.
	if (back["polite"] == true) == (told["polite"] == true) {
		t.Fatalf("both ends polite or neither: %v %v", back, told)
	}
}

// Long enough gone, and it is a new pairing — as is a call set up between
// the same two people later, which has channels of its own anyway.
func TestAPairingIsForgottenAfterAWhile(t *testing.T) {
	var h Hub
	a := h.Join(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_b-2", PeerLinx: true})
	b := h.Join(Leg{Channel: "PJSIP/d_b-2", Peer: "PJSIP/d_a-1", PeerLinx: true})
	session := next(t, a)["session"]
	next(t, b)

	h.Leave(a)
	next(t, b)
	h.mu.Lock()
	for key, kept := range h.holds {
		kept.at = kept.at.Add(-ResumeWithin - time.Second)
		h.holds[key] = kept
	}
	h.mu.Unlock()

	again := h.Join(Leg{Channel: "PJSIP/d_a-1", Peer: "PJSIP/d_b-2", PeerLinx: true})
	back := next(t, again)
	next(t, b)
	if back["resumed"] == true {
		t.Fatalf("a pairing nobody came back to was resumed: %v", back)
	}
	if back["session"] == session {
		t.Fatalf("a new pairing kept the old session %v", session)
	}
	// Nothing is kept beyond its welcome.
	h.mu.Lock()
	kept := len(h.holds)
	h.mu.Unlock()
	if kept != 0 {
		t.Fatalf("%d pairings still held", kept)
	}
}
