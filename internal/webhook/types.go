package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// TestEventType is sent by "Test" in the admin portal (POST
// /webhooks/{id}/test), only to that endpoint.
const TestEventType = "webhook.test"

// Delivery statuses.
const (
	StatusPending   = "pending"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Reasons an endpoint was disabled.
const (
	DisabledGone    = "gone"    // the receiver answered 410 Gone
	DisabledFailing = "failing" // every delivery failed for FailingLimit
	DisabledAdmin   = "admin"   // an admin turned it off
)

var (
	ErrNotFound       = errors.New("not found")
	ErrVersionChanged = errors.New("changed since it was read")
	ErrDuplicate      = errors.New("already exists")
)

// Endpoint is a registered webhook receiver. Its secrets are sealed with
// dbsecret (ADR-030) under the row id "webhook_endpoint:<id>".
type Endpoint struct {
	ID, TenantID            uuid.UUID
	URL, Description        string
	EventTypes              []string // empty: every event type
	Enabled                 bool
	DisabledReason          *string
	DisabledAt              *time.Time
	SecretEnc               []byte
	PreviousSecretEnc       []byte
	PreviousSecretExpiresAt *time.Time
	FailingSince            *time.Time
	LastSuccessAt           *time.Time
	Version                 int
	CreatedBy               string
	CreatedAt, UpdatedAt    time.Time
}

// sealID is the additional data binding a sealed secret to its endpoint.
func sealID(id uuid.UUID) string { return "webhook_endpoint:" + id.String() }

// Event is one outbox row: something happened, to be sent to every
// endpoint subscribed to its type.
type Event struct {
	ID, TenantID uuid.UUID
	Type         string
	Body         []byte // the exact JSON sent
	CreatedAt    time.Time
}

// NewEvent builds an event with its Standard Webhooks envelope,
// {"type", "timestamp", "data"}. The caller inserts it (store.InsertEvent)
// in the same transaction as the change it describes.
func NewEvent(tenant uuid.UUID, eventType string, data any, at time.Time) (Event, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return Event{}, err
	}
	at = at.UTC().Truncate(time.Millisecond)
	body, err := json.Marshal(struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Data      any       `json:"data"`
	}{eventType, at, data})
	if err != nil {
		return Event{}, err
	}
	return Event{ID: id, TenantID: tenant, Type: eventType, Body: body, CreatedAt: at}, nil
}

// Delivery is one event on its way to one endpoint.
type Delivery struct {
	ID, TenantID, EndpointID, EventID uuid.UUID
	EventType, Status                 string
	Attempts, MaxAttempts             int
	NextAttemptAt                     *time.Time
	ReplayOf                          *uuid.UUID
	CreatedAt                         time.Time
	FinishedAt                        *time.Time
	Log                               []Attempt // oldest first; filled only when asked for
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
	URL                     string
	SecretEnc               []byte
	PreviousSecretEnc       []byte
	PreviousSecretExpiresAt *time.Time
	Body                    []byte
}

// Outcome is the result of one attempt, to be recorded.
type Outcome struct {
	DeliveryID, EndpointID, TenantID uuid.UUID
	EventType                        string
	Attempt                          Attempt
	// Status is the delivery's new status; NextAttemptAt is set when it's
	// still pending.
	Status        string
	Attempts      int
	NextAttemptAt *time.Time
	Succeeded     bool
	Gone          bool
}

// AllowlistEntry is a private range or host name outbound connections may
// reach (exactly one of CIDR and Host is set).
type AllowlistEntry struct {
	ID          uuid.UUID
	CIDR        *string
	Host        *string
	Description string
	CreatedBy   string
	CreatedAt   time.Time
}

// Store is the database access webhooks need (internal/store implements it).
type Store interface {
	CreateEndpoint(ctx context.Context, e Endpoint, audit auth.AuditEntry) error
	Endpoint(ctx context.Context, tenant, id uuid.UUID) (Endpoint, error)
	ListEndpoints(ctx context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]Endpoint, error)
	// UpdateEndpoint saves e if its stored version is still e.Version and
	// returns it with the new version (ErrVersionChanged otherwise). Turning
	// an endpoint off cancels its pending deliveries.
	UpdateEndpoint(ctx context.Context, e Endpoint, audit auth.AuditEntry) (Endpoint, error)
	DeleteEndpoint(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error

	CreateTestDelivery(ctx context.Context, ev Event, d Delivery, audit auth.AuditEntry) error
	Delivery(ctx context.Context, tenant, id uuid.UUID) (Delivery, error)
	ListDeliveries(ctx context.Context, tenant, endpoint uuid.UUID, status string, before *uuid.UUID, limit int) ([]Delivery, error)
	// ReplayDelivery queues a new delivery of the same event to the same endpoint.
	ReplayDelivery(ctx context.Context, original Delivery, d Delivery, audit auth.AuditEntry) error
	// ReplayFailed queues one new delivery per event that failed or was
	// cancelled for endpoint since, unless one is already pending or succeeded.
	ReplayFailed(ctx context.Context, e Endpoint, since, now time.Time, maxAttempts, limit int, audit auth.AuditEntry) (int, error)

	// DispatchEvents fans undispatched outbox events out into deliveries.
	DispatchEvents(ctx context.Context, now time.Time, maxAttempts, limit int) (int, error)
	// ClaimDeliveries takes due deliveries, leasing each until leaseUntil.
	ClaimDeliveries(ctx context.Context, now, leaseUntil time.Time, limit int) ([]Job, error)
	// RecordAttempt logs an attempt and updates the delivery and the
	// endpoint's health. It disables the endpoint on 410 Gone, or when it
	// has failed since before failingCutoff, and returns the reason if it did.
	RecordAttempt(ctx context.Context, o Outcome, failingCutoff time.Time) (disabled string, err error)
	// Cleanup drops log entries and events older than cutoff and expired
	// previous secrets.
	Cleanup(ctx context.Context, cutoff, now time.Time) error

	ListAllowlist(ctx context.Context) ([]AllowlistEntry, error)
	CreateAllowlistEntry(ctx context.Context, e AllowlistEntry, audit auth.AuditEntry) error
	DeleteAllowlistEntry(ctx context.Context, id uuid.UUID, audit auth.AuditEntry) error
}
