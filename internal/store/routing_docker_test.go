package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/routing"
)

// TestRoutingDocker saves ring groups against real Postgres (migration
// 0032) and walks calls through asterisk.linx_route the way the dialplan
// does: all at once, one after another (skipping who can't ring and the
// caller), "if nobody answers", the shared number space, and an
// extension's removal. It needs Docker: make test-docker.
func TestRoutingDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-routing-store-test")
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
	newExtension := func(number, name, username string) pbx.Extension {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: name, Enabled: true,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		if username != "" {
			d := pbx.Device{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, ExtensionID: e.ID, Name: "Phone", Kind: pbx.KindSoftphone,
				SIPUsername: username, DigestHash: pbx.DigestHash(username, "password"), Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
			if err := s.CreateDevice(ctx, d, audit); err != nil {
				t.Fatal(err)
			}
		}
		return e
	}
	a := newExtension("101", "Aisha", "d_aisha001")
	b := newExtension("102", "Bilal", "d_bilal002")
	c := newExtension("103", "Chen", "")
	d := newExtension("104", "Dana", "d_dana0004")
	if _, err := s.SetDeviceOnline(ctx, "d_aisha001", true, nil, now); err != nil {
		t.Fatal(err)
	}
	member := func(e pbx.Extension) routing.Member { return routing.Member{ExtensionID: e.ID} }
	sales := routing.RingGroup{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Sales", Number: "600", Strategy: routing.StrategyAll,
		RingSeconds: 25, TurnSeconds: 15, Members: []routing.Member{member(a), member(b), member(c)},
		NoAnswer: routing.Destination{Kind: routing.KindExtension, ExtensionID: &d.ID}, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRingGroup(ctx, sales, audit); err != nil {
		t.Fatal(err)
	}
	support := routing.RingGroup{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Support", Strategy: routing.StrategyInTurn,
		RingSeconds: 25, TurnSeconds: 10, Members: []routing.Member{member(b), member(c), member(a)},
		NoAnswer: routing.Destination{Kind: routing.KindRingGroup, RingGroupID: &sales.ID}, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRingGroup(ctx, support, audit); err != nil {
		t.Fatal(err)
	}

	type step struct {
		Action, Targets string
		Seconds         int
		Next            string
		Counts          int
		Label           string
	}
	route := func(dest, caller string) *step {
		t.Helper()
		var st step
		err := pool.QueryRow(ctx, `SELECT * FROM asterisk.linx_route($1, $2)`, dest, caller).
			Scan(&st.Action, &st.Targets, &st.Seconds, &st.Next, &st.Counts, &st.Label)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return &st
	}
	check := func(name string, got *step, want step) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s = %+v, want %+v", name, got, want)
		}
	}

	// Dialling 600 rings everyone in Sales who has a phone, then Dana.
	check("n:600", route("n:600", "104"), step{"dial", "PJSIP/d_aisha001&PJSIP/d_bilal002", 25, "e:104", 1, "600"})
	// Aisha calling her own group doesn't ring herself.
	check("n:600 from 101", route("n:600", "101"), step{"dial", "PJSIP/d_bilal002", 25, "e:104", 1, "600"})
	// A person nobody answers (or who has no phone) goes to their
	// voicemail (migration 0034).
	check("e:104", route("e:104", ""), step{"dial", "PJSIP/d_dana0004", 30, "v:" + d.ID.String(), 1, "104"})
	check("n:103 (no phone)", route("n:103", ""), step{"next", "", 30, "v:" + c.ID.String(), 1, "103"})
	check("m:not-available", route("m:not-available", ""), step{"message", "not-available", 0, "", 0, ""})
	if got := route("n:699", ""); got != nil {
		t.Errorf("unused number routes: %+v", got)
	}
	if got := route("m:Dial(evil)", ""); got != nil {
		t.Errorf("unknown message routes: %+v", got)
	}

	// Support: Bilal, then (Chen has no phone) Aisha, then Sales.
	g := "g:" + support.ID.String()
	check("support 1", route(g, ""), step{"dial", "PJSIP/d_bilal002", 10, g + ":1", 0, ""})
	check("support 2", route(g+":1", ""), step{"dial", "PJSIP/d_aisha001", 10, g + ":3", 0, ""})
	check("support end", route(g+":3", ""), step{"next", "", 0, "g:" + sales.ID.String(), 1, ""})
	check("support from Bilal", route(g, "102"), step{"dial", "PJSIP/d_aisha001", 10, g + ":3", 0, ""})

	// Numbers: a group's number is no extension's, and the other way round.
	if err := s.CreateExtension(ctx, pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: "600", DisplayName: "X",
		Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}, audit); !errors.Is(err, pbx.ErrNumberIsRingGroup) {
		t.Errorf("extension 600: %v, want ErrNumberIsRingGroup", err)
	}
	dup := support
	dup.ID, dup.Name, dup.Number = uuid.Must(uuid.NewV7()), "Other", "101"
	if err := s.CreateRingGroup(ctx, dup, audit); !errors.Is(err, routing.ErrNumberTaken) {
		t.Errorf("group 101: %v, want ErrNumberTaken", err)
	}
	dup.Number, dup.Name = "", "Sales"
	if err := s.CreateRingGroup(ctx, dup, audit); !errors.Is(err, routing.ErrDuplicateName) {
		t.Errorf("second Sales: %v, want ErrDuplicateName", err)
	}
	if n, err := s.NextFreeExtensionNumber(ctx, tenant, "AE", 3, 600, 699); err != nil || n != "601" {
		t.Errorf("next group number = %q, %v; want 601", n, err)
	}

	// Removing a group another sends calls to is refused.
	if err := s.DeleteRingGroup(ctx, tenant, sales.ID, audit); !errors.Is(err, routing.ErrInUse) {
		t.Errorf("deleting Sales: %v, want ErrInUse", err)
	}

	// An extension removed leaves its groups; Sales' unanswered calls then
	// play "not available".
	if err := s.DeleteExtension(ctx, tenant, d.ID, now, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExtension(ctx, tenant, b.ID, now, audit); err != nil {
		t.Fatal(err)
	}
	groups, err := s.RingGroups(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Name != "Sales" || groups[0].NoAnswer.Kind != routing.KindMessage || len(groups[0].Members) != 2 {
		t.Fatalf("after removals: %+v", groups)
	}
	if !groups[0].Members[0].CanRing || groups[0].Members[1].CanRing {
		t.Errorf("can ring: %+v", groups[0].Members)
	}
	check("n:600 after", route("n:600", ""), step{"dial", "PJSIP/d_aisha001", 25, "m:not-available", 1, "600"})

	// Members in a new order, version checked.
	sales = groups[0]
	sales.Members = []routing.Member{member(c), member(a)}
	if err := s.UpdateRingGroup(ctx, sales, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateRingGroup(ctx, sales, audit); !errors.Is(err, routing.ErrVersionChanged) {
		t.Errorf("stale update: %v", err)
	}

	// Asterisk's role may call the function, and nothing else new.
	var canRoute, canRead bool
	if err := pool.QueryRow(ctx, `SELECT has_function_privilege('linx_asterisk', 'asterisk.linx_route(text, text)', 'EXECUTE'),
		has_table_privilege('linx_asterisk', 'ring_group', 'SELECT')`).Scan(&canRoute, &canRead); err != nil {
		t.Fatal(err)
	}
	if !canRoute || canRead {
		t.Errorf("linx_asterisk: execute linx_route %v (want true), read ring_group %v (want false)", canRoute, canRead)
	}
}
