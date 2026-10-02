package callhistory

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// Filter picks calls to list, newest first.
type Filter struct {
	TenantID uuid.UUID
	// Party: only this person's calls (their own history, or an admin's
	// "Anyone ▾" choice).
	Party *uuid.UUID
	// Missed: only calls the party (or, with no party, anyone) missed.
	Missed bool
	// Number: calls from or to a number containing these digits.
	Number string
	From   *time.Time
	To     *time.Time
	Before *Cursor
	Limit  int
}

// Cursor is where the next page starts: the last call shown.
type Cursor struct {
	StartedAt time.Time
	ID        uuid.UUID
}

// String is the cursor as the API passes it.
func (c Cursor) String() string {
	return strconv.FormatInt(c.StartedAt.UnixMicro(), 10) + "." + c.ID.String()
}

// ParseCursor reads a Cursor's String.
func ParseCursor(s string) (Cursor, error) {
	at, id, ok := strings.Cut(s, ".")
	if !ok {
		return Cursor{}, errors.New("bad cursor")
	}
	us, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return Cursor{}, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return Cursor{}, err
	}
	return Cursor{StartedAt: time.UnixMicro(us).UTC(), ID: u}, nil
}

// Listed is a call as a list shows it.
type Listed struct {
	Call
	// Missed: the filter's party missed it (with no party: anyone did).
	Missed bool
	// The voicemail left, if it's still kept.
	VoicemailID       *uuid.UUID
	VoicemailBoxID    *uuid.UUID
	VoicemailDuration time.Duration
}

// Words is the call's way through Linx, one sentence per step; a
// voicemail step says whether a message was left.
func (l Listed) Words() []string {
	out := make([]string, 0, len(l.Steps))
	for _, s := range l.Steps {
		w := s.Words()
		if s.Kind == StepVoicemail {
			if l.VoicemailID != nil {
				w += " · left a message (" + clock(l.VoicemailDuration) + ")"
			} else if l.VoicemailSource != "" {
				w += " · no message kept"
			}
		}
		out = append(out, w)
	}
	return out
}

func clock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

const (
	// DefaultLimit and MaxLimit: a page.
	DefaultLimit = 50
	MaxLimit     = 200
	// CSVMax is the most calls one download holds.
	CSVMax = 100000
)

// ErrNoExtension: the person has no extension, so no calls of their own.
var ErrNoExtension = errors.New("no extension")

// Service is call history for the API: a person's own calls, everyone's
// for admins and reporters, the badge, and the download.
type Service struct {
	Store   Store
	Builder *Builder
	Now     func() time.Time
}

// fresh reads call records not read yet, so a list is never behind the
// call that just ended. A failure only means a slightly older list.
func (s *Service) fresh(ctx context.Context) {
	if s.Builder != nil {
		_ = s.Builder.Read(ctx)
	}
}

func limit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	return min(n, MaxLimit)
}

// Mine lists the signed-in person's calls (f.Party is set from them).
// One past the limit is fetched to tell whether there's more.
func (s *Service) Mine(ctx context.Context, tenant, user uuid.UUID, f Filter) ([]Listed, *Cursor, error) {
	ext, err := s.Store.UserExtension(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	if ext == nil {
		return nil, nil, nil
	}
	f.TenantID, f.Party = tenant, ext
	return s.list(ctx, f)
}

// All lists everyone's calls.
func (s *Service) All(ctx context.Context, tenant uuid.UUID, f Filter) ([]Listed, *Cursor, error) {
	f.TenantID = tenant
	return s.list(ctx, f)
}

func (s *Service) list(ctx context.Context, f Filter) ([]Listed, *Cursor, error) {
	s.fresh(ctx)
	f.Limit = limit(f.Limit)
	want := f.Limit
	f.Limit++
	items, err := s.Store.ListCalls(ctx, f)
	if err != nil {
		return nil, nil, err
	}
	if len(items) <= want {
		return items, nil, nil
	}
	items = items[:want]
	last := items[want-1]
	return items, &Cursor{StartedAt: last.StartedAt, ID: last.ID}, nil
}

// MissedCount is the badge: calls the person missed since they last
// opened Call history.
func (s *Service) MissedCount(ctx context.Context, user uuid.UUID) (int, error) {
	s.fresh(ctx)
	return s.Store.MissedCount(ctx, user)
}

// Seen clears the badge.
func (s *Service) Seen(ctx context.Context, user uuid.UUID) error {
	return s.Store.SetCallsSeen(ctx, user, s.Now())
}

// KeepDays and SetKeepDays: how long calls are kept (30 days to 2 years).
func (s *Service) KeepDays(ctx context.Context) (int, error) { return s.Store.CallHistoryKeepDays(ctx) }

// ErrKeepDays: outside 30–730.
var ErrKeepDays = &apihttp.Error{Status: http.StatusBadRequest, Code: "keep_days_invalid",
	Detail: "Keep call history between 30 days and 2 years."}

func (s *Service) SetKeepDays(ctx context.Context, days int, audit auth.AuditEntry) error {
	if days < 30 || days > 730 {
		return ErrKeepDays
	}
	audit.Action, audit.Target = "call_history.keep_days", "settings"
	audit.Detail = map[string]any{"days": days}
	return s.Store.SetCallHistoryKeepDays(ctx, days, audit)
}

// CSVHeader is the download's first line.
var CSVHeader = []string{"started", "direction", "from_number", "from_name", "to_number", "to_name", "line", "ring_group",
	"result", "missed", "answered_by", "talk_seconds", "voicemail", "steps"}

// WriteCSV writes every call f picks (up to CSVMax), one line per call,
// times in loc. Text that a spreadsheet would run as a formula gets a
// leading apostrophe.
func (s *Service) WriteCSV(ctx context.Context, w io.Writer, f Filter, loc *time.Location) error {
	s.fresh(ctx)
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	written := 0
	f.Limit = 1000
	for written < CSVMax {
		items, err := s.Store.ListCalls(ctx, f)
		if err != nil {
			return err
		}
		for _, c := range items {
			vm := ""
			if c.VoicemailID != nil {
				vm = clock(c.VoicemailDuration)
			}
			missed := "no"
			if c.Missed {
				missed = "yes"
			}
			rec := []string{c.StartedAt.In(loc).Format("2006-01-02 15:04:05"), c.Direction, c.FromNumber, c.FromName, c.ToNumber, c.ToName,
				c.TrunkName, c.RingGroupName, c.Result, missed, c.AnsweredByName, strconv.Itoa(c.TalkSeconds), vm, strings.Join(c.Words(), "; ")}
			for i, v := range rec {
				rec[i] = csvSafe(v)
			}
			if err := cw.Write(rec); err != nil {
				return err
			}
			written++
		}
		if len(items) < f.Limit {
			break
		}
		last := items[len(items)-1]
		f.Before = &Cursor{StartedAt: last.StartedAt, ID: last.ID}
	}
	cw.Flush()
	return cw.Error()
}

// csvSafe keeps a value from running as a spreadsheet formula (a caller's
// name comes from outside). Phone numbers starting with + stay as they
// are when they're only a number.
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '-', '@', '\t', '\r':
		return "'" + v
	case '+':
		for _, r := range v[1:] {
			if r < '0' || r > '9' {
				return "'" + v
			}
		}
	}
	return v
}
