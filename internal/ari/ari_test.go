package ari

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type recordApp struct {
	serve func(ctx context.Context, c *Conn)
}

func (a recordApp) Serve(ctx context.Context, c *Conn) { a.serve(ctx, c) }

func basic(user, pass string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	return h
}

func newHandler(app App) *Handler {
	return &Handler{User: "asterisk", Password: []byte("pw"), App: app, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func dial(t *testing.T, srv *httptest.Server, hdr http.Header, protos []string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPHeader: hdr, Subprotocols: protos})
}

func TestHandlerRefusesWrongPassword(t *testing.T) {
	srv := httptest.NewServer(newHandler(recordApp{func(context.Context, *Conn) { t.Error("app must not run") }}))
	defer srv.Close()
	for _, hdr := range []http.Header{nil, basic("asterisk", "nope"), basic("other", "pw")} {
		_, resp, err := dial(t, srv, hdr, []string{"ari"})
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("header %v: want 401, got resp=%v err=%v", hdr, resp, err)
		}
	}
}

func TestHandlerRequiresSubprotocol(t *testing.T) {
	srv := httptest.NewServer(newHandler(recordApp{func(context.Context, *Conn) {}}))
	defer srv.Close()
	ws, _, err := dial(t, srv, basic("asterisk", "pw"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	_, _, err = ws.Read(context.Background())
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Errorf("want policy violation close, got %v", err)
	}
}

// TestEventsAndREST plays Asterisk: it sends an event, answers the app's
// REST request, and checks both reach the right place.
func TestEventsAndREST(t *testing.T) {
	got := make(chan string, 4)
	app := recordApp{func(ctx context.Context, c *Conn) {
		var chans []Channel
		if err := c.Get(ctx, "channels", &chans); err != nil {
			got <- "get error: " + err.Error()
			return
		}
		got <- "channels:" + chans[0].Endpoint()
		for ev := range c.Events() {
			got <- "event:" + ev.Type + ":" + ev.Channel.ID + ":" + ev.Timestamp.UTC().Format(time.RFC3339)
		}
	}}
	srv := httptest.NewServer(newHandler(app))
	defer srv.Close()
	ws, _, err := dial(t, srv, basic("asterisk", "pw"), []string{"ari"})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var req restRequest
	if err := json.Unmarshal(data, &req); err != nil || req.Type != "RESTRequest" || req.Method != "GET" || req.URI != "channels" || req.RequestID == "" {
		t.Fatalf("bad request %s (%v)", data, err)
	}
	body, _ := json.Marshal([]Channel{{ID: "1", Name: "PJSIP/d_abcdefgh-00000001"}})
	resp, _ := json.Marshal(map[string]any{"type": "RESTResponse", "request_id": req.RequestID, "status_code": 200,
		"reason_phrase": "OK", "message_body": string(body)})
	if err := ws.Write(ctx, websocket.MessageText, resp); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s != "channels:d_abcdefgh" {
		t.Fatalf("got %q", s)
	}

	ev := `{"type":"ChannelCreated","timestamp":"2026-09-24T10:00:00.123+0400","channel":{"id":"42","name":"PJSIP/d_abcdefgh-00000002"}}`
	if err := ws.Write(ctx, websocket.MessageText, []byte(ev)); err != nil {
		t.Fatal(err)
	}
	if s := <-got; s != "event:ChannelCreated:42:2026-09-24T06:00:00Z" {
		t.Fatalf("got %q", s)
	}
}

func TestEndpointName(t *testing.T) {
	for in, want := range map[string]string{
		"PJSIP/d_abcdefgh-0000000a": "d_abcdefgh",
		"PJSIP/d_abcdefgh":          "d_abcdefgh",
		"Local/101@x-00000001;1":    "",
	} {
		if got := (Channel{Name: in}).Endpoint(); got != want {
			t.Errorf("Endpoint(%q) = %q, want %q", in, got, want)
		}
	}
}
