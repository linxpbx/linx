package routing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

// Office hours and holidays (docs/PHASE1F.md §7, migration 0033). The
// database answers "open or closed at this moment" (schedule_open_at), for
// calls, the simulator and these screens alike.

var errScheduleChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "These hours were changed since you read them. Fetch them again and retry."}

func scheduleNotFound() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no schedule with that id."}
}

var (
	opensPattern  = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)
	closesPattern = regexp.MustCompile(`^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$`)
)

// DefaultSpans is a new schedule's week: Monday to Friday, 08:00-17:00
// (migration 0033's schedule_add_default, the same).
func DefaultSpans() []Span {
	out := make([]Span, 0, 5)
	for d := 1; d <= 5; d++ {
		out = append(out, Span{Weekday: d, Opens: "08:00", Closes: "17:00"})
	}
	return out
}

// ScheduleView is a schedule as the screens show it.
type ScheduleView struct {
	Schedule
	Words  string
	Now    ScheduleState
	UsedBy []UsedBy
}

// ScheduleInput is a new schedule; nil Spans means DefaultSpans.
type ScheduleInput struct {
	Name     string
	Spans    []Span
	Holidays []Holiday
}

// SchedulePatch changes a schedule; nil fields stay as they are.
type SchedulePatch struct {
	Name     *string
	Spans    *[]Span
	Holidays *[]Holiday
}

func (s *Service) rules() error {
	if s.Rules == nil {
		return errors.New("routing: no rules store")
	}
	return nil
}

// TimeZone is the server's time zone.
func (s *Service) TimeZone(ctx context.Context) (string, error) {
	if err := s.rules(); err != nil {
		return "", err
	}
	return s.Rules.TimeZone(ctx)
}

// ListSchedules returns every schedule, by name, with whether it's open
// now.
func (s *Service) ListSchedules(ctx context.Context) ([]ScheduleView, error) {
	if err := s.rules(); err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.Rules.OfficeHours(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	states, err := s.Rules.OfficeHoursStates(ctx, p.TenantID, s.Now())
	if err != nil {
		return nil, err
	}
	incoming, err := s.Rules.IncomingList(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	out := make([]ScheduleView, 0, len(list))
	for _, sc := range list {
		v := ScheduleView{Schedule: sc, Words: HoursWords(sc.Spans), Now: states[sc.ID], UsedBy: []UsedBy{}}
		for _, in := range incoming {
			if in.Rule != nil && in.Rule.ScheduleID != nil && *in.Rule.ScheduleID == sc.ID {
				v.UsedBy = append(v.UsedBy, UsedBy{Kind: in.Kind, ID: in.ID, Name: incomingName(in), How: "schedule"})
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) getSchedule(ctx context.Context, id uuid.UUID) (ScheduleView, error) {
	list, err := s.ListSchedules(ctx)
	if err != nil {
		return ScheduleView{}, err
	}
	for _, v := range list {
		if v.ID == id {
			return v, nil
		}
	}
	return ScheduleView{}, scheduleNotFound()
}

// CreateSchedule adds a schedule.
func (s *Service) CreateSchedule(ctx context.Context, in ScheduleInput) (ScheduleView, error) {
	if err := s.rules(); err != nil {
		return ScheduleView{}, err
	}
	p, err := principal(ctx)
	if err != nil {
		return ScheduleView{}, err
	}
	all, err := s.Rules.OfficeHours(ctx, p.TenantID)
	if err != nil {
		return ScheduleView{}, err
	}
	if len(all) >= MaxSchedules {
		return ScheduleView{}, invalid("too_many", fmt.Sprintf("A server can have up to %d schedules.", MaxSchedules))
	}
	id, err := uuid.NewV7()
	if err != nil {
		return ScheduleView{}, err
	}
	now := s.Now().UTC()
	sc := Schedule{ID: id, TenantID: p.TenantID, Name: strings.TrimSpace(in.Name), Spans: in.Spans, Holidays: in.Holidays,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if sc.Spans == nil {
		sc.Spans = DefaultSpans()
	}
	if sc.Holidays == nil {
		sc.Holidays = []Holiday{}
	}
	if err := checkSchedule(&sc); err != nil {
		return ScheduleView{}, err
	}
	_, a, err := audit(ctx, "schedule.create", "schedule:"+id.String())
	if err != nil {
		return ScheduleView{}, err
	}
	a.Detail = map[string]any{"name": sc.Name, "hours": HoursWords(sc.Spans), "holidays": len(sc.Holidays)}
	if err := s.Rules.CreateOfficeHours(ctx, sc, a); err != nil {
		return ScheduleView{}, scheduleError(err, sc)
	}
	return s.getSchedule(ctx, id)
}

// UpdateSchedule applies patch. ifMatch, when not empty, must match the
// schedule's current ETag (412 otherwise).
func (s *Service) UpdateSchedule(ctx context.Context, id uuid.UUID, patch SchedulePatch, ifMatch string) (ScheduleView, error) {
	if err := s.rules(); err != nil {
		return ScheduleView{}, err
	}
	p, err := principal(ctx)
	if err != nil {
		return ScheduleView{}, err
	}
	all, err := s.Rules.OfficeHours(ctx, p.TenantID)
	if err != nil {
		return ScheduleView{}, err
	}
	i := slices.IndexFunc(all, func(sc Schedule) bool { return sc.ID == id })
	if i < 0 {
		return ScheduleView{}, scheduleNotFound()
	}
	sc := all[i]
	if ifMatch != "" && !matchETag(ifMatch, sc.Version) {
		return ScheduleView{}, errScheduleChanged
	}
	if patch.Name != nil {
		sc.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Spans != nil {
		sc.Spans = *patch.Spans
	}
	if patch.Holidays != nil {
		sc.Holidays = *patch.Holidays
	}
	if err := checkSchedule(&sc); err != nil {
		return ScheduleView{}, err
	}
	sc.UpdatedAt = s.Now().UTC()
	_, a, err := audit(ctx, "schedule.update", "schedule:"+id.String())
	if err != nil {
		return ScheduleView{}, err
	}
	a.Detail = map[string]any{"name": sc.Name, "hours": HoursWords(sc.Spans), "holidays": len(sc.Holidays)}
	if err := s.Rules.UpdateOfficeHours(ctx, sc, a); err != nil {
		if errors.Is(err, ErrVersionChanged) {
			return ScheduleView{}, errScheduleChanged
		}
		if errors.Is(err, ErrNotFound) {
			return ScheduleView{}, scheduleNotFound()
		}
		return ScheduleView{}, scheduleError(err, sc)
	}
	return s.getSchedule(ctx, id)
}

// DeleteSchedule removes a schedule no number uses.
func (s *Service) DeleteSchedule(ctx context.Context, id uuid.UUID) error {
	if err := s.rules(); err != nil {
		return err
	}
	p, a, err := audit(ctx, "schedule.delete", "schedule:"+id.String())
	if err != nil {
		return err
	}
	err = s.Rules.DeleteOfficeHours(ctx, p.TenantID, id, a)
	if errors.Is(err, ErrNotFound) {
		return scheduleNotFound()
	}
	if errors.Is(err, ErrInUse) {
		return &apihttp.Error{Status: http.StatusConflict, Code: "schedule_in_use",
			Detail: "A number's calls follow these hours. Change that number first (Incoming)."}
	}
	return err
}

func scheduleError(err error, sc Schedule) error {
	if errors.Is(err, ErrDuplicateName) {
		return &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
			Detail: fmt.Sprintf("A schedule named %q already exists.", sc.Name)}
	}
	return err
}

// checkSchedule validates sc, putting its spans in order.
func checkSchedule(sc *Schedule) error {
	if n := len([]rune(sc.Name)); n < 1 || n > 60 {
		return invalid("name_invalid", "Give it a name of 1 to 60 characters.")
	}
	if len(sc.Spans) > MaxSpans {
		return invalid("spans_too_many", fmt.Sprintf("A schedule can have up to %d opening times.", MaxSpans))
	}
	for _, sp := range sc.Spans {
		if sp.Weekday < 0 || sp.Weekday > 6 {
			return invalid("span_invalid", "A day is 0 (Sunday) to 6 (Saturday).")
		}
		if !opensPattern.MatchString(sp.Opens) || !closesPattern.MatchString(sp.Closes) {
			return invalid("span_invalid", `Times are written "08:00"; closing can be "24:00".`)
		}
		if sp.Opens >= sp.Closes {
			return invalid("span_invalid", fmt.Sprintf("On %s, closing (%s) must be after opening (%s).", dayNames[sp.Weekday], sp.Closes, sp.Opens))
		}
	}
	slices.SortFunc(sc.Spans, func(a, b Span) int {
		if a.Weekday != b.Weekday {
			return a.Weekday - b.Weekday
		}
		return strings.Compare(a.Opens, b.Opens)
	})
	for i := 1; i < len(sc.Spans); i++ {
		a, b := sc.Spans[i-1], sc.Spans[i]
		if a.Weekday == b.Weekday && b.Opens < a.Closes {
			return invalid("span_overlap", fmt.Sprintf("On %s, %s–%s and %s–%s overlap.", dayNames[a.Weekday], a.Opens, a.Closes, b.Opens, b.Closes))
		}
	}
	if len(sc.Holidays) > MaxHolidays {
		return invalid("holidays_too_many", fmt.Sprintf("A schedule can have up to %d holidays.", MaxHolidays))
	}
	for i := range sc.Holidays {
		h := &sc.Holidays[i]
		h.Name = strings.TrimSpace(h.Name)
		if n := len([]rune(h.Name)); n < 1 || n > 60 {
			return invalid("holiday_invalid", "Give each holiday a name of 1 to 60 characters.")
		}
		first, err1 := time.Parse(time.DateOnly, h.FirstDay)
		last, err2 := time.Parse(time.DateOnly, h.LastDay)
		if err1 != nil || err2 != nil {
			return invalid("holiday_invalid", fmt.Sprintf("%s: dates are written 2026-12-02.", h.Name))
		}
		if last.Before(first) {
			return invalid("holiday_invalid", fmt.Sprintf("%s: the last day is before the first.", h.Name))
		}
		if last.Sub(first) >= 366*24*time.Hour {
			return invalid("holiday_invalid", fmt.Sprintf("%s: a holiday can be up to a year long.", h.Name))
		}
	}
	return nil
}

var dayNames = [7]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
var dayShort = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// HoursWords says a week's hours in a few words: "Mon–Fri 08:00–17:00",
// "Mon–Thu 08:00–13:00 and 14:00–17:00, Fri 08:00–12:00", "Every day
// 00:00–24:00", "Never open". Days run Monday to Sunday.
func HoursWords(spans []Span) string {
	var byDay [7][]string
	for _, sp := range spans {
		byDay[sp.Weekday] = append(byDay[sp.Weekday], sp.Opens+"–"+sp.Closes)
	}
	text := func(d int) string { return listNames(byDay[d], "") }
	order := []int{1, 2, 3, 4, 5, 6, 0}
	var parts []string
	for i := 0; i < len(order); {
		d := order[i]
		if len(byDay[d]) == 0 {
			i++
			continue
		}
		j := i
		for j+1 < len(order) && len(byDay[order[j+1]]) > 0 && text(order[j+1]) == text(d) {
			j++
		}
		if i == 0 && j == len(order)-1 {
			return "Every day " + text(d)
		}
		days := dayShort[d]
		switch {
		case j == i+1:
			days += ", " + dayShort[order[j]]
		case j > i+1:
			days += "–" + dayShort[order[j]]
		}
		parts = append(parts, days+" "+text(d))
		i = j + 1
	}
	if len(parts) == 0 {
		return "Never open"
	}
	return strings.Join(parts, "; ")
}
