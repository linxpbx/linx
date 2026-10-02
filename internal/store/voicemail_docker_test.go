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
	"linxpbx.com/linx/internal/voicemail"
)

// TestVoicemailDocker checks voicemail against real Postgres (migration
// 0034): every person and ring group gets a box; linx_route sends
// unanswered calls there, with the "we're closed" greeting outside office
// hours; a box that's off plays "not available"; a message is stored once
// with its voicemail.created event; and removals keep routing sane. It
// needs Docker: make test-docker.
func TestVoicemailDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-voicemail-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
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
	newExtension := func(number, name string) pbx.Extension {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: name, Enabled: true,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		return e
	}
	sara := newExtension("101", "Sara Haddad")
	omar := newExtension("102", "Omar")
	// A group that sends its unanswered calls to its own box, made in one
	// go (the box comes with the group).
	sales := routing.RingGroup{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Sales", Number: "600", Strategy: routing.StrategyAll,
		RingSeconds: 25, TurnSeconds: 15, Members: []routing.Member{{ExtensionID: sara.ID}}, Version: 1, CreatedAt: now, UpdatedAt: now}
	sales.NoAnswer = routing.Destination{Kind: routing.KindVoicemail, RingGroupID: &sales.ID}
	if err := s.CreateRingGroup(ctx, sales, audit); err != nil {
		t.Fatal(err)
	}
	groups, err := s.RingGroups(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if na := groups[0].NoAnswer; na.Kind != routing.KindVoicemail || na.RingGroupID == nil || *na.RingGroupID != sales.ID || na.ExtensionID != nil {
		t.Fatalf("Sales' unanswered calls: %+v", na)
	}

	box, err := s.VoicemailBox(ctx, sara.ID)
	if err != nil || box.Owner != "Sara Haddad (101)" || !box.Enabled || !box.Email || box.ExtensionID == nil {
		t.Fatalf("Sara's box: %+v, %v", box, err)
	}
	gbox, err := s.VoicemailBox(ctx, sales.ID)
	if err != nil || gbox.Owner != "Sales" || gbox.Email || gbox.RingGroupID == nil {
		t.Fatalf("Sales' box: %+v, %v", gbox, err)
	}
	if _, err := s.VoicemailBox(ctx, uuid.New()); !errors.Is(err, voicemail.ErrNotFound) {
		t.Errorf("no such box: %v", err)
	}

	step := func(dest string) *routing.Step {
		t.Helper()
		st, err := s.RouteStep(ctx, dest, "", now)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	check := func(name string, got *routing.Step, want routing.Step) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}
	v := "v:" + sara.ID.String()
	check("Sara (no phone)", step("e:101"), routing.Step{Action: "next", Seconds: 30, Next: v, Counts: 1, Label: "101"})
	check("Sales", step("g:"+sales.ID.String()), routing.Step{Action: "next", Seconds: 25, Next: "v:" + sales.ID.String(), Counts: 1, Label: "600"})
	check("Sara's box", step(v), routing.Step{Action: "voicemail", Targets: sara.ID.String(), Label: "unavailable"})
	check("closed greeting", step(v+":closed"), routing.Step{Action: "voicemail", Targets: sara.ID.String(), Label: "closed"})
	if st := step(v + ":Dial(evil)"); st != nil {
		t.Errorf("a strange greeting routes: %+v", st)
	}
	if st := step("v:" + uuid.NewString()); st == nil || st.Next != "m:not-available" {
		t.Errorf("no such box: %+v", st)
	}
	// Turned off: straight on to "not available".
	if _, err := pool.Exec(ctx, `UPDATE voicemail_box SET enabled = false WHERE id = $1`, omar.ID); err != nil {
		t.Fatal(err)
	}
	check("Omar, box off", step("e:102"), routing.Step{Action: "next", Seconds: 30, Next: "m:not-available", Counts: 1, Label: "102"})
	check("Omar's box, off", step("v:"+omar.ID.String()), routing.Step{Action: "next", Next: "m:not-available"})

	// A number: Sara in office hours, her voicemail when nobody answers,
	// and Sales' voicemail with the "we're closed" greeting outside them.
	cur, err := s.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cur.SiteKind = "business"
	if _, err := s.UpdateSettings(ctx, cur, audit); err != nil {
		t.Fatal(err)
	}
	hours, err := s.OfficeHours(ctx, tenant)
	if err != nil || len(hours) != 1 {
		t.Fatalf("office hours: %+v, %v", hours, err)
	}
	line := trunk.Trunk{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Provider", Kind: trunk.KindRegistration, Host: "sip.example.com",
		Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic,
		DialFormat: trunk.DialE164, Codecs: []string{"alaw"}, MaxCalls: 4, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTrunk(ctx, line, audit); err != nil {
		t.Fatal(err)
	}
	did := trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: line.ID, Number: "+97142000100",
		ExtensionID: &sara.ID, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateDID(ctx, did, audit); err != nil {
		t.Fatal(err)
	}
	in := routing.Incoming{Kind: routing.IncomingNumber, ID: did.ID, TenantID: tenant, Version: 1,
		Rings: &routing.Destination{Kind: routing.KindExtension, ExtensionID: &sara.ID},
		Rule: &routing.Rule{NoAnswerSeconds: 20, NoAnswer: routing.Destination{Kind: routing.KindVoicemail, ExtensionID: &sara.ID},
			ScheduleID: &hours[0].ID, Closed: routing.Destination{Kind: routing.KindVoicemail, RingGroupID: &sales.ID}}}
	if err := s.SetIncoming(ctx, in, audit); err != nil {
		t.Fatal(err)
	}
	list, err := s.IncomingList(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.ID != did.ID {
			continue
		}
		if r := x.Rule; r == nil || r.NoAnswer.Kind != routing.KindVoicemail || *r.NoAnswer.ExtensionID != sara.ID ||
			r.Closed.Kind != routing.KindVoicemail || *r.Closed.RingGroupID != sales.ID {
			t.Errorf("rule read back: %+v", x.Rule)
		}
	}
	dubai := time.FixedZone("Dubai", 4*3600)
	sunday := time.Date(2026, 10, 4, 10, 0, 0, 0, dubai)
	st, err := s.RouteStep(ctx, "d:"+did.ID.String(), "", sunday)
	if err != nil || st == nil || st.Next != "v:"+sales.ID.String()+":closed" || st.Label != "closed" {
		t.Errorf("closed on Sunday: %+v, %v", st, err)
	}
	// Sales' box is in use by the number now: Sales can't be removed.
	if err := s.DeleteRingGroup(ctx, tenant, sales.ID, audit); !errors.Is(err, routing.ErrInUse) {
		t.Errorf("deleting Sales: %v, want ErrInUse", err)
	}

	// A message: stored once, with its event; only Sara's own account
	// gets it by email.
	m := voicemail.Message{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, BoxID: sara.ID, Source: "1727850000.42",
		CallerNumber: "0501234567", CallerName: "Ahmed Ali", ReceivedAt: now, Duration: 42 * time.Second,
		Audio: make([]byte, 42*voicemail.SampleRate), CreatedAt: now}
	for i, want := range []bool{true, false} {
		m2 := m
		if i == 1 {
			m2.ID = uuid.Must(uuid.NewV7())
		}
		added, err := s.AddVoicemail(ctx, m2, map[string]any{"id": m2.ID})
		if err != nil || added != want {
			t.Fatalf("AddVoicemail #%d: %v, %v; want %v", i+1, added, err, want)
		}
	}
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE type = 'voicemail.created'`).Scan(&events); err != nil || events != 1 {
		t.Errorf("voicemail.created events = %d, %v", events, err)
	}
	got, err := s.VoicemailMessage(ctx, tenant, m.ID)
	if err != nil || got.Duration != 42*time.Second || len(got.Audio) != 42*voicemail.SampleRate || got.CallerName != "Ahmed Ali" {
		t.Errorf("message read back: %+v, %v", got.Duration, err)
	}
	if _, err := s.VoicemailMessage(ctx, uuid.New(), m.ID); !errors.Is(err, voicemail.ErrNotFound) {
		t.Errorf("another tenant's message: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO app_user (id, tenant_id, email, name, role, extension_id, password_hash,
			password_updated_at, created_at, updated_at) VALUES ($1, $2, 'sara@example.com', 'Sara', 'user', $3, 'x', now(), now(), now()),
			($4, $2, 'old@example.com', 'Old', 'user', $3, 'x', now(), now(), now())`,
		uuid.New(), tenant, sara.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE app_user SET disabled_at = now() WHERE email = 'old@example.com'`); err != nil {
		t.Fatal(err)
	}
	if to, err := s.VoicemailEmailTo(ctx, box); err != nil || len(to) != 1 || to[0] != "sara@example.com" {
		t.Errorf("email to %v, %v", to, err)
	}
	if to, err := s.VoicemailEmailTo(ctx, gbox); err != nil || len(to) != 0 {
		t.Errorf("a group's box emails %v, %v", to, err)
	}
	if ext, name, err := s.CallerExtension(ctx, tenant, "102"); err != nil || ext == nil || *ext != omar.ID || name != "Omar" {
		t.Errorf("caller 102: %v %q %v", ext, name, err)
	}

	// Removing Sara: the number's "if nobody answers" (her voicemail)
	// plays "not available" instead; her box keeps its message.
	if err := s.DeleteExtension(ctx, tenant, sara.ID, now, audit); err != nil {
		t.Fatal(err)
	}
	list, err = s.IncomingList(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.ID != did.ID {
			continue
		}
		if x.Rule == nil || x.Rule.NoAnswer.Kind != routing.KindMessage {
			t.Errorf("after Sara's removal: %+v", x.Rule)
		}
		in = x
	}
	if _, err := s.VoicemailMessage(ctx, tenant, m.ID); err != nil {
		t.Errorf("Sara's message after her removal: %v", err)
	}
	if st := step(v); st == nil || st.Next != "m:not-available" {
		t.Errorf("a removed person's box: %+v", st)
	}

	// Once nothing else sends calls there, Sales goes with its box.
	in.Rings, in.Rule = nil, nil
	if err := s.SetIncoming(ctx, in, audit); err != nil {
		t.Fatal(err)
	}
	gm := m
	gm.ID, gm.BoxID, gm.Source = uuid.Must(uuid.NewV7()), sales.ID, "1727850001.1"
	if _, err := s.AddVoicemail(ctx, gm, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRingGroup(ctx, tenant, sales.ID, audit); err != nil {
		t.Fatalf("deleting Sales: %v", err)
	}
	if _, err := s.VoicemailBox(ctx, sales.ID); !errors.Is(err, voicemail.ErrNotFound) {
		t.Errorf("Sales' box after removal: %v", err)
	}

	// Asterisk's role can't read voicemail.
	var read bool
	if err := pool.QueryRow(ctx, `SELECT has_table_privilege('linx_asterisk', 'voicemail_message', 'SELECT')
		OR has_table_privilege('linx_asterisk', 'voicemail_box', 'SELECT')`).Scan(&read); err != nil || read {
		t.Errorf("linx_asterisk reads voicemail: %v, %v", read, err)
	}
}
