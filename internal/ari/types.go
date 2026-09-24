package ari

import (
	"strings"
	"time"
)

// Event is any ARI event, with the fields Linx reads (ARI's events.json).
type Event struct {
	Type        string       `json:"type"`
	Timestamp   Time         `json:"timestamp"`
	Application string       `json:"application"`
	Channel     *Channel     `json:"channel,omitempty"`
	Caller      *Channel     `json:"caller,omitempty"` // Dial
	Peer        *Channel     `json:"peer,omitempty"`   // Dial
	DialStatus  string       `json:"dialstatus,omitempty"`
	Cause       int          `json:"cause,omitempty"`
	CauseTxt    string       `json:"cause_txt,omitempty"`
	Endpoint    *Endpoint    `json:"endpoint,omitempty"`
	ContactInfo *ContactInfo `json:"contact_info,omitempty"`
}

// Channel is one call leg.
type Channel struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"` // PJSIP/d_7k2m9x4q-00000001
	State        string   `json:"state"`
	Caller       CallerID `json:"caller"`
	Connected    CallerID `json:"connected"`
	Dialplan     Dialplan `json:"dialplan"`
	CreationTime Time     `json:"creationtime"`
}

// Endpoint returns the PJSIP endpoint (a device's SIP username) a channel
// belongs to, or "" for any other kind of channel.
func (c Channel) Endpoint() string {
	rest, ok := strings.CutPrefix(c.Name, "PJSIP/")
	if !ok {
		return ""
	}
	if i := strings.LastIndexByte(rest, '-'); i > 0 {
		return rest[:i]
	}
	return rest
}

type CallerID struct {
	Name   string `json:"name"`
	Number string `json:"number"`
}

type Dialplan struct {
	Context  string `json:"context"`
	Exten    string `json:"exten"`
	Priority int    `json:"priority"`
	AppName  string `json:"app_name"`
}

// Endpoint is a PJSIP endpoint: one Linx device.
type Endpoint struct {
	Technology string   `json:"technology"`
	Resource   string   `json:"resource"`
	State      string   `json:"state"` // online, offline, unknown
	ChannelIDs []string `json:"channel_ids"`
}

// ContactInfo is a registration (ContactStatusChange).
type ContactInfo struct {
	URI           string `json:"uri"`
	ContactStatus string `json:"contact_status"` // Created, Removed, Reachable, Unreachable, ...
	AOR           string `json:"aor"`
}

// Time parses ARI's timestamps ("2025-03-17T08:32:35.709-0600").
type Time struct{ time.Time }

func (t *Time) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05-0700", time.RFC3339Nano} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return nil
}
