package install

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"linxpbx.com/linx/internal/certs"
)

// The certificate page (docs/INSTALL.md §4, docs/ui/INSTALL_SCREENS.md
// §2.6–2.7 and §3.1): once the answers are saved, the host gets Linx's
// first certificate and the page ticks itself off. With a front door that
// passes port 443 through, Let's Encrypt checks the names on port 443
// (CertPort443: the DNS record to add, then a test certificate, then the
// real one); with one that can't (Caddy or Nginx Proxy Manager, or home
// only), the DNS company's token is asked on the plain page instead
// (CertToken). Then the page moves to https://<domain> with a
// one-time handoff.

// Certificate page modes.
const (
	CertPort443 = "port443"
	CertToken   = "token"
)

// Stage states.
const (
	StageRunning = "running"
	StageOK      = "ok"
	StageFailed  = "failed"
)

// DNS states: what a name points at right now.
const (
	DNSMissing = "missing" // no record yet
	DNSWrong   = "wrong"   // it points somewhere else
	DNSOK      = "ok"
	DNSError   = "error" // the look-up itself failed
)

// HandoffLifetime is how long a handoff to the secure page works.
const HandoffLifetime = 2 * time.Minute

// CertView is the certificate page, as the host tells it.
type CertView struct {
	Mode      string `json:"mode"`
	Domain    string `json:"domain"`
	FrontDoor string `json:"front_door"`
	// Records are the DNS records to add by hand (CertPort443): one per
	// name on the first certificate.
	AddRecords []Record `json:"add_records,omitempty"`
	// Setup is what to do on the front door first (nil: nothing).
	Setup *DoorSetup `json:"setup,omitempty"`
	DNS   DNSCheck   `json:"dns"`
	// Prepare: starting what the check and the browser reach (the web
	// port, and Linx's own port 443 router).
	Prepare Stage `json:"prepare"`
	// Reach: the test certificate, which proves Let's Encrypt reaches
	// this server on port 443 (CertPort443).
	Reach Stage `json:"reach"`
	// Records: Linx pointing its names in DNS with the token (CertToken).
	Records Stage `json:"records"`
	// Certificate: the real one.
	Certificate Stage `json:"certificate"`
	// TokenSaved: the token is on the server (CertToken).
	TokenSaved bool `json:"token_saved,omitempty"`
	// SecureURL is https://<domain>, once the certificate is ready.
	SecureURL string `json:"secure_url,omitempty"`
}

// Ready reports whether the real certificate is deployed.
func (c *CertView) Ready() bool { return c != nil && c.Certificate.State == StageOK }

// Record is a DNS record to add.
type Record struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DoorSetup is the front door's own steps: a block to paste, router
// forwards. The page asks to tick it off before Let's Encrypt is asked.
type DoorSetup struct {
	Files []SetupFile `json:"files,omitempty"`
	// Steps are plain-words steps, one per line.
	Steps []string `json:"steps,omitempty"`
	Done  bool     `json:"done,omitempty"`
}

// SetupFile is a generated block to paste somewhere.
type SetupFile struct {
	Title string `json:"title"`
	// Where it goes on that machine.
	Path string `json:"path,omitempty"`
	Text string `json:"text"`
}

// DNSCheck is the last look at the records to add: State is DNSOK once
// every one points here, else the first one's that doesn't.
type DNSCheck struct {
	State     string      `json:"state,omitempty"`
	Names     []NameCheck `json:"names,omitempty"`
	CheckedAt time.Time   `json:"checked_at,omitzero"`
}

// NameCheck is what one name points at right now.
type NameCheck struct {
	Name  string   `json:"name"`
	State string   `json:"state"`
	Seen  []string `json:"seen,omitempty"`
}

// Stage is one step: not started (""), running, ok or failed.
type Stage struct {
	State string `json:"state,omitempty"`
	// Kind is certs.Problem* when it failed.
	Kind   string    `json:"kind,omitempty"`
	Detail string    `json:"detail,omitempty"`
	At     time.Time `json:"at,omitzero"`
}

// Certifier is what the host does for the certificate page (linx setup's
// real one runs docker compose; tests fake it).
type Certifier interface {
	// Plan is the certificate page for the saved answers.
	Plan(ctx context.Context, a Answers, f Facts) (CertView, error)
	// Prepare starts what Let's Encrypt and the browser reach: the control
	// plane's HTTPS port and the front door Linx runs itself.
	Prepare(ctx context.Context, c CertView) error
	// Lookup is name's addresses as the domain's own name servers give
	// them (empty when there's no record).
	Lookup(ctx context.Context, name string) ([]string, error)
	// Obtain gets the certificate through port 443: the test one only
	// proves it works, the real one is deployed.
	Obtain(ctx context.Context, staging bool) error
	// SaveToken checks the DNS company's token and keeps it as the host's
	// secret file. A refusal is plain words for the page.
	SaveToken(ctx context.Context, c CertView, token string) (refusal string, err error)
	// Records points Linx's names in DNS with the saved token.
	Records(ctx context.Context) error
	// ObtainWithToken gets the certificate with the saved token.
	ObtainWithToken(ctx context.Context) error
}

// certPlan starts the certificate page once the answers are saved.
func (h *Host) certPlan(ctx context.Context, a Answers) {
	if h.Cert == nil {
		return
	}
	h.mu.Lock()
	have, facts := h.st.View.Cert != nil, h.st.View.Facts
	h.mu.Unlock()
	if have {
		return
	}
	cv, err := h.Cert.Plan(ctx, a, facts)
	if err != nil {
		h.log().Error("planning the certificate page", "err", err)
		cv = CertView{Domain: a.Domain, FrontDoor: a.FrontDoor, Prepare: Stage{State: StageFailed, Kind: certs.ProblemOther, Detail: err.Error()}}
	}
	h.mu.Lock()
	if h.st.View.Cert == nil {
		h.st.View.Cert = &cv
		h.saveLocked()
		h.changedLocked()
	}
	h.mu.Unlock()
	h.kickCert()
}

func (h *Host) kickCert() {
	h.mu.Lock()
	k := h.kick
	h.mu.Unlock()
	if k != nil {
		select {
		case k <- struct{}{}:
		default:
		}
	}
}

// updateCert changes the certificate page and tells the control plane.
func (h *Host) updateCert(f func(c *CertView), progress ...Progress) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.st.View.Cert == nil {
		return
	}
	c := *h.st.View.Cert
	f(&c)
	h.st.View.Cert = &c
	h.st.Progress = append(h.st.Progress, progress...)
	h.saveLocked()
	h.changedLocked()
}

func (h *Host) cert() *CertView {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.st.View.Cert == nil {
		return nil
	}
	c := *h.st.View.Cert
	return &c
}

func (h *Host) line(text string, waiting, failed bool) Progress {
	return Progress{At: h.now().UTC(), Text: text, Waiting: waiting, Failed: failed}
}

func stageFailed(err error, at time.Time) Stage {
	p := certs.Classify(err)
	return Stage{State: StageFailed, Kind: p.Kind, Detail: p.Detail, At: at.UTC()}
}

func (h *Host) poll() time.Duration {
	if h.Poll > 0 {
		return h.Poll
	}
	return 5 * time.Second
}

// runCert works through the certificate page until the certificate is
// ready or ctx ends. Nothing is asked of Let's Encrypt again after a
// failure until the page says to try again.
func (h *Host) runCert(ctx context.Context) {
	for ctx.Err() == nil {
		h.certStep(ctx)
		select {
		case <-ctx.Done():
		case <-h.kick:
		case <-time.After(h.poll()):
		}
	}
}

func (h *Host) certStep(ctx context.Context) {
	c := h.cert()
	if c == nil || c.Ready() {
		return
	}
	now := h.now().UTC()
	switch c.Prepare.State {
	case StageFailed, StageRunning:
		return
	case "":
		h.updateCert(func(c *CertView) { c.Prepare = Stage{State: StageRunning, At: now} })
		if err := h.Cert.Prepare(ctx, *c); err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log().Error("starting the web port", "err", err)
			h.updateCert(func(c *CertView) {
				c.Prepare = Stage{State: StageFailed, Kind: certs.ProblemOther, Detail: err.Error(), At: h.now().UTC()}
			}, h.line("Couldn't start Linx's web port: "+firstLine(err.Error()), false, true))
			return
		}
		h.updateCert(func(c *CertView) { c.Prepare = Stage{State: StageOK, At: h.now().UTC()} })
		c = h.cert()
	}

	if c.Mode == CertToken {
		h.tokenStep(ctx, c)
		return
	}

	// Port 443: DNS first, every name looked at on every step.
	dns := DNSCheck{State: DNSOK}
	var names []string
	for _, r := range c.AddRecords {
		seen, err := h.Cert.Lookup(ctx, r.Name)
		nc := NameCheck{Name: r.Name, State: dnsState(seen, err, r.Value), Seen: seen}
		dns.Names = append(dns.Names, nc)
		names = append(names, r.Name)
		if nc.State != DNSOK && dns.State == DNSOK {
			dns.State = nc.State
		}
	}
	dns.CheckedAt = h.now().UTC()
	var lines []Progress
	if dns.State == DNSOK && c.DNS.State != DNSOK {
		lines = append(lines, h.line(strings.Join(names, " and ")+" point at this server", false, false))
	} else if dns.State != DNSOK && c.DNS.State == "" && len(c.AddRecords) > 0 {
		lines = append(lines, h.line("Waiting for "+strings.Join(names, " and ")+" to point at "+c.AddRecords[0].Value, true, false))
	}
	h.updateCert(func(c *CertView) { c.DNS = dns }, lines...)
	if dns.State != DNSOK || (c.Setup != nil && !c.Setup.Done) {
		return
	}

	if c.Reach.State == "" {
		h.updateCert(func(c *CertView) { c.Reach = Stage{State: StageRunning, At: h.now().UTC()} },
			h.line("Asking Let's Encrypt to check port 443 (test certificate)", true, false))
		if err := h.Cert.Obtain(ctx, true); err != nil {
			if ctx.Err() != nil {
				return
			}
			st := stageFailed(err, h.now())
			h.updateCert(func(c *CertView) { c.Reach = st },
				h.line("Let's Encrypt couldn't reach this server on port 443: "+st.Detail, false, true))
			return
		}
		h.updateCert(func(c *CertView) { c.Reach = Stage{State: StageOK, At: h.now().UTC()} },
			h.line("Let's Encrypt reached this server on port 443", false, false))
		c = h.cert()
	}
	if c.Reach.State != StageOK || c.Certificate.State != "" {
		return
	}
	h.realCert(ctx, func(ctx context.Context) error { return h.Cert.Obtain(ctx, false) })
}

func (h *Host) tokenStep(ctx context.Context, c *CertView) {
	if !c.TokenSaved {
		return
	}
	if c.Records.State == "" {
		h.updateCert(func(c *CertView) { c.Records = Stage{State: StageRunning, At: h.now().UTC()} },
			h.line("Pointing "+c.Domain+" and turn."+c.Domain+" at this server (DNS)", true, false))
		if err := h.Cert.Records(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			st := Stage{State: StageFailed, Kind: certs.ProblemOther, Detail: firstLine(err.Error()), At: h.now().UTC()}
			h.updateCert(func(c *CertView) { c.Records = st }, h.line("Couldn't set the DNS records: "+st.Detail, false, true))
			return
		}
		h.updateCert(func(c *CertView) { c.Records = Stage{State: StageOK, At: h.now().UTC()} },
			h.line("DNS records set", false, false))
		c = h.cert()
	}
	if c.Records.State != StageOK || c.Certificate.State != "" {
		return
	}
	h.realCert(ctx, h.Cert.ObtainWithToken)
}

func (h *Host) realCert(ctx context.Context, obtain func(context.Context) error) {
	h.updateCert(func(c *CertView) { c.Certificate = Stage{State: StageRunning, At: h.now().UTC()} },
		h.line("Getting the certificate (can take a few minutes)", true, false))
	if err := obtain(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		st := stageFailed(err, h.now())
		h.updateCert(func(c *CertView) { c.Certificate = st }, h.line("Couldn't get the certificate: "+st.Detail, false, true))
		return
	}
	h.updateCert(func(c *CertView) {
		c.Certificate = Stage{State: StageOK, At: h.now().UTC()}
		c.SecureURL = "https://" + c.Domain
	}, h.line("Certificate ready: https://"+h.cert().Domain, false, false))
}

func dnsState(seen []string, err error, want string) string {
	switch {
	case err != nil:
		return DNSError
	case len(seen) == 0:
		return DNSMissing
	case want == "":
		return DNSOK
	}
	for _, a := range seen {
		if a != want {
			return DNSWrong
		}
	}
	return DNSOK
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// resetCert makes a restarted host redo what didn't finish: the stack
// stopped with the old service, and a running step died with it.
func resetCert(c *CertView) {
	if c == nil || c.Ready() {
		return
	}
	c.Prepare = Stage{}
	for _, s := range []*Stage{&c.Reach, &c.Records, &c.Certificate} {
		if s.State == StageRunning {
			*s = Stage{}
		}
	}
}

// Errors the certificate page's requests can get, in plain words.
var (
	errNotYet   = errors.New("That isn't possible at this point of the install.")
	errNoCert   = errors.New("The certificate isn't ready yet.")
	errBadToken = errors.New("That link to the secure page can't be used. Go back to the first page and press Open the secure page again.")
)

// open reports whether the plain page's session can still act.
func (h *Host) plainOpenLocked() bool {
	v := h.st.View
	return v.Ended == "" && v.SessionHash != "" && !v.Secure && h.now().Before(v.ExpiresAt)
}

// doorReady is the page's "I've done this" for the front door's steps.
func (h *Host) doorReady() error {
	h.mu.Lock()
	ok := h.plainOpenLocked() && h.st.View.Cert != nil && h.st.View.Cert.Setup != nil
	h.mu.Unlock()
	if !ok {
		return errNotYet
	}
	h.updateCert(func(c *CertView) {
		// A copy: views already handed out share the old one.
		s := *c.Setup
		s.Done = true
		c.Setup = &s
	})
	h.kickCert()
	return nil
}

// retry clears a failed step so it runs again.
func (h *Host) retry() error {
	h.mu.Lock()
	ok := h.plainOpenLocked() && h.st.View.Cert != nil
	h.mu.Unlock()
	if !ok {
		return errNotYet
	}
	h.updateCert(func(c *CertView) {
		for _, s := range []*Stage{&c.Prepare, &c.Reach, &c.Records, &c.Certificate} {
			if s.State == StageFailed {
				*s = Stage{}
			}
		}
	})
	h.kickCert()
	return nil
}

// token keeps the DNS company's token (CertToken only).
func (h *Host) token(ctx context.Context, token string) ([]FieldError, error) {
	c := h.cert()
	h.mu.Lock()
	ok := h.plainOpenLocked()
	h.mu.Unlock()
	if !ok || c == nil || c.Mode != CertToken || c.Ready() {
		return nil, errNotYet
	}
	refusal, err := h.Cert.SaveToken(ctx, *c, token)
	if err != nil {
		return nil, err
	}
	if refusal != "" {
		return []FieldError{{Step: StepToken, Field: "token", Message: refusal}}, nil
	}
	h.updateCert(func(c *CertView) {
		c.TokenSaved = true
		// A new token tries again whatever failed with the old one.
		for _, s := range []*Stage{&c.Records, &c.Certificate} {
			if s.State == StageFailed {
				*s = Stage{}
			}
		}
	}, h.line("DNS token saved", false, false))
	h.kickCert()
	return nil, nil
}

// handoff makes a new one-time link to the secure page.
func (h *Host) handoff() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.plainOpenLocked() {
		return "", errNotYet
	}
	if !h.st.View.Cert.Ready() {
		return "", errNoCert
	}
	secret := NewSecret()
	h.st.HandoffHash, h.st.HandoffExpires = Hash(secret), h.now().Add(HandoffLifetime).UTC()
	h.saveLocked()
	return secret, nil
}

// redeem moves the session to the secure page, once: the plain page's
// cookie stops working, and the secure page's gets a new hour.
func (h *Host) redeem(m Message) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	st := &h.st
	if !h.plainOpenLocked() || st.HandoffHash == "" || !h.now().Before(st.HandoffExpires) ||
		!ValidSecret(m.Secret) || !ValidHash(m.SessionHash) ||
		subtle.ConstantTimeCompare([]byte(Hash(m.Secret)), []byte(st.HandoffHash)) != 1 {
		return errBadToken
	}
	st.HandoffHash, st.HandoffExpires = "", time.Time{}
	st.View.SessionHash, st.View.Secure = m.SessionHash, true
	st.View.ExpiresAt = h.now().Add(LinkLifetime).UTC()
	st.Progress = append(st.Progress, h.line("Moved to the secure page", false, false))
	h.saveLocked()
	h.changedLocked()
	return nil
}
