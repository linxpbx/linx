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
// it would send on the call link, and {"call":ID,"close":true}. To it:
// {"call":ID,"msg":M} with M what the call link would send, and
// {"call":ID,"closed":"why"} when Linx ends the link. The line is the
// phone's identity, rechecked by the relay; who it is talking to is
// Asterisk's word, as on the call link.

// lineLinksMax is how many calls' links one line may have open at once.
const lineLinksMax = 4

type lineLinks struct {
	ctx      context.Context
	resolver callResolver
	hub      *calllink.Hub
	username string
	send     func([]byte) error

	mu    sync.Mutex
	links map[string]*lineLink
}

type lineLink struct {
	cancel context.CancelFunc
	mu     sync.Mutex
	member *calllink.Member
}

func newLineLinks(ctx context.Context, resolver callResolver, hub *calllink.Hub, username string, send func([]byte) error) *lineLinks {
	return &lineLinks{ctx: ctx, resolver: resolver, hub: hub, username: username, send: send, links: map[string]*lineLink{}}
}

// received is one side message from the phone. Anything malformed is
// dropped: the relay has already limited its size and rate.
func (l *lineLinks) received(b []byte) {
	var in struct {
		Call  string          `json:"call"`
		Open  bool            `json:"open"`
		Close bool            `json:"close"`
		Msg   json.RawMessage `json:"msg"`
	}
	if json.Unmarshal(b, &in) != nil || !callIDOK(in.Call) {
		return
	}
	switch {
	case in.Open:
		l.open(in.Call)
	case in.Close:
		l.close(in.Call)
	case len(in.Msg) > 0:
		l.relay(in.Call, in.Msg)
	}
}

func (l *lineLinks) open(call string) {
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
	link := &lineLink{cancel: cancel}
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
	link.mu.Lock()
	m := link.member
	link.mu.Unlock()
	var in struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if m == nil || json.Unmarshal(msg, &in) != nil || in.Type != "signal" || len(in.Data) == 0 {
		return
	}
	l.hub.Relay(m, in.Data)
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
		wctx, cancel := context.WithTimeout(ctx, callLinkRinging)
		leg, err = l.resolver.WaitAnswered(wctx, l.username, call, callLinkWaiting, func() error { return nil })
		cancel()
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
	m := l.hub.Join(leg)
	defer l.hub.Leave(m)
	link.mu.Lock()
	link.member = m
	link.mu.Unlock()

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
		case <-check.C:
			if !l.resolver.Alive(ctx, leg.ChannelID) {
				l.say(call, "closed", "the call has ended")
				return
			}
		}
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
