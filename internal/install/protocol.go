// Package install is the web-first install's link between the browser and
// the server (docs/INSTALL.md, ADR-057): `sudo linx setup` prints one link
// to plain HTTP on port 6464, the control plane in install mode shows the
// pages there (Server), and linx setup, running on the host as the
// linx-setup service (Host), decides everything: whether the link is
// right, and whether the answers are.
//
// The two talk over one `docker exec -i linx-control-plane service
// install-bridge`, the ops-agent pattern (ADR-056): nothing on the host
// listens. The browser only asks; a control plane someone broke into can
// suggest a different domain, and never run anything on the host (the host
// checks every answer with the same code as setup.yaml).
package install

import (
	"encoding/json"
	"time"
)

// Port is the plain-HTTP port of the install's first page.
const Port = 6464

// DefaultSocket is where the control plane in install mode listens for the
// host's bridge, in a tmpfs only its own user can open
// (deploy/compose/install.yaml).
const DefaultSocket = "/run/linx/install.sock"

// LinkLifetime is how long a link, and the browser session that claimed
// it, lasts: long enough to sign up with a DNS company on the way
// (docs/INSTALL.md §6, owner decision 2026-09-28).
const LinkLifetime = 4 * time.Hour

// The bridge carries one JSON object per line, both ways.
//
// Host → control plane: "view" (on connecting and after every change) and
// "result" (to a request, same ID).
// Control plane → host: "claim", "check", and the certificate page's
// "door_ready", "retry", "token", "handoff" and "redeem" (requests: the
// host answers with "result"), and "draft" (no answer).
const (
	TypeView      = "view"
	TypeResult    = "result"
	TypeClaim     = "claim"
	TypeCheck     = "check"
	TypeDraft     = "draft"
	TypeDoorReady = "door_ready"
	TypeRetry     = "retry"
	TypeToken     = "token"
	TypeHandoff   = "handoff"
	TypeRedeem    = "redeem"
	TypeSkipToken = "skip_token"
	TypeExtras    = "extras"
	TypeInstall   = "install"
)

// Message is one line on the bridge.
type Message struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`

	// view
	View *View `json:"view,omitempty"`

	// claim: the link's secret as the browser gave it, the hash of the
	// session the control plane made for that browser, and who it is.
	// redeem: the handoff as the secure page got it, and the hash of the
	// secure page's new session. handoff's result: the new handoff.
	Secret      string `json:"secret,omitempty"`
	SessionHash string `json:"session_hash,omitempty"`
	Browser     string `json:"browser,omitempty"`
	Address     string `json:"address,omitempty"`

	// draft
	Draft json.RawMessage `json:"draft,omitempty"`

	// check
	Answers *Answers `json:"answers,omitempty"`

	// token: the DNS company's token (docs/INSTALL.md §4.3).
	Token string `json:"token,omitempty"`

	// extras
	Extras *Extras `json:"extras,omitempty"`

	// result
	OK     bool         `json:"ok,omitempty"`
	Errors []FieldError `json:"errors,omitempty"`
	Error  string       `json:"error,omitempty"`
}

// View is what the host tells the control plane: all it needs to decide
// who sees the pages and what to show them. The host's own state
// (HostState) also holds the link's secret, which never leaves the host
// except in the terminal.
type View struct {
	// LinkHash is the SHA-256 of the link's secret while nobody has opened
	// it (Hash); empty once claimed.
	LinkHash string `json:"link_hash,omitempty"`
	// ExpiresAt ends the link, and the session that claimed it.
	ExpiresAt time.Time `json:"expires_at"`
	// SessionHash is the SHA-256 of the claiming browser's cookie.
	SessionHash string    `json:"session_hash,omitempty"`
	ClaimedAt   time.Time `json:"claimed_at,omitzero"`
	Facts       Facts     `json:"facts"`
	// Draft is the page's answers so far, kept so the same browser comes
	// back to the same step. Opaque to the host.
	Draft json.RawMessage `json:"draft,omitempty"`
	// Accepted is set once the host has checked and saved the answers.
	Accepted *Answers `json:"accepted,omitempty"`
	// Ended is "" while the install page is open, else why it closed.
	Ended string `json:"ended,omitempty"`
	// Cert is the certificate page, once the answers are saved.
	Cert *CertView `json:"cert,omitempty"`
	// Secure: the session moved to https://<domain> (the handoff was
	// used); SessionHash is then the secure page's cookie's, and the plain
	// page's no longer works.
	Secure bool `json:"secure,omitempty"`
	// Finish is the secure page's steps (FinishView), once there.
	Finish *FinishView `json:"finish,omitempty"`
}

// Why a link or session ended.
const (
	EndedExpired   = "expired"
	EndedCancelled = "cancelled"
)

// Facts are what setup found on the server, shown on the pages as facts
// rather than asked.
type Facts struct {
	// PublicAddress is the address this server reaches the internet from
	// (a home's router address, at home).
	PublicAddress string `json:"public_address,omitempty"`
	// LANAddress and LANNetwork: this server on a home or office network
	// (both empty on a server with its own public address).
	LANAddress string `json:"lan_address,omitempty"`
	LANNetwork string `json:"lan_network,omitempty"`
	// Where is what setup's detection suggests: WhereHome or WhereRented.
	Where string `json:"where"`
	// Port443 names the program already using TCP port 443 here, if any.
	Port443 string `json:"port_443,omitempty"`
	// TimeZone is this server's own clock setting, e.g. Etc/UTC.
	TimeZone string `json:"time_zone,omitempty"`
	// Hardware is a one-line summary: "4 processor cores, 8 GB memory".
	Hardware string `json:"hardware,omitempty"`
}

// Where the server is (docs/ui/INSTALL_SCREENS.md §2.2).
const (
	WhereHome   = "home"
	WhereRented = "rented"
)

// Answers are the plain page's questions (docs/ui/INSTALL_SCREENS.md
// §2.2–2.5). The host turns them into setup.yaml's installer.Config.
type Answers struct {
	Where        string `json:"where"`
	FrontDoor    string `json:"front_door"`
	ProxyAddress string `json:"proxy_address,omitempty"`
	TURNUDPPort  int    `json:"turn_udp_port,omitempty"`
	Domain       string `json:"domain"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	// TimeZone is the time zone for schedules (the browser's, unless
	// changed), e.g. Asia/Dubai.
	TimeZone string `json:"time_zone"`
	// AgreedToTerms: Let's Encrypt's Subscriber Agreement was ticked.
	AgreedToTerms bool `json:"agreed_to_terms"`
}

// FieldError is one answer the host refused, in plain words, with the step
// and field it belongs to so the page can go back there.
type FieldError struct {
	Step    string `json:"step"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// The plain page's steps, as FieldError.Step names them.
const (
	StepWhere     = "where"
	StepFrontDoor = "front_door"
	StepDomain    = "domain"
	StepYou       = "you"
	StepToken     = "token"
)

// MaxDraft is the most a page may keep as its draft.
const MaxDraft = 8 << 10
