// Package alert is Linx's admin alerting (ADR-029; docs/API.md §5): a
// channel is somewhere to send a short message (ntfy, Gotify, Slack,
// Microsoft Teams, Telegram or a generic signed webhook); an alert is a
// problem, identified by a `key` that stays open while the problem lasts.
// Feature code calls Fire/Resolve (in-process, like a log line); a
// background Engine decides when that becomes a real notification —
// holding back a flapping problem for 5 minutes, sending reminders while
// it stays open, respecting each channel's severity and quiet hours, and
// sending a "resolved" message once it clears.
package alert

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Severity, low to high.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// severityRank orders severities for the "at least this severe" filter.
var severityRank = map[string]int{SeverityInfo: 0, SeverityWarning: 1, SeverityCritical: 2}

// meetsSeverity reports whether severity is at least min.
func meetsSeverity(severity, min string) bool { return severityRank[severity] >= severityRank[min] }

// Channel kinds (docs/API.md §5).
const (
	KindNtfy     = "ntfy"
	KindGotify   = "gotify"
	KindSlack    = "slack"
	KindTeams    = "teams"
	KindTelegram = "telegram"
	KindWebhook  = "webhook" // generic, Standard Webhooks signed
)

// Alert statuses.
const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
)

// Delivery kinds and statuses.
const (
	DeliveryFired    = "fired"
	DeliveryReminder = "reminder"
	DeliveryResolved = "resolved"
	DeliveryTest     = "test"
	DeliveryDigest   = "digest"

	DeliveryHeld      = "held"
	DeliveryPending   = "pending"
	DeliverySucceeded = "succeeded"
	DeliveryFailed    = "failed"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrVersionChanged = errors.New("changed since it was read")
)

// QuietHours is a channel's do-not-disturb window in its own local time
// (docs/API.md §5). Start may be after End: the window then wraps past
// midnight (e.g. 22:00-07:00).
type QuietHours struct {
	Start, End     int // minutes since midnight, 0-1439
	Timezone       string
	BypassCritical bool
}

// Active reports whether t (in Timezone) falls inside the window.
func (q *QuietHours) Active(t time.Time) (bool, error) {
	if q == nil {
		return false, nil
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return false, err
	}
	minute := t.In(loc).Hour()*60 + t.In(loc).Minute()
	if q.Start <= q.End {
		return minute >= q.Start && minute < q.End, nil
	}
	// Wraps past midnight.
	return minute >= q.Start || minute < q.End, nil
}

// Config is a channel's kind-specific settings, sealed as one JSON object
// (ADR-030) under dbsecret row id "alert_channel:<id>". Fields apply per
// kind (docs/API.md §5); ValidateFor checks exactly the right ones are set.
type Config struct {
	ServerURL   string `json:"server_url,omitempty"`   // ntfy, gotify
	Topic       string `json:"topic,omitempty"`        // ntfy
	AccessToken string `json:"access_token,omitempty"` // ntfy
	AppToken    string `json:"app_token,omitempty"`    // gotify
	URL         string `json:"url,omitempty"`          // slack, teams, webhook
	BotToken    string `json:"bot_token,omitempty"`    // telegram
	ChatID      string `json:"chat_id,omitempty"`      // telegram
	// Secret signs a webhook channel's messages (Standard Webhooks). Linx
	// generates it when the channel is created; never set by the admin, so
	// it's never part of the API's Config and never listed in allFields.
	Secret string `json:"secret,omitempty"`
}

// Channel is a registered alert destination.
type Channel struct {
	ID, TenantID         uuid.UUID
	Kind, Name           string
	ConfigEnc            []byte
	MinSeverity          string
	QuietHours           *QuietHours
	Enabled              bool
	Version              int
	CreatedBy            string
	CreatedAt, UpdatedAt time.Time
}

// sealID is the additional data binding a sealed config to its channel.
func sealID(id uuid.UUID) string { return "alert_channel:" + id.String() }

// Alert is one problem, open or resolved.
type Alert struct {
	ID, TenantID                         uuid.UUID
	Key, Severity, Title, Message, Link  string
	Status                               string
	FirstSeenAt, LastSeenAt, StableSince time.Time
	NotifiedAt, LastReminderAt           *time.Time
	ResolvedAt, ResolvedNotifiedAt       *time.Time
}

// Delivery is one channel send: one alert, or several combined into a
// quiet-hours digest.
type Delivery struct {
	ID, TenantID, ChannelID uuid.UUID
	AlertIDs                []uuid.UUID
	Kind, Status            string
	Attempts, MaxAttempts   int
	NextAttemptAt           *time.Time
	CreatedAt               time.Time
	FinishedAt              *time.Time
}

// Attempt is one HTTP request in the delivery log.
type Attempt struct {
	At              time.Time
	StatusCode      *int
	DurationMS      int
	ResponseExcerpt *string
	Error           *string
}

// Job is a claimed delivery with what's needed to send it.
type Job struct {
	Delivery
	ChannelKind string // the channel's kind (ntfy, slack, ...); Delivery.Kind is fired/reminder/resolved/digest/test
	ConfigEnc   []byte
	Alerts      []Alert // the alert(s) this delivery covers, oldest first
}

// Outcome is the result of one attempt, to be recorded.
type Outcome struct {
	DeliveryID, ChannelID, TenantID uuid.UUID
	Attempt                         Attempt
	Status                          string
	Attempts                        int
	NextAttemptAt                   *time.Time
	Succeeded                       bool
}

// FireOptions change how a newly opened alert is held back.
type FireOptions struct {
	// StableSince backdates the start of the hold-back (StableFor): an
	// alert opened with StableSince = now - StableFor + 2 min notifies
	// after 2 minutes instead of 5. Zero: now.
	StableSince time.Time
	// OneShot: the alert tells of something that happened, not a problem
	// that lasts. It closes itself as it notifies, with no "resolved"
	// notice.
	OneShot bool
}

// Store is the database access alerts need (internal/store implements it).
type Store interface {
	CreateChannel(ctx context.Context, c Channel, audit auth.AuditEntry) error
	Channel(ctx context.Context, tenant, id uuid.UUID) (Channel, error)
	ListChannels(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]Channel, error)
	// EnabledChannels returns every enabled channel of tenant, for fan-out.
	EnabledChannels(ctx context.Context, tenant uuid.UUID) ([]Channel, error)
	// UpdateChannel saves c if its stored version is still c.Version and
	// returns it with the new version (ErrVersionChanged otherwise).
	UpdateChannel(ctx context.Context, c Channel, audit auth.AuditEntry) (Channel, error)
	DeleteChannel(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error

	// Fire opens (or re-affirms) the alert with key, bumping last_seen_at
	// (and severity/title/message/link, in case the source's description
	// changed). justOpened is true the moment it goes from none/resolved to
	// open (stable_since is reset then, restarting the flap hold-back).
	Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string, now time.Time) (a Alert, justOpened bool, err error)
	// FireWith is Fire with a different flap hold-back or a one-shot alert
	// (FireOptions).
	FireWith(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string, now time.Time, opts FireOptions) (a Alert, justOpened bool, err error)
	// Resolve closes the open alert with key, if there is one.
	Resolve(ctx context.Context, tenant uuid.UUID, key string, now time.Time) (a Alert, wasOpen bool, err error)
	ListAlerts(ctx context.Context, tenant uuid.UUID, status string, before *uuid.UUID, limit int) ([]Alert, error)

	// DueToNotify returns open alerts stable since before cutoff that have
	// never been notified.
	DueToNotify(ctx context.Context, cutoff time.Time, limit int) ([]Alert, error)
	// DueForReminder returns open, notified alerts whose last reminder (or
	// first notification) is older than cutoff.
	DueForReminder(ctx context.Context, cutoff time.Time, limit int) ([]Alert, error)
	// DueForResolvedNotice returns resolved alerts that were notified but
	// haven't had their "resolved" message sent.
	DueForResolvedNotice(ctx context.Context, limit int) ([]Alert, error)

	// Notify fans a's kind ('fired', 'reminder' or 'resolved') out to due
	// (a pending delivery, sent now) and held (a held delivery, for a
	// channel currently in quiet hours) channels, and in the same
	// transaction marks a's matching timestamp (notified_at, last_reminder_at
	// or resolved_notified_at) so it isn't picked up again. ev, if not nil,
	// is inserted as a webhook event alongside (fired/resolved only).
	Notify(ctx context.Context, a Alert, kind string, due, held []uuid.UUID, now time.Time, maxAttempts int, ev *WebhookEvent) error

	// HeldChannels returns every channel (of any tenant) that currently
	// has at least one held delivery, for the engine to check whether
	// their quiet hours have ended.
	HeldChannels(ctx context.Context) ([]Channel, error)
	// FlushHeld merges a channel's held deliveries into one pending digest
	// delivery due now.
	FlushHeld(ctx context.Context, channel uuid.UUID, now time.Time, maxAttempts int) error

	// ClaimAlertDeliveries takes due deliveries, leasing each until
	// leaseUntil, with the alerts and channel config each needs to send.
	// (Named distinctly from webhook.Store's ClaimDeliveries: internal/store
	// implements both interfaces on one type, so their method sets can't
	// collide.)
	ClaimAlertDeliveries(ctx context.Context, now, leaseUntil time.Time, limit int) ([]Job, error)
	// RecordAlertAttempt logs an attempt and advances the delivery.
	RecordAlertAttempt(ctx context.Context, o Outcome) error

	// CleanupAlerts drops finished deliveries and their log older than cutoff.
	CleanupAlerts(ctx context.Context, cutoff time.Time) error
}

// WebhookEvent is the minimal shape QueueDeliveries needs to also emit a
// webhook event (alert.fired / alert.resolved), without internal/alert
// importing internal/webhook (which would create an import cycle, since
// webhook.Worker fires the webhook.disabled alert). internal/store builds
// the real webhook.Event from this.
type WebhookEvent struct {
	Type string
	Data map[string]any
}
