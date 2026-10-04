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
		{Extension: "101", Name: "Sara Haddad", Reachable: true, Presence: pbx.PresenceAvailable},
		{Extension: "102", Name: "Reception", Reachable: false},
		{Extension: "103", Name: "Nobody's", Reachable: false},
	}
	if !slices.Equal(members, want) {
		t.Fatalf("TeamMembers = %+v\nwant %+v", members, want)
	}

	// A phone in a pocket is not a closed browser (owner, 2026-10-04,
	// migration 0045). Reception's phone is an iPhone that has never
	// signed in and never will until somebody rings it: with an Apple key
	// in place and a push token sent, that person is reachable, and the
	// Team list has to say so or nobody can tell who to ring.
	omarExt := newExtension("104", "Omar's")
	appPhone := uuid.Must(uuid.NewV7())
	if err := s.CreateDevice(ctx, pbx.Device{ID: appPhone, TenantID: tenant, ExtensionID: omarExt.ID,
		Name: "Omar's iPhone", Kind: pbx.KindIOS, SIPUsername: "d_omar0001",
		DigestHash: pbx.DigestHash("d_omar0001", "password"), Enabled: true, Version: 1,
		CreatedAt: now, UpdatedAt: now}, audit); err != nil {
		t.Fatal(err)
	}
	omar := newPerson("omar@example.com", "Omar Nasser", &omarExt.ID)
	if _, err := pool.Exec(ctx, `INSERT INTO device_identity
		(device_id, tenant_id, user_id, public_key, cert_serial, cert_fingerprint, cert_not_after,
		 enrolled_at, last_seen_at, expires_at, voip_token, push_environment)
		VALUES ($1, $2, $3, '\x00', '01', '\x01', $4, $5, $5, $4, repeat('ab', 32), 'production')`,
		appPhone, tenant, omar.ID, now.Add(180*24*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	reachable := func(number string) bool {
		t.Helper()
		members, err := s.TeamMembers(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range members {
			if m.Extension == number {
				return m.Reachable
			}
		}
		t.Fatalf("no extension %s", number)
		return false
	}
	// No Apple key yet: nothing can wake that phone, so saying the person
	// is there would be a lie.
	if reachable("104") {
		t.Error("an app phone is reachable with no Apple key in place")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO push_settings
		(tenant_id, enabled, team_id, key_id, bundle_id, key_enc, wait_ms, updated_at)
		VALUES ($1, true, 'AY75S2Z9UK', 'ABCDE12345', 'com.linxpbx.app', '\x00', 6000, now())`,
		tenant); err != nil {
		t.Fatal(err)
	}
	if !reachable("104") {
		t.Error("an app phone a push can wake should count as reachable")
	}
	// And it is the same phone the dialplan would push to: one view, read
	// by both, so the list and the ringing can't disagree.
	var aors string
	if err := pool.QueryRow(ctx, `SELECT aors FROM asterisk.linx_wake($1)`,
		"PJSIP/d_omar0001&PJSIP/d_desk0001").Scan(&aors); err != nil {
		t.Fatal(err)
	}
	if aors != "d_omar0001" {
		t.Errorf("linx_wake = %q, want d_omar0001", aors)
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
