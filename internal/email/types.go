// Package email is how Linx sends email (ADR-066, docs/PHASE1F.md §4):
// through an SMTP account the owner already has (a mail provider with an
// app password, or a sending service), never a mail server of its own.
// Always encrypted (TLS from the start, or STARTTLS) with the certificate
// checked; the password sealed (ADR-030); the server's address through
// the private-address guard, like webhooks. Emails wait in a database
// queue (sealed, since invites and resets carry links that work like
// passwords), are tried 3 times, at most HourlyLimit an hour, and are
// wiped once sent or given up on. Caller mistakes come back as
// *apihttp.Error.
package email

import (
	"context"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Security is how the connection is encrypted. There is no plain one.
const (
	// SecurityTLS: TLS from the start (port 465).
	SecurityTLS = "tls"
	// SecuritySTARTTLS: plain hello, then STARTTLS before anything else
	// (port 587); a server that doesn't offer it is refused.
	SecuritySTARTTLS = "starttls"
)

// Limits (docs/PHASE1F.md §4).
const (
	DefaultHourlyLimit = 60
	MaxHourlyLimit     = 1000
	// MaxAttempts is how often one email is tried.
	MaxAttempts = 3
	// SendTimeout bounds one attempt: connecting, encrypting, signing in
	// and sending.
	SendTimeout = 30 * time.Second
	// MaxRecipients bounds one email's To (an alert channel's list).
	MaxRecipients = 10
)

// retryDelays are the gaps after the first and second failed tries.
var retryDelays = []time.Duration{time.Minute, 15 * time.Minute}

// Kinds of email, for the queue and the audit.
const (
	KindTest   = "test"
	KindInvite = "invite"
	KindAlert  = "alert"
)

// Queue statuses.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
)

// Config is the tenant's email setting. PasswordEnc is sealed with
// ADR-030's key, row id SealID(TenantID).
type Config struct {
	TenantID    uuid.UUID
	Enabled     bool
	Preset      string
	Host        string
	Port        int
	Security    string
	Username    string
	FromAddress string
	FromName    string
	PasswordEnc []byte
	HourlyLimit int
	// ArrivedAt is when an admin said a test email arrived (the admin
	// home's "Set up email" ticks then); nil until then, and again after
	// the server or account changes.
	ArrivedAt *time.Time
	Version   int
	UpdatedAt time.Time
}

// SealID is the password's row id for dbsecret.
func SealID(tenant uuid.UUID) string { return "email_settings:" + tenant.String() }

// outboxSealID binds one queued email's sealed content to its row.
func outboxSealID(id uuid.UUID) string { return "email_outbox:" + id.String() }

// Message is one email in the queue. Its content (subject and bodies) is
// sealed as one JSON object in ContentEnc, and emptied once it's sent or
// given up on.
type Message struct {
	ID, TenantID  uuid.UUID
	Kind          string
	To            []string
	ContentEnc    []byte
	Status        string
	Attempts      int
	NextAttemptAt *time.Time
	LastError     string
	CreatedAt     time.Time
	SentAt        *time.Time
}

// Content is what one email says.
type Content struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

// Status is what the Email card shows about the queue.
type Status struct {
	LastSentAt *time.Time
	SentLastHour,
	Waiting int
	// LastError is the newest failure of an email still waiting or given
	// up on in the last day ("" when there's none).
	LastError string
}

// Store is the database access email needs (internal/store implements it).
type Store interface {
	// EmailSettings returns the tenant's setting, auth.ErrNotFound when
	// nobody has saved one yet.
	EmailSettings(ctx context.Context, tenant uuid.UUID) (Config, error)
	// SaveEmailSettings writes c; version is the one it replaces (0 for
	// none yet), auth.ErrVersionChanged when someone saved in between.
	SaveEmailSettings(ctx context.Context, c Config, version int, audit auth.AuditEntry) error
	// EnqueueEmail adds m (pending, due now) with its audit entry.
	EnqueueEmail(ctx context.Context, m Message, audit auth.AuditEntry) error
	// ClaimEmails takes up to limit due emails of tenants whose email is
	// on, leasing each until leaseUntil, at most room per tenant (what
	// its hourly limit has left).
	ClaimEmails(ctx context.Context, now, leaseUntil time.Time, limit int) ([]Message, error)
	// FinishEmail records an attempt on m: sent, failed for good, or
	// pending again at next. Sent and failed emails lose their content.
	FinishEmail(ctx context.Context, m Message, status, lastError string, next *time.Time, now time.Time) error
	// NextEmailDue is when the next pending email is due (nil: none),
	// for the worker's timer.
	NextEmailDue(ctx context.Context) (*time.Time, error)
	// EmailStatus is the tenant's queue as the card shows it.
	EmailStatus(ctx context.Context, tenant uuid.UUID, now time.Time) (Status, error)
	// CleanupEmails drops sent and failed emails older than cutoff.
	CleanupEmails(ctx context.Context, cutoff time.Time) error
}
