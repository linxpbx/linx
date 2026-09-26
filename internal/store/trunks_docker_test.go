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

	t.Run("create, get, list", func(t *testing.T) {
		tr := newTrunk("Line A")
		got, err := s.Trunk(ctx, tenant, tr.ID)
		if err != nil || got.Name != "Line A" || got.Version != 1 {
			t.Fatalf("Trunk = %+v, %v", got, err)
		}
		if eventCount(t, "trunk.created") != 1 {
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
		noLevel := newExtension("401")
		limited := newExtension("402")
		limited.CallPermissionLevelID = ptr(newLevel("Local only", []string{"landline"}).ID)
		if _, err := s.UpdateExtension(ctx, limited, audit("extension.update")); err != nil {
			t.Fatalf("assigning level: %v", err)
		}
		manager := newExtension("403")
		manager.CallPermissionLevelID = ptr(newLevel("Manager", []string{"landline", "mobile", "national", "toll_free", "international"}).ID)
		if _, err := s.UpdateExtension(ctx, manager, audit("extension.update")); err != nil {
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
	})
}

func ptr[T any](v T) *T { return &v }
