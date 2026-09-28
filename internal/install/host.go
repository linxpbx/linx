package install

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// HostPath is where linx setup keeps the install's state on the host: root
// only. It holds the link's secret until a browser opens it, so running
// `sudo linx setup` again can show the same link.
const HostPath = "/etc/linx/install-state.json"

// Unit is the systemd unit linx setup runs the install in, so it carries on
// when the terminal closes (docs/INSTALL.md §3).
const Unit = "linx-setup.service"

// HostState is the install as the host sees it.
type HostState struct {
	// Secret is the link's secret, until it's claimed.
	Secret string `json:"secret,omitempty"`
	View   View   `json:"view"`
	// Browser and Address: who opened the link.
	Browser  string     `json:"browser,omitempty"`
	Address  string     `json:"address,omitempty"`
	Progress []Progress `json:"progress,omitempty"`
}

// Progress is one line the terminal shows under the link.
type Progress struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
	// Waiting: still going (… rather than ✓).
	Waiting bool `json:"waiting,omitempty"`
	// Failed: ✕.
	Failed bool `json:"failed,omitempty"`
}

// NewHostState starts an install with a new link.
func NewHostState(now time.Time, facts Facts) HostState {
	secret := NewSecret()
	return HostState{Secret: secret, View: View{LinkHash: Hash(secret), ExpiresAt: now.Add(LinkLifetime).UTC(), Facts: facts}}
}

// Live reports whether the link, or the session that claimed it, is still
// open at now.
func (st HostState) Live(now time.Time) bool {
	return st.View.Ended == "" && now.Before(st.View.ExpiresAt) && (st.Secret != "" || st.View.SessionHash != "")
}

// LoadHostState reads the state file.
func LoadHostState(path string) (HostState, error) {
	var st HostState
	b, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("%s: %w", path, err)
	}
	return st, nil
}

// Save writes the state file atomically, root only.
func (st HostState) Save(path string) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
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
	return os.Rename(tmp.Name(), path)
}

// Cancel ends the link or session in the state file at path (sudo linx
// setup --new-link, with the service stopped): the next start makes a new
// link and keeps the answers given so far.
func Cancel(path string) error {
	st, err := LoadHostState(path)
	if err != nil {
		return err
	}
	st.Secret, st.View.LinkHash, st.View.SessionHash = "", "", ""
	if st.View.Ended == "" {
		st.View.Ended = EndedCancelled
	}
	return st.Save(path)
}

// Host is linx setup's side of the bridge, running as the linx-setup
// service: it keeps the state, decides claims and checks answers.
type Host struct {
	// Path is the state file (HostPath).
	Path string
	// Facts finds what the pages show as facts, for a new link.
	Facts func(ctx context.Context) Facts
	// Dial opens the bridge: docker exec -i into the control plane's
	// install-bridge, in real use.
	Dial func(ctx context.Context) (io.ReadWriteCloser, error)
	// Check validates answers the way setup.yaml is validated and, if
	// they're right, saves them. It returns the terminal's progress line.
	Check func(ctx context.Context, a Answers) (progress string, errs []FieldError, err error)
	// OnEnd runs once when the page closes (the link expired or was
	// cancelled): it stops the install's stack.
	OnEnd func(ctx context.Context, reason string)
	Now   func() time.Time
	Log   *slog.Logger
	// Retry is the wait between bridge attempts (default 2 s).
	Retry time.Duration

	mu     sync.Mutex
	st     HostState
	notify chan struct{}
}

func (h *Host) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Host) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// State is a copy of the current state.
func (h *Host) State() HostState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.st
}

// Start loads the state file, or starts a new link if there's none still
// open, and saves it.
func (h *Host) Start(ctx context.Context) error {
	st, err := LoadHostState(h.Path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		h.log().Warn("install state unreadable, starting a new link", "err", err)
	}
	if err != nil || !st.Live(h.now()) {
		var facts Facts
		if h.Facts != nil {
			facts = h.Facts(ctx)
		}
		old := st
		st = NewHostState(h.now(), facts)
		// A new link after the hour, or --new-link: the answers given so
		// far carry over, so whoever opens it doesn't start again.
		st.View.Draft, st.View.Accepted = old.View.Draft, old.View.Accepted
	}
	h.mu.Lock()
	h.st = st
	h.mu.Unlock()
	return st.Save(h.Path)
}

// Run keeps the bridge open until the page closes or ctx ends. Start must
// have run.
func (h *Host) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.watchExpiry(ctx, cancel)
	retry := h.Retry
	if retry <= 0 {
		retry = 2 * time.Second
	}
	for ctx.Err() == nil {
		conn, err := h.Dial(ctx)
		if err != nil {
			h.log().Warn("can't reach the install page yet", "err", err)
		} else {
			if err := h.Serve(ctx, conn); err != nil && ctx.Err() == nil {
				h.log().Warn("link to the install page ended", "err", err)
			}
			conn.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(retry):
		}
	}
}

func (h *Host) watchExpiry(ctx context.Context, done context.CancelFunc) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		h.mu.Lock()
		expired := h.st.View.Ended == "" && !h.now().Before(h.st.View.ExpiresAt)
		ended := h.st.View.Ended
		h.mu.Unlock()
		if expired {
			h.End(ctx, EndedExpired)
			ended = EndedExpired
		}
		if ended != "" {
			// Give the page a moment to hear it closed.
			select {
			case <-ctx.Done():
			case <-time.After(2 * time.Second):
			}
			done()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// End closes the page for reason, once.
func (h *Host) End(ctx context.Context, reason string) {
	h.mu.Lock()
	if h.st.View.Ended != "" {
		h.mu.Unlock()
		return
	}
	h.st.Secret, h.st.View.LinkHash, h.st.View.Ended = "", "", reason
	text := "The link expired. Run sudo linx setup again for a new one."
	if reason == EndedCancelled {
		text = "This link was cancelled."
	}
	h.st.Progress = append(h.st.Progress, Progress{At: h.now().UTC(), Text: text, Failed: true})
	h.saveLocked()
	h.changedLocked()
	h.mu.Unlock()
	if h.OnEnd != nil {
		h.OnEnd(context.WithoutCancel(ctx), reason)
	}
}

func (h *Host) saveLocked() {
	if err := h.st.Save(h.Path); err != nil {
		h.log().Error("saving the install state", "err", err)
	}
}

func (h *Host) changedLocked() {
	if h.notify != nil {
		select {
		case h.notify <- struct{}{}:
		default:
		}
	}
}

// Serve answers one bridge connection until it ends.
func (h *Host) Serve(ctx context.Context, rw io.ReadWriter) error {
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
		h.mu.Lock()
		v := h.st.View
		h.mu.Unlock()
		return send(Message{Type: TypeView, View: &v})
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
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		switch m.Type {
		case TypeClaim:
			if m.ID == "" || len(m.ID) > 64 {
				continue
			}
			_ = send(Message{Type: TypeResult, ID: m.ID, OK: h.claim(m)})
		case TypeCheck:
			if m.ID == "" || len(m.ID) > 64 {
				continue
			}
			res := h.check(ctx, m)
			res.Type, res.ID = TypeResult, m.ID
			_ = send(res)
		case TypeDraft:
			h.draft(m.Draft)
		}
	}
	cancel()
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

// browserNames are the only browser names the terminal will print.
var browserNames = []string{"Edge", "Firefox", "Chrome", "Safari", "a browser"}

// claim decides whether the link the control plane was given is this
// install's, still unclaimed and in time. The host checks the secret
// itself: the control plane's word isn't enough.
func (h *Host) claim(m Message) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := &h.st
	if st.View.Ended != "" || st.Secret == "" || !h.now().Before(st.View.ExpiresAt) ||
		!ValidHash(m.SessionHash) || !ValidSecret(m.Secret) ||
		subtle.ConstantTimeCompare([]byte(m.Secret), []byte(st.Secret)) != 1 {
		return false
	}
	st.Secret, st.View.LinkHash = "", ""
	st.View.SessionHash, st.View.ClaimedAt = m.SessionHash, h.now().UTC()
	st.Browser = "a browser"
	if slices.Contains(browserNames, m.Browser) {
		st.Browser = m.Browser
	}
	st.Address = ""
	if a, err := netip.ParseAddr(m.Address); err == nil {
		st.Address = a.String()
	}
	text := "Link opened (" + st.Browser
	if st.Address != "" {
		text += ", " + st.Address
	}
	st.Progress = append(st.Progress, Progress{At: h.now().UTC(), Text: text + ")"})
	h.saveLocked()
	h.changedLocked()
	return true
}

func (h *Host) check(ctx context.Context, m Message) Message {
	h.mu.Lock()
	open := h.st.View.Ended == "" && h.st.View.SessionHash != "" && h.now().Before(h.st.View.ExpiresAt)
	h.mu.Unlock()
	if !open {
		return Message{Error: "This setup link has closed. Run sudo linx setup again for a new one."}
	}
	if m.Answers == nil {
		return Message{Error: "No answers were sent."}
	}
	a := *m.Answers
	text, errs, err := h.Check(ctx, a)
	if err != nil {
		h.log().Error("saving the install answers", "err", err)
		return Message{Error: "Setup couldn't save your answers on the server: " + err.Error()}
	}
	if len(errs) > 0 {
		return Message{Errors: errs}
	}
	h.mu.Lock()
	h.st.View.Accepted = &a
	h.st.Progress = append(h.st.Progress, Progress{At: h.now().UTC(), Text: text})
	h.saveLocked()
	h.changedLocked()
	h.mu.Unlock()
	return Message{OK: true}
}

func (h *Host) draft(d json.RawMessage) {
	if len(d) == 0 || len(d) > MaxDraft || d[0] != '{' || !json.Valid(d) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.st.View.Ended != "" || h.st.View.SessionHash == "" {
		return
	}
	h.st.View.Draft = append(json.RawMessage(nil), d...)
	h.saveLocked()
}
