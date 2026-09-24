// Package ari is Linx's small hand-written client for Asterisk's REST
// Interface (ADR-034). Asterisk connects out to the control plane (ARI
// outbound websocket), so here the control plane is the websocket server:
// Handler accepts that connection, checks Asterisk's password, and hands a
// Conn to the app. Events arrive on the Conn; REST requests go back over the
// same websocket (ARI REST over websocket), so Asterisk needs no listening
// ARI port at all.
package ari

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// maxMessage bounds one websocket message: an event, or a REST response
// such as GET /channels at 100 calls.
const maxMessage = 4 << 20

// requestTimeout bounds one REST request over the websocket.
const requestTimeout = 10 * time.Second

// App is what runs on an Asterisk connection. Serve returns when the
// connection ends (ctx is cancelled then too).
type App interface {
	Serve(ctx context.Context, c *Conn)
}

// Handler accepts Asterisk's outbound ARI websocket. Only one connection is
// served at a time: a new one (Asterisk reconnecting) replaces the old.
type Handler struct {
	User     string
	Password []byte
	App      App
	Log      *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	user, pass, ok := r.BasicAuth()
	if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(h.User)) != 1 ||
		subtle.ConstantTimeCompare([]byte(pass), h.Password) != 1 {
		h.Log.Warn("ARI connection refused: wrong credentials", "remote", r.RemoteAddr)
		w.Header().Set("WWW-Authenticate", `Basic realm="linx-ari"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"ari"}})
	if err != nil {
		h.Log.Warn("ARI websocket upgrade failed", "remote", r.RemoteAddr, "err", err)
		return
	}
	if ws.Subprotocol() != "ari" {
		ws.Close(websocket.StatusPolicyViolation, "the ari subprotocol is required")
		return
	}
	ws.SetReadLimit(maxMessage)

	// Replace any connection still being served, and wait for it to wind
	// down so the app never runs twice at once.
	h.mu.Lock()
	if h.cancel != nil {
		h.cancel()
		<-h.done
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.cancel, h.done = cancel, done
	h.mu.Unlock()

	defer func() {
		cancel()
		close(done)
		h.mu.Lock()
		if h.done == done {
			h.cancel, h.done = nil, nil
		}
		h.mu.Unlock()
	}()

	c := newConn(ws)
	h.Log.Info("Asterisk connected", "remote", r.RemoteAddr)
	appDone := make(chan struct{})
	go func() {
		defer close(appDone)
		h.App.Serve(ctx, c)
	}()
	err = c.readLoop(ctx)
	cancel()
	<-appDone
	ws.Close(websocket.StatusNormalClosure, "")
	h.Log.Info("Asterisk disconnected", "err", err)
}

// Close ends the connection being served, if any (for shutdown).
func (h *Handler) Close() {
	h.mu.Lock()
	cancel, done := h.cancel, h.done
	h.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// Conn is one Asterisk connection.
type Conn struct {
	ws     *websocket.Conn
	events chan Event
	nextID atomic.Uint64

	mu      sync.Mutex
	pending map[string]chan restResponse
}

func newConn(ws *websocket.Conn) *Conn {
	return &Conn{ws: ws, events: make(chan Event, 1024), pending: map[string]chan restResponse{}}
}

// Events delivers every event Asterisk sends, in order, and is closed when
// the connection ends.
func (c *Conn) Events() <-chan Event { return c.events }

type restRequest struct {
	Type          string `json:"type"`
	TransactionID string `json:"transaction_id"`
	RequestID     string `json:"request_id"`
	Method        string `json:"method"`
	URI           string `json:"uri"`
}

type restResponse struct {
	Type        string `json:"type"`
	RequestID   string `json:"request_id"`
	StatusCode  int    `json:"status_code"`
	Reason      string `json:"reason_phrase"`
	MessageBody string `json:"message_body"`
}

// Get performs a read-only REST request (e.g. "channels") over the
// websocket and decodes the JSON body into out.
func (c *Conn) Get(ctx context.Context, uri string, out any) error {
	id := fmt.Sprintf("linx-%d", c.nextID.Add(1))
	ch := make(chan restResponse, 1)
	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return errors.New("ARI connection closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg, err := json.Marshal(restRequest{Type: "RESTRequest", TransactionID: id, RequestID: id, Method: http.MethodGet, URI: uri})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := c.ws.Write(ctx, websocket.MessageText, msg); err != nil {
		return fmt.Errorf("ARI GET %s: %w", uri, err)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("ARI GET %s: %w", uri, ctx.Err())
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("ARI GET %s: connection closed", uri)
		}
		if resp.StatusCode/100 != 2 {
			return fmt.Errorf("ARI GET %s: %d %s", uri, resp.StatusCode, resp.Reason)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal([]byte(resp.MessageBody), out)
	}
}

// readLoop reads until the connection or ctx ends: REST responses go to
// their waiting Get, everything else to Events.
func (c *Conn) readLoop(ctx context.Context) error {
	defer func() {
		close(c.events)
		c.mu.Lock()
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = nil
		c.mu.Unlock()
	}()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			return err
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &head); err != nil {
			continue
		}
		if head.Type == "RESTResponse" {
			var resp restResponse
			if err := json.Unmarshal(data, &resp); err != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[resp.RequestID]
			c.mu.Unlock()
			if ch != nil {
				ch <- resp
			}
			continue
		}
		var ev Event
		if err := json.Unmarshal(data, &ev); err != nil {
			continue
		}
		select {
		case c.events <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
