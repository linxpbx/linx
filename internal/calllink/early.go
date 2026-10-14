package calllink

import (
	"encoding/json"
)

// A video call's picture is set up while the call rings (docs/VIDEO_CALLS.md
// §3.1). Set up after the answer, it took a round of messages each way on
// the answering phone's line, and on a phone just woken on mobile data each
// took seconds: on the owner's iPhone on Etisalat 5G (2026-10-14) the
// answer to its offer reached it 6 s after it was sent, and the picture
// came 9–11 s after the call was answered.
//
// So the caller offers as soon as the call rings, and Linx gives that offer
// to every Linx device ringing for it — they are the devices Asterisk is
// ringing for this caller (Resolver.Origin), nothing else. Each holds the
// offer, sends nothing back, and answers only when its person answers: the
// answer goes up its line right behind the call's own 200 OK, and the
// picture's route is found while it travels. A ringing device learns the
// caller's addresses, which the caller chose to call; the caller learns
// nothing of a device until it answers.

// Limits on what a caller may leave waiting for the devices it rings.
const (
	earlyBacklog   = 32
	earlyBytes     = 64 << 10
	earlyListeners = 8
)

type earlyCall struct {
	session uint64
	// caller is whether the caller's end is open on it; listeners can
	// come first.
	caller    bool
	backlog   [][]byte
	size      int
	listeners map[*Listener]struct{}
}

// Listener is a ringing device's end of its caller's exchange.
type Listener struct {
	// Out is what to give the device, in order; closed when the exchange
	// ends (answered, or the caller gone) or the device leaves it.
	Out     chan []byte
	call    string
	session uint64
	closed  bool
}

func (h *Hub) earlyLocked(caller string) *earlyCall {
	if h.early == nil {
		h.early = map[string]*earlyCall{}
	}
	e := h.early[caller]
	if e == nil {
		e = &earlyCall{listeners: map[*Listener]struct{}{}}
		h.early[caller] = e
	}
	return e
}

// EarlyCaller opens the exchange of the ringing call whose caller's channel
// is caller, afresh if it was open (the caller's link came back), and
// returns its session. The devices ringing for it are told.
func (h *Hub) EarlyCaller(caller string) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.earlyLocked(caller)
	h.pairings++
	e.session, e.caller, e.backlog, e.size = h.pairings, true, nil, 0
	for l := range e.listeners {
		h.tellLocked(l, EarlyMessage{Type: "early", Session: e.session})
	}
	return e.session
}

// EarlySend gives what the caller said to every device ringing for it, and
// keeps it for any that come later. False when the exchange isn't open or
// has had all it may keep.
func (h *Hub) EarlySend(caller string, data json.RawMessage) bool {
	b, err := json.Marshal(SignalMessage{Type: "signal", Data: data})
	if err != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.early[caller]
	if e == nil || !e.caller || len(e.backlog) >= earlyBacklog || e.size+len(b) > earlyBytes {
		return false
	}
	e.backlog = append(e.backlog, b)
	e.size += len(b)
	for l := range e.listeners {
		h.queueLocked(l, b)
	}
	return true
}

// EarlyListen is a device ringing for caller joining its exchange: what the
// caller has said so far comes first. Nil when too many already have.
func (h *Hub) EarlyListen(caller string) *Listener {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.earlyLocked(caller)
	if len(e.listeners) >= earlyListeners {
		return nil
	}
	l := &Listener{Out: make(chan []byte, earlyBacklog+Outbox), call: caller}
	e.listeners[l] = struct{}{}
	if e.caller {
		h.tellLocked(l, EarlyMessage{Type: "early", Session: e.session})
		for _, b := range e.backlog {
			h.queueLocked(l, b)
		}
	}
	return l
}

// Session is the exchange's session as l was last told it (0: not yet).
func (h *Hub) Session(l *Listener) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return l.session
}

// EarlyLeave takes l out of its exchange.
func (h *Hub) EarlyLeave(l *Listener) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.leaveLocked(l)
	if e := h.early[l.call]; e != nil {
		delete(e.listeners, l)
		if !e.caller && len(e.listeners) == 0 {
			delete(h.early, l.call)
		}
	}
}

// EarlyEnd ends caller's exchange: the call was answered, or ended.
func (h *Hub) EarlyEnd(caller string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.early[caller]
	if e == nil {
		return
	}
	delete(h.early, caller)
	for l := range e.listeners {
		h.leaveLocked(l)
	}
}

func (h *Hub) leaveLocked(l *Listener) {
	if !l.closed {
		l.closed = true
		close(l.Out)
	}
}

func (h *Hub) tellLocked(l *Listener, m EarlyMessage) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	l.session = m.Session
	h.queueLocked(l, b)
}

// queueLocked gives l b, or lets it go if it has stopped reading.
func (h *Hub) queueLocked(l *Listener, b []byte) {
	if l.closed {
		return
	}
	select {
	case l.Out <- b:
	default:
		h.leaveLocked(l)
	}
}
