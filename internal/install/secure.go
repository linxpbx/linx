package install

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"linxpbx.com/linx/internal/certs"
	"linxpbx.com/linx/internal/webapp"
)

// Install mode's HTTPS side (docs/INSTALL.md §4.1 and §5.1): the port every
// front door sends meet., api. and turn.<domain> to. Before there's a
// certificate it answers only Let's Encrypt's acme-tls/1 check, with the
// challenge certificate certd left for that name; once the certificate is
// deployed it also serves the secure page, reached through the one-time
// handoff.

// TLSConfig is the HTTPS port's: the challenge certificate for Let's
// Encrypt's check, else the deployed one (none yet: the handshake fails).
func TLSConfig(ch certs.Challenges, serving *certs.ServingCert) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1", certs.ACMETLS1},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if certs.IsChallenge(hello) {
				return ch.Certificate(hello.ServerName)
			}
			return serving.GetCertificate(hello)
		},
	}
}

// ChallengeTLSConfig answers only Let's Encrypt's check: the TURN-over-TLS
// port, which turn.<domain> reaches through Pangolin and nginx.
func ChallengeTLSConfig(ch certs.Challenges) *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{certs.ACMETLS1},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if !certs.IsChallenge(hello) {
				return nil, certs.ErrNoChallenge
			}
			return ch.Certificate(hello.ServerName)
		},
	}
}

// ServeChallenges completes Let's Encrypt's handshakes on ln and closes
// each connection: the check needs nothing more.
func ServeChallenges(ctx context.Context, ln net.Listener, conf *tls.Config) {
	stop := context.AfterFunc(ctx, func() { ln.Close() })
	defer stop()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go func() {
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			tc := tls.Server(c, conf)
			if tc.HandshakeContext(ctx) == nil {
				_ = tc.Close()
			}
		}()
	}
}

// SecureHandler serves the HTTPS port: only on meet.<domain>, and only
// once the host has a certificate page. Without the secure page's session
// it answers the handoff page (which has no secrets: the handoff is in the
// address's fragment, which never reaches a server), its files, the
// handoff itself and the plain page's reachability probe.
func (s *Server) SecureHandler() http.Handler {
	web := webapp.Handler(s.Web)
	return webapp.Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, _ := s.Snapshot()
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if v.Cert == nil || !strings.EqualFold(host, "meet."+v.Cert.Domain) {
			notFound(w)
			return
		}
		p := r.URL.Path
		switch {
		case p == "/install/api/ping" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
			// The plain page checks it can open this address before moving
			// there.
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		case p == "/install/api/redeem" && r.Method == http.MethodPost:
			s.redeem(w, r)
			return
		case p == "/install/continue" || (path.Ext(p) != "" && !strings.HasPrefix(p, "/install/api/")):
			web.ServeHTTP(w, r)
			return
		}
		if !s.session(r, true) {
			notFound(w)
			return
		}
		switch {
		case p == "/install/api/state" && r.Method == http.MethodGet:
			s.getState(w)
		case strings.HasPrefix(p, "/install/api/"):
			notFound(w)
		case p == "/":
			http.Redirect(w, r, "/install", http.StatusSeeOther)
		case p == "/install":
			web.ServeHTTP(w, r)
		default:
			notFound(w)
		}
	}))
}

// redeem swaps the handoff for the secure page's own session cookie.
func (s *Server) redeem(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeProblem(w, http.StatusForbidden, "This change didn't come from the install page.")
		return
	}
	var body struct {
		Handoff string `json:"handoff"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || !ValidSecret(body.Handoff) {
		writeProblem(w, http.StatusBadRequest, errBadToken.Error())
		return
	}
	token := NewSecret()
	if _, ok := s.relay(w, r, Message{Type: TypeRedeem, Secret: body.Handoff, SessionHash: Hash(token)}, claimTimeout); !ok {
		return
	}
	s.mu.Lock()
	// As with a claim: the host's new view follows its result.
	s.view.SessionHash, s.view.Secure = Hash(token), true
	s.view.ExpiresAt = s.now().Add(LinkLifetime).UTC()
	expires := s.view.ExpiresAt
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: SecureCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
	})
	w.WriteHeader(http.StatusNoContent)
}
