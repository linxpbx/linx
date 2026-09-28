package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"linxpbx.com/linx/internal/webapp"
)

// The repair page (docs/INSTALL.md §7, docs/ui/INSTALL_SCREENS.md §5.3):
// when https://<domain> is broken on an installed server, `sudo linx
// setup` also opens the Server settings page on port 6464, the install's
// first-page port, behind a new one-time link. The full control plane
// serves it (it has the accounts), so a system admin signs in there with a
// password and authenticator app; a passkey can't work at a bare address.
// `sudo linx setup --new-link --no-sign-in` makes a link that skips the
// sign-in: whoever runs it is root on the server already.

// RepairPath is the repair link's state on the host: root only.
const RepairPath = "/etc/linx/repair-state.json"

// RepairCookieName is the claimed browser's cookie on port 6464.
const RepairCookieName = "__Host-linx_repair"

// RepairState is the repair link as the host keeps it.
type RepairState struct {
	// Secret is the link's secret, until a browser opens it.
	Secret string `json:"secret,omitempty"`
	// SessionHash is the claiming browser's cookie's SHA-256.
	SessionHash string    `json:"session_hash,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	NoSignIn    bool      `json:"no_sign_in,omitempty"`
	// Problem is why https://<domain> didn't answer.
	Problem string `json:"problem,omitempty"`
	// Browser and Address: who opened the link.
	Browser string `json:"browser,omitempty"`
	Address string `json:"address,omitempty"`
}

// NewRepairState is a new repair link, open for LinkLifetime.
func NewRepairState(now time.Time, noSignIn bool, problem string) RepairState {
	return RepairState{Secret: NewSecret(), ExpiresAt: now.Add(LinkLifetime).UTC(), NoSignIn: noSignIn, Problem: problem}
}

// Live reports whether the link, or the browser that opened it, still works.
func (r RepairState) Live(now time.Time) bool {
	return now.Before(r.ExpiresAt) && (r.Secret != "" || r.SessionHash != "")
}

// LoadRepairState reads the state file.
func LoadRepairState(p string) (RepairState, error) {
	var r RepairState
	b, err := os.ReadFile(p)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %w", p, err)
	}
	return r, nil
}

// Save writes the state file atomically, root only.
func (r RepairState) Save(p string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// claim is a browser opening the repair link: the host checks the secret
// itself, once.
func (h *SettingsHost) claim(m Message) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	rs := h.repair
	if h.RepairPath == "" || rs.Secret == "" || !h.now().Before(rs.ExpiresAt) || !ValidHash(m.SessionHash) ||
		!Matches(m.Secret, Hash(rs.Secret)) {
		return false
	}
	rs.Secret, rs.SessionHash = "", m.SessionHash
	rs.Browser, rs.Address = m.Browser, m.Address
	if err := rs.Save(h.RepairPath); err != nil {
		h.log().Error("saving the repair link", "err", err)
		return false
	}
	h.repair = rs
	h.log().Info("repair link opened", "browser", m.Browser, "address", m.Address)
	return true
}

// repairAPI is all of the API the repair page may reach: signing in (a
// password and authenticator app or recovery code; passkeys and company
// sign-in can't work at a bare address), "confirm it's you", who's signed
// in, and the Server settings page. Anything else on port 6464 is the "can't be used" page.
var repairAPI = map[string]bool{
	"POST /api/v1/session":                 true,
	"POST /api/v1/session/mfa":             true,
	"DELETE /api/v1/session":               true,
	"POST /api/v1/session/confirm":         true,
	"GET /api/v1/me":                       true,
	"GET /api/v1/server-settings":          true,
	"POST /api/v1/server-settings":         true,
	"POST /api/v1/server-settings/preview": true,
}

// Repair page paths.
const (
	RepairPage        = "/repair"
	RepairStatePath   = "/repair/api/state"
	RepairSettingsAPI = "/repair/api/server-settings"
)

// RepairHandler serves port 6464 on an installed server while setup has
// the repair link open. Without the claimed cookie it answers only the
// link itself; with it, the page, the few API operations in repairAPI
// (through api), and, for a link that skips the sign-in, the Server
// settings page's own operations (through settings, which answers under
// RepairSettingsAPI without a session: setup on the server vouched).
func (s *Server) RepairHandler(api, settings http.Handler) http.Handler {
	web := webapp.Handler(s.Web)
	return webapp.PlainHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, open := s.repairView()
		if !open {
			notFound(w)
			return
		}
		if !s.repairSession(r, v) {
			if !s.allow(r) {
				w.Header().Set("Retry-After", "10")
				http.Error(w, "Too many requests. Wait a moment and try again.", http.StatusTooManyRequests)
				return
			}
			if secret, ok := strings.CutPrefix(r.URL.Path, RepairPage+"/"); ok && r.Method == http.MethodGet && ValidSecret(secret) {
				s.claimRepair(w, r, secret, v)
				return
			}
			notFound(w)
			return
		}
		switch p := r.URL.Path; {
		case p == RepairStatePath && r.Method == http.MethodGet:
			writeJSON(w, http.StatusOK, repairState{NoSignIn: v.Server.NoSignIn, Problem: v.Server.Problem, Domain: v.Server.Domain,
				ExpiresAt: v.ExpiresAt, ExpiresIn: max(0, int(v.ExpiresAt.Sub(s.now()).Seconds()))})
		case p == RepairSettingsAPI || strings.HasPrefix(p, RepairSettingsAPI+"/"):
			if !v.Server.NoSignIn {
				notFound(w)
				return
			}
			if r.Method != http.MethodGet && !sameOrigin(r) {
				writeProblem(w, http.StatusForbidden, "This change didn't come from the repair page.")
				return
			}
			settings.ServeHTTP(w, r)
		case repairAPI[r.Method+" "+p]:
			api.ServeHTTP(w, r)
		case strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, RepairPage+"/api/"):
			notFound(w)
		case p == "/" || strings.HasPrefix(p, RepairPage+"/"):
			http.Redirect(w, r, RepairPage, http.StatusSeeOther)
		case p == RepairPage || path.Ext(p) != "":
			web.ServeHTTP(w, r)
		default:
			notFound(w)
		}
	}))
}

// repairState is GET /repair/api/state.
type repairState struct {
	NoSignIn  bool      `json:"no_sign_in"`
	Problem   string    `json:"problem,omitempty"`
	Domain    string    `json:"domain"`
	ExpiresAt time.Time `json:"expires_at"`
	ExpiresIn int       `json:"expires_in"`
}

// repairView is the host's view while the repair page is open.
func (s *Server) repairView() (View, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.view
	open := s.conn != nil && s.haveView && v.Server != nil && v.Server.Repair && s.now().Before(v.ExpiresAt)
	return v, open
}

func (s *Server) repairSession(r *http.Request, v View) bool {
	c, err := r.Cookie(RepairCookieName)
	return err == nil && v.SessionHash != "" && Matches(c.Value, v.SessionHash)
}

func (s *Server) claimRepair(w http.ResponseWriter, r *http.Request, secret string, v View) {
	if !Matches(secret, v.LinkHash) {
		notFound(w)
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
		s.log().Warn("claiming the repair link", "err", err)
		starting(w)
		return
	}
	if !res.OK {
		notFound(w)
		return
	}
	s.mu.Lock()
	if s.view.SessionHash == "" {
		s.view.LinkHash, s.view.SessionHash = "", Hash(token)
	}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: RepairCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Expires: v.ExpiresAt, MaxAge: int(time.Until(v.ExpiresAt).Seconds()),
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, RepairPage, http.StatusSeeOther)
}

// PreviewServerSettings asks setup on the server what c needs and does.
func (s *Server) PreviewServerSettings(ctx context.Context, c ServerChange) (ServerPreview, error) {
	res, err := s.request(ctx, Message{Type: TypeServerPreview, Change: &c}, checkTimeout)
	switch {
	case err != nil:
		return ServerPreview{}, err
	case res.Error != "":
		return ServerPreview{}, &Refused{Detail: res.Error}
	case res.Preview == nil:
		return ServerPreview{}, &Refused{Detail: "Setup on the server refused that."}
	}
	return *res.Preview, nil
}
