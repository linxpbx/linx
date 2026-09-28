package install

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"
)

// Running setup again on an installed server (docs/INSTALL.md §7,
// docs/ui/INSTALL_SCREENS.md §5.2): `sudo linx setup` starts the
// linx-setup service in settings mode, and for four hours the full Linx's
// System → Server settings page can change this server's own settings.
// The page is only for a signed-in system admin who confirmed it's them
// (the control plane checks); the host, as for the install, only ever runs
// setup's own plans from setup.yaml with the few choices the page may
// change, checked here again.

// TypeServerChange is the Server settings page's Apply (control plane →
// host; the host answers with "result").
const TypeServerChange = "server_change"

// ServerView is the Server settings page, as the host tells it.
type ServerView struct {
	// Where, FrontDoor and Domain are shown, not changed, here.
	Where     string `json:"where"`
	FrontDoor string `json:"front_door"`
	Domain    string `json:"domain"`
	Provider  string `json:"provider"`
	// Token is "saved", or "" (none: the certificate renews through port
	// 443 and DNS records are the owner's).
	Token string `json:"token,omitempty"`
	// Profile is the size in use; Profiles, ProfilePick and ProfileReason
	// as on the install's extras.
	Profile       string          `json:"profile"`
	Profiles      []ProfileOption `json:"profiles,omitempty"`
	ProfilePick   string          `json:"profile_pick,omitempty"`
	ProfileReason string          `json:"profile_reason,omitempty"`
	// Portainer is on; PortainerAllowed: offered (a server at home).
	Portainer        bool `json:"portainer"`
	PortainerAllowed bool `json:"portainer_allowed,omitempty"`
	// Apply is the last change's progress.
	Apply Stage         `json:"apply"`
	Steps []InstallStep `json:"steps,omitempty"`
	// Keep is what to write down (Portainer's password when it's turned
	// on), until the page closes.
	Keep []KeepItem `json:"keep,omitempty"`
	// ExpiresAt closes the page (sudo linx setup opens it again).
	ExpiresAt time.Time `json:"expires_at"`
}

// ServerChange is what the page may change.
type ServerChange struct {
	// Profile is lite, standard or performance.
	Profile   string `json:"profile"`
	Portainer bool   `json:"portainer"`
	// Token is a new DNS token ("" keeps the one there is, or none).
	Token string `json:"token,omitempty"`
}

// SettingsApplier is the host's side (cmd/linx; tests fake it).
type SettingsApplier interface {
	// View is the settings as they are now.
	View(ctx context.Context) (ServerView, error)
	// CheckToken checks a new DNS token without saving it: a refusal is
	// plain words for the page.
	CheckToken(ctx context.Context, token string) (refusal string, err error)
	Steps(ctx context.Context, c ServerChange) ([]string, error)
	// Run applies c, reporting each row as Applier.Run does.
	Run(ctx context.Context, c ServerChange, report func(i int, state, detail string), keep func(KeepItem)) error
}

var (
	errApplying   = errors.New("A change is still being made. Wait for it to finish.")
	errNoChange   = errors.New("Nothing to change.")
	errBadProfile = errors.New("That size isn't one of the choices.")
)

// SettingsHost is linx setup's side in settings mode. Nothing is kept on
// disk: a restarted service starts a fresh page from setup.yaml.
type SettingsHost struct {
	Apply SettingsApplier
	Dial  func(ctx context.Context) (io.ReadWriteCloser, error)
	Now   func() time.Time
	Log   *slog.Logger
	// Lifetime is how long the page stays open (default LinkLifetime).
	Lifetime time.Duration
	// Retry is the wait between bridge attempts (default 2 s).
	Retry time.Duration

	mu     sync.Mutex
	view   ServerView
	notify chan struct{}
}

func (h *SettingsHost) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *SettingsHost) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// Snapshot is the page as it is now.
func (h *SettingsHost) Snapshot() ServerView {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.view
	v.Steps = append([]InstallStep(nil), v.Steps...)
	v.Keep = append([]KeepItem(nil), v.Keep...)
	return v
}

// Run reads the settings and keeps the bridge open until the page's time
// is up or ctx ends.
func (h *SettingsHost) Run(ctx context.Context) error {
	v, err := h.Apply.View(ctx)
	if err != nil {
		return err
	}
	life := h.Lifetime
	if life <= 0 {
		life = LinkLifetime
	}
	v.ExpiresAt = h.now().Add(life).UTC()
	h.mu.Lock()
	h.view = v
	h.mu.Unlock()

	ctx, cancel := context.WithDeadline(ctx, v.ExpiresAt)
	defer cancel()
	retry := h.Retry
	if retry <= 0 {
		retry = 2 * time.Second
	}
	for ctx.Err() == nil {
		// A change being made carries on past the hour: it ends itself.
		conn, err := h.Dial(ctx)
		if err != nil {
			h.log().Warn("can't reach the control plane yet", "err", err)
		} else {
			if err := h.Serve(ctx, conn); err != nil && ctx.Err() == nil {
				h.log().Warn("link to the control plane ended", "err", err)
			}
			conn.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
	h.waitApplied()
	return nil
}

// waitApplied lets a change still being made finish before the service
// ends (its commands would otherwise be cut off halfway).
func (h *SettingsHost) waitApplied() {
	for {
		h.mu.Lock()
		running := h.view.Apply.State == StageRunning
		h.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(time.Second)
	}
}

func (h *SettingsHost) changed(fn func(v *ServerView)) {
	h.mu.Lock()
	fn(&h.view)
	n := h.notify
	h.mu.Unlock()
	if n != nil {
		select {
		case n <- struct{}{}:
		default:
		}
	}
}

// Serve answers one bridge connection until it ends.
func (h *SettingsHost) Serve(ctx context.Context, rw io.ReadWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if c, ok := rw.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { c.Close() })
		defer stop()
	}
	var wmu sync.Mutex
	enc := json.NewEncoder(rw)
	send := func(m Message) error {
		wmu.Lock()
		defer wmu.Unlock()
		return enc.Encode(m)
	}
	notify := make(chan struct{}, 1)
	h.mu.Lock()
	h.notify = notify
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.notify == notify {
			h.notify = nil
		}
		h.mu.Unlock()
	}()
	sendView := func() error {
		v := h.Snapshot()
		return send(Message{Type: TypeView, View: &View{ExpiresAt: v.ExpiresAt, Server: &v}})
	}
	if err := sendView(); err != nil {
		return err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-notify:
				if sendView() != nil {
					cancel()
					return
				}
			}
		}
	}()
	sc := bufio.NewScanner(rw)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		var m Message
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.ID == "" || len(m.ID) > 64 {
			continue
		}
		res := Message{Type: TypeResult, ID: m.ID}
		switch m.Type {
		case TypeServerChange:
			errs, err := h.change(ctx, m.Change)
			switch {
			case err != nil:
				res.Error = err.Error()
			case len(errs) > 0:
				res.Errors = errs
			default:
				res.OK = true
			}
		default:
			res.Error = errNotYet.Error()
		}
		_ = send(res)
	}
	cancel()
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// change checks c and starts making it in the background.
func (h *SettingsHost) change(ctx context.Context, c *ServerChange) ([]FieldError, error) {
	if c == nil {
		return nil, errNoChange
	}
	v := h.Snapshot()
	switch {
	case v.Apply.State == StageRunning:
		return nil, errApplying
	case c.Portainer && !v.PortainerAllowed:
		return nil, errNoPortainer
	}
	valid := false
	for _, o := range v.Profiles {
		valid = valid || o.Name == c.Profile
	}
	if !valid {
		return nil, errBadProfile
	}
	if c.Profile == v.Profile && c.Portainer == v.Portainer && c.Token == "" {
		return nil, errNoChange
	}
	if c.Token != "" {
		refusal, err := h.Apply.CheckToken(ctx, c.Token)
		if err != nil {
			return nil, err
		}
		if refusal != "" {
			return []FieldError{{Step: StepToken, Field: "token", Message: refusal}}, nil
		}
	}
	titles, err := h.Apply.Steps(ctx, *c)
	if err != nil {
		return nil, err
	}
	steps := make([]InstallStep, len(titles))
	for i, t := range titles {
		steps[i] = InstallStep{Title: t}
	}
	h.changed(func(v *ServerView) { v.Steps, v.Apply = steps, Stage{State: StageRunning, At: h.now().UTC()} })
	go h.run(context.WithoutCancel(ctx), *c)
	return nil, nil
}

func (h *SettingsHost) run(ctx context.Context, c ServerChange) {
	report := func(i int, state, detail string) {
		h.changed(func(v *ServerView) {
			if i >= 0 && i < len(v.Steps) {
				v.Steps[i].State, v.Steps[i].Detail = state, detail
			}
		})
	}
	keep := func(k KeepItem) { h.changed(func(v *ServerView) { v.Keep = append(v.Keep, k) }) }
	err := h.Apply.Run(ctx, c, report, keep)
	if err != nil {
		h.log().Error("changing the server's settings", "err", err)
		h.changed(func(v *ServerView) {
			v.Apply = Stage{State: StageFailed, Detail: firstLine(err.Error()), At: h.now().UTC()}
		})
		return
	}
	// The page shows the settings as they are now.
	nv, verr := h.Apply.View(ctx)
	h.changed(func(v *ServerView) {
		if verr == nil {
			steps, keep, exp := v.Steps, v.Keep, v.ExpiresAt
			*v = nv
			v.Steps, v.Keep, v.ExpiresAt = steps, keep, exp
		}
		v.Apply = Stage{State: StageOK, At: h.now().UTC()}
	})
}
