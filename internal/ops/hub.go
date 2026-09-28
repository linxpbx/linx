package ops

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// DefaultSocket is where the control plane listens for linx-ops-agent
// (through ops-bridge), in a tmpfs only its own user can open
// (deploy/compose/compose.yaml).
const DefaultSocket = "/run/linx/ops.sock"

// Errors the API turns into plain answers.
var (
	ErrNotConnected   = errors.New("the server helper isn't connected")
	ErrUnknownService = errors.New("not a Linx service")
	ErrNotAllowed     = errors.New("this service can't be restarted from the browser")
	ErrTooSoon        = errors.New("it was restarted moments ago")
	ErrBusy           = errors.New("the server helper is busy")
)

// Request timeouts: a log is quick; a restart waits for docker restart,
// which gives a service 20 s to stop and then starts it again.
const (
	logsTimeout    = 15 * time.Second
	restartTimeout = 90 * time.Second
	statusTimeout  = 15 * time.Second
)

// Hub is the control plane's side of the link: it keeps the newest
// agent connection and the state it last reported, and sends it requests.
type Hub struct {
	Log *slog.Logger

	mu        sync.Mutex
	conn      *hubConn
	status    []Container
	checkedAt time.Time
}

type hubConn struct {
	c       net.Conn
	wmu     sync.Mutex
	pending map[string]chan Message
}

// Snapshot is what System → Status shows.
type Snapshot struct {
	Connected  bool
	CheckedAt  time.Time
	Containers []Container
}

func (h *Hub) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// Listen opens the socket at path (replacing a stale one from a previous
// run), readable and writable only by this process's user.
func Listen(path string) (net.Listener, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve accepts agent connections until ctx ends. A new connection
// replaces the one before it (an agent that reconnected; there's only
// ever one agent).
func (h *Hub) Serve(ctx context.Context, ln net.Listener) {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				h.log().Error("server helper socket", "err", err)
			}
			return
		}
		go h.handle(c)
	}
}

func (h *Hub) handle(c net.Conn) {
	hc := &hubConn{c: c, pending: map[string]chan Message{}}
	h.mu.Lock()
	old := h.conn
	h.conn = hc
	h.mu.Unlock()
	if old != nil {
		old.c.Close()
	}
	h.log().Info("server helper connected")

	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		switch m.Type {
		case TypeStatus:
			h.mu.Lock()
			h.status, h.checkedAt = keepKnown(m.Containers), m.CheckedAt
			h.mu.Unlock()
		case TypeReply:
			h.mu.Lock()
			ch := hc.pending[m.ID]
			delete(hc.pending, m.ID)
			h.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	c.Close()
	h.mu.Lock()
	if h.conn == hc {
		h.conn = nil
		h.log().Warn("server helper disconnected")
	}
	for id, ch := range hc.pending {
		close(ch)
		delete(hc.pending, id)
	}
	h.mu.Unlock()
}

// keepKnown drops anything reported for a service not in Services.
func keepKnown(cs []Container) []Container {
	out := make([]Container, 0, len(cs))
	for _, c := range cs {
		if _, ok := Lookup(c.Service); ok {
			out = append(out, c)
		}
	}
	return out
}

// Snapshot is the state the agent last reported.
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Snapshot{Connected: h.conn != nil, CheckedAt: h.checkedAt, Containers: append([]Container(nil), h.status...)}
}

// Refresh asks the agent to check every container now ("Check now").
func (h *Hub) Refresh(ctx context.Context) error {
	_, err := h.request(ctx, Message{Op: OpStatus}, statusTimeout)
	return err
}

// Logs reads a service's last lines (ClampLines).
func (h *Hub) Logs(ctx context.Context, service string, lines int) ([]LogLine, error) {
	if _, ok := Lookup(service); !ok {
		return nil, ErrUnknownService
	}
	m, err := h.request(ctx, Message{Op: OpLogs, Service: service, Lines: ClampLines(lines)}, logsTimeout)
	if err != nil {
		return nil, err
	}
	return m.Log, nil
}

// Restart restarts a service, returning "restarted" once it's back, or
// "restarting" for the control plane itself.
func (h *Hub) Restart(ctx context.Context, service string) (string, error) {
	svc, ok := Lookup(service)
	if !ok {
		return "", ErrUnknownService
	}
	if !svc.Restart {
		return "", ErrNotAllowed
	}
	m, err := h.request(ctx, Message{Op: OpRestart, Service: service}, restartTimeout)
	if err != nil {
		return "", err
	}
	return m.Result, nil
}

func (h *Hub) request(ctx context.Context, m Message, timeout time.Duration) (Message, error) {
	h.mu.Lock()
	hc := h.conn
	if hc == nil {
		h.mu.Unlock()
		return Message{}, ErrNotConnected
	}
	m.Type, m.ID = TypeRequest, rand.Text()
	ch := make(chan Message, 1)
	hc.pending[m.ID] = ch
	h.mu.Unlock()

	forget := func() {
		h.mu.Lock()
		delete(hc.pending, m.ID)
		h.mu.Unlock()
	}
	b, err := json.Marshal(m)
	if err != nil {
		forget()
		return Message{}, err
	}
	hc.wmu.Lock()
	_ = hc.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = hc.c.Write(append(b, '\n'))
	hc.wmu.Unlock()
	if err != nil {
		forget()
		return Message{}, ErrNotConnected
	}

	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			return Message{}, ErrNotConnected
		}
		if r.OK {
			return r, nil
		}
		return r, replyError(r)
	case <-t.C:
		forget()
		return Message{}, fmt.Errorf("the server helper didn't answer in %s", timeout)
	case <-ctx.Done():
		forget()
		return Message{}, ctx.Err()
	}
}

func replyError(r Message) error {
	switch r.Code {
	case CodeUnknownService:
		return ErrUnknownService
	case CodeNotAllowed:
		return ErrNotAllowed
	case CodeTooSoon:
		return ErrTooSoon
	case CodeBusy:
		return ErrBusy
	}
	if r.Error == "" {
		return errors.New("the server helper couldn't do it")
	}
	return errors.New(r.Error)
}
