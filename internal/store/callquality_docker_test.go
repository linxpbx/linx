package store

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/callquality"
	"linxpbx.com/linx/internal/callreport"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
)

// TestCallQualityDocker checks Linx's own judgement of calls against real
// Postgres (migration 0055): a leg Asterisk measured is kept with its phone,
// shown beside the phone's own report of the same call and its verdict
// line, old records go as new ones come, and a missed wake is kept as bad.
// It needs Docker: make test-docker.
func TestCallQualityDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-callquality-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "test", Result: auth.ResultOK}
	ext := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: "101", DisplayName: "Sara Haddad",
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateExtension(ctx, ext, audit); err != nil {
		t.Fatal(err)
	}
	username := pbx.NewSIPUsername()
	phone := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: ext.ID, Name: "Sara's iPhone",
		Kind: pbx.KindIOS, SIPUsername: username, DigestHash: pbx.DigestHash(username, "a-password-nobody-keeps"),
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateDevice(ctx, phone, audit); err != nil {
		t.Fatal(err)
	}

	clock := now
	q := callquality.Service{Store: s, Now: func() time.Time { return clock }, Log: slog.New(slog.DiscardHandler)}
	q.Leg(ctx, "PJSIP/"+username+"-00000001", "old-call", clock, "lp=0;rxjitter=0.002;rxcount=3000;txcount=3000;rlp=0;rtt=0.04")
	clock = now.Add(callquality.KeptFor + time.Hour)
	q.Leg(ctx, "PJSIP/"+username+"-00000002", "call-1", clock.Add(-time.Minute),
		"lp=300;rxjitter=0.002;rxcount=2700;txcount=3000;rlp=0;rtt=0.04")
	q.WakeMissed(ctx, []string{username}, 10*time.Second)
	q.Leg(ctx, "PJSIP/someone-elses-00000003", "x", clock, "rxcount=1;txcount=1")

	reports := callreport.Service{Store: s, Now: func() time.Time { return clock }}
	if err := reports.Save(ctx, tenant, phone.ID, callreport.Report{CallID: "call-1", Direction: "incoming", Peer: "201",
		StartedAt: clock, Text: "Linx call details\nto: 201\nverdict: Had problems: their picture stopped 3 times, 4 s in all\nroute: relayed (udp)"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.CallQuality(ctx, tenant, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("kept %d records, want the leg and the wake (the week-old one gone): %+v", len(got), got)
	}
	var leg, wake *CallQualityItem
	for i := range got {
		switch got[i].Kind {
		case callquality.KindCall:
			leg = &got[i]
		case callquality.KindWake:
			wake = &got[i]
		}
	}
	if leg == nil || leg.CallID != "call-1" || leg.Verdict != callquality.Bad || leg.DeviceName != "Sara's iPhone" ||
		leg.Extension != "101" || leg.Person != "Sara Haddad" || leg.Sound.InLost < 9.9 || leg.PhoneReport == nil ||
		leg.PhoneVerdict != "Had problems: their picture stopped 3 times, 4 s in all" {
		t.Errorf("leg: %+v", leg)
	}
	if wake == nil || wake.Verdict != callquality.Bad || wake.PhoneReport != nil ||
		!strings.Contains(strings.Join(wake.Reasons, ""), "hadn't connected") {
		t.Errorf("wake: %+v", wake)
	}
	if n, err := s.BadCallsSince(ctx, phone.ID, now); err != nil || n != 2 {
		t.Errorf("bad since: %d, %v", n, err)
	}
	other, err := s.CallQuality(ctx, uuid.New(), 50)
	if err != nil || len(other) != 0 {
		t.Errorf("another company saw %d, %v", len(other), err)
	}
}
