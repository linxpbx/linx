package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/db"
	"linxpbx.com/linx/internal/db/dbtest"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunk"
)

// TestTrunksDocker checks the trunk, DID, WireGuard profile and call
// permission level queries (migration 0017) against real Postgres,
// including the delete-cascade, the RESTRICT-on-delete-in-use checks and
// numbering_route's new permission-level gate. It needs Docker: make
// test-docker.
func TestTrunksDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool := dbtest.Start(t, ctx, "linx-trunks-store-test")
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	s := New(pool)
	tenant, err := s.DefaultTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := numbering.Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncNumbering(ctx, data, time.Now().UTC()); err != nil {
		t.Fatalf("SyncNumbering: %v", err)
	}
	audit := func(action string) auth.AuditEntry {
		return auth.AuditEntry{TenantID: &tenant, Actor: "system", Action: action, Result: auth.ResultOK}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)

	newTrunk := func(name string) trunk.Trunk {
		id := uuid.Must(uuid.NewV7())
		tr := trunk.Trunk{ID: id, TenantID: tenant, Name: name, Kind: trunk.KindRegistration, Host: "sip.example.com",
			Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic,
			DialFormat: trunk.DialE164, Codecs: []string{"alaw", "ulaw"}, MaxCalls: 4, Enabled: true, Version: 1,
			CreatedAt: now, UpdatedAt: now}
		if err := s.CreateTrunk(ctx, tr, audit("trunk.create")); err != nil {
			t.Fatalf("CreateTrunk: %v", err)
		}
		return tr
	}
	newExtension := func(number string) pbx.Extension {
		id := uuid.Must(uuid.NewV7())
		e := pbx.Extension{ID: id, TenantID: tenant, Number: number, DisplayName: "Test", Enabled: true, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateExtension(ctx, e, audit("extension.create")); err != nil {
			t.Fatalf("CreateExtension: %v", err)
		}
		return e
	}
	eventCount := func(t *testing.T, eventType string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM event_outbox WHERE tenant_id = $1 AND type = $2", tenant, eventType).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("trunk status: changes fire trunk.status_changed, keep the version", func(t *testing.T) {
		tr := newTrunk("Status line")
		before := eventCount(t, "trunk.status_changed")
		changed, err := s.SetTrunkStatus(ctx, tenant, tr.ID, "registered", "Linx is signed in to it.", now)
		if err != nil || !changed {
			t.Fatalf("SetTrunkStatus: %v %v", changed, err)
		}
		// Same status, new words: no event.
		if changed, err := s.SetTrunkStatus(ctx, tenant, tr.ID, "registered", "Still signed in.", now); err != nil || changed {
			t.Fatalf("same status: %v %v", changed, err)
		}
		got, err := s.Trunk(ctx, tenant, tr.ID)
		if err != nil || got.Status != "registered" || got.StatusDetail != "Still signed in." || got.StatusSince == nil ||
			!got.StatusSince.Equal(now) || got.Version != 1 {
			t.Fatalf("after: %+v %v", got, err)
		}
		if n := eventCount(t, "trunk.status_changed"); n != before+1 {
			t.Errorf("status events %d, want %d", n, before+1)
		}
		if _, err := s.SetTrunkStatus(ctx, tenant, tr.ID, "bogus", "", now); err == nil {
			t.Error("an unknown status was stored")
		}
		if changed, err := s.SetTrunkStatus(ctx, tenant, uuid.New(), "registered", "", now); err != nil || changed {
			t.Errorf("unknown trunk: %v %v", changed, err)
		}
		all, err := s.AllTrunks(ctx)
		if err != nil || len(all) == 0 {
			t.Fatalf("AllTrunks: %d %v", len(all), err)
		}
		if tt, err := s.TrunkTenant(ctx, tr.ID); err != nil || tt != tenant {
			t.Errorf("TrunkTenant = %v %v", tt, err)
		}
	})

	t.Run("outside calls and first calls to a country", func(t *testing.T) {
		tr := newTrunk("Calls line")
		call := func(dialled string, talk int, at time.Time) pbx.OutsideCall {
			r := numbering.Classify("AE", dialled)
			return pbx.OutsideCall{ID: uuid.Must(uuid.NewV7()), Tenant: tenant, TrunkID: &tr.ID, Extension: "101", Dialled: dialled,
				Number: r.E164, Result: r, StartedAt: at, EndedAt: at.Add(time.Duration(talk) * time.Second), TalkSeconds: talk, WentOut: true}
		}
		for _, c := range []pbx.OutsideCall{
			call("00442079460000", 120, now.Add(-10*time.Minute)),
			call("+33142685300", 60, now.Add(-20*time.Minute)),
			call("0501234567", 600, now.Add(-5*time.Minute)),   // at home: not counted
			call("00442079460000", 999, now.Add(-3*time.Hour)), // too long ago
		} {
			if err := s.RecordOutsideCall(ctx, c); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordOutsideCall(ctx, c); err != nil { // twice: ignored
				t.Fatal(err)
			}
		}
		// A call on a line that's since been deleted keeps no trunk.
		gone := call("+12025550123", 0, now)
		ghost := uuid.New()
		gone.TrunkID = &ghost
		if err := s.RecordOutsideCall(ctx, gone); err != nil {
			t.Fatalf("call on a deleted line: %v", err)
		}
		calls, talk, err := s.CallsAbroadSince(ctx, tenant, "AE", now.Add(-time.Hour))
		if err != nil || calls != 3 || talk != 180 {
			t.Fatalf("CallsAbroadSince = %d, %d, %v; want 3, 180", calls, talk, err)
		}
		if first, err := s.FirstCallToRegion(ctx, tenant, "GB", "101", now); err != nil || !first {
			t.Fatalf("first GB: %v %v", first, err)
		}
		if first, err := s.FirstCallToRegion(ctx, tenant, "GB", "102", now); err != nil || first {
			t.Fatalf("second GB: %v %v", first, err)
		}
		if err := s.CleanupOutsideCalls(ctx, now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		var left int
		_ = pool.QueryRow(ctx, "SELECT count(*) FROM outside_call").Scan(&left)
		if left != 4 {
			t.Errorf("%d calls left after cleanup, want 4", left)
		}
		if m, c, err := s.InternationalAlertLimits(ctx); err != nil || m != 30 || c != 10 {
			t.Fatalf("limits = %d %d %v", m, c, err)
		}
		if err := s.SetInternationalAlertLimits(ctx, 60, 5, audit("routing.international_alert")); err != nil {
			t.Fatal(err)
		}
		if m, c, _ := s.InternationalAlertLimits(ctx); m != 60 || c != 5 {
			t.Errorf("limits now %d %d", m, c)
		}
	})

	t.Run("create, get, list", func(t *testing.T) {
		created := eventCount(t, "trunk.created")
		tr := newTrunk("Line A")
		got, err := s.Trunk(ctx, tenant, tr.ID)
		if err != nil || got.Name != "Line A" || got.Version != 1 {
			t.Fatalf("Trunk = %+v, %v", got, err)
		}
		if eventCount(t, "trunk.created") != created+1 {
			t.Error("trunk.created event wasn't fired")
		}
		list, err := s.ListTrunks(ctx, tenant, nil, 10)
		if err != nil || len(list) == 0 {
			t.Fatalf("ListTrunks = %v, %v", list, err)
		}
		if err := s.CreateTrunk(ctx, trunk.Trunk{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Line A",
			Kind: trunk.KindRegistration, Host: "x", Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP,
			CertTrust: trunk.CertPublic, DialFormat: trunk.DialE164, Codecs: []string{"alaw"}, MaxCalls: 4, Version: 1,
			CreatedAt: now, UpdatedAt: now}, audit("trunk.create")); !errors.Is(err, trunk.ErrDuplicate) {
			t.Errorf("duplicate name: err = %v, want ErrDuplicate", err)
		}
	})

	t.Run("update with optimistic concurrency", func(t *testing.T) {
		tr := newTrunk("Line B")
		tr.MaxCalls = 8
		updated, err := s.UpdateTrunk(ctx, tr, audit("trunk.update"))
		if err != nil || updated.MaxCalls != 8 || updated.Version != 2 {
			t.Fatalf("UpdateTrunk = %+v, %v", updated, err)
		}
		if eventCount(t, "trunk.updated") == 0 {
			t.Error("trunk.updated event wasn't fired")
		}
		if _, err := s.UpdateTrunk(ctx, tr, audit("trunk.update")); !errors.Is(err, trunk.ErrVersionChanged) {
			t.Errorf("stale update: err = %v, want ErrVersionChanged", err)
		}
	})

	t.Run("delete cascades to its DIDs", func(t *testing.T) {
		tr := newTrunk("Line C")
		did := trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: tr.ID, Number: "97150" + "0000001",
			Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateDID(ctx, did, audit("trunk_did.create")); err != nil {
			t.Fatalf("CreateDID: %v", err)
		}
		if err := s.DeleteTrunk(ctx, tenant, tr.ID, now, audit("trunk.delete")); err != nil {
			t.Fatalf("DeleteTrunk: %v", err)
		}
		if _, err := s.DID(ctx, tenant, did.ID); !errors.Is(err, trunk.ErrNotFound) {
			t.Errorf("DID after trunk delete: err = %v, want ErrNotFound", err)
		}
		if eventCount(t, "trunk.deleted") == 0 {
			t.Error("trunk.deleted event wasn't fired")
		}
	})

	t.Run("creating a trunk on an unknown WireGuard profile fails", func(t *testing.T) {
		bogus := uuid.Must(uuid.NewV7())
		tr := trunk.Trunk{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Line D", Kind: trunk.KindRegistration,
			Host: "x", Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic,
			DialFormat: trunk.DialE164, Codecs: []string{"alaw"}, MaxCalls: 4, WireGuardProfileID: &bogus, Version: 1,
			CreatedAt: now, UpdatedAt: now}
		if err := s.CreateTrunk(ctx, tr, audit("trunk.create")); !errors.Is(err, trunk.ErrNotFound) {
			t.Errorf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("DID number must be unique across trunks", func(t *testing.T) {
		a, b := newTrunk("Line E"), newTrunk("Line F")
		number := "971501111111"
		if err := s.CreateDID(ctx, trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: a.ID, Number: number,
			Version: 1, CreatedAt: now, UpdatedAt: now}, audit("trunk_did.create")); err != nil {
			t.Fatalf("first CreateDID: %v", err)
		}
		if err := s.CreateDID(ctx, trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: b.ID, Number: number,
			Version: 1, CreatedAt: now, UpdatedAt: now}, audit("trunk_did.create")); !errors.Is(err, trunk.ErrDuplicate) {
			t.Errorf("duplicate DID: err = %v, want ErrDuplicate", err)
		}
	})

	t.Run("DID on an unknown extension fails, then routes once set", func(t *testing.T) {
		tr := newTrunk("Line G")
		bogus := uuid.Must(uuid.NewV7())
		did := trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: tr.ID, Number: "971502222222",
			ExtensionID: &bogus, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateDID(ctx, did, audit("trunk_did.create")); !errors.Is(err, trunk.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		did.ExtensionID = nil
		if err := s.CreateDID(ctx, did, audit("trunk_did.create")); err != nil {
			t.Fatalf("CreateDID: %v", err)
		}
		ext := newExtension("201")
		did.ExtensionID = &ext.ID
		updated, err := s.UpdateDID(ctx, did, audit("trunk_did.update"))
		if err != nil || updated.ExtensionID == nil || *updated.ExtensionID != ext.ID {
			t.Fatalf("UpdateDID = %+v, %v", updated, err)
		}
	})

	t.Run("SetOutboundOrder orders and clears priorities", func(t *testing.T) {
		a, b := newTrunk("Primary"), newTrunk("Backup")
		out, err := s.SetOutboundOrder(ctx, tenant, []uuid.UUID{a.ID, b.ID}, now, audit("outbound_routing.update"))
		if err != nil {
			t.Fatalf("SetOutboundOrder: %v", err)
		}
		byID := map[uuid.UUID]trunk.Trunk{}
		for _, t := range out {
			byID[t.ID] = t
		}
		if p := byID[a.ID].OutboundPriority; p == nil || *p != 1 {
			t.Errorf("a's priority = %v, want 1", p)
		}
		if p := byID[b.ID].OutboundPriority; p == nil || *p != 2 {
			t.Errorf("b's priority = %v, want 2", p)
		}
		// Re-ordering to just b clears a's priority.
		if _, err := s.SetOutboundOrder(ctx, tenant, []uuid.UUID{b.ID}, now, audit("outbound_routing.update")); err != nil {
			t.Fatalf("SetOutboundOrder: %v", err)
		}
		got, err := s.Trunk(ctx, tenant, a.ID)
		if err != nil || got.OutboundPriority != nil {
			t.Errorf("a's priority after re-order = %v, %v, want nil", got.OutboundPriority, err)
		}
	})

	newWireGuardProfile := func(name string) trunk.WireGuardProfile {
		id := uuid.Must(uuid.NewV7())
		w := trunk.WireGuardProfile{ID: id, TenantID: tenant, Name: name, Address: "10.6.0.2/32",
			PrivateKeyEnc: []byte("sealed-private-key"), PublicKey: "pub", PeerPublicKey: "peer-pub",
			PeerEndpointHost: "vpn.example.com", PeerEndpointPort: 51820, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateWireGuardProfile(ctx, w, audit("wireguard_profile.create")); err != nil {
			t.Fatalf("CreateWireGuardProfile: %v", err)
		}
		return w
	}

	t.Run("WireGuard profile CRUD and delete-in-use", func(t *testing.T) {
		w := newWireGuardProfile("Provider VPN")
		got, err := s.WireGuardProfile(ctx, tenant, w.ID)
		if err != nil || got.Name != "Provider VPN" {
			t.Fatalf("WireGuardProfile = %+v, %v", got, err)
		}
		tr := trunk.Trunk{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, Name: "Through VPN", Kind: trunk.KindRegistration,
			Host: "x", Port: 5061, Transport: trunk.TransportTLS, MediaEncryption: trunk.MediaSRTP, CertTrust: trunk.CertPublic,
			DialFormat: trunk.DialE164, Codecs: []string{"alaw"}, MaxCalls: 4, WireGuardProfileID: &w.ID, Version: 1,
			CreatedAt: now, UpdatedAt: now}
		if err := s.CreateTrunk(ctx, tr, audit("trunk.create")); err != nil {
			t.Fatalf("CreateTrunk: %v", err)
		}
		if err := s.DeleteWireGuardProfile(ctx, tenant, w.ID, audit("wireguard_profile.delete")); !errors.Is(err, trunk.ErrInUse) {
			t.Errorf("delete in-use profile: err = %v, want ErrInUse", err)
		}
		if err := s.DeleteTrunk(ctx, tenant, tr.ID, now, audit("trunk.delete")); err != nil {
			t.Fatalf("DeleteTrunk: %v", err)
		}
		if err := s.DeleteWireGuardProfile(ctx, tenant, w.ID, audit("wireguard_profile.delete")); err != nil {
			t.Errorf("delete unused profile: %v", err)
		}
	})

	newLevel := func(name string, categories []string) trunk.CallPermissionLevel {
		id := uuid.Must(uuid.NewV7())
		l := trunk.CallPermissionLevel{ID: id, TenantID: tenant, Name: name, AllowedCategories: categories, Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.CreateCallPermissionLevel(ctx, l, audit("call_permission_level.create")); err != nil {
			t.Fatalf("CreateCallPermissionLevel: %v", err)
		}
		return l
	}

	t.Run("call permission level CRUD and delete-in-use", func(t *testing.T) {
		staff := newLevel("Staff", []string{"mobile", "national", "landline", "toll_free"})
		got, err := s.CallPermissionLevel(ctx, tenant, staff.ID)
		if err != nil || len(got.AllowedCategories) != 4 {
			t.Fatalf("CallPermissionLevel = %+v, %v", got, err)
		}
		ext := newExtension("301")
		ext.CallPermissionLevelID = &staff.ID
		if _, err := s.UpdateExtension(ctx, ext, audit("extension.update")); err != nil {
			t.Fatalf("assigning level to extension: %v", err)
		}
		if err := s.DeleteCallPermissionLevel(ctx, tenant, staff.ID, audit("call_permission_level.delete")); !errors.Is(err, trunk.ErrInUse) {
			t.Errorf("delete in-use level: err = %v, want ErrInUse", err)
		}
	})

	t.Run("numbering_route gates on the caller's permission level", func(t *testing.T) {
		// No trunk used for outgoing calls yet.
		if _, err := s.SetOutboundOrder(ctx, tenant, nil, now, audit("outbound_routing.update")); err != nil {
			t.Fatal(err)
		}
		noLevel := newExtension("401")
		limited := newExtension("402")
		limited.CallPermissionLevelID = ptr(newLevel("Local only", []string{"landline"}).ID)
		if _, err := s.UpdateExtension(ctx, limited, audit("extension.update")); err != nil {
			t.Fatalf("assigning level: %v", err)
		}
		manager := newExtension("403")
		manager.CallPermissionLevelID = ptr(newLevel("Manager", []string{"landline", "mobile", "national", "toll_free", "international"}).ID)
		manager, err = s.UpdateExtension(ctx, manager, audit("extension.update"))
		if err != nil {
			t.Fatalf("assigning level: %v", err)
		}

		// A mobile number: 'AE' numbering already sets country in migration 0016's default.
		const mobile = "0501234567"
		if r, err := s.Route(ctx, noLevel.ID, mobile); err != nil || r.Allowed || r.Reason != "not_permitted" {
			t.Errorf("no level: Route = %+v, %v, want not_permitted", r, err)
		}
		if r, err := s.Route(ctx, limited.ID, mobile); err != nil || r.Allowed || r.Reason != "not_permitted" {
			t.Errorf("local-only level dialling mobile: Route = %+v, %v, want not_permitted", r, err)
		}
		if r, err := s.Route(ctx, manager.ID, mobile); err != nil || r.Allowed || r.Reason != "no_lines" {
			t.Errorf("manager level dialling mobile (no trunks yet): Route = %+v, %v, want allowed=false reason=no_lines", r, err)
		}
		if r, err := s.Route(ctx, manager.ID, "999"); err != nil || !r.Allowed || r.Reason != "emergency" {
			t.Errorf("emergency ignores permission levels: Route = %+v, %v", r, err)
		}

		// Lines (migration 0018): every enabled trunk with a priority, in
		// order, each sent the number the way it wants it, showing the
		// caller's own DID on that trunk or else the trunk's main number.
		e164 := newTrunk("Provider E164")
		zero := newTrunk("Provider 00")
		zero.DialFormat, zero.CallerIDNumber = trunk.Dial00, "+97142000100"
		local := newTrunk("UCM local")
		local.DialFormat = trunk.DialLocal
		off := newTrunk("Turned off")
		off.Enabled = false
		for _, tr := range []trunk.Trunk{zero, local, off} {
			if _, err := s.UpdateTrunk(ctx, tr, audit("trunk.update")); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.CreateDID(ctx, trunk.DID{ID: uuid.Must(uuid.NewV7()), TenantID: tenant, TrunkID: e164.ID, Number: "+97142000403",
			ExtensionID: &manager.ID, Version: 1, CreatedAt: now, UpdatedAt: now}, audit("did.create")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetOutboundOrder(ctx, tenant, []uuid.UUID{local.ID, e164.ID, zero.ID, off.ID}, now, audit("outbound_routing.update")); err != nil {
			t.Fatal(err)
		}
		lines := func(r numbering.Route) string {
			var out []string
			for _, l := range r.Lines {
				out = append(out, l.Trunk+" "+l.Number+" "+l.CallerID)
			}
			return strings.Join(out, " | ")
		}
		for _, c := range []struct{ dialled, reason, lines string }{
			{mobile, "allowed", "UCM local 0501234567  | Provider E164 +971501234567 +97142000403 | Provider 00 00971501234567 +97142000100"},
			{"+44 20 7946 0958", "allowed", "UCM local 00442079460958  | Provider E164 +442079460958 +97142000403 | Provider 00 00442079460958 +97142000100"},
			{"999", "emergency", "UCM local 999  | Provider E164 999 +97142000403 | Provider 00 999 +97142000100"},
		} {
			r, err := s.Route(ctx, manager.ID, c.dialled)
			if err != nil || !r.Allowed || r.Reason != c.reason || lines(r) != c.lines {
				t.Errorf("Route(%s) = %v %s [%s], %v; want %s [%s]", c.dialled, r.Allowed, r.Reason, lines(r), err, c.reason, c.lines)
			}
		}

		// What Asterisk gets: the same, packed for the dialplan.
		var got string
		if err := pool.QueryRow(ctx, `SELECT concat_ws(',', reason, category, withhold, lines) FROM asterisk.linx_outbound($1, $2)`,
			"d_nodevice", mobile).Scan(&got); err != nil || got != "unknown_caller,mobile,f," {
			t.Errorf("linx_outbound for no device = %q, %v", got, err)
		}

		// Calls in: a trunk reaches its own DIDs, however the number is
		// written, and nothing else.
		ext := func(endpoint, dialled string) string {
			var n []string
			rows, err := pool.Query(ctx, `SELECT * FROM asterisk.linx_inbound($1, $2)`, endpoint, dialled)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var v string
				rows.Scan(&v)
				n = append(n, v)
			}
			return strings.Join(n, ",")
		}
		mine := "trunk-" + e164.ID.String()
		for dialled, want := range map[string]string{"+97142000403": "403", "97142000403": "403", "042000403": "403",
			"+97142000404": "", "0501234567": ""} {
			if got := ext(mine, dialled); got != want {
				t.Errorf("linx_inbound(%s) = %q, want %q", dialled, got, want)
			}
		}
		if got := ext("trunk-"+zero.ID.String(), "+97142000403"); got != "" {
			t.Errorf("another trunk reached this trunk's DID: %q", got)
		}
		// A DID whose extension is turned off rings nobody, but is still
		// this trunk's (so the caller hears "not in use", not an error).
		manager.Enabled = false
		if _, err := s.UpdateExtension(ctx, manager, audit("extension.update")); err != nil {
			t.Fatal(err)
		}
		var rows int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM asterisk.linx_inbound($1, '+97142000403') x WHERE x = ''`, mine).Scan(&rows); err != nil || rows != 1 {
			t.Errorf("DID of a turned-off extension: %d rows, %v", rows, err)
		}
	})
}

func ptr[T any](v T) *T { return &v }
