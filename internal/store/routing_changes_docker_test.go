package store

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/routing"
	"linxpbx.com/linx/internal/trunk"
)

// TestRoutingChangesDocker keeps routing versions and puts them back
// against real Postgres (migration 0038): a put-back undoes everything
// since (a group added since goes, one removed comes back), a person
// removed since stays out, undoing the put-back gives the later routing
// again, "Undo" refuses once someone changed routing again, a version that
// no longer fits is refused whole, a preview keeps nothing, saves that
// don't touch routing aren't kept, and only the last 50 are. It needs
// Docker: make test-docker.
func TestRoutingChangesDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-routing-changes-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	audit := auth.AuditEntry{TenantID: &tenant, Actor: "system:test", Action: "test", Result: auth.ResultOK}
	newExtension := func(number, name string) pbx.Extension {
		e := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: number, DisplayName: name, Enabled: true,
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit); err != nil {
			t.Fatal(err)
		}
		return e
	}
	aisha := newExtension("101", "Aisha")
	bilal := newExtension("102", "Bilal")
	latest := func() uuid.UUID {
		t.Helper()
		list, err := s.RoutingChanges(ctx, tenant)
		if err != nil || len(list) == 0 {
			t.Fatalf("changes: %v, %v", list, err)
		}
		return list[0].ID
	}
	count := func() int {
		t.Helper()
		list, err := s.RoutingChanges(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	group := func(id uuid.UUID) *routing.RingGroup {
		t.Helper()
		list, err := s.RingGroups(ctx, tenant)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range list {
			if g.ID == id {
				return &g
			}
		}
		return nil
	}
	members := func(g *routing.RingGroup) []uuid.UUID {
		var out []uuid.UUID
		for _, m := range g.Members {
			out = append(out, m.ExtensionID)
		}
		return out
	}

	// The routing at the start: Sales (600, Aisha and Bilal, then Aisha),
	// office hours, a number ringing Sales in them, one line out, a
	// calling level allowing mobiles.
	sales := routing.RingGroup{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Sales", Number: "600", Strategy: routing.StrategyAll,
		RingSeconds: 25, TurnSeconds: 15, Members: []routing.Member{{ExtensionID: aisha.ID}, {ExtensionID: bilal.ID}},
		NoAnswer: routing.Destination{Kind: routing.KindExtension, ExtensionID: &aisha.ID}, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateRingGroup(ctx, sales, audit); err != nil {
		t.Fatal(err)
	}
	office := routing.Schedule{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Office hours", Spans: routing.DefaultSpans(),
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateOfficeHours(ctx, office, audit); err != nil {
		t.Fatal(err)
	}
	line := trunk.Trunk{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Provider", Kind: trunk.KindRegistration, Host: "sip.example.com",
		Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic,
		DialFormat: trunk.DialE164, Codecs: []string{"alaw"}, MaxCalls: 4, Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTrunk(ctx, line, audit); err != nil {
		t.Fatal(err)
	}
	did := trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: line.ID, Number: "+97142000100",
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateDID(ctx, did, audit); err != nil {
		t.Fatal(err)
	}
	in := routing.Incoming{Kind: routing.IncomingNumber, ID: did.ID, TenantID: tenant, Version: 1,
		Rings: &routing.Destination{Kind: routing.KindRingGroup, RingGroupID: &sales.ID},
		Rule: &routing.Rule{NoAnswerSeconds: 25, NoAnswer: routing.Destination{Kind: routing.KindMessage, Message: routing.MessageNotAvailable},
			ScheduleID: &office.ID, Closed: routing.Destination{Kind: routing.KindMessage, Message: routing.MessageClosed}}}
	if err := s.SetIncoming(ctx, in, audit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetOutboundOrder(ctx, tenant, []uuid.UUID{line.ID}, now, audit); err != nil {
		t.Fatal(err)
	}
	level := trunk.CallPermissionLevel{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Staff", AllowedCategories: []string{"mobile"},
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateCallPermissionLevel(ctx, level, audit); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 4 {
		t.Errorf("%d changes kept for a group, office hours, a number's rule and the line order, want 4", n)
	}

	// Saving a number's label or a line's other settings isn't a routing
	// change.
	did.Label = "Main"
	did.Version = 2
	did.RingGroupID = &sales.ID
	if _, err := s.UpdateDID(ctx, did, audit); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 4 {
		t.Errorf("a label change was kept as a routing change (%d)", n)
	}

	// Then: Sales one after another, 601, Bilal only; a new group
	// Support the number rings; mobiles and abroad; a holiday; and Aisha
	// leaves.
	sales.Strategy, sales.Number, sales.Members, sales.Version = routing.StrategyInTurn, "601", []routing.Member{{ExtensionID: bilal.ID}}, 1
	if err := s.UpdateRingGroup(ctx, sales, audit); err != nil {
		t.Fatal(err)
	}
	salesChange := latest()
	support := routing.RingGroup{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Support", Number: "700", Strategy: routing.StrategyAll,
		RingSeconds: 25, TurnSeconds: 15, Members: []routing.Member{{ExtensionID: bilal.ID}},
		NoAnswer: routing.Destination{Kind: routing.KindVoicemail, RingGroupID: nil}, Version: 1, CreatedAt: now, UpdatedAt: now}
	support.NoAnswer.RingGroupID = &support.ID
	if err := s.CreateRingGroup(ctx, support, audit); err != nil {
		t.Fatal(err)
	}
	in.Version = 3
	in.Rings = &routing.Destination{Kind: routing.KindRingGroup, RingGroupID: &support.ID}
	if err := s.SetIncoming(ctx, in, audit); err != nil {
		t.Fatal(err)
	}
	level.AllowedCategories, level.Version = []string{"mobile", "international"}, 1
	if _, err := s.UpdateCallPermissionLevel(ctx, level, audit); err != nil {
		t.Fatal(err)
	}
	office.Holidays, office.Version = []routing.Holiday{{Name: "National Day", FirstDay: "2026-12-02", LastDay: "2026-12-03"}}, 1
	if err := s.UpdateOfficeHours(ctx, office, audit); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExtension(ctx, tenant, aisha.ID, now, audit); err != nil {
		t.Fatal(err)
	}

	newest := func() uuid.UUID {
		t.Helper()
		changes, err := s.RoutingChanges(ctx, tenant)
		if err != nil || len(changes) == 0 {
			t.Fatalf("routing changes: %v", err)
		}
		return changes[0].ID
	}

	// A preview keeps nothing, and says which change was the newest.
	seen, err := s.WithRoutingBefore(ctx, tenant, salesChange, func(st routing.StateStore) error {
		groups, err := st.RingGroups(ctx, tenant)
		if err != nil {
			return err
		}
		if len(groups) != 1 || groups[0].Number != "600" {
			t.Errorf("in the preview: %+v", groups)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != newest() {
		t.Errorf("the preview's newest change %v, want %v", seen, newest())
	}
	if g := group(support.ID); g == nil || group(sales.ID).Number != "601" {
		t.Fatal("the preview was kept")
	}

	// "Undo" of the Sales change refuses: routing changed since.
	if _, err := s.PutRoutingBack(ctx, tenant, salesChange, salesChange, audit); !errors.Is(err, routing.ErrVersionChanged) {
		t.Errorf("undo of an older change: %v", err)
	}
	// So does a put-back checked against routing that changed since.
	if _, err := s.PutRoutingBack(ctx, tenant, salesChange, salesChange, audit); !errors.Is(err, routing.ErrVersionChanged) {
		t.Errorf("a put-back checked before a later change: %v", err)
	}

	// Putting back the version before the Sales change.
	putBack, err := s.PutRoutingBack(ctx, tenant, salesChange, seen, audit)
	if err != nil {
		t.Fatal(err)
	}
	g := group(sales.ID)
	if g.Strategy != routing.StrategyAll || g.Number != "600" || !slices.Equal(members(g), []uuid.UUID{bilal.ID}) {
		t.Errorf("Sales put back: %+v (Aisha left, so Bilal alone)", g)
	}
	if g.NoAnswer.Kind != routing.KindMessage || g.NoAnswer.Message != routing.MessageNotAvailable {
		t.Errorf("Sales's unanswered calls went to Aisha, who left: %+v", g.NoAnswer)
	}
	if group(support.ID) != nil {
		t.Error("Support, added since, is still there")
	}
	list, err := s.IncomingList(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Rings == nil || list[0].Rings.RingGroupID == nil || *list[0].Rings.RingGroupID != sales.ID ||
		list[0].Rule == nil || list[0].Rule.ScheduleID == nil {
		t.Errorf("the number put back: %+v", list[0])
	}
	if list[0].Version <= 3 {
		t.Errorf("the number's version %d wasn't bumped", list[0].Version)
	}
	out, err := s.OutgoingRouting(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Lines, []string{"Provider"}) || len(out.Levels) != 1 || !slices.Equal(out.Levels[0].Categories, []string{"mobile"}) {
		t.Errorf("outgoing put back: %+v", out)
	}
	hours, err := s.OfficeHours(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 1 || len(hours[0].Holidays) != 0 || routing.HoursWords(hours[0].Spans) != "Mon–Fri 08:00–17:00" {
		t.Errorf("office hours put back: %+v", hours)
	}
	changes, err := s.RoutingChanges(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if changes[0].ID != putBack || changes[0].PutBackOf == nil || *changes[0].PutBackOf != salesChange || changes[0].PutBackAt == nil {
		t.Errorf("the put-back's own line: %+v", changes[0])
	}

	// Undoing the put-back gives the later routing again; Support comes
	// back with an empty box.
	if _, err := s.PutRoutingBack(ctx, tenant, putBack, putBack, audit); err != nil {
		t.Fatal(err)
	}
	g = group(sales.ID)
	if g.Strategy != routing.StrategyInTurn || g.Number != "601" {
		t.Errorf("Sales after undoing the put-back: %+v", g)
	}
	if sp := group(support.ID); sp == nil || sp.Number != "700" || sp.NoAnswer.Kind != routing.KindVoicemail {
		t.Errorf("Support after undoing the put-back: %+v", sp)
	}
	list, err = s.IncomingList(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if *list[0].Rings.RingGroupID != support.ID {
		t.Errorf("the number after undoing the put-back rings %v", list[0].Rings)
	}

	// A version that no longer fits is refused whole: 600 is now
	// someone's extension.
	newExtension("600", "Omar")
	before := group(sales.ID)
	var pb *routing.PutBackError
	if _, err := s.PutRoutingBack(ctx, tenant, salesChange, newest(), audit); !errors.As(err, &pb) {
		t.Errorf("putting back 600 over an extension: %v", err)
	}
	if g := group(sales.ID); g.Number != before.Number || g.Strategy != before.Strategy {
		t.Errorf("a refused put-back changed Sales: %+v", g)
	}

	// Only the last 50 are kept.
	for i := range 52 {
		level.Version = 0
		cur, err := s.CallPermissionLevel(ctx, tenant, level.ID)
		if err != nil {
			t.Fatal(err)
		}
		cur.WithholdCallerID = i%2 == 0
		if _, err := s.UpdateCallPermissionLevel(ctx, cur, audit); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(); n != KeepRoutingChanges {
		t.Errorf("%d changes kept, want %d", n, KeepRoutingChanges)
	}
	if _, err := s.PutRoutingBack(ctx, tenant, salesChange, newest(), audit); !errors.Is(err, routing.ErrNotFound) {
		t.Errorf("putting back a pruned change: %v", err)
	}

}

// TestRoutingChangesServiceDocker runs System → Routing changes through
// internal/routing on real Postgres: the list in words, "Undo" and "Redo",
// "confirm it's you" only when calls abroad or premium numbers come back
// on, and trunks:write for anything outgoing. It needs Docker: make
// test-docker.
func TestRoutingChangesServiceDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-routing-changes-service-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	admin := auth.Principal{Type: auth.TypeUser, ID: uuid.NewString(), TenantID: tenant, Role: auth.RoleAdmin, Scopes: auth.Scopes}
	long := now.Add(-time.Hour)
	unconfirmed := auth.WithSession(auth.WithPrincipal(ctx, admin), auth.UserSession{ConfirmedAt: &long})
	confirmed := auth.WithSession(auth.WithPrincipal(ctx, admin), auth.UserSession{ConfirmedAt: &now})
	svc := &routing.Service{Store: s, Rules: s, Changes: s, Now: time.Now}
	s.SetRoutingWords(svc.Words)

	aisha := pbx.Extension{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Number: "101", DisplayName: "Aisha", Enabled: true,
		Version: 1, CreatedAt: now, UpdatedAt: now}
	audit := auth.AuditEntry{TenantID: &tenant, Actor: admin.Actor(), Action: "test", Result: auth.ResultOK}
	if err := s.CreateExtension(ctx, aisha, audit); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateRingGroup(unconfirmed, routing.Input{Name: "Sales", MemberIDs: []uuid.UUID{aisha.ID}}); err != nil {
		t.Fatal(err)
	}
	level := trunk.CallPermissionLevel{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Staff", AllowedCategories: []string{"mobile"},
		Version: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateCallPermissionLevel(ctx, level, audit); err != nil {
		t.Fatal(err)
	}
	level.AllowedCategories = []string{"mobile", "international"}
	if _, err := s.UpdateCallPermissionLevel(ctx, level, audit); err != nil {
		t.Fatal(err)
	}

	list, err := svc.ListChanges(unconfirmed)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Summary != "Changed Staff" || list[1].Summary != "Added the ring group Sales" {
		t.Fatalf("changes: %+v", list)
	}
	if c := list[0].Changes; len(c) != 1 || c[0].Before != "Can call mobiles." || c[0].After != "Can call mobiles and abroad." {
		t.Errorf("Staff's change: %+v", c)
	}
	if c := list[1].Changes; len(c) != 1 || c[0].Before != "" ||
		c[0].After != "Calls ring Aisha. If nobody answers in 25 seconds, callers can leave a voicemail for Sales." {
		t.Errorf("Sales's change: %+v", c)
	}
	levelChange, salesChange := list[0].ID, list[1].ID

	pv, err := svc.PreviewPutBack(unconfirmed, levelChange)
	if err != nil {
		t.Fatal(err)
	}
	if pv.NeedConfirm || !pv.Outgoing || len(pv.Changes) != 1 || pv.Changes[0].After != "Can call mobiles." {
		t.Errorf("preview of taking abroad off: %+v", pv)
	}
	// Undo of an older change refuses.
	var apiErr *apihttp.Error
	if _, err := svc.PutBack(unconfirmed, salesChange, true); !errors.As(err, &apiErr) || apiErr.Code != "routing_changed" {
		t.Errorf("undo of an older change: %v", err)
	}
	// Without trunks:write, outgoing calls can't be put back.
	narrow := admin
	narrow.Scopes = []string{"routing:read", "routing:write"}
	if _, err := svc.PutBack(auth.WithPrincipal(ctx, narrow), levelChange, true); !errors.As(err, &apiErr) || apiErr.Code != "insufficient_scope" {
		t.Errorf("put back without trunks:write: %v", err)
	}
	// Undo takes abroad off: no confirmation needed.
	undo, err := svc.PutBack(unconfirmed, levelChange, true)
	if err != nil {
		t.Fatal(err)
	}
	// Redo turns it back on: confirm it's you.
	if _, err := svc.PutBack(unconfirmed, undo, true); !errors.As(err, &apiErr) || apiErr.Code != "confirm_required" {
		t.Errorf("redo turning abroad on, unconfirmed: %v", err)
	}
	if _, err := svc.PutBack(confirmed, undo, true); err != nil {
		t.Fatal(err)
	}
	out, err := s.OutgoingRouting(ctx, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Levels[0].Categories, []string{"mobile", "international"}) {
		t.Errorf("after redo: %+v", out.Levels)
	}
	list, err = svc.ListChanges(unconfirmed)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 4 || list[0].Action != "routing.undo" || list[0].PutBackOf == nil || *list[0].PutBackOf != undo ||
		list[0].Summary != "Changed Staff" || list[0].ActorName != "" {
		t.Errorf("after redo: %+v", list[0])
	}
}
