package alert

import "testing"

func TestParseClockAndFormatClock(t *testing.T) {
	tests := map[string]int{"00:00": 0, "07:00": 420, "22:00": 1320, "23:59": 1439}
	for s, want := range tests {
		got, err := parseClock(s)
		if err != nil || got != want {
			t.Errorf("parseClock(%q) = %d, %v, want %d", s, got, err, want)
		}
		if back := FormatClock(want); back != s {
			t.Errorf("FormatClock(%d) = %q, want %q", want, back, s)
		}
	}
	for _, bad := range []string{"24:00", "12:60", "noon", "1:00", ""} {
		if _, err := parseClock(bad); err == nil {
			t.Errorf("parseClock(%q) succeeded", bad)
		}
	}
}

func TestQuietHoursInputToModel(t *testing.T) {
	if q, err := (*QuietHoursInput)(nil).toModel(); q != nil || err != nil {
		t.Errorf("nil input = %v, %v", q, err)
	}
	off := &QuietHoursInput{Enabled: false}
	if q, err := off.toModel(); q != nil || err != nil {
		t.Errorf("disabled input = %v, %v", q, err)
	}
	on := &QuietHoursInput{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Europe/Istanbul"}
	q, err := on.toModel()
	if err != nil {
		t.Fatal(err)
	}
	if q.Start != 22*60 || q.End != 7*60 || q.Timezone != "Europe/Istanbul" || !q.BypassCritical {
		t.Errorf("toModel() = %+v", q)
	}
	explicit := &QuietHoursInput{Enabled: true, Start: "22:00", End: "07:00", Timezone: "UTC", BypassCritical: boolPtr(false)}
	q2, err := explicit.toModel()
	if err != nil || q2.BypassCritical {
		t.Errorf("explicit bypass_critical=false = %+v, %v", q2, err)
	}
	for _, bad := range []*QuietHoursInput{
		{Enabled: true, Start: "bad", End: "07:00", Timezone: "UTC"},
		{Enabled: true, Start: "22:00", End: "bad", Timezone: "UTC"},
		{Enabled: true, Start: "22:00", End: "07:00", Timezone: ""},
		{Enabled: true, Start: "22:00", End: "07:00", Timezone: "Not/AZone"},
	} {
		if _, err := bad.toModel(); err == nil {
			t.Errorf("toModel(%+v) succeeded", bad)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func TestMatchETag(t *testing.T) {
	tests := []struct {
		ifMatch string
		version int
		want    bool
	}{
		{`"1"`, 1, true},
		{`"2"`, 1, false},
		{"*", 5, true},
		{`"1", "2"`, 2, true},
		{`  "3"  `, 3, true},
	}
	for _, tt := range tests {
		if got := matchETag(tt.ifMatch, tt.version); got != tt.want {
			t.Errorf("matchETag(%q, %d) = %v, want %v", tt.ifMatch, tt.version, got, tt.want)
		}
	}
}

func TestETagRoundTrip(t *testing.T) {
	if got := ETag(7); got != `"7"` {
		t.Errorf("ETag(7) = %q", got)
	}
}
