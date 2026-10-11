package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/callreport"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
)

// TestCallReportsDocker checks what phones send about their calls against
// real Postgres (migration 0049): a report is kept and read back newest
// first, only for its own company, and saving one drops that phone's
// reports past seven days or past its newest hundred. It needs Docker:
// make test-docker.
func TestCallReportsDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-callreport-store-test")
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
	svc := callreport.Service{Store: s, Now: func() time.Time { return clock }}
	send := func(callID string) {
		t.Helper()
		if err := svc.Save(ctx, tenant, phone.ID, callreport.Report{CallID: callID, Direction: "incoming",
			Peer: "201", StartedAt: clock, Text: "Linx call details\nroute: relayed (udp)"}); err != nil {
			t.Fatal(err)
		}
	}

	// An old one, then a week later a new one: the old one goes.
	send("old")
	clock = now.Add(callreport.KeepFor + time.Hour)
	send("new")
	got, err := svc.List(ctx, tenant, phone.ID)
	if err != nil || len(got) != 1 || got[0].CallID != "new" || got[0].Text != "Linx call details\nroute: relayed (udp)" {
		t.Fatalf("after a week: %+v, %v", got, err)
	}

	// Never more than the newest hundred per phone.
	for i := range callreport.KeepPerPhone + 5 {
		clock = clock.Add(time.Second)
		send(fmt.Sprintf("call-%03d", i))
	}
	got, err = svc.List(ctx, tenant, phone.ID)
	if err != nil || len(got) != callreport.KeepPerPhone {
		t.Fatalf("kept %d, %v; want %d", len(got), err, callreport.KeepPerPhone)
	}
	if got[0].CallID != fmt.Sprintf("call-%03d", callreport.KeepPerPhone+4) {
		t.Errorf("newest first: got %q first", got[0].CallID)
	}

	// Another company sees none of them.
	if other, err := svc.List(ctx, uuid.Must(uuid.NewV7()), phone.ID); err != nil || len(other) != 0 {
		t.Errorf("another tenant saw %d reports, %v", len(other), err)
	}

	// Taking the phone away takes its reports with it.
	if _, err := pool.Exec(ctx, `DELETE FROM device WHERE id = $1`, phone.ID); err != nil {
		t.Fatal(err)
	}
	if left, err := svc.List(ctx, tenant, phone.ID); err != nil || len(left) != 0 {
		t.Errorf("a removed phone kept %d reports, %v", len(left), err)
	}
}
