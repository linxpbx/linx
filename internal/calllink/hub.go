// Package calllink is the call link (ADR-087, docs/VIDEO_CALLS.md §3.1):
// how the two Linx devices of one call find each other and set up the
// direct link their picture travels on. Each device opens a websocket once
// its call is answered; the hub pairs the two ends of the same call and
// passes what one says to the other, unread. Who is talking to whom is
// Asterisk's word (Resolver), never the device's.
package calllink

import (
	"encoding/json"
	"sync"
	"time"
)

// Leg is one device's side of a call, as Asterisk has it.
type Leg struct {
	// Channel is this device's channel (PJSIP/d_xxxxxxxx-00000001) and
	// ChannelID Asterisk's id for it.
	Channel, ChannelID string
	// Peer is the channel it is bridged to.
	Peer string
	// PeerLinx is whether that channel is an app's or a browser's, which
	// can open a link of its own; a desk phone or a trunk can't.
	PeerLinx bool
	// CanResume is whether this device knows how to go on with the picture
	// it already has when its link drops and comes straight back
	// (PeerMessage.Resumed, ADR-096). A pairing is only resumed when both
	// ends say so: the session number is what keeps an older device from
	// taking what was said on the pairing before as this one's, so an end
	// that would build a new connection anyway must be given a new number
	// (two browsers, which don't, went into an endless round of offers and
	// answers when they weren't — CI run 39632296425).
	CanResume bool
	// AnsweredAt is when Asterisk joined this channel to the other side
	// (its bridge was made): the one moment every device on the call
	// counts its timer from (owner, 2026-10-17). Zero when not known.
	AnsweredAt time.Time
}

// Messages to a device.
type (
	// PeerMessage says the other end is here (Video true) or will never
	// be (Video false: not a Linx device). Polite is the WebRTC "perfect
	// negotiation" role: the polite side gives way when both offer at once.
	//
	// Session numbers this pairing of the two ends. A device stamps what it
	// sends with it and drops what arrives stamped with another: an end that
	// reconnects is paired again, and a description from the pairing before
	// must not land on the new connection (the picture never came back after
	// a dropped connection, 2026-10-13).
	//
	// Early is a pairing that goes on from the two ends' exchange while the
	// call rang (EarlyMessage): its Session is that exchange's, and what was
	// said in it stands.
	//
	// Resumed is the same pairing as before, after one end's link dropped
	// and came straight back (ResumeWithin): its Session is the one it had,
	// and both ends keep the picture they already have rather than building
	// it again. A link drops on any network that stumbles for a second, and
	// rebuilding cost the owner's calls from China five to seven seconds of
	// frozen picture each time (2026-10-21).
	PeerMessage struct {
		Type    string `json:"type"` // "peer"
		Video   bool   `json:"video"`
		Polite  bool   `json:"polite,omitempty"`
		Session uint64 `json:"session,omitempty"`
		Early   bool   `json:"early,omitempty"`
		Resumed bool   `json:"resumed,omitempty"`
		// AnsweredMsAgo is how long before this message the call was
		// answered (Leg.AnsweredAt). It is sent as a span, not a moment,
		// so a device whose own clock is wrong still counts the same.
		AnsweredMsAgo *int64 `json:"answered_ms_ago,omitempty"`
	}
	// EarlyMessage opens the exchange of a call that is still ringing
	// (Hub.EarlyCaller): the caller is told to offer (Offer), and each of
	// the devices ringing for it gets that offer as it comes, to answer
	// the moment its person does. Session stamps it, as PeerMessage's.
	EarlyMessage struct {
		Type    string `json:"type"` // "early"
		Session uint64 `json:"session"`
		Offer   bool   `json:"offer,omitempty"`
	}
	// SignalMessage carries what the other end sent, as it sent it.
	SignalMessage struct {
		Type string          `json:"type"` // "signal"
		Data json.RawMessage `json:"data"`
	}
	// GoneMessage says the other end's link has closed.
	GoneMessage struct {
		Type string `json:"type"` // "peer-gone"
	}
)

// Outbox is how many messages may wait for a slow device before its link
// is closed: a device that stops reading must not hold the server's memory.
const Outbox = 64

// Member is one open link.
type Member struct {
	Leg
	// Out is what to write to the device, in order; closed when the hub
	// lets the member go (replaced, or too slow).
	Out    chan []byte
	peer   *Member
	closed bool
	// early is the session of the exchange this end had while the call
	// rang, if it had one.
	early uint64
	// session is the pairing this member is in, once it has one.
	session uint64
}

// ResumeWithin is how long a pairing is remembered after one of its ends
// goes, so a link that drops and comes back goes on with the same session
// instead of starting a new one. Long enough for a stumbling network to
// find its feet, short enough that a call set up again later is new.
const ResumeWithin = 20 * time.Second

// held is a pairing waiting for an end to come back.
type held struct {
	session uint64
	at      time.Time
}

// pairKey names a pairing whichever end asks about it.
func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}

// Hub pairs links by their channels.
type Hub struct {
	mu      sync.Mutex
	members map[string]*Member // by Channel
	// pairings counts the pairings made, for PeerMessage.Session, and the
	// early exchanges begun (EarlyMessage.Session), so the two never meet.
	pairings uint64
	// holds is each pairing that has lost an end, by pairKey, for
	// ResumeWithin. Swept on every join, so nothing is kept beyond it.
	holds map[string]held
	// early is the exchanges of calls still ringing, by the caller's
	// channel ID.
	early map[string]*earlyCall
}

// Join opens a link for leg. A link already open for the same channel (the
// device reconnected) is closed in its favour.
func (h *Hub) Join(leg Leg) *Member { return h.JoinEarly(leg, 0) }

// JoinEarly is Join for an end that had the exchange whose session is early
// while the call rang (0: none). Paired with the other end of the same
// exchange, the pairing goes on from it (PeerMessage.Early).
func (h *Hub) JoinEarly(leg Leg, early uint64) *Member {
	m := &Member{Leg: leg, Out: make(chan []byte, Outbox), early: early}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.members == nil {
		h.members = map[string]*Member{}
	}
	h.sweepHoldsLocked()
	if old := h.members[leg.Channel]; old != nil {
		h.dropLocked(old)
	}
	h.members[leg.Channel] = m
	if !leg.PeerLinx {
		h.sendLocked(m, PeerMessage{Type: "peer", Video: false, AnsweredMsAgo: msAgo(leg.AnsweredAt)})
		return m
	}
	if other := h.members[leg.Peer]; other != nil && other.Peer == leg.Channel && other.peer == nil {
		m.peer, other.peer = other, m
		session, early := m.early, m.early != 0 && m.early == other.early
		resumed := false
		switch kept, ok := h.holds[pairKey(m.Channel, other.Channel)]; {
		case early:
		case ok && m.CanResume && other.CanResume:
			// The same two ends as a moment ago: the pairing goes on.
			session, resumed = kept.session, true
		default:
			h.pairings++
			session = h.pairings
		}
		m.session, other.session = session, session
		// Either rule would do as long as both ends agree on it.
		h.sendLocked(m, PeerMessage{Type: "peer", Video: true, Polite: m.Channel < other.Channel, Session: session,
			Early: early, Resumed: resumed, AnsweredMsAgo: msAgo(m.AnsweredAt)})
		h.sendLocked(other, PeerMessage{Type: "peer", Video: true, Polite: other.Channel < m.Channel, Session: session,
			Early: early, Resumed: resumed, AnsweredMsAgo: msAgo(other.AnsweredAt)})
	}
	return m
}

// msAgo is how long ago at was, in milliseconds, or nil if it isn't known.
func msAgo(at time.Time) *int64 {
	if at.IsZero() {
		return nil
	}
	ms := max(time.Since(at).Milliseconds(), 0)
	return &ms
}

// Relay passes data from m to the other end, if there is one yet. It
// reports false when there isn't: the device spoke before it was told the
// other end was there, which is harmless and dropped.
func (h *Hub) Relay(m *Member, data json.RawMessage) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m.closed || m.peer == nil {
		return false
	}
	h.sendLocked(m.peer, SignalMessage{Type: "signal", Data: data})
	return true
}

// Partner is the channel m's link is paired with, if it is.
func (h *Hub) Partner(m *Member) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m.closed || m.peer == nil {
		return "", false
	}
	return m.peer.Channel, true
}

// Linked reports whether the link for channel is open and paired, and with
// which channel.
func (h *Hub) Linked(channel string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.members[channel]
	if m == nil || m.closed || m.peer == nil {
		return "", false
	}
	return m.peer.Channel, true
}

// Tell sends v to the device whose link is for channel; false if it has
// none open.
func (h *Hub) Tell(channel string, v any) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.members[channel]
	if m == nil || m.closed {
		return false
	}
	h.sendLocked(m, v)
	return true
}

// Leave closes m's link and tells the other end.
func (h *Hub) Leave(m *Member) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.members[m.Channel] == m {
		delete(h.members, m.Channel)
	}
	h.dropLocked(m)
}

func (h *Hub) dropLocked(m *Member) {
	if m.closed {
		return
	}
	m.closed = true
	close(m.Out)
	if p := m.peer; p != nil {
		m.peer, p.peer = nil, nil
		if m.session != 0 {
			// Remembered, in case this end comes straight back: the two
			// keep the picture they have instead of building another.
			if h.holds == nil {
				h.holds = map[string]held{}
			}
			h.holds[pairKey(m.Channel, p.Channel)] = held{session: m.session, at: time.Now()}
		}
		h.sendLocked(p, GoneMessage{Type: "peer-gone"})
	}
	if h.members[m.Channel] == m {
		delete(h.members, m.Channel)
	}
}

// sweepHoldsLocked forgets pairings nobody came back to.
func (h *Hub) sweepHoldsLocked() {
	for key, kept := range h.holds {
		if time.Since(kept.at) > ResumeWithin {
			delete(h.holds, key)
		}
	}
}

// sendLocked queues v for m, or lets m go if it has stopped reading.
func (h *Hub) sendLocked(m *Member, v any) {
	if m.closed {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	select {
	case m.Out <- b:
	default:
		h.dropLocked(m)
	}
}
