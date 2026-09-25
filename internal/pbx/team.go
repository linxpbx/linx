package pbx

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A person's chosen status (migration 0014). "dnd" also stops their
// extension ringing.
const (
	PresenceAvailable = "available"
	PresenceAway      = "away"
	PresenceDND       = "dnd"
)

// Team statuses, most specific first: a call beats a chosen status, which
// beats whether a phone is signed in (docs/ui/WEB_SCREENS_PHASE1C.md §6).
const (
	TeamOnCall    = "on_call"
	TeamRinging   = "ringing"
	TeamDND       = "dnd"
	TeamAway      = "away"
	TeamAvailable = "available"
	TeamOffline   = "offline"
)

// ErrPresenceInvalid is a status that isn't available, away or dnd.
var ErrPresenceInvalid = errors.New("presence must be available, away or dnd")

// TeamMember is an extension as the Team list shows it: the name of the
// person on it (or the extension's own name, for a desk phone nobody signs
// in to), whether any phone on it is signed in, and the person's chosen
// status ("" with nobody on it).
type TeamMember struct {
	Extension string
	Name      string
	Online    bool
	Presence  string
}

// TeamStore is the database access Team needs.
type TeamStore interface {
	// TeamMembers lists the tenant's enabled extensions.
	TeamMembers(ctx context.Context, tenant uuid.UUID) ([]TeamMember, error)
	SetPresence(ctx context.Context, tenant, user uuid.UUID, presence string) error
}

// TeamStatus is one row of the Team list.
type TeamStatus struct {
	Extension string
	Name      string
	Status    string
	// Since is when the call started ringing or was answered, for the
	// "On a call · 04:12" timer.
	Since *time.Time
}

// Team builds the Team list (GET /team and its live updates).
type Team struct {
	Store TeamStore
	Calls interface{ ActiveCalls() []ActiveCall }
	// OnChange, if set, is called after someone's status changes.
	OnChange func()
}

// List is the tenant's Team list, sorted by name.
func (t *Team) List(ctx context.Context, tenant uuid.UUID) ([]TeamStatus, error) {
	members, err := t.Store.TeamMembers(ctx, tenant)
	if err != nil {
		return nil, err
	}
	var calls []ActiveCall
	if t.Calls != nil {
		calls = t.Calls.ActiveCalls()
	}
	return TeamStatuses(members, calls), nil
}

// SetPresence records a person's chosen status.
func (t *Team) SetPresence(ctx context.Context, tenant, user uuid.UUID, presence string) error {
	if !slices.Contains([]string{PresenceAvailable, PresenceAway, PresenceDND}, presence) {
		return ErrPresenceInvalid
	}
	if err := t.Store.SetPresence(ctx, tenant, user, presence); err != nil {
		return err
	}
	if t.OnChange != nil {
		t.OnChange()
	}
	return nil
}

// TeamStatuses combines the members with the calls in progress.
func TeamStatuses(members []TeamMember, calls []ActiveCall) []TeamStatus {
	type callState struct {
		status string
		since  time.Time
	}
	busy := map[string]callState{}
	mark := func(ext, status string, since time.Time) {
		if ext == "" {
			return
		}
		if cur, ok := busy[ext]; ok && cur.status == TeamOnCall {
			return
		}
		busy[ext] = callState{status, since}
	}
	for _, c := range calls {
		switch c.State {
		case CallAnswered:
			at := c.StartedAt
			if c.AnsweredAt != nil {
				at = *c.AnsweredAt
			}
			mark(c.From.Extension, TeamOnCall, at)
			if c.AnsweredBy != nil {
				mark(c.AnsweredBy.Extension, TeamOnCall, at)
			}
		case CallRinging:
			mark(c.From.Extension, TeamOnCall, c.StartedAt)
			mark(c.To, TeamRinging, c.StartedAt)
		default: // talking to Linx itself (a message, the echo test)
			mark(c.From.Extension, TeamOnCall, c.StartedAt)
		}
	}
	out := make([]TeamStatus, 0, len(members))
	for _, m := range members {
		s := TeamStatus{Extension: m.Extension, Name: m.Name}
		if b, ok := busy[m.Extension]; ok {
			since := b.since
			s.Status, s.Since = b.status, &since
		} else {
			switch {
			case m.Presence == PresenceDND:
				s.Status = TeamDND
			case !m.Online:
				s.Status = TeamOffline
			case m.Presence == PresenceAway:
				s.Status = TeamAway
			default:
				s.Status = TeamAvailable
			}
		}
		out = append(out, s)
	}
	slices.SortStableFunc(out, func(a, b TeamStatus) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Extension, b.Extension)
	})
	return out
}
