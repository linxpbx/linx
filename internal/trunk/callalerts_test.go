package trunk

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
)

type callAlertStore struct {
	calls   []pbx.OutsideCall
	regions map[string]bool
}

func (s *callAlertStore) Country(context.Context) (string, error) { return "AE", nil }
func (s *callAlertStore) InternationalAlertLimits(context.Context) (int, int, error) {
	return 30, 3, nil
}
func (s *callAlertStore) RecordOutsideCall(_ context.Context, c pbx.OutsideCall) error {
	s.calls = append(s.calls, c)
	return nil
}
func (s *callAlertStore) CallsAbroadSince(_ context.Context, _ uuid.UUID, home string, since time.Time) (int, int, error) {
	n, talk := 0, 0
	for _, c := range s.calls {
		if abroad(c.Result, home) && !c.EndedAt.Before(since) {
			n++
			talk += c.TalkSeconds
		}
	}
	return n, talk, nil
}
func (s *callAlertStore) FirstCallToRegion(_ context.Context, _ uuid.UUID, region, _ string, _ time.Time) (bool, error) {
	if s.regions[region] {
		return false, nil
	}
	s.regions[region] = true
	return true, nil
}
func (s *callAlertStore) CleanupOutsideCalls(context.Context, time.Time) error { return nil }

type announcer struct {
	announced []string // titles
	open      map[string]string
}

func (a *announcer) Announce(_ context.Context, _ uuid.UUID, key, severity, title, message, _ string) error {
	a.announced = append(a.announced, severity+": "+title+": "+message)
	return nil
}
func (a *announcer) FireAfter(_ context.Context, _ uuid.UUID, key, _, _, message, _ string, _ time.Duration) error {
	a.open[key] = message
	return nil
}
func (a *announcer) Resolve(_ context.Context, _ uuid.UUID, key string) error {
	delete(a.open, key)
	return nil
}

func TestCallAlerts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	st := &callAlertStore{regions: map[string]bool{}}
	an := &announcer{open: map[string]string{}}
	var inProgress []pbx.OutsideCall
	ca := &CallAlerts{Store: st, Alerts: an, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler),
		InProgress: func() []pbx.OutsideCall { return inProgress }}
	tenant := uuid.New()
	call := func(dialled string, talk int, wentOut bool) pbx.OutsideCall {
		r := numbering.Classify("AE", dialled)
		return pbx.OutsideCall{ID: uuid.New(), Tenant: tenant, Extension: "101", Dialled: dialled, Number: r.E164, Result: r,
			StartedAt: now, WentOut: wentOut, EndedAt: now, TalkSeconds: talk}
	}

	// Emergency: announced when dialled, whatever happens next.
	ca.OutsideCallStarted(ctx, call("999", 0, false))
	ca.OutsideCallStarted(ctx, call("0501234567", 0, false))
	if len(an.announced) != 1 || !strings.HasPrefix(an.announced[0], "critical: Emergency call from extension 101") ||
		!strings.Contains(an.announced[0], "999 (police)") {
		t.Fatalf("announced %v", an.announced)
	}

	// Refused calls aren't kept; a mobile call is, but is nothing unusual.
	ca.OutsideCallEnded(ctx, call("00442079460000", 0, false))
	ca.OutsideCallEnded(ctx, call("0501234567", 600, true))
	if len(st.calls) != 1 || len(an.announced) != 1 || len(an.open) != 0 {
		t.Fatalf("calls %d, announced %v, open %v", len(st.calls), an.announced, an.open)
	}

	// The first call to the UK is announced; the second isn't.
	ca.OutsideCallEnded(ctx, call("00442079460000", 60, true))
	ca.OutsideCallEnded(ctx, call("+442079460001", 60, true))
	if len(an.announced) != 2 || !strings.Contains(an.announced[1], "First call to United Kingdom") {
		t.Fatalf("announced %v", an.announced)
	}

	// A third call abroad is within the limit (3); a fourth in progress
	// passes it.
	ca.OutsideCallEnded(ctx, call("0033142685300", 60, true))
	if len(an.open) != 0 {
		t.Fatalf("alert open at 3 calls: %v", an.open)
	}
	answered := now.Add(-40 * time.Minute)
	c := call("+12025550123", 0, true)
	c.AnsweredAt = &answered
	inProgress = []pbx.OutsideCall{c}
	ca.checkAbroad(ctx, tenant, "AE")
	if msg := an.open[internationalAlertKey]; !strings.Contains(msg, "4 calls abroad, 43 minutes") {
		t.Fatalf("alert = %q", msg)
	}

	// An hour later it's back to normal.
	now, inProgress = now.Add(2*time.Hour), nil
	ca.checkAbroad(ctx, tenant, "AE")
	if len(an.open) != 0 {
		t.Errorf("alert still open: %v", an.open)
	}
}
