package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/routing"
	"linxpbx.com/linx/internal/trunk"
)

// TestOfficeHoursDocker checks office hours at fixed times against real
// Postgres (migration 0033): open, closed, the weekend, a holiday, a
// holiday every year, in the server's time zone; and a number's "When
// someone calls" walked through linx_route_at the way the dialplan does.
// It needs Docker: make test-docker.
func TestOfficeHoursDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-hours-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTimeZone(ctx, pool, "Asia/Dubai"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTimeZone(ctx, pool, "Mars/Olympus"); err == nil {
		t.Error("an unknown time zone was taken")
	}
	if err := db.SetTimeZone(ctx, pool, "Asia/Dubai"); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "test", Result: auth.ResultOK}

	// A business gets "Office hours" (Mon-Fri 08:00-17:00), once.
	cur, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cur.SiteKind = "business"
	for range 2 {
		if _, err := s.UpdateSettings(ctx, cur, audit); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.OfficeHours(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "Office hours" || routing.HoursWords(list[0].Spans) != "Mon–Fri 08:00–17:00" {
		t.Fatalf("default schedule = %+v", list)
	}
	office := list[0]
	office.Holidays = []routing.Holiday{
		{Name: "National Day", FirstDay: "2026-12-02", LastDay: "2026-12-03"},
		{Name: "New Year's Day", FirstDay: "2025-01-01", LastDay: "2025-01-01", EveryYear: true},
	}
	office.Spans = append(office.Spans, routing.Span{Weekday: 6, Opens: "09:00", Closes: "24:00"})
	if err := s.UpdateOfficeHours(ctx, office, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOfficeHours(ctx, office, audit); !errors.Is(err, routing.ErrVersionChanged) {
		t.Errorf("stale update: %v", err)
	}
	office.Version++

	dubai := time.FixedZone("Dubai", 4*3600)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, dubai) }
	for _, c := range []struct {
		name    string
		at      time.Time
		open    bool
		holiday string
		changes time.Time
	}{
		{"Wednesday morning", at(2026, 9, 30, 10, 0), true, "", at(2026, 9, 30, 17, 0)},
		{"Wednesday at closing", at(2026, 9, 30, 17, 0), false, "", at(2026, 10, 1, 8, 0)},
		{"Friday evening", at(2026, 10, 2, 20, 0), false, "", at(2026, 10, 3, 9, 0)},
		{"Saturday night", at(2026, 10, 3, 23, 30), true, "", at(2026, 10, 4, 0, 0)},
		{"Sunday", at(2026, 10, 4, 10, 0), false, "", at(2026, 10, 5, 8, 0)},
		{"National Day", at(2026, 12, 2, 10, 0), false, "National Day", at(2026, 12, 4, 8, 0)},
		{"New Year 2027, every year", at(2027, 1, 1, 10, 0), false, "New Year's Day", at(2027, 1, 2, 9, 0)},
	} {
		states, err := s.OfficeHoursStates(ctx, tenant, c.at)
		if err != nil {
			t.Fatal(err)
		}
		st := states[office.ID]
		if st.Open != c.open || st.Holiday != c.holiday || st.ChangesAt == nil || !st.ChangesAt.Equal(c.changes) {
			t.Errorf("%s: %+v (changes %v), want open %v holiday %q changes %v", c.name, st, st.ChangesAt, c.open, c.holiday, c.changes)
		}
	}

	// A number: Aisha in office hours (20 s, then Sales), "We're closed"
	// outside them, Bilal on holidays.
	newExtension := func(number, name, username string) pbx.Extension {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: name, Enabled: true,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		d := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: e.ID, Name: "Phone", Kind: pbx.KindSoftphone,
			SIPUsername: username, DigestHash: pbx.DigestHash(username, "password"), Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateDevice(ctx, d, audit); err != nil {
			t.Fatal(err)
		}
		return e
	}
	aisha := newExtension("101", "Aisha", "d_aisha001")
	bilal := newExtension("102", "Bilal", "d_bilal002")
	sales := routing.RingGroup{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Sales", Number: "600", Strategy: routing.StrategyAll,
		RingSeconds: 25, TurnSeconds: 15, Members: []routing.Member{{ExtensionID: bilal.ID}},
		NoAnswer: routing.Destination{Kind: routing.KindMessage, Message: routing.MessageNotAvailable}, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRingGroup(ctx, sales, audit); err != nil {
		t.Fatal(err)
	}
	line := trunk.Trunk{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Provider", Kind: trunk.KindRegistration, Host: "sip.example.com",
		Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic,
		DialFormat: trunk.DialE164, Codecs: []string{"alaw"}, MaxCalls: 4, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTrunk(ctx, line, audit); err != nil {
		t.Fatal(err)
	}
	did := trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: line.ID, Number: "+97142000100",
		ExtensionID: &aisha.ID, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateDID(ctx, did, audit); err != nil {
		t.Fatal(err)
	}
	incoming := func() routing.Incoming {
		t.Helper()
		all, err := s.IncomingList(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		for _, in := range all {
			if in.ID == did.ID {
				return in
			}
		}
		t.Fatal("number missing from IncomingList")
		return routing.Incoming{}
	}
	in := incoming()
	if in.Kind != routing.IncomingNumber || in.Rings == nil || *in.Rings.ExtensionID != aisha.ID || in.Rule != nil || in.LineName != "Provider" {
		t.Fatalf("before: %+v", in)
	}
	in.Rule = &routing.Rule{NoAnswerSeconds: 20, NoAnswer: routing.Destination{Kind: routing.KindRingGroup, RingGroupID: &sales.ID},
		ScheduleID: &office.ID, Closed: routing.Destination{Kind: routing.KindMessage, Message: routing.MessageClosed},
		Holiday: &routing.Destination{Kind: routing.KindExtension, ExtensionID: &bilal.ID}}
	if err := s.SetIncoming(ctx, in, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIncoming(ctx, in, audit); !errors.Is(err, routing.ErrVersionChanged) {
		t.Errorf("stale SetIncoming: %v", err)
	}
	in = incoming()
	if in.Rule == nil || in.Rule.NoAnswerSeconds != 20 || in.Rule.Holiday == nil || *in.Rule.ScheduleID != office.ID {
		t.Fatalf("after: %+v %+v", in, in.Rule)
	}

	step := func(dest string, when time.Time) routing.Step {
		t.Helper()
		st, err := s.RouteStep(ctx, dest, "", when)
		if err != nil || st == nil {
			t.Fatalf("RouteStep(%s): %v, %v", dest, st, err)
		}
		return *st
	}
	d := "d:" + did.ID.String()
	check := func(name string, got, want routing.Step) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
	check("open", step(d, at(2026, 9, 30, 10, 0)), routing.Step{Action: "dial", Targets: "PJSIP/d_aisha001", Seconds: 20,
		Next: "g:" + sales.ID.String(), Counts: 1, Label: "101"})
	check("closed", step(d, at(2026, 10, 2, 20, 0)), routing.Step{Action: "next", Next: "m:closed", Label: "closed"})
	check("holiday", step(d, at(2026, 12, 2, 10, 0)), routing.Step{Action: "next", Next: "e:102", Label: "holiday"})
	check("closed message", step("m:closed", now), routing.Step{Action: "message", Targets: "closed"})

	// The dialplan's own function (now) answers like linx_route_at.
	var action string
	if err := pool.QueryRow(ctx, `SELECT action FROM asterisk.linx_route($1, '')`, d).Scan(&action); err != nil || action == "" {
		t.Errorf("asterisk.linx_route(d:…) = %q, %v", action, err)
	}

	// A ring group the number rings can't be removed; nor the hours.
	in.Rings = &routing.Destination{Kind: routing.KindRingGroup, RingGroupID: &sales.ID}
	if err := s.SetIncoming(ctx, in, audit); err != nil {
		t.Fatal(err)
	}
	check("open, ring group", step(d, at(2026, 9, 30, 10, 0)), routing.Step{Action: "next", Next: "g:" + sales.ID.String(), Label: "open"})
	if err := s.DeleteRingGroup(ctx, tenant, sales.ID, audit); !errors.Is(err, routing.ErrInUse) {
		t.Errorf("deleting Sales: %v, want ErrInUse", err)
	}
	if err := s.DeleteOfficeHours(ctx, tenant, office.ID, audit); !errors.Is(err, routing.ErrInUse) {
		t.Errorf("deleting Office hours: %v, want ErrInUse", err)
	}
	// The trunk's own PATCH of who it rings (Phone lines) clears the group.
	got, err := s.DID(ctx, tenant, did.ID)
	if err != nil || got.RingGroupID == nil || got.ExtensionID != nil {
		t.Fatalf("DID: %+v, %v", got, err)
	}

	// Removing Bilal sends holidays to "not available".
	if err := s.DeleteExtension(ctx, tenant, bilal.ID, now, audit); err != nil {
		t.Fatal(err)
	}
	in = incoming()
	if h := in.Rule.Holiday; h == nil || h.Kind != routing.KindMessage || h.Message != routing.MessageNotAvailable {
		t.Errorf("holidays after Bilal's removal: %+v", h)
	}

	// Just ring: the rule goes, and the person rings as before.
	in.Rings, in.Rule = &routing.Destination{Kind: routing.KindExtension, ExtensionID: &aisha.ID}, nil
	if err := s.SetIncoming(ctx, in, audit); err != nil {
		t.Fatal(err)
	}
	check("just ring", step(d, at(2026, 10, 2, 20, 0)), routing.Step{Action: "next", Next: "e:101", Label: "always"})
	if err := s.DeleteOfficeHours(ctx, tenant, office.ID, audit); err != nil {
		t.Errorf("deleting unused Office hours: %v", err)
	}

	// Asterisk's role may call linx_route only, not linx_route_at or the tables.
	var direct, read bool
	if err := pool.QueryRow(ctx, `SELECT has_function_privilege('linx_asterisk', 'linx_route_at(text, text, timestamptz)', 'EXECUTE'),
		has_table_privilege('linx_asterisk', 'incoming_rule', 'SELECT')`).Scan(&direct, &read); err != nil {
		t.Fatal(err)
	}
	if direct || read {
		t.Errorf("linx_asterisk: linx_route_at %v, incoming_rule %v (want both false)", direct, read)
	}
}
