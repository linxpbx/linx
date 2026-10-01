package install

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
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

// The Server settings page's requests (control plane → host; the host
// answers with "result"): Apply, and what a change would need first.
const (
	TypeServerChange  = "server_change"
	TypeServerPreview = "server_preview"
)

// ServerView is the Server settings page, as the host tells it.
type ServerView struct {
	// Where is shown, not changed: it follows this server's network.
	Where     string `json:"where"`
	FrontDoor string `json:"front_door"`
	// ProxyAddress and TURNUDPPort: the front door's own settings, if any.
	ProxyAddress string `json:"proxy_address,omitempty"`
	TURNUDPPort  int    `json:"turn_udp_port,omitempty"`
	// FrontDoors are the front doors this server may use
	// (installer.FrontDoorsFor Where).
	FrontDoors []string `json:"front_doors,omitempty"`
	Domain     string   `json:"domain"`
	Provider   string   `json:"provider"`
	// PublicAddress and LANAddress are this server's, for the page's words.
	PublicAddress string `json:"public_address,omitempty"`
	LANAddress    string `json:"lan_address,omitempty"`
	// Token is "saved", or "" (none: the certificate renews through port
	// 443 and DNS records are the owner's).
	Token string `json:"token,omitempty"`
	// DNSByHand: there's a key, but the owner keeps the records (Stop).
	DNSByHand bool `json:"dns_by_hand,omitempty"`
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
	// Problem is why https://<domain> didn't answer when setup checked it
	// ("" if it did).
	Problem string `json:"problem,omitempty"`
	// Repair: the page is also open on port 6464, because the secure
	// address is broken (docs/INSTALL.md §7). NoSignIn: its link skips
	// the sign-in (sudo linx setup --new-link --no-sign-in).
	Repair   bool `json:"repair,omitempty"`
	NoSignIn bool `json:"no_sign_in,omitempty"`
}

// ServerChange is what the page may change.
type ServerChange struct {
	// Profile is lite, standard or performance.
	Profile   string `json:"profile"`
	Portainer bool   `json:"portainer"`
	// Token is a new DNS token ("" keeps the one there is, or none); Key
	// a new key from the DNS company form (Provider and its fields).
	Token string  `json:"token,omitempty"`
	Key   *DNSKey `json:"dns_key,omitempty"`
	// DNSByHand, if set, stops (true) or starts again (false) Linx keeping
	// the DNS records right.
	DNSByHand *bool `json:"dns_by_hand,omitempty"`
	// Domain is a new domain ("" keeps it).
	Domain string `json:"domain,omitempty"`
	// FrontDoor is a new front door ("" keeps it), with its own settings.
	FrontDoor    string `json:"front_door,omitempty"`
	ProxyAddress string `json:"proxy_address,omitempty"`
	TURNUDPPort  int    `json:"turn_udp_port,omitempty"`
	// DoorDone: the front door's steps (ServerPreview.Setup) are done.
	DoorDone bool `json:"door_done,omitempty"`
}

// Moves reports whether c changes the domain or the front door: Linx's
// web address, and what reaches it.
func (c ServerChange) Moves() bool { return c.Domain != "" || c.FrontDoor != "" }

// normalize puts an old-style Token into Key, and drops an empty Key.
func (c *ServerChange) normalize() {
	if c.Key == nil && strings.TrimSpace(c.Token) != "" {
		c.Key = &DNSKey{Token: c.Token}
	}
	c.Token = ""
	if c.Key != nil && c.Key.Empty() {
		c.Key = nil
	}
}

// ServerPreview is what a change needs before it can be made, and what it
// will do (docs/ui/INSTALL_SCREENS.md §5.2).
type ServerPreview struct {
	// Errors are refusals, in plain words, per field.
	Errors []FieldError `json:"errors,omitempty"`
	// AddRecords are DNS records to add by hand first (no DNS token).
	AddRecords []Record `json:"add_records,omitempty"`
	// Setup is the front door's steps, to do first (nil: none).
	Setup *DoorSetup `json:"setup,omitempty"`
	// Warnings are what else the change means, one sentence each.
	Warnings []string `json:"warnings,omitempty"`
	// Steps are the rows Apply will show.
	Steps []string `json:"steps,omitempty"`
	// Address is Linx's web address after the change.
	Address string `json:"address"`
}

// SettingsApplier is the host's side (cmd/linx; tests fake it).
type SettingsApplier interface {
	// View is the settings as they are now.
	View(ctx context.Context) (ServerView, error)
	// Preview checks c, changing nothing (a new DNS token is asked about
	// at the DNS company, read-only), and says what it needs and does.
	Preview(ctx context.Context, c ServerChange) (ServerPreview, error)
	// Run applies c, reporting each row as Applier.Run does.
	Run(ctx context.Context, c ServerChange, report func(i int, state, detail string), keep func(KeepItem)) error
}

var (
	errApplying   = errors.New("A change is still being made. Wait for it to finish.")
	errNoChange   = errors.New("Nothing to change.")
	errBadProfile = errors.New("That size isn't one of the choices.")
	errBadDoor    = errors.New("That isn't one of the front doors this server can use.")
)

// Step names a ServerPreview's FieldErrors use besides the install's.
const StepDoorDone = "door_done"

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
	// RepairPath is the repair link's state file (RepairPath), when the
	// page is also open on port 6464; "" otherwise.
	RepairPath string

	mu     sync.Mutex
	view   ServerView
	repair RepairState
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
	var rs RepairState
	if h.RepairPath != "" {
		if rs, err = LoadRepairState(h.RepairPath); err != nil {
			return err
		}
		// The link's own time: a restarted service keeps it.
		v.ExpiresAt, v.Repair, v.NoSignIn, v.Problem = rs.ExpiresAt, true, rs.NoSignIn, rs.Problem
	}
	h.mu.Lock()
	h.view, h.repair = v, rs
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
		h.mu.Lock()
		rs := h.repair
		h.mu.Unlock()
		out := &View{ExpiresAt: v.ExpiresAt, Server: &v, SessionHash: rs.SessionHash}
		if rs.Secret != "" {
			out.LinkHash = Hash(rs.Secret)
		}
		return send(Message{Type: TypeView, View: out})
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
		case TypeClaim:
			res.OK = h.claim(m)
			if res.OK {
				h.changed(func(*ServerView) {})
			}
		case TypeServerPreview:
			if m.Change == nil {
				res.Error = errNoChange.Error()
				break
			}
			if p, err := h.preview(ctx, *m.Change); err != nil {
				res.Error = err.Error()
			} else {
				res.OK, res.Preview = true, &p
			}
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

// preview checks what the page may change, then asks setup what the
// change needs.
func (h *SettingsHost) preview(ctx context.Context, c ServerChange) (ServerPreview, error) {
	c.normalize()
	v := h.Snapshot()
	switch {
	case c.Portainer && !v.PortainerAllowed:
		return ServerPreview{}, errNoPortainer
	case c.FrontDoor != "" && !slices.Contains(v.FrontDoors, c.FrontDoor):
		return ServerPreview{}, errBadDoor
	}
	valid := false
	for _, o := range v.Profiles {
		valid = valid || o.Name == c.Profile
	}
	if !valid {
		return ServerPreview{}, errBadProfile
	}
	if c.Domain == v.Domain {
		c.Domain = ""
	}
	if c.FrontDoor == v.FrontDoor && c.ProxyAddress == v.ProxyAddress && c.TURNUDPPort == v.TURNUDPPort {
		c.FrontDoor, c.ProxyAddress, c.TURNUDPPort = "", "", 0
	}
	if c.DNSByHand != nil && (*c.DNSByHand == v.DNSByHand || v.Token == "") {
		c.DNSByHand = nil
	}
	if c.Profile == v.Profile && c.Portainer == v.Portainer && c.Key == nil && c.DNSByHand == nil && !c.Moves() {
		return ServerPreview{}, errNoChange
	}
	return h.Apply.Preview(ctx, c)
}

// change checks c and starts making it in the background.
func (h *SettingsHost) change(ctx context.Context, c *ServerChange) ([]FieldError, error) {
	if c == nil {
		return nil, errNoChange
	}
	c.normalize()
	if h.Snapshot().Apply.State == StageRunning {
		return nil, errApplying
	}
	p, err := h.preview(ctx, *c)
	if err != nil {
		return nil, err
	}
	if len(p.Errors) > 0 {
		return p.Errors, nil
	}
	if p.Setup != nil && c.Moves() && !c.DoorDone {
		return []FieldError{{Step: StepDoorDone, Field: "door_done",
			Message: "Do the steps shown for what's in front of this server first, then tick that they're done."}}, nil
	}
	steps := make([]InstallStep, len(p.Steps))
	for i, t := range p.Steps {
		steps[i] = InstallStep{Title: t}
	}
	h.changed(func(v *ServerView) {
		v.Steps, v.Keep, v.Apply = steps, nil, Stage{State: StageRunning, At: h.now().UTC()}
	})
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
			steps, keep, exp, problem, repair, noSignIn := v.Steps, v.Keep, v.ExpiresAt, v.Problem, v.Repair, v.NoSignIn
			*v = nv
			v.Steps, v.Keep, v.ExpiresAt, v.Repair, v.NoSignIn = steps, keep, exp, repair, noSignIn
			if !c.Moves() {
				v.Problem = problem
			}
		}
		v.Apply = Stage{State: StageOK, At: h.now().UTC()}
	})
}
