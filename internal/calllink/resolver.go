package calllink

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"linxpbx.com/linx/internal/ari"
)

// ErrNoCall is a device asking about a call Asterisk doesn't show it in.
var ErrNoCall = errors.New("no such call")

// ErrNotAnswered is a device's own call that is still ringing. It is a
// kind of ErrNoCall, so whatever needs an answered call refuses it as one;
// only the call link waits for it to be answered (WaitAnswered).
var ErrNotAnswered = fmt.Errorf("%w: not answered yet", ErrNoCall)

// ErrNoAsterisk is Asterisk not connected to the control plane right now.
var ErrNoAsterisk = errors.New("the phone system isn't connected")

// Resolver finds a device's leg of a call through ARI.
type Resolver struct {
	// Linx reports whether a SIP username is an app's or a browser's.
	Linx func(ctx context.Context, sipUsername string) (bool, error)
	// Settle is how long to wait for a just-answered call's bridge to show
	// (zero: fifteen seconds). The answering phone asks for its link the
	// moment it answers, and on a phone just woken on mobile data that
	// answer took 7 s to reach Asterisk (2026-10-13); with two seconds the
	// link was refused and the picture never started.
	Settle time.Duration

	mu      sync.Mutex
	conn    *ari.Conn
	changes chan struct{}
}

// Changed is told whenever Asterisk's calls change (CallTracker.OnChange),
// which wakes every link waiting for its call to be answered.
func (r *Resolver) Changed() {
	r.mu.Lock()
	if r.changes != nil {
		close(r.changes)
		r.changes = nil
	}
	r.mu.Unlock()
}

// changed is closed at the next change.
func (r *Resolver) changed() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.changes == nil {
		r.changes = make(chan struct{})
	}
	return r.changes
}

// WaitAnswered is Resolve for a call that may still be ringing: it waits,
// woken by Asterisk's own events rather than asking it over and over, until
// the call is answered, ends, or ctx is done. tick is called every interval
// meanwhile (the link's sign of life) and stops the wait when it fails.
func (r *Resolver) WaitAnswered(
	ctx context.Context, sipUsername, callID string, interval time.Duration, tick func() error,
) (Leg, error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		// Taken before looking, so a change between the two isn't missed.
		next := r.changed()
		leg, err := r.Resolve(ctx, sipUsername, callID)
		if !errors.Is(err, ErrNotAnswered) {
			return leg, err
		}
		select {
		case <-ctx.Done():
			return Leg{}, ctx.Err()
		case <-next:
		case <-t.C:
			if err := tick(); err != nil {
				return Leg{}, err
			}
		}
	}
}

// Connected is given Asterisk's ARI connection as it comes and goes (nil
// when it goes).
func (r *Resolver) Connected(c *ari.Conn) {
	r.mu.Lock()
	r.conn = c
	r.mu.Unlock()
}

func (r *Resolver) current() *ari.Conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.conn
}

// own finds the channel of sipUsername's whose SIP Call-ID is callID and
// that is up: ErrNotAnswered while it is still ringing.
func (r *Resolver) own(ctx context.Context, c *ari.Conn, sipUsername, callID string) (*ari.Channel, error) {
	var chans []ari.Channel
	if err := c.Get(ctx, "channels", &chans); err != nil {
		return nil, err
	}
	for i, ch := range chans {
		if ch.ProtocolID != callID || ch.Endpoint() != sipUsername {
			continue
		}
		if ch.State != "Up" {
			return nil, ErrNotAnswered
		}
		return &chans[i], nil
	}
	return nil, ErrNoCall
}

// Origin is, for a call of sipUsername's whose SIP Call-ID is callID, in
// any state, the ID of the device's own channel and of the channel the call
// began on (Asterisk's linkedid, which every channel of one call shares):
// the same two for the device that made the call, and for a device being
// rung, the caller's. It is how a ringing device is given its caller's
// early offer (Hub.EarlyListen) on Asterisk's word alone.
func (r *Resolver) Origin(ctx context.Context, sipUsername, callID string) (own, origin string, err error) {
	c := r.current()
	if c == nil {
		return "", "", ErrNoAsterisk
	}
	var chans []ari.Channel
	if err := c.Get(ctx, "channels", &chans); err != nil {
		return "", "", err
	}
	for _, ch := range chans {
		if ch.ProtocolID != callID || ch.Endpoint() != sipUsername {
			continue
		}
		origin, err := variable(ctx, c, ch.ID, "CHANNEL(linkedid)")
		if err != nil {
			return "", "", err
		}
		if origin == "" {
			return "", "", ErrNoCall
		}
		return ch.ID, origin, nil
	}
	return "", "", ErrNoCall
}

// Room is the group call sipUsername's call callID was an invitation to
// (X-Linx-Room, ADR-088), from the channel's own variable — which only the
// control plane's own invitation sets — or "" for any other call.
func (r *Resolver) Room(ctx context.Context, sipUsername, callID string) (string, error) {
	c := r.current()
	if c == nil {
		return "", ErrNoAsterisk
	}
	mine, err := r.own(ctx, c, sipUsername, callID)
	if err != nil {
		return "", err
	}
	room, err := variable(ctx, c, mine.ID, "LINXROOM")
	if err != nil && !ari.Gone(err) {
		return "", err
	}
	return room, nil
}

// Invitation is ringing someone into a group call.
type Invitation struct {
	// Extension is who is rung; Room the group call; From whose name and
	// number it rings with.
	Extension, Room, FromName, FromNumber string
	// Ring is how long, in seconds.
	Ring int
}

// Invite rings an extension's phones into a group call: an originate of
// Local/<extension>@linx-room-invite, whose other half waits in the
// control plane's Stasis app until the call ends (asteriskconf).
func (r *Resolver) Invite(ctx context.Context, app, dialplanContext string, in Invitation) error {
	c := r.current()
	if c == nil {
		return ErrNoAsterisk
	}
	q := url.Values{}
	q.Set("endpoint", "Local/"+in.Extension+"@"+dialplanContext)
	q.Set("app", app)
	q.Set("appArgs", "room")
	name := strings.Map(func(r rune) rune {
		if r == '"' || r == '<' || r == '>' || r == '\\' || r < ' ' {
			return -1
		}
		return r
	}, in.FromName)
	q.Set("callerId", fmt.Sprintf("\"%s\" <%s>", name, in.FromNumber))
	q.Set("timeout", fmt.Sprint(in.Ring))
	body := map[string]any{"variables": map[string]string{"__LINXROOM": in.Room, "__LINXVIDEO": "yes"}}
	return c.DoJSON(ctx, "POST", "channels?"+q.Encode(), body, nil)
}

// Resolve finds the channel of sipUsername's whose SIP Call-ID is callID
// and the channel it is bridged to. A device can only ever find its own
// channel, so it can only ever be linked to whoever it is talking to.
func (r *Resolver) Resolve(ctx context.Context, sipUsername, callID string) (Leg, error) {
	c := r.current()
	if c == nil {
		return Leg{}, ErrNoAsterisk
	}
	mine, err := r.own(ctx, c, sipUsername, callID)
	if err != nil {
		return Leg{}, err
	}
	settle := r.Settle
	if settle == 0 {
		settle = 15 * time.Second
	}
	// Answered a moment ago, the bridge may not be there yet.
	until := time.Now().Add(settle)
	var peer string
	for {
		v, err := variable(ctx, c, mine.ID, "BRIDGEPEER")
		if err != nil && !ari.Gone(err) {
			return Leg{}, err
		}
		if v, err = r.through(ctx, c, v); err != nil {
			return Leg{}, err
		}
		if peer = v; peer != "" || time.Now().After(until) {
			break
		}
		select {
		case <-ctx.Done():
			return Leg{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	if peer == "" {
		return Leg{}, ErrNoCall
	}
	leg := Leg{Channel: mine.Name, ChannelID: mine.ID, Peer: peer}
	if other := (ari.Channel{Name: peer}).Endpoint(); other != "" && r.Linx != nil {
		linx, err := r.Linx(ctx, other)
		if err != nil {
			return Leg{}, err
		}
		leg.PeerLinx = linx
	}
	return leg, nil
}

// through is the channel a bridge peer stands for: itself, or — for a
// call that rang a phone being woken (ari.WakeLegContext) — the channel
// on the far side of that Local channel, until Asterisk takes the Local
// channel out of the call and the two are bridged directly. "" while the
// far side isn't bridged yet.
func (r *Resolver) through(ctx context.Context, c *ari.Conn, peer string) (string, error) {
	for range 2 {
		var other string
		if shared, ok := ari.WakeLeg(peer, "1"); ok {
			other = shared + ";2"
		} else if shared, ok := ari.WakeLeg(peer, "2"); ok {
			other = shared + ";1"
		} else {
			return peer, nil
		}
		var chans []ari.Channel
		if err := c.Get(ctx, "channels", &chans); err != nil {
			return "", err
		}
		peer = ""
		for _, ch := range chans {
			if ch.Name != other {
				continue
			}
			v, err := variable(ctx, c, ch.ID, "BRIDGEPEER")
			if err != nil && !ari.Gone(err) {
				return "", err
			}
			peer = v
		}
		if peer == "" {
			return "", nil
		}
	}
	return "", nil
}

// Alive reports whether the channel is still there; an error that isn't
// "gone" (Asterisk briefly away) counts as alive, so a link isn't cut by a
// reconnect.
func (r *Resolver) Alive(ctx context.Context, channelID string) bool {
	c := r.current()
	if c == nil {
		return true
	}
	var ch ari.Channel
	err := c.Get(ctx, "channels/"+url.PathEscape(channelID), &ch)
	return err == nil || !ari.Gone(err)
}

func variable(ctx context.Context, c *ari.Conn, channelID, name string) (string, error) {
	var v struct {
		Value string `json:"value"`
	}
	err := c.Get(ctx, "channels/"+url.PathEscape(channelID)+"/variable?variable="+url.QueryEscape(name), &v)
	return strings.TrimSpace(v.Value), err
}
