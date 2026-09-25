package siprelay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

const me = "d_Mine1234"

func TestParseMessage(t *testing.T) {
	m, err := parseMessage([]byte("REGISTER sip:linx SIP/2.0\r\n" +
		"Via: SIP/2.0/WSS x.invalid;branch=z9hG4bK1\r\n" +
		"f: \"A <b> @c\" <sip:d_Mine1234@linx;transport=ws>;tag=1\r\n" +
		"To: sip:d_Mine1234@linx\r\n" +
		"i: abc@x\r\n" +
		"CSeq:  2   REGISTER\r\n" +
		"Authorization: Digest username=\"d_Mine1234\", realm=\"linxpbx\",\r\n nonce=\"n\", uri=\"sip:linx\"\r\n" +
		"Content-Length: 0\r\n\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.request || m.method != "REGISTER" || m.fromUser != me || m.toUser != me || m.fromCount != 1 ||
		m.callID != "abc@x" || m.cseq != "2 REGISTER" || len(m.authUsers) != 1 || m.authUsers[0] != me {
		t.Errorf("%+v", m)
	}

	m, err = parseMessage([]byte("SIP/2.0 401 Unauthorized\r\nCall-ID: abc@x\r\nCSeq: 2 REGISTER\r\n" +
		"WWW-Authenticate: Digest realm=\"linxpbx\", nonce=\"x,y\", stale=TRUE, algorithm=MD5\r\n\r\n"))
	if err != nil || m.request || m.status != 401 || !m.staleChallenge {
		t.Errorf("%+v %v", m, err)
	}

	if m, err := parseMessage([]byte("\r\n\r\n")); err != nil || !m.keepAlive {
		t.Errorf("keep-alive: %+v %v", m, err)
	}
	for _, bad := range []string{"hello", "INVITE sip:x\r\n\r\n", "SIP/2.0 99 x\r\n\r\n", "INVITE sip:x SIP/2.0\r\nNoColon\r\n\r\n"} {
		if _, err := parseMessage([]byte(bad)); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestURIUser(t *testing.T) {
	for in, want := range map[string]string{
		`<sip:d_x@h>;tag=1`:                  "d_x",
		`sip:d_x@h;tag=1`:                    "d_x",
		`SIPS:d_x@h`:                         "d_x",
		`"Bob \"<sip:evil@h>\"" <sip:d_x@h>`: "d_x",
		`"x" <sip:d%5Fx@h>`:                  "d%5Fx",
		`<sip:h>`:                            "",
		`<tel:+1555>`:                        "",
		`"unterminated <sip:d_x@h>`:          "",
		`<sip:d_x@h`:                         "",
	} {
		if got := uriUser(in); got != want {
			t.Errorf("uriUser(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeAsterisk is Asterisk's websocket: it records what it receives and
// answers each request with whatever reply returns.
type fakeAsterisk struct {
	srv   *httptest.Server
	mu    sync.Mutex
	got   []string
	reply func(req string) []string
	conns chan *websocket.Conn
}

func newFakeAsterisk(t *testing.T) *fakeAsterisk {
	f := &fakeAsterisk{conns: make(chan *websocket.Conn, 4), reply: func(string) []string { return nil }}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"sip"}})
		if err != nil {
			return
		}
		f.conns <- c
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			f.mu.Lock()
			f.got = append(f.got, string(b))
			reply := f.reply
			f.mu.Unlock()
			for _, resp := range reply(string(b)) {
				c.Write(context.Background(), websocket.MessageText, []byte(resp))
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAsterisk) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.got...)
}

type harness struct {
	t       *testing.T
	relay   *Relay
	ast     *fakeAsterisk
	srv     *httptest.Server
	line    Line
	audits  chan string
	checkOK atomic.Bool
}

func newHarness(t *testing.T, tweak func(*Relay)) *harness {
	h := &harness{t: t, ast: newFakeAsterisk(t), audits: make(chan string, 8),
		line: Line{TenantID: uuid.New(), UserID: uuid.New(), SessionID: uuid.New(), Username: me}}
	h.checkOK.Store(true)
	h.relay = &Relay{
		Dial: func(ctx context.Context) (*websocket.Conn, error) {
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.ast.srv.URL, "http"), &websocket.DialOptions{Subprotocols: []string{"sip"}})
			return c, err
		},
		Check: func(context.Context, Line) error {
			if !h.checkOK.Load() {
				return errors.New("session revoked")
			}
			return nil
		},
		Audit: func(_ context.Context, l Line, reason string) { h.audits <- reason },
		// Generous, so only tests that mean to hit them do.
		Rate: 1000, Burst: 1000, CheckInterval: time.Hour,
	}
	if tweak != nil {
		tweak(h.relay)
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.relay.Serve(w, r, h.line) }))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *harness) dial() *websocket.Conn {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http"), &websocket.DialOptions{Subprotocols: []string{"sip"}})
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, msg string) {
	t.Helper()
	if err := c.Write(context.Background(), websocket.MessageText, []byte(msg)); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, c *websocket.Conn) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	return string(b), err
}

// wantClosed reads until the relay closes the connection and checks why.
func wantClosed(t *testing.T, c *websocket.Conn, code websocket.StatusCode, reason string) {
	t.Helper()
	for {
		_, err := read(t, c)
		if err == nil {
			continue
		}
		var ce websocket.CloseError
		if !errors.As(err, &ce) || ce.Code != code || ce.Reason != reason {
			t.Fatalf("closed with %v, want %d %q", err, code, reason)
		}
		return
	}
}

var cseqN atomic.Int64

func request(method, from, to string, extra ...string) string {
	n := cseqN.Add(1)
	return fmt.Sprintf("%s sip:linx SIP/2.0\r\nVia: SIP/2.0/WSS x.invalid;branch=z9hG4bK%d\r\nFrom: <sip:%s@linx>;tag=a\r\n"+
		"To: <sip:%s@linx>\r\nCall-ID: call-%d\r\nCSeq: %d %s\r\n%sContent-Length: 0\r\n\r\n",
		method, n, from, to, n, n, method, strings.Join(extra, ""))
}

func authz(user string) string {
	return `Authorization: Digest username="` + user + `", realm="linxpbx", nonce="n"` + "\r\n"
}

// replyTo answers a request with status (and the challenge header, if any).
func replyTo(req string, status int, extra string) string {
	var hdrs []string
	for _, l := range strings.Split(req, "\r\n") {
		for _, p := range []string{"Via:", "From:", "To:", "Call-ID:", "CSeq:"} {
			if strings.HasPrefix(l, p) {
				hdrs = append(hdrs, l)
			}
		}
	}
	return fmt.Sprintf("SIP/2.0 %d X\r\n%s\r\n%sContent-Length: 0\r\n\r\n", status, strings.Join(hdrs, "\r\n"), extra)
}

func TestRelaysOwnLine(t *testing.T) {
	h := newHarness(t, nil)
	// Answers requests only (a real Asterisk doesn't answer responses).
	h.ast.reply = func(req string) []string {
		if strings.HasPrefix(req, "SIP/2.0 ") {
			return nil
		}
		return []string{replyTo(req, 200, "")}
	}
	c := h.dial()
	reg := request("REGISTER", me, me, authz(me))
	send(t, c, reg)
	resp, err := read(t, c)
	if err != nil || !strings.HasPrefix(resp, "SIP/2.0 200") {
		t.Fatalf("%q %v", resp, err)
	}
	// Keep-alives aren't forwarded; responses to Asterisk's requests are.
	send(t, c, "\r\n\r\n")
	send(t, c, "SIP/2.0 200 OK\r\nFrom: <sip:asterisk@linx>;tag=x\r\nTo: <sip:someone@x>\r\nCall-ID: q\r\nCSeq: 1 OPTIONS\r\n\r\n")
	send(t, c, request("INVITE", me, "101"))
	if resp, err := read(t, c); err != nil || !strings.Contains(resp, " INVITE") {
		t.Fatalf("%q %v", resp, err)
	}
	got := h.ast.received()
	if len(got) != 3 || got[0] != reg || !strings.HasPrefix(got[1], "SIP/2.0 200") || !strings.HasPrefix(got[2], "INVITE ") {
		t.Errorf("Asterisk received %d messages: %q", len(got), got)
	}
	if h.relay.Connections() != 1 {
		t.Errorf("%d connections", h.relay.Connections())
	}
}

func TestRefusesOtherLines(t *testing.T) {
	for name, msg := range map[string]string{
		"From another device":           request("INVITE", "d_Other123", "101"),
		"REGISTER for another device":   request("REGISTER", me, "d_Other123"),
		"credentials of another device": request("INVITE", me, "101", authz("d_Other123")),
		"escaped username":              request("REGISTER", "d%5FMine1234", "d%5FMine1234"),
		"two From headers":              strings.Replace(request("INVITE", me, "101"), "To:", "From: <sip:d_Other123@linx>\r\nTo:", 1),
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			c := h.dial()
			send(t, c, msg)
			wantClosed(t, c, websocket.StatusPolicyViolation, ReasonNotYourLine)
			if got := <-h.audits; got != ReasonNotYourLine {
				t.Errorf("audited %q", got)
			}
			if got := h.ast.received(); len(got) != 0 {
				t.Errorf("forwarded %q", got)
			}
		})
	}
}

func TestRefusesOtherMessages(t *testing.T) {
	for name, tc := range map[string]struct{ msg, reason string }{
		"not SIP": {"GET / HTTP/1.1\r\n\r\n", ReasonMalformed},
		// Two messages in one frame: Asterisk would read the second, which
		// the relay never checked.
		"second message after the body":   {request("OPTIONS", me, me) + request("INVITE", "d_Other123", "101"), ReasonMalformed},
		"body longer than Content-Length": {request("MESSAGE", me, "101") + "hello", ReasonMalformed},
		"two Content-Lengths":             {strings.Replace(request("MESSAGE", me, "101"), "\r\n\r\n", "\r\nl: 5\r\n\r\nhello", 1), ReasonMalformed},
		// A bare LF could hide a From header inside another header's value.
		"bare LF in the headers": {strings.Replace(request("INVITE", me, "101"), "Via:", "Subject: x\nFrom: <sip:d_Other123@linx>;tag=b\r\nVia:", 1), ReasonMalformed},
		"bare CR in the headers": {strings.Replace(request("INVITE", me, "101"), "Via:", "Subject: x\rFrom: <sip:d_Other123@linx>;tag=b\r\nVia:", 1), ReasonMalformed},
		"unknown method":         {request("PUBLISH", me, me), ReasonMethod},
		"too big":                {request("MESSAGE", me, "101") + strings.Repeat("x", DefaultMaxMessage), ""},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, nil)
			c := h.dial()
			send(t, c, tc.msg)
			if tc.reason == "" {
				// coder/websocket closes an over-limit read itself (1009)
				// before the relay sees the message.
				_, err := read(t, c)
				if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
					t.Fatalf("got %v", err)
				}
			} else {
				wantClosed(t, c, websocket.StatusPolicyViolation, tc.reason)
			}
			if got := h.ast.received(); len(got) != 0 {
				t.Errorf("forwarded %q", got)
			}
		})
	}
}

func TestRateLimit(t *testing.T) {
	h := newHarness(t, func(r *Relay) { r.Rate, r.Burst = 1, 3 })
	c := h.dial()
	for range 4 {
		send(t, c, request("OPTIONS", me, me))
	}
	wantClosed(t, c, websocket.StatusPolicyViolation, ReasonTooMany)
	if got := <-h.audits; got != ReasonTooMany {
		t.Errorf("audited %q", got)
	}
}

func TestAuthFailures(t *testing.T) {
	h := newHarness(t, nil)
	// Asterisk rejects every credentialed request; the second is answered
	// with a stale challenge (not a failure), the third succeeds (resets).
	var n atomic.Int32
	h.ast.reply = func(req string) []string {
		switch n.Add(1) {
		case 2:
			return []string{replyTo(req, 401, `WWW-Authenticate: Digest realm="linxpbx", nonce="n2", stale=true`+"\r\n")}
		case 3:
			return []string{replyTo(req, 200, "")}
		}
		return []string{replyTo(req, 401, `WWW-Authenticate: Digest realm="linxpbx", nonce="n"`+"\r\n")}
	}
	c := h.dial()
	// 1: failure, 2: stale, 3: success (resets), 4, 5: failures, 6: third
	// failure in a row closes.
	for i := range 6 {
		send(t, c, request("REGISTER", me, me, authz(me)))
		if i < 5 {
			if resp, err := read(t, c); err != nil || !strings.HasPrefix(resp, "SIP/2.0 ") {
				t.Fatalf("reply %d: %q %v", i+1, resp, err)
			}
		}
	}
	wantClosed(t, c, websocket.StatusPolicyViolation, ReasonAuthFailures)
	if got := <-h.audits; got != ReasonAuthFailures {
		t.Errorf("audited %q", got)
	}

	// The challenge to a request without credentials (every sign-in's
	// first step) is no failure.
	h2 := newHarness(t, nil)
	h2.ast.reply = func(req string) []string { return []string{replyTo(req, 401, "")} }
	c2 := h2.dial()
	for i := range 5 {
		send(t, c2, request("REGISTER", me, me))
		if _, err := read(t, c2); err != nil {
			t.Fatalf("reply %d: %v", i+1, err)
		}
	}
}

func TestSessionEnds(t *testing.T) {
	t.Run("signed out", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.dial()
		eventually(t, func() bool { return h.relay.Connections() == 1 })
		h.relay.CloseSession(h.line.SessionID)
		wantClosed(t, c, websocket.StatusPolicyViolation, ReasonSignedOut)
		eventually(t, func() bool { return h.relay.Connections() == 0 })
		select {
		case a := <-h.audits:
			t.Errorf("audited %q: signing out isn't a refusal", a)
		default:
		}
	})
	t.Run("person disabled", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.dial()
		eventually(t, func() bool { return h.relay.Connections() == 1 })
		h.relay.CloseUser(uuid.New()) // someone else: no effect
		h.relay.CloseUser(h.line.UserID)
		wantClosed(t, c, websocket.StatusPolicyViolation, ReasonSignedOut)
	})
	t.Run("found by the periodic check", func(t *testing.T) {
		h := newHarness(t, func(r *Relay) { r.CheckInterval = 20 * time.Millisecond })
		c := h.dial()
		h.checkOK.Store(false)
		wantClosed(t, c, websocket.StatusPolicyViolation, ReasonSignedOut)
	})
	t.Run("opened again", func(t *testing.T) {
		h := newHarness(t, nil)
		first := h.dial()
		eventually(t, func() bool { return h.relay.Connections() == 1 })
		second := h.dial()
		wantClosed(t, first, websocket.StatusPolicyViolation, ReasonReplaced)
		h.ast.reply = func(req string) []string { return []string{replyTo(req, 200, "")} }
		send(t, second, request("OPTIONS", me, me))
		if _, err := read(t, second); err != nil {
			t.Fatal(err)
		}
		if h.relay.Connections() != 1 {
			t.Errorf("%d connections", h.relay.Connections())
		}
	})
	t.Run("Asterisk goes away", func(t *testing.T) {
		h := newHarness(t, nil)
		c := h.dial()
		ast := <-h.ast.conns
		ast.Close(websocket.StatusGoingAway, "")
		_, err := read(t, c)
		if websocket.CloseStatus(err) != websocket.StatusGoingAway {
			t.Fatalf("got %v", err)
		}
	})
}

func TestNeedsSIPSubprotocol(t *testing.T) {
	h := newHarness(t, nil)
	c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(h.srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	_, err = read(t, c)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("got %v", err)
	}
}

func TestAsteriskUnreachable(t *testing.T) {
	h := newHarness(t, func(r *Relay) {
		r.Dial = func(context.Context) (*websocket.Conn, error) { return nil, errors.New("down") }
	})
	_, resp, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(h.srv.URL, "http"), &websocket.DialOptions{Subprotocols: []string{"sip"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("got %v %v", resp, err)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
