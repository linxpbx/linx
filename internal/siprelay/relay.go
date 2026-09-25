// Package siprelay is the control plane's /sip relay (ADR-038, docs/WEB.md
// §5): the only way SIP reaches Linx from the internet. A signed-in
// browser's websocket is relayed, message by message and unchanged, to
// Asterisk's internal secure websocket, after checks that keep everything
// else out:
//
//   - every request must come from the session's own web device (From, and
//     To for REGISTER, and any Authorization username); anything else closes
//     the connection and is audited;
//   - 3 failed authentications in a row close it;
//   - at most Rate messages a second (Burst at once), each at most
//     MaxMessage bytes;
//   - the session is checked again every CheckInterval, and CloseSession /
//     CloseUser drop it at once when it ends (signing out, the person
//     disabled, ...).
package siprelay

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"golang.org/x/time/rate"
)

// Line is who one relayed connection belongs to.
type Line struct {
	TenantID, UserID, SessionID uuid.UUID
	// SessionTokenHash is how the session is looked up again (Check).
	SessionTokenHash []byte
	// Username is the session's web device's SIP username: the only one
	// this connection may use.
	Username string
	IP       netip.Addr
}

// Defaults (docs/WEB.md §5).
const (
	DefaultCheckInterval   = 15 * time.Second
	DefaultMaxMessage      = 64 << 10
	DefaultRate            = 20
	DefaultBurst           = 40
	DefaultMaxAuthFailures = 3
)

// Subprotocol is the websocket subprotocol SIP uses (RFC 7118).
const Subprotocol = "sip"

// Close reasons the browser sees, and the audit log records (reason).
const (
	ReasonSignedOut    = "signed out"
	ReasonReplaced     = "opened again elsewhere"
	ReasonNotYourLine  = "not this browser's phone line"
	ReasonAuthFailures = "too many failed sign-ins"
	ReasonTooMany      = "too many messages"
	ReasonMalformed    = "not a SIP message"
	ReasonMethod       = "request not allowed"
)

// Relay relays browsers' SIP websockets to Asterisk. The zero value isn't
// usable: set Dial and Check.
type Relay struct {
	// Dial opens a websocket to Asterisk's browser transport, with the sip
	// subprotocol.
	Dial func(ctx context.Context) (*websocket.Conn, error)
	// Check reports whether line may carry on: its session still live and
	// its web device still current. Called every CheckInterval.
	Check func(ctx context.Context, line Line) error
	// Audit records a refused connection (action "sip.relay_closed").
	Audit func(ctx context.Context, line Line, reason string)
	Log   *slog.Logger

	CheckInterval   time.Duration
	MaxMessage      int64
	Rate            rate.Limit
	Burst           int
	MaxAuthFailures int

	mu    sync.Mutex
	conns map[uuid.UUID]*relayConn // by session
}

type relayConn struct {
	line   Line
	cancel context.CancelCauseFunc
}

// closeCause is why a connection ended: shown to the browser as the close
// reason, audited when refused says so.
type closeCause struct {
	reason  string
	refused bool
}

func (c *closeCause) Error() string { return c.reason }

func (r *Relay) defaults() {
	if r.CheckInterval == 0 {
		r.CheckInterval = DefaultCheckInterval
	}
	if r.MaxMessage == 0 {
		r.MaxMessage = DefaultMaxMessage
	}
	if r.Rate == 0 {
		r.Rate = DefaultRate
	}
	if r.Burst == 0 {
		r.Burst = DefaultBurst
	}
	if r.MaxAuthFailures == 0 {
		r.MaxAuthFailures = DefaultMaxAuthFailures
	}
	if r.Log == nil {
		r.Log = slog.New(slog.DiscardHandler)
	}
}

// Serve relays one browser websocket for line, whose session and web device
// the caller has already checked (and Origin: see the control plane's
// /sip handler). It returns when the connection ends.
func (r *Relay) Serve(w http.ResponseWriter, req *http.Request, line Line) {
	r.mu.Lock()
	r.defaults()
	r.mu.Unlock()

	dialCtx, cancelDial := context.WithTimeout(req.Context(), 10*time.Second)
	up, err := r.Dial(dialCtx)
	cancelDial()
	if err != nil {
		r.Log.Error("sip relay: can't reach the phone system", "err", err)
		http.Error(w, "The phone system isn't reachable right now.", http.StatusBadGateway)
		return
	}
	defer up.CloseNow()
	// Origin is checked by the caller against this server's own address;
	// coder/websocket's own check (Origin host = Host) agrees with it.
	down, err := websocket.Accept(w, req, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		up.Close(websocket.StatusGoingAway, "")
		return // Accept has already answered
	}
	defer down.CloseNow()
	if down.Subprotocol() != Subprotocol {
		down.Close(websocket.StatusPolicyViolation, "use the sip subprotocol")
		up.Close(websocket.StatusGoingAway, "")
		return
	}
	down.SetReadLimit(r.MaxMessage)
	up.SetReadLimit(r.MaxMessage)

	ctx, cancel := context.WithCancelCause(req.Context())
	defer cancel(nil)
	rc := &relayConn{line: line, cancel: cancel}
	r.add(rc)
	defer r.remove(rc)

	s := &session{relay: r, line: line, up: up, down: down, limiter: rate.NewLimiter(r.Rate, r.Burst)}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); cancel(s.fromBrowser(ctx)) }()
	go func() { defer wg.Done(); cancel(s.fromAsterisk(ctx)) }()
	go func() { defer wg.Done(); s.recheck(ctx, cancel) }()
	<-ctx.Done()

	cause := context.Cause(ctx)
	var cc *closeCause
	switch {
	case errors.As(cause, &cc):
		if cc.refused && r.Audit != nil {
			r.Audit(context.WithoutCancel(req.Context()), line, cc.reason)
		}
		r.Log.Info("sip relay closed", "user", line.UserID, "reason", cc.reason)
		down.Close(websocket.StatusPolicyViolation, cc.reason)
		up.Close(websocket.StatusNormalClosure, "")
	case errors.Is(cause, errAsteriskClosed):
		down.Close(websocket.StatusGoingAway, "the phone system closed the connection")
	default:
		up.Close(websocket.StatusNormalClosure, "")
		down.CloseNow()
	}
	up.CloseNow()
	down.CloseNow()
	wg.Wait()
}

var errAsteriskClosed = errors.New("asterisk closed the connection")

// add registers c, closing any other connection of the same session: a
// session has one phone line, so one connection.
func (r *Relay) add(c *relayConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conns == nil {
		r.conns = map[uuid.UUID]*relayConn{}
	}
	if old := r.conns[c.line.SessionID]; old != nil {
		old.cancel(&closeCause{reason: ReasonReplaced})
	}
	r.conns[c.line.SessionID] = c
}

func (r *Relay) remove(c *relayConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Only if it's still this connection (a newer one may have replaced it).
	if r.conns[c.line.SessionID] == c {
		delete(r.conns, c.line.SessionID)
	}
}

// CloseSession drops the session's connection at once, if it has one.
func (r *Relay) CloseSession(session uuid.UUID) {
	r.closeWhere(func(l Line) bool { return l.SessionID == session })
}

// CloseUser drops every connection of the person's.
func (r *Relay) CloseUser(user uuid.UUID) {
	r.closeWhere(func(l Line) bool { return l.UserID == user })
}

// CloseUsername drops the connection using this web device.
func (r *Relay) CloseUsername(username string) {
	r.closeWhere(func(l Line) bool { return l.Username == username })
}

func (r *Relay) closeWhere(match func(Line) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.conns {
		if match(c.line) {
			c.cancel(&closeCause{reason: ReasonSignedOut})
		}
	}
}

// Connections is how many browsers are connected right now.
func (r *Relay) Connections() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.conns)
}

// session is one relayed connection's state.
type session struct {
	relay    *Relay
	line     Line
	up, down *websocket.Conn
	limiter  *rate.Limiter

	mu sync.Mutex
	// credentialed are requests the browser sent with credentials, by
	// Call-ID and CSeq, waiting for Asterisk's answer; failures counts
	// consecutive rejected ones.
	credentialed []string
	failures     int
}

const maxCredentialed = 32

func refused(reason string) error { return &closeCause{reason: reason, refused: true} }

// Reads and writes don't use the connection's context: coder/websocket
// drops a connection outright when a read or write's context ends, and Serve
// needs it open to send the browser its close reason. Serve closes both
// connections when it's done, which ends any read in progress.

func (s *session) fromBrowser(ctx context.Context) error {
	for {
		typ, b, err := s.down.Read(context.Background())
		if err != nil {
			if errors.Is(err, websocket.ErrMessageTooBig) {
				return refused(ReasonTooMany)
			}
			return err
		}
		if !s.limiter.Allow() {
			return refused(ReasonTooMany)
		}
		m, err := parseMessage(b)
		if err != nil {
			return refused(ReasonMalformed)
		}
		if m.keepAlive {
			continue
		}
		if m.request {
			if reason := s.checkRequest(m); reason != "" {
				return refused(reason)
			}
		}
		if err := write(s.up, typ, b); err != nil {
			return err
		}
	}
}

// checkRequest returns why the browser may not send m, or "".
func (s *session) checkRequest(m message) string {
	if !allowedMethods[m.method] {
		return ReasonMethod
	}
	if m.fromCount != 1 || m.fromUser != s.line.Username {
		return ReasonNotYourLine
	}
	if m.method == "REGISTER" && (m.toCount != 1 || m.toUser != s.line.Username) {
		return ReasonNotYourLine
	}
	for _, u := range m.authUsers {
		if u != s.line.Username {
			return ReasonNotYourLine
		}
	}
	if len(m.authUsers) > 0 {
		s.mu.Lock()
		s.credentialed = append(s.credentialed, m.callID+"\x00"+m.cseq)
		if len(s.credentialed) > maxCredentialed {
			s.credentialed = s.credentialed[1:]
		}
		s.mu.Unlock()
	}
	return ""
}

func (s *session) fromAsterisk(ctx context.Context) error {
	for {
		typ, b, err := s.up.Read(context.Background())
		if err != nil {
			if ctx.Err() == nil {
				return errAsteriskClosed
			}
			return err
		}
		if m, err := parseMessage(b); err == nil && !m.request && !m.keepAlive {
			if s.answered(m) >= s.relay.MaxAuthFailures {
				return refused(ReasonAuthFailures)
			}
		}
		if err := write(s.down, typ, b); err != nil {
			return err
		}
	}
}

func write(c *websocket.Conn, typ websocket.MessageType, b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.Write(ctx, typ, b)
}

// answered records Asterisk's final answer to a request the browser sent
// with credentials, and returns the consecutive failures so far. A
// challenge marked stale (an expired nonce: the browser answers it again
// with the same password) isn't a failure.
func (s *session) answered(m message) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.status < 200 {
		return s.failures
	}
	key := m.callID + "\x00" + m.cseq
	for i, k := range s.credentialed {
		if k != key {
			continue
		}
		s.credentialed = append(s.credentialed[:i], s.credentialed[i+1:]...)
		switch {
		case (m.status == 401 || m.status == 407) && !m.staleChallenge:
			s.failures++
		case m.status < 300:
			s.failures = 0
		}
		break
	}
	return s.failures
}

// recheck checks the session every CheckInterval until ctx ends.
func (s *session) recheck(ctx context.Context, cancel context.CancelCauseFunc) {
	t := time.NewTicker(s.relay.CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.relay.Check(ctx, s.line); err != nil {
				if ctx.Err() == nil {
					s.relay.Log.Info("sip relay: session ended", "user", s.line.UserID, "err", err)
				}
				cancel(&closeCause{reason: ReasonSignedOut})
				return
			}
		}
	}
}
