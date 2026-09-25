package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/asteriskconf"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/siprelay"
	"linxpbx.com/linx/internal/stepca"
	controlplaneapi "linxpbx.com/linx/services/control-plane/api"
)

// The /sip relay (ADR-038, docs/WEB.md §5): a signed-in browser's phone
// line, relayed to Asterisk's secure websocket on linx-sipws.

// sipwsURLFromEnv is Asterisk's browser websocket, checked against the
// internal CA's root (the control plane issues its certificate, sipws.go).
func sipwsURLFromEnv(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("LINX_SIPWS_URL")); v != "" {
		return v
	}
	return fmt.Sprintf("wss://%s:%d%s", asteriskconf.DefaultSIPWSHost, asteriskconf.SIPWSPort, asteriskconf.SIPWSPath)
}

// sipStore is what the relay reads.
type sipStore interface {
	auth.SessionStore
	WebDeviceForSession(ctx context.Context, session uuid.UUID) (pbx.Device, error)
	RevokeDeadWebDevices(ctx context.Context, at time.Time) ([]pbx.Device, error)
	Audit(ctx context.Context, e auth.AuditEntry) error
}

// newSIPRelay builds the relay: it dials rawURL (wss://, verified against
// caRootFile) and rechecks each connection's session in st.
func newSIPRelay(rawURL, caRootFile string, st sipStore, log *slog.Logger) (*siprelay.Relay, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "wss" {
		return nil, fmt.Errorf("LINX_SIPWS_URL %q: must be wss:// (the relay never talks to Asterisk unencrypted)", rawURL)
	}
	roots, err := stepca.LoadRoots(caRootFile)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
	return &siprelay.Relay{
		Dial: func(ctx context.Context) (*websocket.Conn, error) {
			c, _, err := websocket.Dial(ctx, rawURL, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{siprelay.Subprotocol}})
			return c, err
		},
		Check: func(ctx context.Context, line siprelay.Line) error { return checkLine(ctx, st, line, time.Now()) },
		Audit: func(ctx context.Context, line siprelay.Line, reason string) {
			e := auth.AuditEntry{TenantID: &line.TenantID, Actor: auth.TypeUser + ":" + line.UserID.String(), IP: line.IP,
				Action: "sip.relay_closed", Target: "session:" + line.SessionID.String(), Result: auth.ResultDenied,
				Detail: map[string]any{"reason": reason, "sip_username": line.Username}}
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := st.Audit(ctx, e); err != nil {
				log.Error("auditing a refused phone line failed", "err", err)
			}
		},
		Log: log,
	}, nil
}

// checkLine is the relay's periodic check: the session is still live and
// still has this web device. It also counts the open connection as use of
// the session, like any request, so a page left open doesn't go idle.
func checkLine(ctx context.Context, st sipStore, line siprelay.Line, now time.Time) error {
	sess, err := st.SessionByTokenHash(ctx, line.SessionTokenHash)
	if err != nil {
		return err
	}
	if err := liveSession(sess, now); err != nil {
		return err
	}
	d, err := st.WebDeviceForSession(ctx, sess.ID)
	if err != nil {
		return err
	}
	if d.SIPUsername != line.Username || !d.Enabled {
		return errors.New("the session has a different phone line now")
	}
	idle := now.Add(auth.SessionIdleTTL)
	if idle.After(sess.ExpiresAt) {
		idle = sess.ExpiresAt
	}
	return st.TouchSession(ctx, sess.ID, now, idle, line.IP)
}

func liveSession(s auth.UserSession, now time.Time) error {
	switch {
	case s.RevokedAt != nil:
		return errors.New("signed out")
	case !now.Before(s.ExpiresAt), !now.Before(s.IdleExpiresAt):
		return errors.New("expired")
	case !s.MFAVerified:
		return errors.New("sign-in unfinished")
	}
	return nil
}

// sipHandler is GET /sip: it takes only a signed-in session's cookie, from
// a page on this server's own address, whose session has a phone line
// (POST /api/v1/me/web-phone), then hands the websocket to the relay.
func sipHandler(authn *auth.Authenticator, st sipStore, relay *siprelay.Relay) http.Handler {
	return authn.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			apihttp.WriteProblem(w, http.StatusForbidden, "origin_invalid", "Open the phone line from this server's own web page.")
			return
		}
		sess, ok := auth.SessionFromContext(r.Context())
		if !ok {
			apihttp.WriteProblem(w, http.StatusUnauthorized, "session_required", "Sign in first.")
			return
		}
		if !sess.MFAVerified {
			apihttp.WriteProblem(w, http.StatusForbidden, "sign_in_unfinished", "Finish signing in (your authenticator code) first.")
			return
		}
		d, err := st.WebDeviceForSession(r.Context(), sess.ID)
		if errors.Is(err, pbx.ErrNotFound) {
			apihttp.WriteProblem(w, http.StatusConflict, "no_phone_line",
				"Get this browser's phone line first: POST "+controlplaneapi.WebPhonePath+".")
			return
		}
		if err != nil {
			apihttp.WriteProblem(w, http.StatusServiceUnavailable, "unavailable", "Linx can't check that right now. Try again shortly.")
			return
		}
		relay.Serve(w, r, siprelay.Line{
			TenantID: sess.TenantID, UserID: sess.UserID, SessionID: sess.ID, SessionTokenHash: sess.TokenHash,
			Username: d.SIPUsername, IP: auth.ClientIPFromContext(r.Context()),
		})
	}))
}

// sameOrigin reports whether the websocket was opened by a page on this
// server's own address (docs/WEB.md §5): Origin must be exactly
// https://<Host>. A browser always sends Origin on a websocket; its absence
// means something other than a browser page, which has no business here.
func sameOrigin(r *http.Request) bool {
	o, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && o.Scheme == "https" && o.Host != "" && strings.EqualFold(o.Host, r.Host) &&
		o.Path == "" && o.User == nil && o.RawQuery == ""
}

// sweepWebDevices revokes web devices whose session has ended every
// interval, and closes their connections (docs/WEB.md §5: "stale web
// devices are revoked when their session ends"). Asterisk already refuses
// them the moment the session ends (migration 0013); this records it.
func sweepWebDevices(ctx context.Context, st sipStore, relay *siprelay.Relay, interval time.Duration, log *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		revokeDeadWebDevices(ctx, st, relay, log)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func revokeDeadWebDevices(ctx context.Context, st sipStore, relay *siprelay.Relay, log *slog.Logger) {
	revoked, err := st.RevokeDeadWebDevices(ctx, time.Now().UTC())
	if err != nil {
		if ctx.Err() == nil {
			log.Error("revoking ended browser phone lines", "err", err)
		}
		return
	}
	for _, d := range revoked {
		relay.CloseUsername(d.SIPUsername)
	}
}
