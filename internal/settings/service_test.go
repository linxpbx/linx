package settings

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// fakeStore is an in-memory settings.Store for testing Service without a
// database (internal/store's real implementation is tested against
// Postgres in make test-docker).
type fakeStore struct {
	settings      Settings
	otherLength   []string
	outsideRanges []string
	// reserved maps "from-to" to the first reserved number and reason in
	// that range, for RangeReservedNumber to return.
	reserved map[string][2]string
	used     map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		settings: Settings{
			Country: "AE", ExtensionDigits: 3, SiteKind: "", SimpleMode: true, SetupStep: 1,
			ExtensionRanges: []Range{
				{Kind: RangePeople, From: 100, To: 599},
				{Kind: RangeGroups, From: 600, To: 699},
				{Kind: RangeReserved, From: 700, To: 899},
			},
		},
		reserved: map[string][2]string{},
		used:     map[string]bool{},
	}
}

func (f *fakeStore) Settings(context.Context) (Settings, error) { return f.settings, nil }

func (f *fakeStore) UpdateSettings(_ context.Context, s Settings, _ auth.AuditEntry) (Settings, error) {
	s.DefaultCallPermissionLevelID = f.settings.DefaultCallPermissionLevelID
	s.SetupStep, s.SetupCompletedAt = f.settings.SetupStep, f.settings.SetupCompletedAt
	f.settings = s
	return s, nil
}

func (f *fakeStore) SetDefaultCallPermissionLevel(_ context.Context, id uuid.UUID, _ auth.AuditEntry) error {
	f.settings.DefaultCallPermissionLevelID = &id
	return nil
}

func (f *fakeStore) SetSetupStep(_ context.Context, step int, completedAt *time.Time) error {
	f.settings.SetupStep = step
	if f.settings.SetupCompletedAt == nil {
		f.settings.SetupCompletedAt = completedAt
	}
	return nil
}

func (f *fakeStore) NextFreeExtensionNumber(_ context.Context, _ uuid.UUID, _ string, digits, from, to int) (string, error) {
	for n := from; n <= to; n++ {
		s := zeroPad(n, digits)
		if !f.used[s] {
			return s, nil
		}
	}
	return "", nil
}

func (f *fakeStore) RangeReservedNumber(_ context.Context, _ string, _, from, to int) (string, string, error) {
	if r, ok := f.reserved[key(from, to)]; ok {
		return r[0], r[1], nil
	}
	return "", "", nil
}

func (f *fakeStore) ExtensionsWithOtherLength(context.Context, uuid.UUID, int) ([]string, error) {
	return f.otherLength, nil
}

func (f *fakeStore) ExtensionsOutsideRanges(context.Context, uuid.UUID, []Range) ([]string, error) {
	return f.outsideRanges, nil
}

func key(from, to int) string { return zeroPad(from, 9) + "-" + zeroPad(to, 9) }

func zeroPad(n, digits int) string { return fmt.Sprintf("%0*d", digits, n) }

func testCtx(tenant uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{
		Type: auth.TypeSystem, ID: "cli", TenantID: tenant, Role: auth.RoleSystemAdmin, Scopes: auth.Scopes,
	})
}

func TestUpdateSettingsExtensionDigits(t *testing.T) {
	st := newFakeStore()
	svc := &Service{Store: st, Now: time.Now}
	ctx := testCtx(uuid.New())

	st.otherLength = []string{"12"}
	digits := 2
	if _, err := svc.Update(ctx, Patch{ExtensionDigits: &digits}); err == nil {
		t.Fatal("expected a refusal while an extension of another length exists")
	} else if code := errCode(err); code != "extension_digits_in_use" {
		t.Errorf("code = %q, want extension_digits_in_use", code)
	}

	st.otherLength = nil
	// The existing 3-digit ranges no longer fit 2 digits, so shrinking
	// them comes in the same patch (docs/ADMIN.md §4).
	smaller := []Range{{Kind: RangePeople, From: 10, To: 59}, {Kind: RangeGroups, From: 60, To: 69}, {Kind: RangeReserved, From: 70, To: 89}}
	out, err := svc.Update(ctx, Patch{ExtensionDigits: &digits, ExtensionRanges: &smaller})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.ExtensionDigits != 2 {
		t.Errorf("ExtensionDigits = %d, want 2", out.ExtensionDigits)
	}
}

func TestUpdateSettingsRangesOverlap(t *testing.T) {
	st := newFakeStore()
	svc := &Service{Store: st, Now: time.Now}
	ctx := testCtx(uuid.New())

	bad := []Range{{Kind: RangePeople, From: 100, To: 650}, {Kind: RangeGroups, From: 600, To: 699}}
	_, err := svc.Update(ctx, Patch{ExtensionRanges: &bad})
	if err == nil || errCode(err) != "range_invalid" {
		t.Fatalf("err = %v, want range_invalid for overlapping ranges", err)
	}
}

func TestUpdateSettingsRangeOutOfBounds(t *testing.T) {
	st := newFakeStore()
	svc := &Service{Store: st, Now: time.Now}
	ctx := testCtx(uuid.New())

	bad := []Range{{Kind: RangePeople, From: 100, To: 1200}}
	_, err := svc.Update(ctx, Patch{ExtensionRanges: &bad})
	if err == nil || errCode(err) != "range_invalid" {
		t.Fatalf("err = %v, want range_invalid for a range that doesn't fit 3 digits", err)
	}
}

func TestUpdateSettingsRangeReserved(t *testing.T) {
	st := newFakeStore()
	st.reserved[key(700, 999)] = [2]string{"999", "international_prefix"}
	svc := &Service{Store: st, Now: time.Now}
	ctx := testCtx(uuid.New())

	bad := []Range{{Kind: RangePeople, From: 100, To: 599}, {Kind: RangeGroups, From: 600, To: 699}, {Kind: RangeReserved, From: 700, To: 999}}
	_, err := svc.Update(ctx, Patch{ExtensionRanges: &bad})
	if err == nil || errCode(err) != "range_invalid" {
		t.Fatalf("err = %v, want range_invalid for a range containing a reserved number", err)
	}
	if !strings.Contains(err.Error(), "999") {
		t.Errorf("error should name the reserved number: %v", err)
	}
}

func TestUpdateSettingsAdminNetworks(t *testing.T) {
	st := newFakeStore()
	svc := &Service{Store: st, Now: time.Now}
	ctx := testCtx(uuid.New())

	restricted := true
	if _, err := svc.Update(ctx, Patch{AdminNetworkRestricted: &restricted}); err == nil || errCode(err) != "admin_networks_required" {
		t.Fatalf("err = %v, want admin_networks_required with no networks listed", err)
	}
	// At home the phone networks from setup count on their own: nothing
	// more needs listing (owner, Phase 1E demo).
	home := &Service{Store: newFakeStore(), Now: time.Now, PhoneNetworks: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}}
	if out, err := home.Update(ctx, Patch{AdminNetworkRestricted: &restricted}); err != nil || !out.AdminNetworkRestricted {
		t.Fatalf("at home, with only the phone networks: %+v, %v", out, err)
	}

	networks := []string{"10.0.0.0/8"}
	out, err := svc.Update(ctx, Patch{AdminNetworkRestricted: &restricted, AdminNetworks: &networks})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !out.AdminNetworkRestricted || len(out.AdminNetworks) != 1 {
		t.Errorf("got %+v", out)
	}

	bad := []string{"not-an-address"}
	if _, err := svc.Update(ctx, Patch{AdminNetworks: &bad}); err == nil || errCode(err) != "admin_networks_invalid" {
		t.Fatalf("err = %v, want admin_networks_invalid", err)
	}
}

func TestUpdateSettingsAdminNetworksNeedsConfirmation(t *testing.T) {
	st := newFakeStore()
	svc := &Service{Store: st, Now: time.Now}
	tenant := uuid.New()
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{
		Type: auth.TypeUser, ID: uuid.NewString(), TenantID: tenant, Role: auth.RoleAdmin, Scopes: auth.Scopes,
	})
	// A session that has never confirmed.
	ctx = auth.WithSession(ctx, auth.UserSession{})
	restricted := true
	networks := []string{"10.0.0.0/8"}
	_, err := svc.Update(ctx, Patch{AdminNetworkRestricted: &restricted, AdminNetworks: &networks})
	if err == nil {
		t.Fatal("expected confirm_required without a fresh confirmation")
	}
	if code := errCode(err); code != "confirm_required" {
		t.Errorf("code = %q, want confirm_required", code)
	}

	// A country-only change needs no confirmation.
	country := "AE"
	if _, err := svc.Update(ctx, Patch{Country: &country}); err != nil {
		t.Errorf("a plain settings change shouldn't need confirm-it's-you: %v", err)
	}
}

func TestNextNumber(t *testing.T) {
	st := newFakeStore()
	st.used["100"] = true
	st.used["101"] = true
	svc := &Service{Store: st, Now: time.Now}
	ctx := testCtx(uuid.New())

	n, err := svc.NextNumber(ctx)
	if err != nil {
		t.Fatalf("NextNumber: %v", err)
	}
	if n != "102" {
		t.Errorf("NextNumber = %q, want 102", n)
	}
}

func errCode(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestAdminAccessIncludesPhoneNetworks(t *testing.T) {
	st := newFakeStore()
	svc := &Service{Store: st, Now: time.Now, PhoneNetworks: []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24")}}

	restricted, networks, err := svc.AdminAccess(context.Background())
	if err != nil {
		t.Fatalf("AdminAccess: %v", err)
	}
	if restricted {
		t.Error("restricted should be false until admin_network_restricted is on")
	}
	if len(networks) != 0 {
		t.Errorf("networks = %v, want none while not restricted", networks)
	}

	st.settings.AdminNetworkRestricted = true
	st.settings.AdminNetworks = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	restricted, networks, err = svc.AdminAccess(context.Background())
	if err != nil {
		t.Fatalf("AdminAccess: %v", err)
	}
	if !restricted {
		t.Error("expected restricted once admin_network_restricted is on")
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.0/24")}
	if len(networks) != len(want) || networks[0] != want[0] || networks[1] != want[1] {
		t.Errorf("networks = %v, want %v (admin_networks then the phone networks from setup)", networks, want)
	}
}
