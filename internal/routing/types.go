// Package routing is where a call can go (ADR-068, docs/PHASE1F.md §6):
// the one destinations list every routing choice uses, and ring groups.
// Its tables are read by the dialplan through asterisk.linx_route
// (migration 0032), one step at a time, so calls keep being routed while
// the control plane restarts. Caller mistakes come back as
// *apihttp.Error.
package routing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// Destination kinds: where a call can go. A voicemail box joins them in
// Phase 1F step 13, menus and queues in Phase 4.
const (
	KindExtension = "extension"
	KindRingGroup = "ring_group"
	KindMessage   = "message"
)

// Messages a call can end on (the dialplan's linx-messages).
const (
	MessageNotAvailable = "not-available"
)

// Ring group strategies.
const (
	StrategyAll    = "all"
	StrategyInTurn = "in_turn"
)

// Limits.
const (
	MaxMembers    = 50
	MaxRingGroups = 200
)

// Destination is one place a call can go.
type Destination struct {
	Kind        string
	ExtensionID *uuid.UUID
	RingGroupID *uuid.UUID
	Message     string
}

// Member is one extension in a ring group.
type Member struct {
	ExtensionID uuid.UUID
	Number      string
	DisplayName string
	// CanRing: a phone or browser of it is connected and its person isn't
	// on Do not disturb.
	CanRing bool
}

// RingGroup rings several extensions for one call.
type RingGroup struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Number      string // "" when it has none
	Strategy    string
	RingSeconds int // all at once: before "if nobody answers"
	TurnSeconds int // one after another: each person
	Members     []Member
	NoAnswer    Destination
	Version     int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ExtensionRef is what the screens show for an extension destination.
type ExtensionRef struct {
	ID          uuid.UUID
	Number      string
	DisplayName string
}

// Errors the Store returns.
var (
	ErrNotFound          = errors.New("not found")
	ErrVersionChanged    = errors.New("changed since it was read")
	ErrDuplicateName     = errors.New("name already used")
	ErrNumberTaken       = errors.New("number already used")
	ErrInUse             = errors.New("in use")
	ErrExtensionNotFound = errors.New("extension not found")
)

// ReservedNumberError is a number that looks like an outside or emergency
// one (the same rule as extensions').
type ReservedNumberError struct {
	Number, Reason, Country string
}

func (e *ReservedNumberError) Error() string {
	return "ring group number " + e.Number + " is reserved (" + e.Reason + ")"
}

// Store is the database access the service needs (internal/store).
type Store interface {
	// RingGroups returns every ring group of the tenant with its members
	// in order, by name.
	RingGroups(ctx context.Context, tenant uuid.UUID) ([]RingGroup, error)
	CreateRingGroup(ctx context.Context, g RingGroup, audit auth.AuditEntry) error
	// UpdateRingGroup saves g if its stored version is still g.Version
	// (ErrVersionChanged otherwise), members included.
	UpdateRingGroup(ctx context.Context, g RingGroup, audit auth.AuditEntry) error
	// DeleteRingGroup removes it; ErrInUse while another group sends its
	// unanswered calls there.
	DeleteRingGroup(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error
	// Extensions returns the tenant's live extensions with these ids
	// (missing ones are left out).
	Extensions(ctx context.Context, tenant uuid.UUID, ids []uuid.UUID) ([]ExtensionRef, error)
}
