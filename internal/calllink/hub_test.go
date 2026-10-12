package calllink

import (
	"encoding/json"
	"testing"
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

func TestAReconnectReplacesTheOldLinkAndPairsAgain(t *testing.T) {
	var h Hub
	a := h.Join(Leg{Channel: "A", Peer: "B", PeerLinx: true})
	b := h.Join(Leg{Channel: "B", Peer: "A", PeerLinx: true})
	next(t, a)
	next(t, b)
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
	if p := next(t, a2); p["type"] != "peer" {
		t.Fatalf("a2 %v", p)
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
