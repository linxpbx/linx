// Package push is how Linx rings a sleeping iPhone or iPad (ADR-074,
// docs/PHASE2.md §5): when a call comes for someone whose app isn't
// connected, Linx asks Apple to wake it, and the dialplan holds the call
// ringing for a few seconds so it is still there when the phone arrives.
// The same gateway sends the quiet notifications for a missed call and a
// new voicemail (owner, 2026-10-03).
//
// What Apple is told is a call's id and the caller's number, and nothing
// else: no name, no token, no SIP password. The push is the only part of a
// Linx call that touches anyone else's computer, which is why it carries so
// little.
//
// The key that signs the pushes is the owner's own, sealed in their own
// database (docs/PHASE2.md §6 (a)); there is no Linx relay in the middle.
// Caller mistakes come back as *apihttp.Error.
package push

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// Which Apple to send to. A build signed for development can only be
// reached on the sandbox, and a token from one is refused by the other.
const (
	Production = "production"
	Sandbox    = "sandbox"
)

// What a push is for. A VoIP push is only ever a real, ringing call: Apple
// kills an app that takes one without reporting a call to CallKit, and
// stops delivering to it after a few of those (docs/PHASE2.md §14 item 1).
const (
	KindWake      = "wake"
	KindMissed    = "missed_call"
	KindVoicemail = "voicemail"
)

// Limits (docs/PHASE2.md §5).
const (
	// DefaultWaitMS is how long a call waits for a woken phone. Apple's own
	// delivery is usually well under a second; this is the room for a phone
	// on a slow mobile network, and it is the longest a caller hears
	// ringing before the phones that are already there ring too.
	DefaultWaitMS = 6000
	MaxWaitMS     = 15000

	// WakesPerMinute bounds the wake pushes one device can be sent: a
	// caller redialling, or a loop in someone's routing, can't turn into a
	// stream of pushes.
	WakesPerMinute = 10
	// QuietPerHour bounds the missed-call and voicemail notifications one
	// device is sent.
	QuietPerHour = 60

	// SendTimeout bounds one request to Apple.
	SendTimeout = 10 * time.Second
	// TokenLife is how long one provider token is used. Apple refuses a
	// token older than an hour, and refuses being given a new one more
	// often than every 20 minutes.
	TokenLife = 45 * time.Minute
)

// Settings is the Apple key this server pushes with.
type Settings struct {
	// Tenant is whose settings these are; it is what the sealed key is
	// tied to, and never leaves the server.
	Tenant      uuid.UUID
	Enabled     bool
	TeamID      string
	KeyID       string
	BundleID    string
	Environment string
	WaitMS      int
	// HasKey: a signing key is stored. The key itself is never read back
	// out of the API.
	HasKey    bool
	Version   int
	UpdatedAt time.Time
}

// Input is a save of the Settings card. Key is the .p8 file's contents,
// empty to keep the one already stored.
type Input struct {
	Enabled     bool
	TeamID      string
	KeyID       string
	BundleID    string
	Environment string
	WaitMS      int
	Key         []byte
}

// Device is one app phone, as the gateway needs it.
type Device struct {
	DeviceID uuid.UUID
	Tenant   uuid.UUID
	// UserID is whose phone it is (for the quiet notifications).
	UserID uuid.UUID
	// SIPUsername is the AOR the dialplan knows it by.
	SIPUsername string
	VoIPToken   string
	AlertToken  string
	Environment string
}

// Call is what a wake push says: the call's id, who is calling, and when.
// Nothing else is sent, and nothing else is ever added here without a line
// in docs/THREAT_MODEL.md.
type Call struct {
	ID   string
	From string
	At   time.Time
}

// Store is the database access the gateway needs (internal/store
// implements it).
type Store interface {
	PushSettings(ctx context.Context) (Settings, []byte, error)
	SavePushSettings(ctx context.Context, s Settings, keyEnc []byte, ifVersion int, audit auth.AuditEntry) (Settings, error)
	// WakeDevices returns the app phones for these SIP usernames that have
	// a VoIP token and are still set up.
	WakeDevices(ctx context.Context, sipUsernames []string) ([]Device, error)
	// AlertDevices returns the app phones of whoever has this extension
	// that may show a quiet notification.
	AlertDevices(ctx context.Context, extension uuid.UUID) ([]Device, error)
	// ForgetPushToken clears a token Apple says is dead, so nothing is sent
	// to it again until the app gives Linx a new one.
	ForgetPushToken(ctx context.Context, device uuid.UUID, kind string, at time.Time) error
	// SavePushTokens is the app telling Linx where to reach it.
	SavePushTokens(ctx context.Context, device uuid.UUID, voip, alert, environment string, at time.Time) error
}

var (
	errSystemAdmin = &apihttp.Error{Status: http.StatusForbidden, Code: "system_admin_only",
		Detail: "Only a system admin can change how Linx rings app phones: it holds an Apple signing key."}
	errChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
		Detail: "Someone changed this setting since you loaded it. Reload and try again."}
	// ErrOff is a send asked for while push isn't set up.
	ErrOff = &apihttp.Error{Status: http.StatusConflict, Code: "push_off",
		Detail: "Calls to the app aren't set up on this server yet (System → Settings)."}
)

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

// Dead is Apple saying a token is no good: the app was removed, or the
// token belongs to the other Apple (sandbox against production).
type Dead struct{ Reason string }

func (e *Dead) Error() string { return "the phone's push token is dead: " + e.Reason }
