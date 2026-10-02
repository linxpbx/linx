package routing

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
)

func TestHoursWords(t *testing.T) {
	week := func(days []int, opens, closes string) []Span {
		var out []Span
		for _, d := range days {
			out = append(out, Span{Weekday: d, Opens: opens, Closes: closes})
		}
		return out
	}
	for _, c := range []struct {
		spans []Span
		want  string
	}{
		{DefaultSpans(), "Mon–Fri 08:00–17:00"},
		{week([]int{0, 1, 2, 3, 4}, "08:00", "17:00"), "Mon–Thu 08:00–17:00; Sun 08:00–17:00"},
		{week([]int{0, 1, 2, 3, 4, 5, 6}, "00:00", "24:00"), "Every day 00:00–24:00"},
		{append(week([]int{1, 2}, "08:00", "13:00"), append(week([]int{1, 2}, "14:00", "17:00"), Span{Weekday: 5, Opens: "08:00", Closes: "12:00"})...),
			"Mon, Tue 08:00–13:00 and 14:00–17:00; Fri 08:00–12:00"},
		{nil, "Never open"},
	} {
		sc := Schedule{Name: "x", Spans: c.spans}
		if err := checkSchedule(&sc); err != nil {
			t.Fatal(err)
		}
		if got := HoursWords(sc.Spans); got != c.want {
			t.Errorf("HoursWords = %q, want %q", got, c.want)
		}
	}
}

func TestCheckSchedule(t *testing.T) {
	for _, c := range []struct {
		name string
		sc   Schedule
		code string
	}{
		{"closes before it opens", Schedule{Name: "x", Spans: []Span{{1, "17:00", "08:00"}}}, "span_invalid"},
		{"bad time", Schedule{Name: "x", Spans: []Span{{1, "8:00", "17:00"}}}, "span_invalid"},
		{"24:00 opens", Schedule{Name: "x", Spans: []Span{{1, "24:00", "24:00"}}}, "span_invalid"},
		{"overlap", Schedule{Name: "x", Spans: []Span{{1, "08:00", "13:00"}, {1, "12:00", "17:00"}}}, "span_overlap"},
		{"no name", Schedule{Name: ""}, "name_invalid"},
		{"holiday backwards", Schedule{Name: "x", Holidays: []Holiday{{Name: "Eid", FirstDay: "2027-03-22", LastDay: "2027-03-20"}}}, "holiday_invalid"},
		{"holiday date", Schedule{Name: "x", Holidays: []Holiday{{Name: "Eid", FirstDay: "22/03/2027", LastDay: "2027-03-22"}}}, "holiday_invalid"},
		{"fine", Schedule{Name: "x", Spans: []Span{{1, "08:00", "13:00"}, {1, "13:00", "24:00"}},
			Holidays: []Holiday{{Name: " Eid ", FirstDay: "2027-03-20", LastDay: "2027-03-22"}}}, ""},
	} {
		err := checkSchedule(&c.sc)
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		var e *apihttp.Error
		if !errors.As(err, &e) || e.Code != c.code {
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
}

func TestIncomingWords(t *testing.T) {
	sara := ExtensionRef{ID: uuid.New(), Number: "101", DisplayName: "Sara Haddad"}
	sales := RingGroup{ID: uuid.New(), Name: "Sales", Strategy: StrategyAll, NoAnswer: Destination{Kind: KindExtension, ExtensionID: &sara.ID}}
	office := Schedule{ID: uuid.New(), Name: "Office hours", Spans: DefaultSpans()}
	l := lookups{groups: map[uuid.UUID]RingGroup{sales.ID: sales}, exts: map[uuid.UUID]ExtensionRef{sara.ID: sara},
		schedules: map[uuid.UUID]Schedule{office.ID: office}}
	closed := Destination{Kind: KindMessage, Message: MessageClosed}
	for _, c := range []struct {
		in   Incoming
		want string
	}{
		{Incoming{Kind: IncomingNumber, Number: "042000100"}, "Calls to 042000100 ring nobody: callers hear the number isn't in use."},
		{Incoming{Kind: IncomingNumber, Number: "042000100", Rings: &Destination{Kind: KindExtension, ExtensionID: &sara.ID}},
			`Calls to 042000100 ring Sara Haddad (101). If nobody answers in 30 seconds, callers hear "not available".`},
		{Incoming{Kind: IncomingNumber, Number: "042000100", Rings: &Destination{Kind: KindRingGroup, RingGroupID: &sales.ID},
			Rule: &Rule{NoAnswerSeconds: 25, ScheduleID: &office.ID, Closed: closed}},
			`Calls to 042000100 ring Sales (all at once) Mon–Fri 08:00–17:00. If nobody answers, the call goes to Sara Haddad (101). At other times and on holidays, callers hear "We're closed".`},
		{Incoming{Kind: IncomingLine, LineName: "UCM", Rings: &Destination{Kind: KindExtension, ExtensionID: &sara.ID},
			Rule: &Rule{NoAnswerSeconds: 20, NoAnswer: Destination{Kind: KindRingGroup, RingGroupID: &sales.ID}, ScheduleID: &office.ID,
				Closed: closed, Holiday: &Destination{Kind: KindMessage, Message: MessageNotAvailable}}},
			`Calls on UCM for none of its numbers ring Sara Haddad (101) Mon–Fri 08:00–17:00. If nobody answers in 20 seconds, the call goes to Sales. At other times, callers hear "We're closed". On holidays, callers hear "not available".`},
	} {
		if got := l.words(c.in); got != c.want {
			t.Errorf("words =\n%s\nwant\n%s", got, c.want)
		}
	}
}
