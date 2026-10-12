package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"linxpbx.com/linx/internal/apihttp"
)

// The video server's signalling (ADR-088, docs/VIDEO_CALLS.md §4.3):
// LiveKit is on linx-private only, and devices reach it here, through the
// same front door, name and certificate as everything else — no new
// hostname, port or front-door change. Only LiveKit's two signalling
// paths are passed on; its API and everything else stay inside. Whoever
// connects still needs a token only the control plane mints.

const (
	// LiveKitPath is where devices reach the video server.
	LiveKitPath = "/livekit/"
	// liveKitMessageLimit is the most one signalling message may be:
	// a room's whole description, for 8 people, is a few tens of KB.
	liveKitMessageLimit = 1 << 20
)

// liveKitHandler passes /livekit/rtc (a websocket) and /livekit/rtc/validate
// (LiveKit's "why couldn't I connect" check) on to LiveKit at target.
func liveKitHandler(target string, log *slog.Logger) http.Handler {
	base, err := url.Parse(target)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Group calls aren't set up on this server.")
		})
	}
	client := &http.Client{Timeout: 10 * time.Second}
	return apihttp.NoStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := "/" + strings.TrimPrefix(r.URL.Path, LiveKitPath)
		if r.Method != http.MethodGet || (path != "/rtc" && !strings.HasPrefix(path, "/rtc/")) {
			http.NotFound(w, r)
			return
		}
		up := *base
		up.Path = path
		up.RawQuery = r.URL.RawQuery
		if path == "/rtc/validate" {
			validate(w, r, client, up.String())
			return
		}
		relayLiveKit(w, r, up, log)
	}))
}

func validate(w http.ResponseWriter, r *http.Request, client *http.Client, to string) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, to, nil)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx's video server isn't answering.")
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<10))
}

// relayLiveKit joins the device's websocket to one of its own to LiveKit
// and copies messages both ways, as they are, until either side closes.
func relayLiveKit(w http.ResponseWriter, r *http.Request, up url.URL, log *slog.Logger) {
	if up.Scheme == "https" {
		up.Scheme = "wss"
	} else {
		up.Scheme = "ws"
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	server, resp, err := websocket.Dial(dctx, up.String(), nil)
	dcancel()
	if err != nil {
		// LiveKit's own answer (a bad token is 401) goes back as it is.
		status := http.StatusServiceUnavailable
		if resp != nil && resp.StatusCode >= 400 {
			status = resp.StatusCode
		}
		http.Error(w, "the video server refused the connection", status)
		return
	}
	defer server.CloseNow()
	device, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer device.CloseNow()
	server.SetReadLimit(liveKitMessageLimit)
	device.SetReadLimit(liveKitMessageLimit)
	errs := make(chan error, 2)
	go func() { errs <- pump(ctx, device, server) }()
	go func() { errs <- pump(ctx, server, device) }()
	err = <-errs
	var closed websocket.CloseError
	if err != nil && !errors.As(err, &closed) && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		log.Debug("video server connection ended", "err", err)
	}
	device.Close(websocket.StatusNormalClosure, "")
	server.Close(websocket.StatusNormalClosure, "")
}

func pump(ctx context.Context, from, to *websocket.Conn) error {
	for {
		kind, b, err := from.Read(ctx)
		if err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err = to.Write(wctx, kind, b)
		cancel()
		if err != nil {
			return err
		}
	}
}
