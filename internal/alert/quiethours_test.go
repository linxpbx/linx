package alert

import (
	"testing"
	"time"
)

func TestQuietHoursActive(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Istanbul")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, loc) }

	tests := []struct {
		name       string
		q          *QuietHours
		t          time.Time
		wantActive bool
	}{
		{"nil quiet hours never active", nil, at(23, 0), false},
		{"normal window, inside", &QuietHours{Start: 22 * 60, End: 23 * 60, Timezone: "Europe/Istanbul"}, at(22, 30), true},
		{"normal window, before", &QuietHours{Start: 22 * 60, End: 23 * 60, Timezone: "Europe/Istanbul"}, at(21, 59), false},
		{"normal window, at end (exclusive)", &QuietHours{Start: 22 * 60, End: 23 * 60, Timezone: "Europe/Istanbul"}, at(23, 0), false},
		{"wraps midnight, late night", &QuietHours{Start: 22 * 60, End: 7 * 60, Timezone: "Europe/Istanbul"}, at(23, 30), true},
		{"wraps midnight, early morning", &QuietHours{Start: 22 * 60, End: 7 * 60, Timezone: "Europe/Istanbul"}, at(6, 59), true},
		{"wraps midnight, daytime", &QuietHours{Start: 22 * 60, End: 7 * 60, Timezone: "Europe/Istanbul"}, at(12, 0), false},
		{"wraps midnight, at end (exclusive)", &QuietHours{Start: 22 * 60, End: 7 * 60, Timezone: "Europe/Istanbul"}, at(7, 0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.q.Active(tt.t)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.wantActive {
				t.Errorf("Active(%v) = %v, want %v", tt.t, got, tt.wantActive)
			}
		})
	}
}

func TestQuietHoursActiveConvertsToChannelTimeZone(t *testing.T) {
	// 23:00 UTC is 08:00 next day in Tokyo — outside a 22:00-07:00 Tokyo
	// quiet window, even though it's plainly late at night in UTC.
	q := &QuietHours{Start: 22 * 60, End: 7 * 60, Timezone: "Asia/Tokyo"}
	utc, err := time.LoadLocation("UTC")
	if err != nil {
		t.Fatal(err)
	}
	active, err := q.Active(time.Date(2026, 1, 1, 23, 0, 0, 0, utc))
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	if active {
		t.Error("23:00 UTC was treated as inside Tokyo's quiet hours")
	}
}

func TestQuietHoursActiveBadTimeZone(t *testing.T) {
	q := &QuietHours{Start: 0, End: 60, Timezone: "Not/AZone"}
	if _, err := q.Active(time.Now()); err == nil {
		t.Error("Active() with an invalid time zone succeeded")
	}
}
