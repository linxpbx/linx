package ops

import (
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The link carries one JSON object per line, both ways.
//
// Agent → control plane: "status" (every StatusInterval, on connecting,
// and when asked) and "reply" (to a request, same ID).
// Control plane → agent: "request" (Op "status", "logs" or "restart").
const (
	TypeStatus  = "status"
	TypeReply   = "reply"
	TypeRequest = "request"

	OpStatus  = "status"
	OpLogs    = "logs"
	OpRestart = "restart"
)

// Reply error codes, so the control plane can say what happened in plain
// words without parsing the helper's text.
const (
	CodeUnknownService = "unknown_service"
	CodeNotAllowed     = "not_allowed"
	CodeTooSoon        = "too_soon"
	CodeBusy           = "busy"
	CodeFailed         = "failed"
	CodeBadRequest     = "bad_request"
)

// StatusInterval is how often the agent reports every container's state.
const StatusInterval = 10 * time.Second

// Message is one line on the link.
type Message struct {
	Type    string `json:"type"`
	ID      string `json:"id,omitempty"`
	Op      string `json:"op,omitempty"`
	Service string `json:"service,omitempty"`
	Lines   int    `json:"lines,omitempty"`

	// Replies.
	OK    bool      `json:"ok,omitempty"`
	Code  string    `json:"code,omitempty"`
	Error string    `json:"error,omitempty"`
	Log   []LogLine `json:"log,omitempty"`
	// Restart replies: "restarted", or "restarting" for the control plane
	// itself (the reply goes first: the link ends with the restart).
	Result string `json:"result,omitempty"`

	// Status.
	Containers []Container `json:"containers,omitempty"`
	CheckedAt  time.Time   `json:"checked_at,omitzero"`
}

// Container is one service's state, as Docker reports it.
type Container struct {
	Service string `json:"service"`
	// State is Docker's: running, restarting, exited, created, paused,
	// dead; "missing" when there's no such container.
	State string `json:"state"`
	// Health is Docker's health check result: healthy, unhealthy,
	// starting, or empty with no health check.
	Health    string    `json:"health,omitempty"`
	StartedAt time.Time `json:"started_at,omitzero"`
	Restarts  int       `json:"restarts"`
}

// LogLine is one line of a service's log.
type LogLine struct {
	Time time.Time `json:"time,omitzero"`
	Text string    `json:"text"`
}

// Plain states, what the API and the browser show (Summary).
const (
	StateRunning    = "running"
	StateStarting   = "starting"
	StateUnhealthy  = "unhealthy"
	StateRestarting = "restarting"
	StateStopped    = "stopped"
	StateMissing    = "missing"
)

// Summary turns Docker's state and health into one plain state.
func (c Container) Summary() string {
	switch c.State {
	case "running":
		switch c.Health {
		case "starting":
			return StateStarting
		case "unhealthy":
			return StateUnhealthy
		}
		return StateRunning
	case "restarting":
		return StateRestarting
	case StateMissing:
		return StateMissing
	}
	return StateStopped
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// CleanLine makes a log line safe to show as text: valid UTF-8, no
// terminal escape sequences or other control characters (tabs become
// spaces), at most maxLineRunes long.
func CleanLine(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = ansiEscape.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, " ")
	if utf8.RuneCountInString(s) > maxLineRunes {
		s = string([]rune(s)[:maxLineRunes]) + "…"
	}
	return s
}
