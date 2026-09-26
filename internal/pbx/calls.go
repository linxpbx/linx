package pbx

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/numbering"
)

// Call states for /calls/active.
const (
	CallRinging  = "ringing"  // devices are ringing, or about to
	CallAnswered = "answered" // a device answered
	CallSystem   = "system"   // Linx itself answered: echo test or a spoken message
)

// Call outcomes, in call.ended.
const (
	OutcomeAnswered     = "answered"      // a device answered
	OutcomeMissed       = "missed"        // devices rang, nobody answered (or the caller gave up)
	OutcomeNotAvailable = "not_available" // nothing could ring: no signed-in device, or calling yourself
	OutcomeNotInUse     = "not_in_use"    // no such number
	OutcomeEchoTest     = "echo_test"     // *43
	// Outside calls Linx refused (docs/TRUNKS.md §5, §9).
	OutcomeNotPermitted = "not_permitted" // the caller's call permission level doesn't allow that kind of number
	OutcomeNoLines      = "no_lines"      // no outside line set up, or every line down or full
	OutcomeLimitReached = "limit_reached" // the caller's extension already has 2 outside calls
)

// Call directions (docs/TRUNKS.md §9).
const (
	DirectionInternal = "internal" // between Linx's own extensions (or to Linx itself)
	DirectionInbound  = "inbound"  // from a trunk
	DirectionOutbound = "outbound" // to an outside number
)

// EchoTestNumber is the dialplan's echo test (internal/asteriskconf).
const EchoTestNumber = "*43"

// trunkEndpointPrefix is how trunks' PJSIP endpoints are named
// (trunk.Trunk's Endpoint).
const trunkEndpointPrefix = "trunk-"

// DIDVariable is the channel variable the dialplan sets on a call from a
// trunk: the phone number the caller dialled (ari.conf's channelvars).
const DIDVariable = "LINX_DID"

// CallStore is the database access the call tracker needs.
type CallStore interface {
	DeviceBySIPUsername(ctx context.Context, username string) (Device, string, error)
	SetDeviceOnline(ctx context.Context, username string, online bool, from *netip.Addr, at time.Time) (bool, error)
	InsertCallEvent(ctx context.Context, tenant uuid.UUID, eventType string, data any, at time.Time) error
	// TrunkTenant returns the tenant of the trunk with id (ErrNotFound if
	// there's none).
	TrunkTenant(ctx context.Context, id uuid.UUID) (uuid.UUID, error)
	// Country is the country outside numbers are read in (docs/TRUNKS.md §5).
	Country(ctx context.Context) (string, error)
}

// CallParty is one end of a call: one of Linx's devices, or someone
// outside (only Number, and Name if their provider sent one).
type CallParty struct {
	Extension string     `json:"extension,omitempty"`
	DeviceID  *uuid.UUID `json:"device_id,omitempty"`
	Name      string     `json:"name"`
	Number    string     `json:"number,omitempty"`
}

// ActiveCall is a call in progress (GET /calls/active).
type ActiveCall struct {
	ID        uuid.UUID
	Direction string
	TrunkID   *uuid.UUID
	From      CallParty
	To        string
	// Ringing is the extension this call rings, once Linx knows it (an
	// outside caller's number rings the extension its DID points to).
	Ringing    string
	State      string
	StartedAt  time.Time
	AnsweredAt *time.Time
	AnsweredBy *CallParty
}

// OutsideCall is an outgoing call as the CallWatcher sees it.
type OutsideCall struct {
	ID, Tenant uuid.UUID
	// TrunkID: the line it went out on (the last one tried); nil if none.
	TrunkID   *uuid.UUID
	Extension string // the caller's
	Dialled   string
	// Number is what was called: E.164, or the short number as dialled.
	Number     string
	Result     numbering.Result
	StartedAt  time.Time
	AnsweredAt *time.Time
	// WentOut: a trunk was dialled (it wasn't refused before that).
	WentOut bool
	// Set when it ended.
	EndedAt     time.Time
	TalkSeconds int
	Outcome     string
}

// CallWatcher hears about outgoing calls (the toll-fraud and emergency
// alerts, docs/TRUNKS.md §9). Its methods must not block for long.
type CallWatcher interface {
	OutsideCallStarted(ctx context.Context, c OutsideCall)
	OutsideCallEnded(ctx context.Context, c OutsideCall)
}

// CallTracker is the control plane's ARI app (docs/PBX.md §4): it watches
// Asterisk's events to keep device online state, fire call webhooks and
// know the calls in progress. It never changes a call: the dialplan rings
// devices by itself, so calls keep working while the control plane is
// away; only these events pause.
type CallTracker struct {
	Store CallStore
	Log   *slog.Logger
	Now   func() time.Time
	// OnChange, if set, is called after any event that may have changed a
	// call or a device's online state (the Team list's live updates). It
	// must not block.
	OnChange func()
	// Watch, if set, hears about outgoing calls.
	Watch CallWatcher

	mu        sync.Mutex
	connected bool
	calls     map[string]*call // by the caller's channel id
	legs      map[string]string
}

type call struct {
	id         uuid.UUID
	tenant     uuid.UUID
	direction  string
	trunkID    *uuid.UUID
	from       CallParty
	to         string
	ringing    string
	outside    numbering.Result // outbound: what was dialled
	wentOut    bool
	startedAt  time.Time
	answeredAt *time.Time
	answeredBy *CallParty
	rang       bool
	up         bool   // the caller's channel is answered (by a device or by Linx)
	where      string // the caller's last dialplan context/exten
	resynced   bool   // found on (re)connect: its call.started was never sent
}

// Connected reports whether Asterisk's ARI connection is up. While it isn't,
// ActiveCalls and device online state may be out of date.
func (t *CallTracker) Connected() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected
}

// ActiveCalls returns the calls in progress, oldest first.
func (t *CallTracker) ActiveCalls() []ActiveCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ActiveCall, 0, len(t.calls))
	for _, c := range t.calls {
		a := ActiveCall{ID: c.id, Direction: c.direction, TrunkID: c.trunkID, From: c.from, To: c.to, Ringing: c.ringing,
			StartedAt: c.startedAt, AnsweredAt: c.answeredAt, AnsweredBy: c.answeredBy}
		switch {
		case c.answeredAt != nil:
			a.State = CallAnswered
		case c.up:
			a.State = CallSystem
		default:
			a.State = CallRinging
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out
}

// Serve runs for one Asterisk connection (ari.App).
func (t *CallTracker) Serve(ctx context.Context, c *ari.Conn) {
	t.mu.Lock()
	t.connected = true
	if t.calls == nil {
		t.calls, t.legs = map[string]*call{}, map[string]string{}
	}
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.connected = false
		t.mu.Unlock()
		t.changed()
	}()

	for ev := range c.Events() {
		if ev.Type == "ApplicationRegistered" {
			// Asterisk only answers REST requests once the app is
			// registered; catch up on calls that changed while we were away.
			t.resync(ctx, c)
			t.changed()
			continue
		}
		t.handle(ctx, ev)
	}
}

func (t *CallTracker) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func eventTime(ev ari.Event, fallback time.Time) time.Time {
	if ev.Timestamp.IsZero() {
		return fallback
	}
	return ev.Timestamp.Time
}

func (t *CallTracker) handle(ctx context.Context, ev ari.Event) {
	at := eventTime(ev, t.now())
	switch ev.Type {
	case "ContactStatusChange":
		t.contact(ctx, ev, at)
	case "ChannelCreated":
		// A device's own call arrives ringing ("Ring"); legs the dialplan
		// dials out to start "Down" and are matched by their Dial event.
		if ev.Channel != nil && ev.Channel.State == "Ring" {
			t.start(ctx, *ev.Channel, at, false)
		}
	case "Dial":
		t.dial(ctx, ev, at)
	case "ChannelStateChange", "ChannelDialplan", "ChannelVarset", "ChannelHangupRequest":
		t.observe(ev.Channel)
	case "ChannelDestroyed":
		t.destroyed(ctx, ev, at)
	default:
		return
	}
	t.changed()
}

func (t *CallTracker) changed() {
	if t.OnChange != nil {
		t.OnChange()
	}
}

// contact turns registrations into device online state.
func (t *CallTracker) contact(ctx context.Context, ev ari.Event, at time.Time) {
	if ev.Endpoint == nil || ev.ContactInfo == nil || ev.Endpoint.Technology != "PJSIP" {
		return
	}
	var online bool
	switch ev.ContactInfo.ContactStatus {
	case "Removed", "Unreachable":
		online = false
	case "Created", "Reachable", "NonQualified", "Updated", "Unknown":
		online = true
	default:
		return
	}
	var from *netip.Addr
	if online {
		from = contactAddr(ev.ContactInfo.URI)
	}
	if _, err := t.Store.SetDeviceOnline(ctx, ev.Endpoint.Resource, online, from, at); err != nil && !errors.Is(err, ErrNotFound) {
		t.Log.Error("recording device sign-in", "device", ev.Endpoint.Resource, "err", err)
	}
}

// contactAddr is the IP address in a registration's contact URI
// (sip:d_x@192.0.2.1:5061;transport=TLS). With rewrite_contact on, Asterisk
// puts the address it actually saw there.
func contactAddr(uri string) *netip.Addr {
	u, err := url.Parse(uri)
	if err != nil {
		return nil
	}
	hostport, _, _ := strings.Cut(u.Opaque, ";")
	if _, h, ok := strings.Cut(hostport, "@"); ok {
		hostport = h
	}
	if ap, err := netip.ParseAddrPort(hostport); err == nil {
		a := ap.Addr().Unmap()
		return &a
	}
	if a, err := netip.ParseAddr(strings.Trim(hostport, "[]")); err == nil {
		a = a.Unmap()
		return &a
	}
	return nil
}

func (t *CallTracker) party(ctx context.Context, endpoint string) (CallParty, uuid.UUID, bool) {
	if endpoint == "" {
		return CallParty{}, uuid.Nil, false
	}
	d, number, err := t.Store.DeviceBySIPUsername(ctx, endpoint)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			t.Log.Error("looking up a call's device", "device", endpoint, "err", err)
		}
		return CallParty{}, uuid.Nil, false
	}
	id := d.ID
	return CallParty{Extension: number, DeviceID: &id, Name: d.Name}, d.TenantID, true
}

// trunkID is the trunk a PJSIP endpoint belongs to, if it's a trunk's.
func trunkID(endpoint string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(endpoint, trunkEndpointPrefix)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	return id, err == nil
}

// cleanNumber and cleanName keep what an outside caller's provider sent to
// the characters the dialplan keeps (internal/asteriskconf's
// linx-trunk-call): the tracker sees the channel before the dialplan runs.
func cleanNumber(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '+' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	return s[:min(len(s), 20)]
}

func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == ' ' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return -1
	}, s)
	return s[:min(len(s), 40)]
}

// isExtensionNumber: what the dialplan tries as an extension first
// (linx-extensions' _XX to _XXXXXX).
func isExtensionNumber(s string) bool {
	if len(s) < 2 || len(s) > 6 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// dialledOutside decides whether a device's call is to an outside number,
// as the dialplan will: extensions can't take emergency, service or
// outside numbers (migration 0016), so a number that's one of those goes
// out, and a short number that isn't is an extension.
func dialledOutside(home, dialled string) (numbering.Result, bool) {
	if dialled == EchoTestNumber {
		return numbering.Result{}, false
	}
	r := numbering.Classify(home, dialled)
	switch r.Category {
	case numbering.Emergency, numbering.Service:
		return r, true
	case numbering.Invalid:
		return r, false
	}
	return r, !isExtensionNumber(dialled)
}

func (t *CallTracker) country(ctx context.Context) string {
	home, err := t.Store.Country(ctx)
	if err != nil || home == "" {
		if err != nil {
			t.Log.Error("reading the country", "err", err)
		}
		return numbering.DefaultCountry
	}
	return home
}

func (t *CallTracker) start(ctx context.Context, ch ari.Channel, at time.Time, resynced bool) {
	started := at
	if !ch.CreationTime.IsZero() {
		started = ch.CreationTime.Time
	}
	c := &call{id: uuid.Must(uuid.NewV7()), startedAt: started, resynced: resynced, direction: DirectionInternal}
	if id, ok := trunkID(ch.Endpoint()); ok {
		tenant, err := t.Store.TrunkTenant(ctx, id)
		if err != nil {
			if !errors.Is(err, ErrNotFound) {
				t.Log.Error("looking up a call's trunk", "trunk", id, "err", err)
			}
			return
		}
		number := cleanNumber(ch.Caller.Number)
		if r := numbering.Classify(t.country(ctx), number); r.E164 != "" {
			number = r.E164
		}
		c.tenant, c.direction, c.trunkID = tenant, DirectionInbound, &id
		c.from = CallParty{Name: cleanName(ch.Caller.Name), Number: number}
		c.to = cleanNumber(ch.Dialplan.Exten)
	} else {
		from, tenant, ok := t.party(ctx, ch.Endpoint())
		if !ok {
			return
		}
		c.tenant, c.from, c.to = tenant, from, ch.Dialplan.Exten
		if r, out := dialledOutside(t.country(ctx), c.to); out {
			c.direction, c.outside = DirectionOutbound, r
		} else if isExtensionNumber(c.to) {
			c.ringing = c.to
		}
	}
	t.mu.Lock()
	if _, dup := t.calls[ch.ID]; dup {
		t.mu.Unlock()
		return
	}
	t.calls[ch.ID] = c
	t.mu.Unlock()
	t.observe(&ch)
	if !resynced {
		t.emit(ctx, c, "call.started", at, nil)
		if c.direction == DirectionOutbound && t.Watch != nil {
			t.Watch.OutsideCallStarted(ctx, t.outsideCall(c))
		}
	}
}

// outsideCall is c for the CallWatcher.
func (t *CallTracker) outsideCall(c *call) OutsideCall {
	t.mu.Lock()
	defer t.mu.Unlock()
	number := c.outside.E164
	if number == "" {
		number = c.outside.Dial
	}
	return OutsideCall{ID: c.id, Tenant: c.tenant, TrunkID: c.trunkID, Extension: c.from.Extension, Dialled: c.to,
		Number: number, Result: c.outside, StartedAt: c.startedAt, AnsweredAt: c.answeredAt, WentOut: c.wentOut}
}

// OutsideCallsInProgress returns the outgoing calls in progress that went
// out on a trunk.
func (t *CallTracker) OutsideCallsInProgress() []OutsideCall {
	t.mu.Lock()
	var out []*call
	for _, c := range t.calls {
		if c.direction == DirectionOutbound && c.wentOut {
			out = append(out, c)
		}
	}
	t.mu.Unlock()
	calls := make([]OutsideCall, 0, len(out))
	for _, c := range out {
		calls = append(calls, t.outsideCall(c))
	}
	return calls
}

// observe keeps what a channel snapshot says about its call's caller side.
func (t *CallTracker) observe(ch *ari.Channel) {
	if ch == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.calls[ch.ID]
	if c == nil {
		return
	}
	if ch.State == "Up" {
		c.up = true
	}
	if ch.Dialplan.Context != "" {
		c.where = ch.Dialplan.Context + "/" + ch.Dialplan.Exten
		if ch.Dialplan.Context == "linx-ring" {
			c.ringing = ch.Dialplan.Exten
		}
	}
	if did := cleanNumber(ch.ChannelVars[DIDVariable]); did != "" && c.direction == DirectionInbound {
		c.to = did
	}
}

func (t *CallTracker) dial(ctx context.Context, ev ari.Event, at time.Time) {
	if ev.Caller == nil || ev.Peer == nil {
		return
	}
	t.mu.Lock()
	c := t.calls[ev.Caller.ID]
	if c == nil {
		t.mu.Unlock()
		return
	}
	t.legs[ev.Peer.ID] = ev.Caller.ID
	c.rang = true
	outLeg := false
	if id, ok := trunkID(ev.Peer.Endpoint()); ok {
		// A line tried for an outgoing call (the last one tried is the
		// one it went out on).
		c.direction, c.trunkID, c.wentOut, outLeg = DirectionOutbound, &id, true, true
	}
	answered := ev.DialStatus == "ANSWER" && c.answeredAt == nil
	number := c.outside.E164
	if number == "" {
		number = c.outside.Dial
	}
	t.mu.Unlock()
	if !answered {
		return
	}
	by := CallParty{Number: number}
	if !outLeg {
		by, _, _ = t.party(ctx, ev.Peer.Endpoint())
	}
	t.mu.Lock()
	c.answeredAt, c.answeredBy = &at, &by
	t.mu.Unlock()
	t.emit(ctx, c, "call.answered", at, nil)
}

func (t *CallTracker) destroyed(ctx context.Context, ev ari.Event, at time.Time) {
	if ev.Channel == nil {
		return
	}
	t.mu.Lock()
	delete(t.legs, ev.Channel.ID)
	c := t.calls[ev.Channel.ID]
	if c == nil {
		t.mu.Unlock()
		return
	}
	delete(t.calls, ev.Channel.ID)
	for leg, owner := range t.legs {
		if owner == ev.Channel.ID {
			delete(t.legs, leg)
		}
	}
	t.mu.Unlock()
	if ev.Channel.Dialplan.Context != "" {
		c.where = ev.Channel.Dialplan.Context + "/" + ev.Channel.Dialplan.Exten
	}
	t.end(ctx, c, at)
}

func (t *CallTracker) end(ctx context.Context, c *call, at time.Time) {
	outcome := OutcomeNotAvailable
	switch {
	case c.answeredAt != nil:
		outcome = OutcomeAnswered
	case c.to == EchoTestNumber:
		outcome = OutcomeEchoTest
	case c.where == "linx-messages/no-lines":
		// Every line tried failed (down, full, refused its certificate):
		// dialled, but nothing rang.
		outcome = OutcomeNoLines
	case c.rang:
		outcome = OutcomeMissed
	case c.where == "linx-messages/not-in-use":
		outcome = OutcomeNotInUse
	case c.where == "linx-messages/not-permitted":
		outcome = OutcomeNotPermitted
	case c.where == "linx-messages/limit":
		outcome = OutcomeLimitReached
	}
	talk := 0
	if c.answeredAt != nil {
		talk = int(at.Sub(*c.answeredAt).Round(time.Second) / time.Second)
	}
	extra := map[string]any{"ended_at": at.UTC(), "outcome": outcome, "duration_seconds": talk}
	t.emit(ctx, c, "call.ended", at, extra)
	// Its own event too, for whoever only wants to hear about missed calls.
	if outcome == OutcomeMissed {
		t.emit(ctx, c, "call.missed", at, extra)
	}
	if c.direction == DirectionOutbound && t.Watch != nil {
		oc := t.outsideCall(c)
		oc.EndedAt, oc.TalkSeconds, oc.Outcome = at, talk, outcome
		t.Watch.OutsideCallEnded(ctx, oc)
	}
}

func (t *CallTracker) emit(ctx context.Context, c *call, eventType string, at time.Time, extra map[string]any) {
	t.mu.Lock()
	data := map[string]any{
		"id": c.id, "direction": c.direction, "from": c.from, "to": c.to, "started_at": c.startedAt.UTC(),
	}
	if c.trunkID != nil {
		data["trunk_id"] = *c.trunkID
	}
	switch c.direction {
	case DirectionOutbound:
		if n := c.outside.E164; n != "" {
			data["outside_number"] = n
		} else if c.outside.Dial != "" {
			data["outside_number"] = c.outside.Dial
		}
	case DirectionInbound:
		if c.from.Number != "" {
			data["outside_number"] = c.from.Number
		}
	}
	if c.answeredAt != nil {
		data["answered_at"] = c.answeredAt.UTC()
		data["answered_by"] = c.answeredBy
	}
	t.mu.Unlock()
	for k, v := range extra {
		data[k] = v
	}
	if err := t.Store.InsertCallEvent(ctx, c.tenant, eventType, data, at); err != nil {
		t.Log.Error("queueing call event", "type", eventType, "err", err)
	}
}

// resyncAttempts bounds how long resync waits for Asterisk to finish booting.
const resyncAttempts = 30

// resync reconciles tracked calls with Asterisk's channels after a
// (re)connect: calls that ended meanwhile get their call.ended now, calls
// that started meanwhile are tracked from here (without a call.started,
// which would arrive late and out of order).
func (t *CallTracker) resync(ctx context.Context, c *ari.Conn) {
	// Asterisk answers 503 until it has fully booted, which can be a few
	// seconds after it registers the app.
	var chans []ari.Channel
	for attempt := 0; ; attempt++ {
		err := c.Get(ctx, "channels", &chans)
		if err == nil {
			break
		}
		if attempt == resyncAttempts || ctx.Err() != nil {
			t.Log.Error("reading calls in progress from Asterisk", "err", err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
	live := map[string]ari.Channel{}
	for _, ch := range chans {
		live[ch.ID] = ch
	}
	now := t.now()
	t.mu.Lock()
	var gone []*call
	for id, cl := range t.calls {
		if _, ok := live[id]; !ok {
			gone = append(gone, cl)
			delete(t.calls, id)
		}
	}
	for leg := range t.legs {
		if _, ok := live[leg]; !ok {
			delete(t.legs, leg)
		}
	}
	t.mu.Unlock()
	for _, cl := range gone {
		t.end(ctx, cl, now)
	}
	for _, ch := range chans {
		// Legs the dialplan dialled out run "AppDial"; everything else
		// is a device's own call.
		if ch.Dialplan.AppName == "AppDial" {
			continue
		}
		t.start(ctx, ch, now, true)
	}
}
