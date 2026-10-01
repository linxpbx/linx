package install

import (
	"context"
	"errors"
	"time"
)

// The secure page (docs/INSTALL.md §5, docs/ui/INSTALL_SCREENS.md §3):
// the DNS token (or Skip, on a rented server where Linx takes 443), the
// extras, then Install: the host runs the same plan as a terminal setup,
// step by step. Its last steps replace this install stack with the full
// one (same project and container names), which closes port 6464, and
// create the first admin with a set-password link whose token the page
// already holds, so the browser moves straight on to the first sign-in.

// Token choices on the secure page.
const (
	TokenSaved   = "saved"
	TokenSkipped = "skipped"
)

// Install step states are Stage states: "", running, ok, failed.

// FinishView is the secure page's part of the view.
type FinishView struct {
	// Token is "" (not answered yet), TokenSaved or TokenSkipped.
	Token string `json:"token,omitempty"`
	// SkipAllowed: Skip is offered (a rented server where Linx takes 443).
	SkipAllowed bool   `json:"skip_allowed,omitempty"`
	Provider    string `json:"provider,omitempty"`
	// Extras, once saved.
	Extras *Extras `json:"extras,omitempty"`
	// Profiles are the sizes offered, ProfilePick setup's pick for this
	// server's hardware and ProfileReason why.
	Profiles      []ProfileOption `json:"profiles,omitempty"`
	ProfilePick   string          `json:"profile_pick,omitempty"`
	ProfileReason string          `json:"profile_reason,omitempty"`
	// PortainerAllowed: Portainer is offered (a server at home: it's only
	// ever on the home network).
	PortainerAllowed bool `json:"portainer_allowed,omitempty"`
	// Steps are the install's steps, as they go.
	Steps []InstallStep `json:"steps,omitempty"`
	// Install is the whole install: running, ok or failed.
	Install Stage `json:"install"`
	// Switching: the full stack is starting in place of this page's; the
	// page waits for https://<domain> to answer as Linx itself.
	Switching bool `json:"switching,omitempty"`
	// SignInPath is /setup/<token>: the first admin's set-password link,
	// ready before the switch. Only the secure page's session sees it.
	SignInPath string `json:"sign_in_path,omitempty"`
	// Keep is what the owner must write down now (the internal CA's backup
	// passphrase; Portainer's password), shown once.
	Keep []KeepItem `json:"keep,omitempty"`
}

// Extras are §3.4's choices.
type Extras struct {
	// Profile is lite, standard or performance ("" = setup's pick).
	Profile   string `json:"profile"`
	Portainer bool   `json:"portainer"`
}

// ProfileOption is one size of server.
type ProfileOption struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// InstallStep is one row of the progress list.
type InstallStep struct {
	Title  string `json:"title"`
	State  string `json:"state,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// KeepItem is a secret the owner writes down, with what it's for.
type KeepItem struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

// ApplyInput is what the host's Applier needs.
type ApplyInput struct {
	Answers    Answers
	SkipToken  bool
	Extras     Extras
	SetupToken string
}

// Applier runs the full install on the host (linx setup's real one; tests
// fake it). Steps lists the rows up front; Run reports each row by index
// (StageRunning, then StageOK or StageFailed with plain words), calls
// switching just before the full stack replaces the install stack, and
// hands back what the owner must keep. It returns once the first admin
// exists.
type Applier interface {
	Steps(ctx context.Context, in ApplyInput) ([]string, error)
	Run(ctx context.Context, in ApplyInput, report func(i int, state, detail string), keep func(KeepItem), switching func()) error
	// SaveToken checks the DNS company's key and keeps it, and the
	// company, on the host.
	SaveToken(ctx context.Context, key DNSKey) (refusal string, err error)
	// Profiles are the sizes offered, and setup's pick for this server
	// with why.
	Profiles(ctx context.Context) (options []ProfileOption, pick, reason string)
}

const (
	// EndedFinished: the install finished and the full stack runs.
	EndedFinished = "finished"
	// EndedStopped: the install stopped after the full stack replaced the
	// installer's, so there's no install page left to try again on; the
	// terminal says what to do. Nothing is stopped.
	EndedStopped = "stopped"
)

var (
	errNeedsToken    = errors.New("Add your DNS company's token first (or Skip, where that's offered).")
	errNeedsExtras   = errors.New("Choose the extras first.")
	errInstallBegun  = errors.New("The install has already started.")
	errBadExtras     = errors.New("That size isn't one of the choices.")
	errSkipForbidden = errors.New("Skipping the token only works on a rented server where Linx takes port 443 itself.")
	errNoPortainer   = errors.New("Portainer is only offered on a server at home.")
)

// canChangeLocked: the token and extras can still change (the install
// hasn't started, or it stopped before the switch and can be tried again).
func (h *Host) canChangeLocked() bool {
	f := h.st.View.Finish
	return h.secureOpenLocked() && f != nil && f.Install.State != StageRunning && f.Install.State != StageOK && !h.st.Switched
}

// secureOpenLocked reports whether the secure page's session can act.
func (h *Host) secureOpenLocked() bool {
	v := h.st.View
	return v.Ended == "" && v.SessionHash != "" && v.Secure && h.now().Before(v.ExpiresAt)
}

func (h *Host) finish() FinishView {
	if h.st.View.Finish == nil {
		return FinishView{}
	}
	f := *h.st.View.Finish
	f.Steps = append([]InstallStep(nil), f.Steps...)
	f.Keep = append([]KeepItem(nil), f.Keep...)
	return f
}

func (h *Host) setFinish(fn func(f *FinishView), progress ...Progress) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f := h.finish()
	fn(&f)
	h.st.View.Finish = &f
	h.st.Progress = append(h.st.Progress, progress...)
	h.saveLocked()
	h.changedLocked()
}

// secureToken is the secure page's DNS token (§3.2). In token mode it was
// already asked on the plain page.
func (h *Host) secureToken(ctx context.Context, token DNSKey) ([]FieldError, error) {
	h.mu.Lock()
	ok := h.canChangeLocked()
	h.mu.Unlock()
	if !ok {
		return nil, errNotYet
	}
	refusal, err := h.Apply.SaveToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if refusal != "" {
		return []FieldError{{Step: StepToken, Field: "token", Message: refusal}}, nil
	}
	h.setFinish(func(f *FinishView) { f.Token = TokenSaved }, h.line("DNS token saved", false, false))
	return nil, nil
}

func (h *Host) skipToken() error {
	h.mu.Lock()
	ok := h.canChangeLocked()
	allowed := ok && h.st.View.Finish.SkipAllowed
	h.mu.Unlock()
	switch {
	case !ok:
		return errNotYet
	case !allowed:
		return errSkipForbidden
	}
	h.setFinish(func(f *FinishView) { f.Token = TokenSkipped }, h.line("DNS token skipped: the certificate renews through port 443", false, false))
	return nil
}

func (h *Host) extras(ctx context.Context, e *Extras) error {
	if e == nil {
		return errBadExtras
	}
	h.mu.Lock()
	ok := h.canChangeLocked()
	portainer := ok && h.st.View.Finish.PortainerAllowed
	h.mu.Unlock()
	if !ok {
		return errNotYet
	}
	if e.Portainer && !portainer {
		return errNoPortainer
	}
	options, _, _ := h.Apply.Profiles(ctx)
	valid := e.Profile == ""
	for _, o := range options {
		valid = valid || o.Name == e.Profile
	}
	if !valid {
		return errBadExtras
	}
	x := *e
	h.setFinish(func(f *FinishView) { f.Extras = &x })
	return nil
}

// beginInstall starts the install in the background (§3.5). A failed one
// can be started again: steps that finished are safe to repeat.
func (h *Host) beginInstall(ctx context.Context) error {
	h.mu.Lock()
	ok := h.secureOpenLocked() && h.st.View.Finish != nil && !h.st.Switched
	f := h.finish()
	var a Answers
	if h.st.View.Accepted != nil {
		a = *h.st.View.Accepted
	}
	h.mu.Unlock()
	switch {
	case !ok || a.Domain == "":
		return errNotYet
	case f.Install.State == StageRunning || f.Install.State == StageOK:
		return errInstallBegun
	case f.Token == "":
		return errNeedsToken
	case f.Extras == nil:
		return errNeedsExtras
	}
	token := setupTokenFromPath(f.SignInPath)
	if token == "" {
		token = NewSecret()
	}
	in := ApplyInput{Answers: a, SkipToken: f.Token == TokenSkipped, Extras: *f.Extras, SetupToken: token}
	titles, err := h.Apply.Steps(ctx, in)
	if err != nil {
		return err
	}
	steps := make([]InstallStep, len(titles))
	for i, t := range titles {
		steps[i] = InstallStep{Title: t}
	}
	h.setFinish(func(f *FinishView) {
		// Keep stays: what a failed try already made (the CA's passphrase)
		// isn't made again.
		f.Steps, f.Install, f.SignInPath = steps, Stage{State: StageRunning, At: h.now().UTC()}, "/setup/"+token
	}, h.line("Installing Linx", true, false))
	go h.runInstall(context.WithoutCancel(ctx), in)
	return nil
}

func setupTokenFromPath(p string) string {
	const prefix = "/setup/"
	if len(p) > len(prefix) && p[:len(prefix)] == prefix {
		return p[len(prefix):]
	}
	return ""
}

func (h *Host) runInstall(ctx context.Context, in ApplyInput) {
	report := func(i int, state, detail string) {
		h.setFinish(func(f *FinishView) {
			if i >= 0 && i < len(f.Steps) {
				f.Steps[i].State, f.Steps[i].Detail = state, detail
			}
		})
	}
	keep := func(k KeepItem) { h.setFinish(func(f *FinishView) { f.Keep = append(f.Keep, k) }) }
	switching := func() {
		h.mu.Lock()
		h.st.Switched = true
		h.mu.Unlock()
		h.setFinish(func(f *FinishView) { f.Switching = true },
			h.line("Starting the full Linx: this installer page (port 6464) closes for good", true, false))
		// Give the page a moment to hear it before the bridge goes.
		time.Sleep(h.switchPause())
	}
	err := h.Apply.Run(ctx, in, report, keep, switching)
	if err != nil {
		h.log().Error("installing Linx", "err", err)
		h.mu.Lock()
		switched := h.st.Switched
		h.mu.Unlock()
		if switched {
			// The page is gone with the installer's stack: the terminal
			// is the only place left to say so.
			h.setFinish(func(f *FinishView) {
				f.Install = Stage{State: StageFailed, Detail: firstLine(err.Error()), At: h.now().UTC()}
			}, h.line("The install stopped after Linx started: "+firstLine(err.Error()), false, true))
			h.End(ctx, EndedStopped)
			return
		}
		h.setFinish(func(f *FinishView) {
			f.Install = Stage{State: StageFailed, Detail: firstLine(err.Error()), At: h.now().UTC()}
			f.Switching = false
		}, h.line("The install stopped: "+firstLine(err.Error()), false, true))
		return
	}
	h.setFinish(func(f *FinishView) { f.Install = Stage{State: StageOK, At: h.now().UTC()} },
		h.line("Linx is installed. First sign-in: https://"+in.Answers.Domain+"/setup/"+in.SetupToken, false, false))
	h.End(ctx, EndedFinished)
}

func (h *Host) switchPause() time.Duration {
	if h.SwitchPause > 0 {
		return h.SwitchPause
	}
	return 2 * time.Second
}
