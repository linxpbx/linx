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

// Destination kinds: where a call can go. Menus and queues join them in
// Phase 4.
const (
	KindExtension = "extension"
	KindRingGroup = "ring_group"
	// KindVoicemail is a voicemail box (ADR-069): a person's
	// (ExtensionID) or a ring group's (RingGroupID). A box's id is its
	// owner's.
	KindVoicemail = "voicemail"
	KindMessage   = "message"
)

// Messages a call can end on (the dialplan's linx-messages).
const (
	MessageNotAvailable = "not-available"
	MessageClosed       = "closed"
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
	MaxSchedules  = 20
	// MaxVoicemailMinutes is how long a message can be (the dialplan's
	// Record, docs/PHASE1F.md §8).
	MaxVoicemailMinutes = 3
	MaxSpans            = 50
	MaxHolidays         = 200
)

// Destination is one place a call can go. For KindVoicemail, ExtensionID
// or RingGroupID says whose box.
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
	// VoicemailOff: the group's own box is turned off.
	VoicemailOff bool
	Version      int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ExtensionRef is what the screens show for an extension destination.
type ExtensionRef struct {
	ID          uuid.UUID
	Number      string
	DisplayName string
	// VoicemailOff: the person's box is turned off.
	VoicemailOff bool
}

// Span is one opening of a schedule on one weekday (0 Sunday … 6
// Saturday), "08:00" to "17:00" ("24:00" is the day's end).
type Span struct {
	Weekday       int
	Opens, Closes string
}

// Holiday closes a schedule all day from FirstDay to LastDay
// ("2026-12-02"), and on the same dates every year if EveryYear.
type Holiday struct {
	Name              string
	FirstDay, LastDay string
	EveryYear         bool
}

// Schedule is a set of office hours with its holidays, in the server's
// time zone.
type Schedule struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Name      string
	Spans     []Span
	Holidays  []Holiday
	Version   int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScheduleState is the database's answer for one moment: open, or closed
// (by a holiday, named), and when that next changes (nil: not within a
// month).
type ScheduleState struct {
	Open      bool
	Holiday   string
	ChangesAt *time.Time
}

// Incoming kinds: one phone number, or a line's calls for none of its
// numbers.
const (
	IncomingNumber = "number"
	IncomingLine   = "line"
)

// Rule is "When someone calls" beyond who it rings (migration 0033's
// incoming_rule): how long a person rings and where the call goes then,
// the office hours, and where calls go outside them and on holidays
// (Holiday nil: the same as Closed).
type Rule struct {
	NoAnswerSeconds int
	NoAnswer        Destination
	ScheduleID      *uuid.UUID
	Closed          Destination
	Holiday         *Destination
}

// Incoming is one number's (or one line's other calls') routing.
type Incoming struct {
	Kind     string // IncomingNumber or IncomingLine
	ID       uuid.UUID
	TenantID uuid.UUID
	Number   string // a number's
	Label    string
	LineID   uuid.UUID
	LineName string
	LineKind string
	// Rings during office hours (or all the time): an extension or a ring
	// group; nil rings nobody.
	Rings *Destination
	// Rule nil: just Rings, all the time (a person 30 s, then "not
	// available").
	Rule    *Rule
	Version int
}

// Step is one answer of linx_route_at (migration 0033), as the dialplan
// gets it.
type Step struct {
	Action, Targets string
	Seconds         int
	Next            string
	Counts          int
	Label           string
}

// RingTarget is one phone or browser a step rings, with whose it is.
type RingTarget struct {
	Username    string
	Number      string
	DisplayName string
}

// RulesStore is the database access office hours, "When someone calls"
// and the simulator need (internal/store).
type RulesStore interface {
	// TimeZone is the server's time zone, which schedules are in.
	TimeZone(ctx context.Context) (string, error)
	OfficeHours(ctx context.Context, tenant uuid.UUID) ([]Schedule, error)
	// ScheduleStates answers for every schedule of the tenant at at.
	OfficeHoursStates(ctx context.Context, tenant uuid.UUID, at time.Time) (map[uuid.UUID]ScheduleState, error)
	CreateOfficeHours(ctx context.Context, sc Schedule, audit auth.AuditEntry) error
	// UpdateSchedule saves sc if its stored version is still sc.Version.
	UpdateOfficeHours(ctx context.Context, sc Schedule, audit auth.AuditEntry) error
	// DeleteSchedule: ErrInUse while a rule uses it.
	DeleteOfficeHours(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error
	// IncomingList returns every number and line of the tenant, numbers
	// first (by number), then lines (by name).
	IncomingList(ctx context.Context, tenant uuid.UUID) ([]Incoming, error)
	// SetIncoming saves in (its Rings and Rule) if the number's or line's
	// stored version is still in.Version.
	SetIncoming(ctx context.Context, in Incoming, audit auth.AuditEntry) error
	// RouteStep asks linx_route_at; nil when there's no such place.
	RouteStep(ctx context.Context, dest, caller string, at time.Time) (*Step, error)
	// RingTargets says whose the phones and browsers are.
	RingTargets(ctx context.Context, usernames []string) ([]RingTarget, error)
	// ExtensionsByNumber returns the tenant's live extensions with these
	// numbers.
	ExtensionsByNumber(ctx context.Context, tenant uuid.UUID, numbers []string) ([]ExtensionRef, error)
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
