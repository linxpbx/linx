package install

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"linxpbx.com/linx/internal/webapp"
)

// CookieName is the claimed browser's session cookie: HttpOnly,
// SameSite=Strict and Secure (port 6464 is HTTPS, docs/INSTALL.md §14
// item 1), bound to the address the link was opened at.
const CookieName = "linx_install"

// SecureCookieName is the session's cookie on https://<domain>, once
// the handoff is used.
const SecureCookieName = "__Host-linx_install"

// Request timeouts on the bridge.
const (
	claimTimeout = 10 * time.Second
	checkTimeout = 30 * time.Second
)

// Errors the page turns into plain words.
var (
	ErrNotConnected = errors.New("setup on the server isn't connected")
)

// Server is the control plane's side in install mode: the pages on port
// 6464 and the host's bridge. It holds what the host last told it (View)
// and nothing else; every decision that matters is the host's.
type Server struct {
	// Web is the built web client (webapp.DefaultDir).
	Web fs.FS
	Log *slog.Logger
	Now func() time.Time
	// Unknown limits requests that don't carry the session (a wrong link,
	// anything else) per address, so the port can't be used to hammer the
	// host through the bridge. Defaults to 20 at once, then one every 3 s.
	Unknown rate.Limit
	Burst   int

	mu       sync.Mutex
	view     View
	haveView bool
	conn     *bridgeConn
	limiters map[netip.Addr]*limiterEntry
}

type limiterEntry struct {
	l    *rate.Limiter
	seen time.Time
}

type bridgeConn struct {
	c       net.Conn
	wmu     sync.Mutex
	pending map[string]chan Message
}

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Listen opens the bridge socket at path (replacing a stale one), readable
// and writable only by this process's user.
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

// ServeBridge accepts the host's bridge until ctx ends. A new connection
// replaces the one before it (setup reconnected; there's only one).
func (s *Server) ServeBridge(ctx context.Context, ln net.Listener) {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				s.log().Error("install bridge socket", "err", err)
			}
			return
		}
		go s.handleBridge(c)
	}
}

func (s *Server) handleBridge(c net.Conn) {
	bc := &bridgeConn{c: c, pending: map[string]chan Message{}}
	s.mu.Lock()
	old := s.conn
	s.conn = bc
	s.mu.Unlock()
	if old != nil {
		old.c.Close()
	}
	s.log().Info("setup connected")

	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var m Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		switch m.Type {
		case TypeView:
			if m.View != nil {
				s.mu.Lock()
				s.view, s.haveView = *m.View, true
				s.mu.Unlock()
			}
		case TypeResult:
			s.mu.Lock()
			ch := bc.pending[m.ID]
			delete(bc.pending, m.ID)
			s.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	c.Close()
	s.mu.Lock()
	if s.conn == bc {
		s.conn = nil
		s.log().Warn("setup disconnected")
	}
	for id, ch := range bc.pending {
		close(ch)
		delete(bc.pending, id)
	}
	s.mu.Unlock()
}

// send writes m to the host without waiting for an answer.
func (s *Server) send(m Message) error {
	s.mu.Lock()
	bc := s.conn
	s.mu.Unlock()
	if bc == nil {
		return ErrNotConnected
	}
	return bc.write(m)
}

func (bc *bridgeConn) write(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	bc.wmu.Lock()
	defer bc.wmu.Unlock()
	_ = bc.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := bc.c.Write(append(b, '\n')); err != nil {
		return ErrNotConnected
	}
	return nil
}

// request sends m to the host and waits for its result.
func (s *Server) request(ctx context.Context, m Message, timeout time.Duration) (Message, error) {
	s.mu.Lock()
	bc := s.conn
	if bc == nil {
		s.mu.Unlock()
		return Message{}, ErrNotConnected
	}
	m.ID = NewSecret()
	ch := make(chan Message, 1)
	bc.pending[m.ID] = ch
	s.mu.Unlock()
	forget := func() {
		s.mu.Lock()
		delete(bc.pending, m.ID)
		s.mu.Unlock()
	}
	if err := bc.write(m); err != nil {
		forget()
		return Message{}, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case r, ok := <-ch:
		if !ok {
			return Message{}, ErrNotConnected
		}
		return r, nil
	case <-t.C:
		forget()
		return Message{}, fmt.Errorf("setup on the server didn't answer in %s", timeout)
	case <-ctx.Done():
		forget()
		return Message{}, ctx.Err()
	}
}

// Snapshot is the view the host last sent, and whether it's connected.
func (s *Server) Snapshot() (View, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.view, s.conn != nil
}

// Handler serves port 6464. Without the claimed session, the only thing it
// answers is the link itself: every other path, a wrong or used link and an
// expired one all get the same "can't be used" page (docs/INSTALL.md §6).
func (s *Server) Handler() http.Handler {
	web := webapp.Handler(s.Web)
	return webapp.PlainHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.session(r, false) {
			if !s.allow(r) {
				w.Header().Set("Retry-After", "10")
				http.Error(w, "Too many requests. Wait a moment and try again.", http.StatusTooManyRequests)
				return
			}
			if secret, ok := strings.CutPrefix(r.URL.Path, "/install/"); ok && r.Method == http.MethodGet && ValidSecret(secret) {
				s.claim(w, r, secret)
				return
			}
			notFound(w)
			return
		}
		switch p := r.URL.Path; {
		case p == "/install/api/state" && r.Method == http.MethodGet:
			s.getState(w, false)
		case p == "/install/api/draft" && r.Method == http.MethodPut:
			s.putDraft(w, r)
		case p == "/install/api/check" && r.Method == http.MethodPost:
			s.check(w, r)
		case p == "/install/api/door-ready" && r.Method == http.MethodPost:
			s.simple(w, r, TypeDoorReady)
		case p == "/install/api/retry" && r.Method == http.MethodPost:
			s.simple(w, r, TypeRetry)
		case p == "/install/api/token" && r.Method == http.MethodPost:
			s.token(w, r)
		case p == "/install/api/handoff" && r.Method == http.MethodPost:
			s.handoff(w, r)
		case strings.HasPrefix(p, "/install/api/"):
			notFound(w)
		case p == "/" || strings.HasPrefix(p, "/install/"):
			// The link again, or the address without it: the page.
			http.Redirect(w, r, "/install", http.StatusSeeOther)
		case p == "/install" || path.Ext(p) != "":
			// The page, and the build's own files (scripts, fonts, icons).
			web.ServeHTTP(w, r)
		default:
			notFound(w)
		}
	}))
}

// session reports whether r carries the claimed browser's cookie for a
// link that's still open: the plain page's until the handoff is used, the
// secure page's after.
func (s *Server) session(r *http.Request, secure bool) bool {
	name := CookieName
	if secure {
		name = SecureCookieName
	}
	c, err := r.Cookie(name)
	if err != nil {
		return false
	}
	s.mu.Lock()
	v, ok := s.view, s.haveView
	s.mu.Unlock()
	return ok && v.Ended == "" && v.SessionHash != "" && v.Secure == secure && s.now().Before(v.ExpiresAt) && Matches(c.Value, v.SessionHash)
}

// allow rate-limits requests without a session, per address.
func (s *Server) allow(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	a := ap.Addr().Unmap()
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limiters == nil {
		s.limiters = map[netip.Addr]*limiterEntry{}
	}
	e := s.limiters[a]
	if e == nil {
		if len(s.limiters) > 10000 {
			for k, v := range s.limiters {
				if now.Sub(v.seen) > 10*time.Minute {
					delete(s.limiters, k)
				}
			}
		}
		limit, burst := s.Unknown, s.Burst
		if limit == 0 {
			limit = rate.Every(3 * time.Second)
		}
		if burst == 0 {
			burst = 20
		}
		e = &limiterEntry{l: rate.NewLimiter(limit, burst)}
		s.limiters[a] = e
	}
	e.seen = now
	return e.l.AllowN(now, 1)
}

// claim swaps the link's secret for a session cookie, if the host agrees
// the link is right and still unclaimed, and takes the secret out of the
// address bar.
func (s *Server) claim(w http.ResponseWriter, r *http.Request, secret string) {
	s.mu.Lock()
	v, ok := s.view, s.haveView
	connected := s.conn != nil
	s.mu.Unlock()
	if !ok {
		// Setup on the host hasn't said anything yet: nothing to check
		// the link against, so it isn't wrong either.
		starting(w)
		return
	}
	if v.Ended != "" || !s.now().Before(v.ExpiresAt) || !Matches(secret, v.LinkHash) {
		notFound(w)
		return
	}
	if !connected {
		starting(w)
		return
	}
	token := NewSecret()
	address := ""
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		address = ap.Addr().Unmap().String()
	}
	res, err := s.request(r.Context(), Message{
		Type: TypeClaim, Secret: secret, SessionHash: Hash(token),
		Browser: BrowserName(r.UserAgent()), Address: address,
	}, claimTimeout)
	if err != nil {
		s.log().Warn("claiming the install link", "err", err)
		starting(w)
		return
	}
	if !res.OK {
		notFound(w)
		return
	}
	s.mu.Lock()
	// The host sends its new view right after its result; set what it
	// agreed to now so the redirect below already finds the session.
	if s.view.SessionHash == "" {
		s.view.LinkHash, s.view.SessionHash, s.view.ClaimedAt = "", Hash(token), s.now().UTC()
	}
	expires := s.view.ExpiresAt
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: CookieName, Value: token, Path: "/", Secure: r.TLS != nil, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/install", http.StatusSeeOther)
}

// pageState is GET /install/api/state: what the page shows.
type pageState struct {
	Facts     Facts           `json:"facts"`
	Draft     json.RawMessage `json:"draft,omitempty"`
	Accepted  *Answers        `json:"accepted,omitempty"`
	ExpiresAt time.Time       `json:"expires_at"`
	// ExpiresIn is the seconds left, by this server's clock: the page
	// counts down from it, so a wrong clock on the visitor's computer
	// doesn't matter.
	ExpiresIn int       `json:"expires_in"`
	Connected bool      `json:"connected"`
	Cert      *CertView `json:"cert,omitempty"`
	// Secure: this is the secure page (https://<domain>).
	Secure bool `json:"secure,omitempty"`
	// Finish is the secure page's steps: only ever sent there.
	Finish *FinishView `json:"finish,omitempty"`
}

func (s *Server) getState(w http.ResponseWriter, secure bool) {
	v, connected := s.Snapshot()
	left := max(0, int(v.ExpiresAt.Sub(s.now()).Seconds()))
	ps := pageState{Facts: v.Facts, Draft: v.Draft, Accepted: v.Accepted, ExpiresAt: v.ExpiresAt,
		ExpiresIn: left, Connected: connected, Cert: v.Cert, Secure: v.Secure}
	if secure {
		ps.Finish = v.Finish
	}
	writeJSON(w, http.StatusOK, ps)
}

// extras is the secure page's §3.4 choices.
func (s *Server) extras(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeProblem(w, http.StatusForbidden, "This change didn't come from the install page.")
		return
	}
	var e Extras
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		writeProblem(w, http.StatusBadRequest, "The choices couldn't be read.")
		return
	}
	s.relay(w, r, Message{Type: TypeExtras, Extras: &e}, claimTimeout)
}

// sameOrigin is the check every change makes on top of the SameSite
// cookie: a JSON body, sent by this page.
func sameOrigin(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	scheme := "http://"
	if r.TLS != nil {
		scheme = "https://"
	}
	return (ct == "application/json" || strings.HasPrefix(ct, "application/json;")) &&
		r.Header.Get("Origin") == scheme+r.Host
}

func (s *Server) putDraft(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeProblem(w, http.StatusForbidden, "This change didn't come from the install page.")
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, MaxDraft+1))
	if err != nil || len(b) > MaxDraft || !json.Valid(b) || len(b) == 0 || b[0] != '{' {
		writeProblem(w, http.StatusBadRequest, "That draft can't be kept.")
		return
	}
	draft := json.RawMessage(b)
	s.mu.Lock()
	s.view.Draft = draft
	s.mu.Unlock()
	// Kept here at once; the host keeps it too, so it outlives a restart.
	_ = s.send(Message{Type: TypeDraft, Draft: draft})
	w.WriteHeader(http.StatusNoContent)
}

// checkResult is POST /install/api/check's answer.
type checkResult struct {
	OK     bool         `json:"ok"`
	Errors []FieldError `json:"errors,omitempty"`
}

func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeProblem(w, http.StatusForbidden, "This change didn't come from the install page.")
		return
	}
	var a Answers
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		writeProblem(w, http.StatusBadRequest, "The answers couldn't be read.")
		return
	}
	res, err := s.request(r.Context(), Message{Type: TypeCheck, Answers: &a}, checkTimeout)
	if err != nil {
		s.log().Warn("checking the install answers", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Setup on the server isn't answering. Check that sudo linx setup is still running, then try again.")
		return
	}
	if res.Error != "" {
		writeProblem(w, http.StatusServiceUnavailable, res.Error)
		return
	}
	if !res.OK {
		writeJSON(w, http.StatusUnprocessableEntity, checkResult{Errors: res.Errors})
		return
	}
	s.mu.Lock()
	s.view.Accepted = &a
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, checkResult{OK: true})
}

// simple sends a request with no body to the host (the certificate page's
// "I've done this" and "Try again").
func (s *Server) simple(w http.ResponseWriter, r *http.Request, typ string) {
	if !sameOrigin(r) {
		writeProblem(w, http.StatusForbidden, "This change didn't come from the install page.")
		return
	}
	s.relay(w, r, Message{Type: typ}, claimTimeout)
}

// relay sends m to the host and answers with its result.
func (s *Server) relay(w http.ResponseWriter, r *http.Request, m Message, timeout time.Duration) (Message, bool) {
	res, err := s.request(r.Context(), m, timeout)
	if err != nil {
		s.log().Warn("install request", "type", m.Type, "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "Setup on the server isn't answering. Check that sudo linx setup is still running, then try again.")
		return res, false
	}
	switch {
	case res.Error != "":
		writeProblem(w, http.StatusConflict, res.Error)
		return res, false
	case len(res.Errors) > 0:
		writeJSON(w, http.StatusUnprocessableEntity, checkResult{Errors: res.Errors})
		return res, false
	case !res.OK:
		writeProblem(w, http.StatusConflict, "Setup on the server refused that.")
		return res, false
	}
	if m.Type != TypeHandoff && m.Type != TypeRedeem {
		w.WriteHeader(http.StatusNoContent)
	}
	return res, true
}

// maxToken is the most a DNS token body may be.
const maxToken = 1 << 10

// token passes the DNS company's token to the host (docs/INSTALL.md §4.3:
// the page warned it isn't encrypted). It's never logged or kept here.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeProblem(w, http.StatusForbidden, "This change didn't come from the install page.")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxToken))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeProblem(w, http.StatusBadRequest, "The token couldn't be read.")
		return
	}
	s.relay(w, r, Message{Type: TypeToken, Token: body.Token}, checkTimeout)
}

// handoff is a new one-time link to the secure page, for this browser.
func (s *Server) handoff(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeProblem(w, http.StatusForbidden, "This change didn't come from the install page.")
		return
	}
	res, ok := s.relay(w, r, Message{Type: TypeHandoff}, claimTimeout)
	if !ok {
		return
	}
	if !ValidSecret(res.Secret) {
		writeProblem(w, http.StatusServiceUnavailable, "Setup on the server didn't make a link to the secure page. Try again.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"handoff": res.Secret})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "detail": detail})
}

// The only pages port 6464 shows without a session: self-contained, one
// style block allowed by its hash, no scripts. Its colours are the design
// tokens' (web/src/styles/tokens.css: bg, surface, border, text,
// text-muted, accent; light and dark), checked by the tests.
const pageStyle = `body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;` +
	`font-family:system-ui,sans-serif;background:#F4F3EF;color:#17191E;padding:16px;box-sizing:border-box}` +
	`main{max-width:24rem;width:100%;background:#FFFFFF;border:1px solid #E3E1DA;border-radius:12px;padding:24px}` +
	`h1{font-size:1.25rem;margin:0 0 12px}p{margin:0 0 12px;line-height:1.5;color:#5B5F68}` +
	`code{display:block;font-size:.95rem;padding:8px 12px;background:#F4F3EF;border-radius:6px;color:#17191E}` +
	`b{display:block;margin-bottom:16px;color:#1F5FD6;font-size:1.5rem}` +
	`@media (prefers-color-scheme:dark){body{background:#17191E;color:#F4F3EF}main{background:#22252C;border-color:#343842}` +
	`p{color:#A6AAB3}code{background:#17191E;color:#F4F3EF}b{color:#7FB0FF}}`

var pageCSP = "default-src 'none'; style-src 'sha256-" + styleHash() + "'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func styleHash() string {
	sum := sha256.Sum256([]byte(pageStyle))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func page(w http.ResponseWriter, status int, title, body string) {
	h := w.Header()
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">`+
		`<meta name="referrer" content="no-referrer"><title>%s · Linx</title><style>%s</style><main><b>Linx</b><h1>%s</h1>%s</main></html>`,
		title, pageStyle, title, body)
}

// notFound is the one answer for a wrong, used, expired or cancelled link,
// and for every other path: a guess learns nothing.
func notFound(w http.ResponseWriter) {
	page(w, http.StatusNotFound, "This link can't be used",
		`<p>Setup links work once, for four hours.</p><p>For a new one, run this on the server. Answers you already gave are kept:</p><code>sudo linx setup</code>`)
}

func starting(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	page(w, http.StatusServiceUnavailable, "Setup is starting",
		`<p>Setup on the server is still starting. Reload this page in a moment.</p>`)
}
