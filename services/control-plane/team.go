package main

import (
	"bytes"
	"context"
	"encoding/json"
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
)

type teamHub struct {
	team *pbx.Team
	log  *slog.Logger

	kick chan struct{}
	mu   sync.Mutex
	subs map[uuid.UUID]map[*teamSub]struct{} // by tenant
	last map[uuid.UUID][]byte
}

type teamSub struct {
	ch chan []byte // holds only the newest list
}

func newTeamHub(team *pbx.Team, log *slog.Logger) *teamHub {
	return &teamHub{team: team, log: log, kick: make(chan struct{}, 1),
		subs: map[uuid.UUID]map[*teamSub]struct{}{}, last: map[uuid.UUID][]byte{}}
}

// Changed asks for the list to be recomputed soon. It never blocks.
func (h *teamHub) Changed() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
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
	return json.Marshal(controlplaneapi.TeamListBody(rows))
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

// teamLiveHandler is GET /api/v1/team/live: signed-in browser sessions
// holding team:read, from this server's own page, like /sip. It ends when
// the session does (checked every teamCheck).
func teamLiveHandler(authn *auth.Authenticator, st auth.SessionStore, hub *teamHub) http.Handler {
	return apihttp.NoStore(authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			apihttp.WriteProblem(w, http.StatusForbidden, "origin_invalid", "Open the Team list from this server's own web page.")
			return
		}
		sess, ok := auth.SessionFromContext(r.Context())
		if !ok {
			apihttp.WriteProblem(w, http.StatusUnauthorized, "session_required", "Sign in first.")
			return
		}
		p, _ := auth.PrincipalFromContext(r.Context())
		if p.Pending || !slices.Contains(p.Scopes, "team:read") {
			apihttp.WriteProblem(w, http.StatusForbidden, "insufficient_scope", "This needs the team:read scope.")
			return
		}
		sub, err := hub.subscribe(r.Context(), sess.TenantID)
		if err != nil {
			apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx can't load the Team list right now. Try again shortly.")
			return
		}
		defer hub.unsubscribe(sess.TenantID, sub)
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{controlplaneapi.TeamSubprotocol}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		if c.Subprotocol() != controlplaneapi.TeamSubprotocol {
			c.Close(websocket.StatusPolicyViolation, "use the "+controlplaneapi.TeamSubprotocol+" subprotocol")
			return
		}
		// Nothing is read from the page; this only handles pings and close.
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
				cur, err := st.SessionByTokenHash(ctx, sess.TokenHash)
				if err != nil || liveSession(cur, time.Now()) != nil {
					c.Close(websocket.StatusPolicyViolation, "signed out")
					return
				}
				pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err = c.Ping(pctx)
				cancel()
				if err != nil {
					return
				}
			}
		}
	})))
}
