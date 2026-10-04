package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// The Team list's live updates (docs/WEB.md §6: "a small events
// websocket"). Each open page gets the whole list at once and again
// whenever it changes; the list is small (one row per extension), so there
// are no diffs to get wrong.

const (
	// teamSettle gathers a burst of changes (a call's several ARI events)
	// into one update.
	teamSettle = 150 * time.Millisecond
	// teamRefresh also recomputes the list this often, for changes nothing
	// announces (an admin renaming an extension, a person disabled).
	teamRefresh = 15 * time.Second
	// teamCheck is how often an open websocket rechecks its session and
	// pings the browser.
	teamCheck = 30 * time.Second
	// teamPerSession is how many Team lists one session may have open at
	// once (a few tabs); each is a connection and a copy of every update.
	teamPerSession = 8
)

type teamHub struct {
	team *pbx.Team
	log  *slog.Logger

	kick chan struct{}
	mu   sync.Mutex
	subs map[uuid.UUID]map[*teamSub]struct{} // by tenant
	last map[uuid.UUID][]byte
	open map[uuid.UUID]int // by session
	// vm counts each tenant's voicemail changes (Phase 1F step 14): the
	// page fetches its own badge count when it moves, so no one's count
	// goes to anyone else.
	vm map[uuid.UUID]int64
	// calls counts each tenant's call history changes (step 15), for the
	// Call history badge, the same way.
	calls map[uuid.UUID]int64
}

type teamSub struct {
	ch chan []byte // holds only the newest list
}

func newTeamHub(team *pbx.Team, log *slog.Logger) *teamHub {
	return &teamHub{team: team, log: log, kick: make(chan struct{}, 1),
		subs: map[uuid.UUID]map[*teamSub]struct{}{}, last: map[uuid.UUID][]byte{}, open: map[uuid.UUID]int{},
		vm: map[uuid.UUID]int64{}, calls: map[uuid.UUID]int64{}}
}

// acquire counts one more open list for session, unless it already has
// teamPerSession; release undoes it.
func (h *teamHub) acquire(session uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.open[session] >= teamPerSession {
		return false
	}
	h.open[session]++
	return true
}

func (h *teamHub) release(session uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.open[session]--; h.open[session] <= 0 {
		delete(h.open, session)
	}
}

// Changed asks for the list to be recomputed soon. It never blocks.
func (h *teamHub) Changed() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// VoicemailChanged says a tenant's voicemail arrived, was heard or was
// deleted. Never blocks.
func (h *teamHub) VoicemailChanged(tenant uuid.UUID) {
	h.mu.Lock()
	h.vm[tenant]++
	h.mu.Unlock()
	h.Changed()
}

// CallsChanged says a tenant's call history changed (a call ended, or old
// calls went). Never blocks.
func (h *teamHub) CallsChanged(tenant uuid.UUID) {
	h.mu.Lock()
	h.calls[tenant]++
	h.mu.Unlock()
	h.Changed()
}

// Run recomputes the list for every tenant someone is watching whenever
// Changed is called (after teamSettle) and every teamRefresh, and sends it
// to the watchers if it differs from the last one sent.
func (h *teamHub) Run(ctx context.Context) {
	t := time.NewTicker(teamRefresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.kick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(teamSettle):
			}
		case <-t.C:
		}
		h.mu.Lock()
		tenants := make([]uuid.UUID, 0, len(h.subs))
		for tenant := range h.subs {
			tenants = append(tenants, tenant)
		}
		h.mu.Unlock()
		for _, tenant := range tenants {
			b, err := h.snapshot(ctx, tenant)
			if err != nil {
				if ctx.Err() == nil {
					h.log.Error("building the Team list", "err", err)
				}
				continue
			}
			h.publish(tenant, b)
		}
	}
}

func (h *teamHub) snapshot(ctx context.Context, tenant uuid.UUID) ([]byte, error) {
	rows, err := h.team.List(ctx, tenant)
	if err != nil {
		return nil, err
	}
	body := controlplaneapi.TeamListBody(rows)
	h.mu.Lock()
	vm, calls := h.vm[tenant], h.calls[tenant]
	h.mu.Unlock()
	body.Voicemail, body.Calls = &vm, &calls
	return json.Marshal(body)
}

func (h *teamHub) publish(tenant uuid.UUID, b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if bytes.Equal(h.last[tenant], b) {
		return
	}
	h.last[tenant] = b
	for s := range h.subs[tenant] {
		s.offer(b)
	}
}

func (s *teamSub) offer(b []byte) {
	select {
	case <-s.ch: // drop a list the page hasn't taken yet: this one replaces it
	default:
	}
	s.ch <- b
}

// subscribe adds a watcher and queues the current list for it.
func (h *teamHub) subscribe(ctx context.Context, tenant uuid.UUID) (*teamSub, error) {
	b, err := h.snapshot(ctx, tenant)
	if err != nil {
		return nil, err
	}
	s := &teamSub{ch: make(chan []byte, 1)}
	s.ch <- b
	h.mu.Lock()
	if h.subs[tenant] == nil {
		h.subs[tenant] = map[*teamSub]struct{}{}
	}
	h.subs[tenant][s] = struct{}{}
	h.last[tenant] = b
	h.mu.Unlock()
	return s, nil
}

func (h *teamHub) unsubscribe(tenant uuid.UUID, s *teamSub) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs[tenant], s)
	if len(h.subs[tenant]) == 0 {
		delete(h.subs, tenant)
		delete(h.last, tenant)
	}
}

// teamStore is what an open Team list rechecks: the browser's session, or
// the phone's device (docs/PHASE2.md §12, step 7).
type teamStore interface {
	auth.SessionStore
	DevicePrincipalFor(ctx context.Context, device uuid.UUID, now time.Time) (tenant, user uuid.UUID, err error)
}

// teamLiveHandler is GET /api/v1/team/live. It takes either
//
//   - a signed-in browser session's cookie, from a page on this server's own
//     address, holding team:read; or
//   - a set-up phone's device token in the Authorization header, with no
//     Origin check, exactly as GET /sip does (docs/PHASE2.md §4): the app is
//     not a web page and sends none, and no web page can put an
//     Authorization header on a websocket.
//
// Either way it ends when the credential does (checked every teamCheck).
func teamLiveHandler(authn *auth.Authenticator, st teamStore, hub *teamHub) http.Handler {
	return apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, hasPrincipal := auth.PrincipalFromContext(r.Context())
		if hasPrincipal && p.DeviceID != nil {
			servePhoneTeamLive(w, r, st, hub, p)
			return
		}
		if !sameOrigin(r) {
			apihttp.WriteProblem(w, http.StatusForbidden, "origin_invalid", "Open the Team list from this server's own web page.")
			return
		}
		sess, ok := auth.SessionFromContext(r.Context())
		if !ok {
			apihttp.WriteProblem(w, http.StatusUnauthorized, "session_required", "Sign in first.")
			return
		}
		if p.Pending || !slices.Contains(p.Scopes, "team:read") {
			apihttp.WriteProblem(w, http.StatusForbidden, "insufficient_scope", "This needs the team:read scope.")
			return
		}
		serveTeamLive(w, r, hub, sess.TenantID, sess.ID, func(ctx context.Context) error {
			cur, err := st.SessionByTokenHash(ctx, sess.TokenHash)
			if err != nil {
				return err
			}
			return liveSession(cur, time.Now())
		})
	})))
}

// servePhoneTeamLive is the Team list for a set-up phone. The authenticator
// has already refused a revoked or expired one; the recheck below is what
// closes a list left open on a phone that has since been stopped.
func servePhoneTeamLive(w http.ResponseWriter, r *http.Request, st teamStore, hub *teamHub, p auth.Principal) {
	if !slices.Contains(p.Scopes, "team:read") {
		apihttp.WriteProblem(w, http.StatusForbidden, "insufficient_scope", "This needs the team:read scope.")
		return
	}
	device := *p.DeviceID
	serveTeamLive(w, r, hub, p.TenantID, device, func(ctx context.Context) error {
		if _, _, err := st.DevicePrincipalFor(ctx, device, time.Now()); err != nil {
			if errors.Is(err, pbx.ErrNotFound) {
				return errors.New("this phone is no longer set up")
			}
			return err
		}
		return nil
	})
}

// serveTeamLive holds the websocket open: the whole list at once and again
// whenever it changes, and nothing is ever read from the other end. holder
// is the session or the phone, for the "how many at once" count; stillThere
// is what is rechecked every teamCheck.
func serveTeamLive(
	w http.ResponseWriter, r *http.Request, hub *teamHub, tenant, holder uuid.UUID,
	stillThere func(context.Context) error,
) {
	if !hub.acquire(holder) {
		apihttp.WriteProblem(w, http.StatusTooManyRequests, "too_many_open",
			"The Team list is open in too many tabs. Close some and try again.")
		return
	}
	defer hub.release(holder)
	sub, err := hub.subscribe(r.Context(), tenant)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx can't load the Team list right now. Try again shortly.")
		return
	}
	defer hub.unsubscribe(tenant, sub)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{controlplaneapi.TeamSubprotocol}})
	if err != nil {
		return
	}
	defer c.CloseNow()
	if c.Subprotocol() != controlplaneapi.TeamSubprotocol {
		c.Close(websocket.StatusPolicyViolation, "use the "+controlplaneapi.TeamSubprotocol+" subprotocol")
		return
	}
	// Nothing is read from the page or the phone; this only handles pings
	// and close.
	ctx := c.CloseRead(context.WithoutCancel(r.Context()))
	check := time.NewTicker(teamCheck)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b := <-sub.ch:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		case <-check.C:
			if err := stillThere(ctx); err != nil {
				c.Close(websocket.StatusPolicyViolation, "signed out")
				return
			}
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			cancel()
			if err != nil {
				return
			}
		}
	}
}
