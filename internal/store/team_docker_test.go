package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
)

// TestTeamDocker runs the Team list's query and people's chosen status
// against real Postgres (migration 0014), including "Do not disturb"
// stopping an extension ringing. It needs Docker: make test-docker.
func TestTeamDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-team-store-test")
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
	newExtension := func(number, name string) pbx.Extension {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: name, Enabled: true,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		return e
	}
	newPhone := func(ext uuid.UUID, username string) {
		d := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: ext, Name: "Phone", Kind: pbx.KindSoftphone,
			SIPUsername: username, DigestHash: pbx.DigestHash(username, "password"), Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateDevice(ctx, d, audit); err != nil {
			t.Fatal(err)
		}
	}
	newPerson := func(email, name string, ext *uuid.UUID) auth.User {
		u := auth.User{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Email: email, Name: name, Role: auth.RoleUser,
			ExtensionID: ext, PasswordHash: "x", PasswordUpdatedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateUser(ctx, u, audit); err != nil {
			t.Fatal(err)
		}
		return u
	}

	sara := newExtension("101", "Front office")
	desk := newExtension("102", "Reception")
	newExtension("103", "Nobody's")
	newPhone(sara.ID, "d_sara0001")
	newPhone(desk.ID, "d_desk0001")
	if _, err := s.SetDeviceOnline(ctx, "d_sara0001", true, nil, now); err != nil {
		t.Fatal(err)
	}
	person := newPerson("sara@example.com", "Sara Haddad", &sara.ID)
	newPerson("noext@example.com", "No Extension", nil)

	members, err := s.TeamMembers(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	want := []pbx.TeamMember{
		{Extension: "101", Name: "Sara Haddad", Online: true, Presence: pbx.PresenceAvailable},
		{Extension: "102", Name: "Reception", Online: false},
		{Extension: "103", Name: "Nobody's", Online: false},
	}
	if !slices.Equal(members, want) {
		t.Fatalf("TeamMembers = %+v\nwant %+v", members, want)
	}

	ringTargets := func(number string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT coalesce(aor, '') FROM asterisk.linx_ring_targets WHERE number = $1`, number)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				t.Fatal(err)
			}
			out = append(out, a)
		}
		return out
	}
	events := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM event_outbox WHERE type = 'presence.changed'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if got := ringTargets("101"); !slices.Equal(got, []string{"d_sara0001"}) {
		t.Fatalf("available: rings %v", got)
	}
	if err := s.SetPresence(ctx, tenant, person.ID, pbx.PresenceDND); err != nil {
		t.Fatal(err)
	}
	// Still listed (so callers hear "not available", not "no such number"),
	// but nothing rings.
	if got := ringTargets("101"); !slices.Equal(got, []string{""}) {
		t.Fatalf("do not disturb: rings %v", got)
	}
	// A desk phone's extension, with nobody on it, isn't affected.
	if got := ringTargets("102"); !slices.Equal(got, []string{"d_desk0001"}) {
		t.Fatalf("desk phone: rings %v", got)
	}
	if err := s.SetPresence(ctx, tenant, person.ID, pbx.PresenceDND); err != nil {
		t.Fatal(err)
	}
	if n := events(); n != 1 {
		t.Errorf("%d presence.changed events, want 1 (setting the same status again isn't a change)", n)
	}
	if err := s.SetPresence(ctx, tenant, person.ID, pbx.PresenceAway); err != nil {
		t.Fatal(err)
	}
	if got := ringTargets("101"); !slices.Equal(got, []string{"d_sara0001"}) {
		t.Fatalf("away: rings %v", got)
	}
	members, _ = s.TeamMembers(ctx, tenant)
	if members[0].Presence != pbx.PresenceAway {
		t.Errorf("presence after SetPresence: %q", members[0].Presence)
	}
	if n := events(); n != 2 {
		t.Errorf("%d presence.changed events, want 2", n)
	}
	if err := s.SetPresence(ctx, tenant, uuid.New(), pbx.PresenceAway); err != pbx.ErrNotFound {
		t.Errorf("unknown person: %v", err)
	}
}
