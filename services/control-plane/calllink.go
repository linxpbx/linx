package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/calllink"
	"linxpbx.com/linx/internal/pbx"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// The call link (ADR-087, docs/VIDEO_CALLS.md §3.1): the websocket the two
// Linx devices of one call set up their direct picture link through.

const (
	// callLinkLimit is the most one message may be: an offer with every
	// route in it is a few kilobytes.
	callLinkLimit = 64 << 10
	// callLinkRate is how many messages a second a device may send, over
	// a second's worth of burst; a device that sends more is cut off.
	callLinkRate = 50
	// callLinkCheck is how often an open link checks that the device is
	// still allowed and its call still there (the /sip relay's pace).
	callLinkCheck = 15 * time.Second
	// callLinkRinging is the longest a link opened while its call rings is
	// held for the answer: longer than any ring, so a call that is never
	// answered closes it by ending first.
	callLinkRinging = 3 * time.Minute
	// callLinkWaiting is how often a link held for the answer says it is
	// still there ({"type":"wait"}), inside the app's 20 s and a front
	// door's idle limit.
	callLinkWaiting = 10 * time.Second
)

// callLinkStore is what the call link reads: whose device is asking.
type callLinkStore interface {
	teamStore
	WebDeviceForSession(ctx context.Context, session uuid.UUID) (pbx.Device, error)
	Device(ctx context.Context, tenant, id uuid.UUID) (pbx.Device, error)
}

// callResolver is the Asterisk side (calllink.Resolver).
type callResolver interface {
	Resolve(ctx context.Context, sipUsername, callID string) (calllink.Leg, error)
	WaitAnswered(ctx context.Context, sipUsername, callID string, interval time.Duration, tick func() error) (calllink.Leg, error)
	Alive(ctx context.Context, channelID string) bool
}

// callLinkHandler is GET /api/v1/call-link. It takes what the Team list
// takes: a browser session from this server's own page, or a phone's
// device token with no Origin. Either way the asker is exactly one device,
// and only that device's own channel can be linked.
func callLinkHandler(authn *auth.Authenticator, st callLinkStore, resolver callResolver, hub *calllink.Hub) http.Handler {
	return apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callID := r.URL.Query().Get("call")
		if !validCallID(w, callID) {
			return
		}
		device, stillThere, ok := askingDevice(w, r, st)
		if !ok {
			return
		}
		leg, err := resolver.Resolve(r.Context(), device.SIPUsername, callID)
		ringing := errors.Is(err, calllink.ErrNotAnswered)
		switch {
		case ringing:
			// Opened while the call rings (2026-10-14): a new connection
			// on a poor mobile link took 9 s and more, so the phone makes
			// it while ringing and it is held here until the answer.
		case errors.Is(err, calllink.ErrNoCall):
			apihttp.WriteProblem(w, http.StatusNotFound, "no_such_call", "Linx can't find that call.")
			return
		case err != nil:
			apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx can't reach the phone system right now.")
			return
		}
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{controlplaneapi.CallLinkSubprotocol}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		if c.Subprotocol() != controlplaneapi.CallLinkSubprotocol {
			c.Close(websocket.StatusPolicyViolation, "use the "+controlplaneapi.CallLinkSubprotocol+" subprotocol")
			return
		}
		ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
		defer cancel()
		if ringing {
			if leg, ok = waitForAnswer(ctx, c, resolver, device.SIPUsername, callID); !ok {
				return
			}
		}
		serveCallLink(ctx, c, resolver, hub, leg, stillThere)
	})))
}

// waitForAnswer holds a link opened while its call rings until the call is
// answered (true, with its leg) or ends, or the device goes. Nothing the
// device sends meanwhile is read: there is nobody yet to send it to.
func waitForAnswer(
	ctx context.Context, c *websocket.Conn, resolver callResolver, sipUsername, callID string,
) (calllink.Leg, bool) {
	ctx, cancel := context.WithTimeout(ctx, callLinkRinging)
	defer cancel()
	waiting := []byte(`{"type":"wait"}`)
	say := func() error {
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		defer wcancel()
		return c.Write(wctx, websocket.MessageText, waiting)
	}
	if say() != nil {
		return calllink.Leg{}, false
	}
	leg, err := resolver.WaitAnswered(ctx, sipUsername, callID, callLinkWaiting, say)
	switch {
	case err == nil:
		return leg, true
	case errors.Is(err, calllink.ErrNoCall), errors.Is(err, context.DeadlineExceeded):
		c.Close(websocket.StatusNormalClosure, "the call has ended")
	default:
		c.Close(websocket.StatusTryAgainLater, "Linx can't reach the phone system right now")
	}
	return calllink.Leg{}, false
}

// askingDevice is the one device a request comes from: a phone by its
// device token, or a browser session's own web phone (from this server's
// own page). It writes the refusal itself when there is none, and returns
// what an open connection rechecks.
func askingDevice(w http.ResponseWriter, r *http.Request, st callLinkStore) (pbx.Device, func(context.Context) error, bool) {
	p, _ := auth.PrincipalFromContext(r.Context())
	if p.Pending || !slices.Contains(p.Scopes, "team:read") {
		apihttp.WriteProblem(w, http.StatusForbidden, "insufficient_scope", "This needs the team:read scope.")
		return pbx.Device{}, nil, false
	}
	if p.DeviceID != nil {
		id := *p.DeviceID
		d, err := st.Device(r.Context(), p.TenantID, id)
		if err != nil {
			apihttp.WriteProblem(w, http.StatusForbidden, "not_a_phone", "This phone isn't set up.")
			return pbx.Device{}, nil, false
		}
		return d, func(ctx context.Context) error {
			_, _, err := st.DevicePrincipalFor(ctx, id, time.Now())
			return err
		}, true
	}
	if !sameOrigin(r) {
		apihttp.WriteProblem(w, http.StatusForbidden, "origin_invalid", "Open the call from this server's own web page.")
		return pbx.Device{}, nil, false
	}
	sess, ok := auth.SessionFromContext(r.Context())
	if !ok {
		apihttp.WriteProblem(w, http.StatusUnauthorized, "session_required", "Sign in first.")
		return pbx.Device{}, nil, false
	}
	d, err := st.WebDeviceForSession(r.Context(), sess.ID)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusForbidden, "no_phone_line", "This page has no phone line.")
		return pbx.Device{}, nil, false
	}
	return d, func(ctx context.Context) error {
		cur, err := st.SessionByTokenHash(ctx, sess.TokenHash)
		if err != nil {
			return err
		}
		return liveSession(cur, time.Now())
	}, true
}

func serveCallLink(
	ctx context.Context, c *websocket.Conn, resolver callResolver, hub *calllink.Hub, leg calllink.Leg,
	stillThere func(context.Context) error,
) {
	c.SetReadLimit(callLinkLimit)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := hub.Join(leg)
	defer hub.Leave(m)

	// Reading: each message from the device, passed on unread.
	go func() {
		defer cancel()
		allowance, last := float64(callLinkRate), time.Now()
		for {
			_, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			now := time.Now()
			allowance = min(callLinkRate, allowance+now.Sub(last).Seconds()*callLinkRate)
			last = now
			if allowance < 1 {
				c.Close(websocket.StatusPolicyViolation, "too many messages")
				return
			}
			allowance--
			var msg struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if json.Unmarshal(b, &msg) != nil || msg.Type != "signal" || len(msg.Data) == 0 {
				continue
			}
			hub.Relay(m, msg.Data)
		}
	}()

	check := time.NewTicker(callLinkCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-m.Out:
			if !ok {
				c.Close(websocket.StatusNormalClosure, "replaced")
				return
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				return
			}
		case <-check.C:
			if err := stillThere(ctx); err != nil {
				c.Close(websocket.StatusPolicyViolation, "signed out")
				return
			}
			if !resolver.Alive(ctx, leg.ChannelID) {
				c.Close(websocket.StatusNormalClosure, "the call has ended")
				return
			}
		}
	}
}
