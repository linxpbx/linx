package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"linxpbx.com/linx/internal/calllink"
)

// A phone's call links on its own phone line (ADR-087, docs/VIDEO_CALLS.md
// §3.1): the same pairing and relaying as GET /api/v1/call-link, carried as
// side messages on the /sip websocket the phone already has open
// (siprelay.Relay.Side) instead of a websocket of its own.
//
// On the owner's iPhone on Etisalat 5G (2026-10-14) every connection newly
// opened was slow for its first 20–30 s, while the phone line, open for a
// while, answered in a tenth of a second. A call link of its own took
// 17–24 s a message, and the picture came 45 s after the answer.
//
// From the phone: {"call":ID,"open":true}, {"call":ID,"msg":M} with M what
// it would send on the call link, and {"call":ID,"close":true}. A phone
// that sets its picture up while the call rings (calllink's early.go) opens
// with "early":true as well, and is sent {"type":"early",...} while it
// rings. To it:
// {"call":ID,"msg":M} with M what the call link would send, and
// {"call":ID,"closed":"why"} when Linx ends the link. The line is the
// phone's identity, rechecked by the relay; who it is talking to is
// Asterisk's word, as on the call link.

// lineLinksMax is how many calls' links one line may have open at once.
const lineLinksMax = 4

// Limits on what a phone may say on a link before it is paired, kept until
// it is (lineLink.pending).
const (
	linePending      = 32
	linePendingBytes = 64 << 10
)

// lineResolver is callResolver and, for setting a picture up while the
// call rings, which call a ringing device's is (calllink.Resolver.Origin).
type lineResolver interface {
	callResolver
	Origin(ctx context.Context, sipUsername, callID string) (own, origin string, err error)
}

type lineLinks struct {
	ctx      context.Context
	resolver lineResolver
	hub      *calllink.Hub
	username string
	send     func([]byte) error

	mu    sync.Mutex
	links map[string]*lineLink
}

type lineLink struct {
	cancel context.CancelFunc
	// early is a phone that sets its picture up while the call rings.
	early bool

	mu     sync.Mutex
	member *calllink.Member
	// caller is, while a call this phone made rings, its channel's ID: what
	// the phone says goes to the devices being rung (Hub.EarlySend).
	caller string
	// session is the ringing exchange's this phone had, if any.
	session uint64
	// pending is what the phone said that nobody could be given yet: a
	// ringing phone's answer, sent the moment its person answers, which is
	// before Linx has seen the call answered.
	pending      []json.RawMessage
	pendingBytes int
}

func newLineLinks(ctx context.Context, resolver lineResolver, hub *calllink.Hub, username string, send func([]byte) error) *lineLinks {
	return &lineLinks{ctx: ctx, resolver: resolver, hub: hub, username: username, send: send, links: map[string]*lineLink{}}
}

// received is one side message from the phone. Anything malformed is
// dropped: the relay has already limited its size and rate.
func (l *lineLinks) received(b []byte) {
	var in struct {
		Call  string          `json:"call"`
		Open  bool            `json:"open"`
		Early bool            `json:"early"`
		Close bool            `json:"close"`
		Msg   json.RawMessage `json:"msg"`
	}
	if json.Unmarshal(b, &in) != nil || !callIDOK(in.Call) {
		return
	}
	switch {
	case in.Open:
		l.open(in.Call, in.Early)
	case in.Close:
		l.close(in.Call)
	case len(in.Msg) > 0:
		l.relay(in.Call, in.Msg)
	}
}

func (l *lineLinks) open(call string, early bool) {
	l.mu.Lock()
	if old := l.links[call]; old != nil {
		old.cancel()
		delete(l.links, call)
	}
	if len(l.links) >= lineLinksMax {
		l.mu.Unlock()
		l.say(call, "closed", "too many calls")
		return
	}
	ctx, cancel := context.WithCancel(l.ctx)
	link := &lineLink{cancel: cancel, early: early}
	l.links[call] = link
	l.mu.Unlock()
	go l.run(ctx, call, link)
}

func (l *lineLinks) close(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if link := l.links[call]; link != nil {
		link.cancel()
		delete(l.links, call)
	}
}

func (l *lineLinks) relay(call string, msg json.RawMessage) {
	l.mu.Lock()
	link := l.links[call]
	l.mu.Unlock()
	if link == nil {
		return
	}
	var in struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(msg, &in) != nil || in.Type != "signal" || len(in.Data) == 0 {
		return
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	// Behind anything still pending, never ahead of it: a route before the
	// answer it belongs to is refused.
	if m := link.member; m != nil && len(link.pending) == 0 && l.hub.Relay(m, in.Data) {
		return
	}
	if link.member == nil && link.caller != "" && l.hub.EarlySend(link.caller, in.Data) {
		return
	}
	if link.early && len(link.pending) < linePending && link.pendingBytes+len(in.Data) <= linePendingBytes {
		link.pending = append(link.pending, in.Data)
		link.pendingBytes += len(in.Data)
	}
}

// ringing is a link held while its call rings, for a phone that sets its
// picture up meanwhile: the caller's is told to offer, a ringing device's is
// given that offer. It returns what ends that once the wait is over.
func (l *lineLinks) ringing(ctx context.Context, call string, link *lineLink) func() {
	own, origin, err := l.resolver.Origin(ctx, l.username, call)
	if err != nil {
		return func() {}
	}
	if own == origin {
		session := l.hub.EarlyCaller(own)
		link.mu.Lock()
		link.caller, link.session = own, session
		link.mu.Unlock()
		_ = l.say(call, "msg", calllink.EarlyMessage{Type: "early", Session: session, Offer: true})
		return func() {
			link.mu.Lock()
			link.caller = ""
			link.mu.Unlock()
			l.hub.EarlyEnd(own)
		}
	}
	listener := l.hub.EarlyListen(origin)
	if listener == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for b := range listener.Out {
			if l.say(call, "msg", json.RawMessage(b)) != nil {
				return
			}
		}
	}()
	return func() {
		l.hub.EarlyLeave(listener)
		<-done
		link.mu.Lock()
		link.session = l.hub.Session(listener)
		link.mu.Unlock()
	}
}

// run is one call's link: held while the call rings, paired once it is
// answered, until the phone closes it, the line ends or the call does.
func (l *lineLinks) run(ctx context.Context, call string, link *lineLink) {
	defer func() {
		link.cancel()
		l.mu.Lock()
		if l.links[call] == link {
			delete(l.links, call)
		}
		l.mu.Unlock()
	}()
	leg, err := l.resolver.Resolve(ctx, l.username, call)
	if errors.Is(err, calllink.ErrNotAnswered) {
		l.say(call, "msg", json.RawMessage(`{"type":"wait"}`))
		stop := func() {}
		if link.early {
			stop = l.ringing(ctx, call, link)
		}
		wctx, cancel := context.WithTimeout(ctx, callLinkRinging)
		leg, err = l.resolver.WaitAnswered(wctx, l.username, call, callLinkWaiting, func() error { return nil })
		cancel()
		stop()
	}
	if err != nil {
		if ctx.Err() == nil {
			why := "Linx can't reach the phone system right now"
			if errors.Is(err, calllink.ErrNoCall) || errors.Is(err, context.DeadlineExceeded) {
				why = "the call has ended"
			}
			l.say(call, "closed", why)
		}
		return
	}
	link.mu.Lock()
	m := l.hub.JoinEarly(leg, link.session)
	link.member = m
	link.mu.Unlock()
	defer l.hub.Leave(m)

	check := time.NewTicker(callLinkCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-m.Out:
			if !ok {
				l.say(call, "closed", "replaced")
				return
			}
			if l.say(call, "msg", json.RawMessage(b)) != nil {
				return
			}
			l.paired(link, b)
		case <-check.C:
			if !l.resolver.Alive(ctx, leg.ChannelID) {
				l.say(call, "closed", "the call has ended")
				return
			}
		}
	}
}

// paired is told each message the hub gives the phone: at its "peer",
// what the phone said before it was paired goes to the other end if the
// pairing goes on from their ringing exchange, and is dropped if not (the
// phone starts afresh, and so does what it says).
func (l *lineLinks) paired(link *lineLink, b []byte) {
	var peer calllink.PeerMessage
	if json.Unmarshal(b, &peer) != nil || peer.Type != "peer" {
		return
	}
	link.mu.Lock()
	defer link.mu.Unlock()
	pending := link.pending
	link.pending, link.pendingBytes = nil, 0
	if !peer.Early {
		return
	}
	for _, data := range pending {
		l.hub.Relay(link.member, data)
	}
}

// say sends the phone {"call":call,<key>:v}.
func (l *lineLinks) say(call, key string, v any) error {
	b, err := json.Marshal(map[string]any{"call": call, key: v})
	if err != nil {
		return err
	}
	return l.send(b)
}
