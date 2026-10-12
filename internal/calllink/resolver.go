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

// ErrNoAsterisk is Asterisk not connected to the control plane right now.
var ErrNoAsterisk = errors.New("the phone system isn't connected")

// Resolver finds a device's leg of a call through ARI.
type Resolver struct {
	// Linx reports whether a SIP username is an app's or a browser's.
	Linx func(ctx context.Context, sipUsername string) (bool, error)
	// Settle is how long to wait for a just-answered call's bridge to show
	// (zero: two seconds).
	Settle time.Duration

	mu   sync.Mutex
	conn *ari.Conn
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
// that is up.
func (r *Resolver) own(ctx context.Context, c *ari.Conn, sipUsername, callID string) (*ari.Channel, error) {
	var chans []ari.Channel
	if err := c.Get(ctx, "channels", &chans); err != nil {
		return nil, err
	}
	for i, ch := range chans {
		if ch.ProtocolID == callID && ch.Endpoint() == sipUsername && ch.State == "Up" {
			return &chans[i], nil
		}
	}
	return nil, ErrNoCall
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
		settle = 2 * time.Second
	}
	// Answered a moment ago, the bridge may not be there yet.
	until := time.Now().Add(settle)
	var peer string
	for {
		v, err := variable(ctx, c, mine.ID, "BRIDGEPEER")
		if err != nil && !ari.Gone(err) {
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
