package routing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestDiffAndSummary(t *testing.T) {
	before := []Item{
		{Key: "g:1", Kind: ItemRingGroup, Name: "Sales", Words: "Calls ring Aisha and Bilal together."},
		{Key: "g:2", Kind: ItemRingGroup, Name: "Support", Words: "Calls ring Chen."},
		{Key: "d:3", Kind: IncomingNumber, Name: "+97142000100", Words: "Calls to +97142000100 ring Sales."},
		{Key: "o:lines", Kind: ItemOutgoing, Name: "Outgoing calls", Words: "Outgoing calls go out on du."},
	}
	same := slices.Clone(before)
	if d := Diff(before, same); len(d) != 0 || Summary(d) != "Nothing changed" {
		t.Errorf("no change: %+v %q", d, Summary(d))
	}

	after := slices.Clone(before)
	after[0].Words = "Calls ring Aisha, then Bilal, 15 seconds each."
	d := Diff(before, after)
	if len(d) != 1 || d[0].Before != before[0].Words || d[0].After != after[0].Words || Summary(d) != "Changed Sales" {
		t.Errorf("one change: %+v %q", d, Summary(d))
	}

	added := append(slices.Clone(before), Item{Key: "s:4", Kind: ItemSchedule, Name: "Support hours", Words: "Open Mon–Fri 09:00–17:00. No holidays."})
	if d := Diff(before, added); Summary(d) != "Added the office hours Support hours" || d[0].Before != "" {
		t.Errorf("added: %+v %q", d, Summary(d))
	}
	removed := slices.Delete(slices.Clone(before), 1, 2)
	if d := Diff(before, removed); Summary(d) != "Removed the ring group Support" || d[0].After != "" {
		t.Errorf("removed: %+v %q", d, Summary(d))
	}

	renamed := slices.Clone(before)
	renamed[1].Name = "Help desk"
	renamed[2].Words = "Calls to +97142000100 ring Help desk."
	d = Diff(before, renamed)
	if len(d) != 2 || d[0].Name != "Support → Help desk" || Summary(d) != "Changed Support → Help desk and +97142000100" {
		t.Errorf("renamed: %+v %q", d, Summary(d))
	}
}

func TestLevelAndScheduleWords(t *testing.T) {
	for _, c := range []struct {
		level Level
		want  string
	}{
		{Level{}, "Can call emergency numbers only."},
		{Level{Categories: []string{"international", "mobile", "landline", "service"}}, "Can call local numbers, service numbers, mobiles and abroad."},
		{Level{Categories: []string{"mobile"}, WithholdCallerID: true}, "Can call mobiles. The number is hidden."},
		{Level{Categories: []string{"international", "mobile"}, AbroadCountries: []string{"GB", "SA"}},
			"Can call mobiles and abroad (only the United Kingdom and Saudi Arabia)."},
		// A list on a level that can't call abroad says nothing.
		{Level{Categories: []string{"mobile"}, AbroadCountries: []string{"SA"}}, "Can call mobiles."},
	} {
		if got := levelWords(c.level); got != c.want {
			t.Errorf("levelWords(%+v) = %q, want %q", c.level, got, c.want)
		}
	}
	sc := Schedule{Spans: DefaultSpans(), Holidays: []Holiday{
		{Name: "Eid al-Fitr", FirstDay: "2027-03-20", LastDay: "2027-03-22"},
		{Name: "National Day", FirstDay: "2026-12-02", LastDay: "2026-12-02", EveryYear: true},
	}}
	if got, want := scheduleWords(sc), "Open Mon–Fri 08:00–17:00. Closed on Eid al-Fitr (2027-03-20 to 2027-03-22), National Day (2026-12-02, every year)."; got != want {
		t.Errorf("scheduleWords = %q, want %q", got, want)
	}
	if got := scheduleWords(Schedule{}); got != "Never open. No holidays." {
		t.Errorf("scheduleWords(never) = %q", got)
	}
}

func TestChangeHeader(t *testing.T) {
	id := uuid.Must(uuid.NewV7())
	for _, c := range []struct {
		name   string
		status int
		note   bool
		want   string
	}{
		{"a change", http.StatusOK, true, id.String()},
		{"no change", http.StatusOK, false, ""},
		{"a refused request", http.StatusConflict, true, ""},
	} {
		h := ChangeHeader(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c.note {
				NoteChange(r.Context(), id)
			}
			w.WriteHeader(c.status)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if got := rec.Header().Get(ChangeHeaderName); got != c.want {
			t.Errorf("%s: header %q, want %q", c.name, got, c.want)
		}
	}
	NoteChange(context.Background(), id) // outside a request: nothing to tell
}
