package trunk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

// oneTrunkStore holds a single trunk; other Store methods panic.
type oneTrunkStore struct {
	Store
	t Trunk
}

func (s *oneTrunkStore) Trunk(context.Context, uuid.UUID, uuid.UUID) (Trunk, error) { return s.t, nil }
func (s *oneTrunkStore) UpdateTrunk(_ context.Context, t Trunk, _ auth.AuditEntry) (Trunk, error) {
	t.Version++
	s.t = t
	return t, nil
}

// An ADR-023 confirmation is for the address and the way the trunk was
// unencrypted when the admin read the warning (Phase 1D review).
func TestUpdateTrunkAsksAgainWhenUnencryptedDifferently(t *testing.T) {
	confirmedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	base := Trunk{ID: uuid.New(), Name: "Provider", Kind: KindIPAuthenticated, Host: "sip.example.com", Port: 5061,
		Transport: TransportTLS, MediaEncryption: MediaNone, CertTrust: CertPublic, DialFormat: DialFormats[0],
		Codecs: []string{"alaw"}, MaxCalls: 2, Enabled: true, Version: 1,
		UnencryptedConfirmedBy: "api_key:first", UnencryptedConfirmedAt: &confirmedAt}
	ctx := auth.WithPrincipal(context.Background(), auth.SystemPrincipal(uuid.New()))
	str := func(s string) *string { return &s }
	yes := true

	for _, tc := range []struct {
		name  string
		patch TrunkPatch
		ask   bool
	}{
		{"a new name keeps it", TrunkPatch{Name: str("Renamed")}, false},
		{"another address asks again", TrunkPatch{Host: str("sip.other.example")}, true},
		{"plain UDP asks again", TrunkPatch{Transport: str(TransportUDP), Port: func() *int { p := 5060; return &p }()}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &oneTrunkStore{t: base}
			svc := &Service{Store: st, Now: time.Now}
			_, err := svc.UpdateTrunk(ctx, base.ID, tc.patch, "")
			if tc.ask != errors.Is(err, errUnencryptedConfirmationRequired) {
				t.Fatalf("err = %v, want confirmation asked: %v", err, tc.ask)
			}
			if !tc.ask {
				return
			}
			tc.patch.ConfirmUnencrypted = &yes
			got, err := svc.UpdateTrunk(ctx, base.ID, tc.patch, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.UnencryptedConfirmedBy != "system:cli" || got.UnencryptedConfirmedAt == nil || got.UnencryptedConfirmedAt.Equal(confirmedAt) {
				t.Errorf("confirmation not renewed: %q %v", got.UnencryptedConfirmedBy, got.UnencryptedConfirmedAt)
			}
		})
	}
}
