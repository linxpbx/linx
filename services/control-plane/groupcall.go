package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/calllink"
	"linxpbx.com/linx/internal/livekit"
	"linxpbx.com/linx/internal/pbx"
)

// Group calls (ADR-088, docs/VIDEO_CALLS.md §4): Add person in a call
// between two Linx devices makes a room on the video server, moves both
// people into it and rings the new person; anyone in the room can add more.
// Every token is minted here, and only for a device Asterisk shows in that
// call, ringing for that room, or LiveKit shows in it already.

const (
	// GroupCallsPath and the paths under it are hand-written handlers
	// (excluded from code generation), like the call link.
	GroupCallsPath = "/api/v1/group-calls"
	// StasisRoom is the Stasis argument of an invitation's waiting half.
	StasisRoom = "room"
	// groupTokenLife is how long a token is good for joining; LiveKit
	// keeps a joined device in and refreshes its token itself.
	groupTokenLife = 10 * time.Minute
	// groupRing is how long an invitation rings.
	groupRing = 30
	// groupRoomsKept is how long a room this control plane made can still
	// be joined from an invitation.
	groupRoomsKept = 12 * time.Hour
)

// groupStore is what group calls read.
type groupStore interface {
	callLinkStore
	DeviceBySIPUsername(ctx context.Context, username string) (pbx.Device, string, error)
	Extension(ctx context.Context, tenant, id uuid.UUID) (pbx.Extension, error)
	ExtensionByNumber(ctx context.Context, tenant uuid.UUID, number string) (pbx.Extension, error)
}

// groupAsterisk is the Asterisk side (calllink.Resolver).
type groupAsterisk interface {
	Resolve(ctx context.Context, sipUsername, callID string) (calllink.Leg, error)
	Room(ctx context.Context, sipUsername, callID string) (string, error)
	Invite(ctx context.Context, app, dialplanContext string, in calllink.Invitation) error
}

// groupRooms is the video server side (livekit.Rooms).
type groupRooms interface {
	Participants(ctx context.Context, room string) ([]string, error)
}

type groupCalls struct {
	St       groupStore
	Asterisk groupAsterisk
	Hub      *calllink.Hub
	Rooms    groupRooms
	Secret   []byte
	Now      func() time.Time
	Log      *slog.Logger

	mu     sync.Mutex
	madeAt map[string]time.Time
}

// GroupCallBody is every group call answer: where to join and with what.
type GroupCallBody struct {
	Room  string `json:"room"`
	Token string `json:"token"`
	// URL is the video server's address on this server (a path: the device
	// joins through the address it already uses).
	URL string `json:"url"`
}

// MoveMessage tells the other end of a call, over the call link, that the
// call has become a group call and how to join it.
type MoveMessage struct {
	Type string `json:"type"` // "move"
	GroupCallBody
}

func (g *groupCalls) handler(authn *auth.Authenticator) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+GroupCallsPath, g.start)
	mux.HandleFunc("POST "+GroupCallsPath+"/join", g.join)
	mux.HandleFunc("POST "+GroupCallsPath+"/{room}/people", g.invite)
	return apihttp.NoStore(apihttp.LimitBody(authn.Middleware(mux)))
}

func (g *groupCalls) ready(w http.ResponseWriter) bool {
	if len(g.Secret) == 0 || g.Rooms == nil {
		apihttp.WriteProblem(w, http.StatusServiceUnavailable, "group_calls_off", "Group calls aren't set up on this server.")
		return false
	}
	return true
}

// start is POST /api/v1/group-calls: Add person in a call between two Linx
// devices. Both people are given the room (the other over the call link)
// and the extension is rung into it.
func (g *groupCalls) start(w http.ResponseWriter, r *http.Request) {
	if !g.ready(w) {
		return
	}
	var req struct {
		Call      string `json:"call"`
		Extension string `json:"extension"`
	}
	if !readJSON(w, r, &req) || !validCallID(w, req.Call) {
		return
	}
	device, _, ok := askingDevice(w, r, g.St)
	if !ok {
		return
	}
	leg, err := g.Asterisk.Resolve(r.Context(), device.SIPUsername, req.Call)
	if !g.foundCall(w, err) {
		return
	}
	// Both ends must be able to go: the other side's link has to be open,
	// or they would be left behind on a call nobody is in.
	if peer, linked := g.Hub.Linked(leg.Channel); !leg.PeerLinx || !linked || peer != leg.Peer {
		apihttp.WriteProblem(w, http.StatusConflict, "not_linx",
			"Only a call between two Linx apps or browsers can become a group call.")
		return
	}
	other, _, err := g.St.DeviceBySIPUsername(r.Context(), pbxChannel(leg.Peer))
	if err != nil {
		apihttp.WriteProblem(w, http.StatusConflict, "not_linx", "Linx can't tell who the other side is.")
		return
	}
	invitee, ok := g.extension(w, r, device, req.Extension)
	if !ok {
		return
	}
	room := "call-" + strings.ToLower(rand.Text())
	g.remember(room)
	mine, err := g.token(r.Context(), device, room)
	if err != nil {
		g.fail(w, err)
		return
	}
	theirs, err := g.token(r.Context(), other, room)
	if err != nil {
		g.fail(w, err)
		return
	}
	g.Hub.Tell(leg.Peer, MoveMessage{Type: "move", GroupCallBody: GroupCallBody{Room: room, Token: theirs, URL: LiveKitPath}})
	if !g.ring(w, r, device, invitee, room) {
		return
	}
	writeJSON(w, http.StatusOK, GroupCallBody{Room: room, Token: mine, URL: LiveKitPath})
}

// join is POST /api/v1/group-calls/join: the invited device, once it has
// answered, asks for the room its call was ringing it into.
func (g *groupCalls) join(w http.ResponseWriter, r *http.Request) {
	if !g.ready(w) {
		return
	}
	var req struct {
		Call string `json:"call"`
	}
	if !readJSON(w, r, &req) || !validCallID(w, req.Call) {
		return
	}
	device, _, ok := askingDevice(w, r, g.St)
	if !ok {
		return
	}
	room, err := g.Asterisk.Room(r.Context(), device.SIPUsername, req.Call)
	if !g.foundCall(w, err) {
		return
	}
	if room == "" || !g.made(room) {
		apihttp.WriteProblem(w, http.StatusNotFound, "no_group_call", "That call isn't an invitation to a group call.")
		return
	}
	token, err := g.token(r.Context(), device, room)
	if err != nil {
		g.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, GroupCallBody{Room: room, Token: token, URL: LiveKitPath})
}

// invite is POST /api/v1/group-calls/{room}/people: someone already in the
// room rings one more person into it.
func (g *groupCalls) invite(w http.ResponseWriter, r *http.Request) {
	if !g.ready(w) {
		return
	}
	room := r.PathValue("room")
	var req struct {
		Extension string `json:"extension"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	device, _, ok := askingDevice(w, r, g.St)
	if !ok {
		return
	}
	if !g.made(room) {
		apihttp.WriteProblem(w, http.StatusNotFound, "no_group_call", "That group call has ended.")
		return
	}
	in, err := g.Rooms.Participants(r.Context(), room)
	if err != nil {
		g.fail(w, err)
		return
	}
	if !slices.Contains(in, device.ID.String()) {
		apihttp.WriteProblem(w, http.StatusForbidden, "not_in_call", "Only someone in the group call can add people to it.")
		return
	}
	if len(in) >= livekit.MaxPeople {
		apihttp.WriteProblem(w, http.StatusConflict, "group_call_full", "A group call holds 8 people.")
		return
	}
	invitee, ok := g.extension(w, r, device, req.Extension)
	if !ok {
		return
	}
	if !g.ring(w, r, device, invitee, room) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *groupCalls) extension(w http.ResponseWriter, r *http.Request, asker pbx.Device, number string) (pbx.Extension, bool) {
	number = strings.TrimSpace(number)
	e, err := g.St.ExtensionByNumber(r.Context(), asker.TenantID, number)
	if err != nil || !e.Enabled || e.DeletedAt != nil {
		apihttp.WriteProblem(w, http.StatusNotFound, "no_such_extension", "There's nobody on that extension.")
		return pbx.Extension{}, false
	}
	if e.ID == asker.ExtensionID {
		apihttp.WriteProblem(w, http.StatusConflict, "already_in_call", "You're already in this call.")
		return pbx.Extension{}, false
	}
	return e, true
}

func (g *groupCalls) ring(w http.ResponseWriter, r *http.Request, asker pbx.Device, invitee pbx.Extension, room string) bool {
	from, err := g.St.Extension(r.Context(), asker.TenantID, asker.ExtensionID)
	if err != nil {
		g.fail(w, err)
		return false
	}
	err = g.Asterisk.Invite(r.Context(), asteriskconf.ARIApp, asteriskconf.RoomInviteContext, calllink.Invitation{
		Extension: invitee.Number, Room: room, FromName: from.DisplayName, FromNumber: from.Number, Ring: groupRing,
	})
	if err != nil {
		g.fail(w, err)
		return false
	}
	return true
}

func (g *groupCalls) token(ctx context.Context, d pbx.Device, room string) (string, error) {
	name := d.Name
	if e, err := g.St.Extension(ctx, d.TenantID, d.ExtensionID); err == nil {
		name = e.DisplayName
	}
	return livekit.Token(g.Secret, d.ID.String(), name, livekit.JoinGrant(room), groupTokenLife, g.now())
}

func (g *groupCalls) foundCall(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, calllink.ErrNoCall):
		apihttp.WriteProblem(w, http.StatusNotFound, "no_such_call", "Linx can't find that call.")
		return false
	case err != nil:
		apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx can't reach the phone system right now.")
		return false
	}
	return true
}

func (g *groupCalls) fail(w http.ResponseWriter, err error) {
	g.Log.Error("group call", "err", err)
	apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx couldn't set up the group call. Try again.")
}

func (g *groupCalls) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *groupCalls) remember(room string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.madeAt == nil {
		g.madeAt = map[string]time.Time{}
	}
	now := g.now()
	for r, at := range g.madeAt {
		if now.Sub(at) > groupRoomsKept {
			delete(g.madeAt, r)
		}
	}
	g.madeAt[room] = now
}

func (g *groupCalls) made(room string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	at, ok := g.madeAt[room]
	return ok && g.now().Sub(at) <= groupRoomsKept
}

// pbxChannel is the PJSIP endpoint of a channel name.
func pbxChannel(name string) string {
	rest, _ := strings.CutPrefix(name, "PJSIP/")
	if i := strings.LastIndexByte(rest, '-'); i > 0 {
		return rest[:i]
	}
	return rest
}

// callIDOK is whether callID could be a SIP Call-ID at all.
func callIDOK(callID string) bool {
	return callID != "" && len(callID) <= 256 &&
		strings.IndexFunc(callID, func(c rune) bool { return c > unicode.MaxASCII || !unicode.IsPrint(c) }) < 0
}

func validCallID(w http.ResponseWriter, callID string) bool {
	if !callIDOK(callID) {
		apihttp.WriteProblem(w, http.StatusBadRequest, "call_invalid", "Say which call this is.")
		return false
	}
	return true
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		apihttp.WriteProblem(w, http.StatusBadRequest, "body_invalid", "That request couldn't be read.")
		return false
	}
	return true
}
