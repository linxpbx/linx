package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/backupschedule"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/moved"
	"linxpbx.com/linx/internal/trunk"
)

// TestMovedDocker runs "Moved to a new place?" (docs/INSTALL.md §8)
// against real Postgres: the place written at each start, a move found
// when another server's database starts, and the facts the checklist
// reads. It needs Docker: make test-docker.
func TestMovedDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-moved-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	home := moved.Place{ServerID: "a", Domain: "pbx.old.com", LANNetworks: []string{"192.168.1.0/24"}, LANAddress: "192.168.1.212",
		PublicAddress: "198.51.100.4", FrontDoor: "pangolin"}

	// The first start, and a restart: no move.
	for i := range 2 {
		if m, err := s.RecordPlace(ctx, home, now.Add(time.Duration(i)*time.Minute), uuid.New()); err != nil || m != nil {
			t.Fatalf("start %d: %+v %v", i, m, err)
		}
	}
	// A start that can't tell the public address keeps the last one.
	blind := home
	blind.PublicAddress = ""
	if _, err := s.RecordPlace(ctx, blind, now.Add(2*time.Minute), uuid.New()); err != nil {
		t.Fatal(err)
	}
	var kept string
	if err := pool.QueryRow(ctx, `SELECT public_address FROM install_place`).Scan(&kept); err != nil || kept != "198.51.100.4" {
		t.Errorf("public address %q %v", kept, err)
	}
	if m, err := s.CurrentMove(ctx); err != nil || m != nil {
		t.Fatalf("no move yet: %+v %v", m, err)
	}

	// The same database started on another server: a move, found once.
	rented := moved.Place{ServerID: "b", Domain: "example.com", PublicAddress: "203.0.113.5", FrontDoor: "linx-443"}
	m, err := s.RecordPlace(ctx, rented, now.Add(time.Hour), uuid.New())
	if err != nil || m == nil || m.Before.Domain != "pbx.old.com" || m.Before.PublicAddress != "198.51.100.4" {
		t.Fatalf("move %+v %v", m, err)
	}
	if again, err := s.RecordPlace(ctx, rented, now.Add(2*time.Hour), uuid.New()); err != nil || again != nil {
		t.Fatalf("restart after the move: %+v %v", again, err)
	}
	cur, err := s.CurrentMove(ctx)
	if err != nil || cur == nil || cur.ID != m.ID || len(cur.Before.LANNetworks) != 1 || cur.After.Domain != "example.com" {
		t.Fatalf("current %+v %v", cur, err)
	}
	cur.Ticks = map[string]bool{moved.ItemOldServer: true}
	hidden := now.Add(moved.HideFor)
	cur.HiddenUntil = &hidden
	if err := s.UpdateMove(ctx, *cur); err != nil {
		t.Fatal(err)
	}
	if cur, _ = s.CurrentMove(ctx); !cur.Ticks[moved.ItemOldServer] || cur.HiddenUntil == nil {
		t.Errorf("after update %+v", cur)
	}

	// Facts: a LAN peer and an IP-authenticated line, desk phones, the
	// admin networks and backups.
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: "trunk.create", Result: auth.ResultOK}
	for _, tr := range []struct{ name, kind string }{{"UCM landlines", trunk.KindLANPeer}, {"Telnyx", trunk.KindIPAuthenticated}, {"VoIP.ms", trunk.KindRegistration}} {
		if err := s.CreateTrunk(ctx, trunk.Trunk{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: tr.name, Kind: tr.kind, Host: "192.0.2.10",
			Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic, DialFormat: trunk.DialE164,
			Codecs: []string{"alaw"}, MaxCalls: 4, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}, audit); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE pbx_setting SET admin_network_restricted = true, admin_networks = '{192.168.1.0/24}'`); err != nil {
		t.Fatal(err)
	}
	f, err := s.Facts(ctx, tenant)
	if err != nil || len(f.LANPeers) != 1 || f.LANPeers[0].Name != "UCM landlines" || len(f.ByAddress) != 1 || f.ByAddress[0].Name != "Telnyx" ||
		!f.AdminRestricted || len(f.AdminNetworks) != 1 || f.AdminNetworks[0] != "192.168.1.0/24" || f.BackedUpSince(m.DetectedAt) {
		t.Fatalf("facts %+v %v", f, err)
	}
	if err := s.InsertRun(ctx, backupschedule.Run{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Trigger: "manual",
		StartedAt: now.Add(3 * time.Hour), FinishedAt: now.Add(3*time.Hour + time.Minute), Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if f, _ = s.Facts(ctx, tenant); !f.BackedUpSince(m.DetectedAt) {
		t.Error("a backup after the move isn't seen")
	}

	// Done: nothing current any more.
	done := now
	cur.DoneAt = &done
	if err := s.UpdateMove(ctx, *cur); err != nil {
		t.Fatal(err)
	}
	if cur, err := s.CurrentMove(ctx); err != nil || cur != nil {
		t.Errorf("done: %+v %v", cur, err)
	}
}
