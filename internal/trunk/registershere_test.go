package trunk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/pbx"
	"linxpbx.com/linx/internal/trunkstatus"
)

// createStore keeps what CreateTrunk saves; other Store methods panic.
type createStore struct {
	oneTrunkStore
}

func (s *createStore) CreateTrunk(_ context.Context, t Trunk, _ auth.AuditEntry) error {
	s.t = t
	return nil
}

func apiCode(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// A phone system that signs in to Linx (docs/SIMPLER.md §1): Linx makes its
// login, keeps only the digest hash, and nothing about connecting to it can
// be set.
func TestRegistersHere(t *testing.T) {
	ctx := auth.WithPrincipal(context.Background(), auth.SystemPrincipal(uuid.New()))
	st := &createStore{}
	svc := &Service{Store: st, Now: time.Now}

	got, err := svc.CreateTrunk(ctx, TrunkInput{Name: "UCM landlines", Kind: KindRegistersHere})
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != got.Endpoint() || got.Host != "" || got.Transport != TransportTLS || got.MediaEncryption != MediaSRTP ||
		got.DialFormat != DialLocal || got.Unencrypted() || len(got.PasswordEnc) != 0 {
		t.Errorf("created %+v", got)
	}
	if len(got.NewPassword) < 26 || got.DigestHash != pbx.DigestHash(got.Endpoint(), got.NewPassword) {
		t.Errorf("login: password %q, hash %q", got.NewPassword, got.DigestHash)
	}
	if st.t.DigestHash != got.DigestHash {
		t.Error("the digest hash wasn't saved")
	}

	for _, in := range []TrunkInput{
		{Name: "x", Kind: KindRegistersHere, Host: "192.168.1.5"},
		{Name: "x", Kind: KindRegistersHere, Username: "mine"},
		{Name: "x", Kind: KindRegistersHere, Password: "secret"},
		{Name: "x", Kind: KindRegistersHere, Transport: TransportUDP},
		{Name: "x", Kind: KindRegistersHere, MediaEncryption: MediaNone, ConfirmUnencrypted: true},
	} {
		if _, err := svc.CreateTrunk(ctx, in); apiCode(err) != "registers_here_fixed" && apiCode(err) != "unencrypted_confirmation_required" {
			t.Errorf("CreateTrunk(%+v) = %v, want it refused", in, err)
		}
	}

	// A new password: same login name, new hash; nothing else can change
	// the login.
	st.t.Version = 1
	svc2 := &Service{Store: &st.oneTrunkStore, Now: time.Now}
	reset, err := svc2.ResetTrunkPassword(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Username != got.Username || reset.NewPassword == got.NewPassword ||
		reset.DigestHash != pbx.DigestHash(got.Endpoint(), reset.NewPassword) {
		t.Errorf("reset %+v", reset)
	}
	pw, host := "x", "sip.example.com"
	for _, patch := range []TrunkPatch{{Password: &pw}, {Host: &host}} {
		if _, err := svc2.UpdateTrunk(ctx, got.ID, patch, ""); apiCode(err) != "registers_here_fixed" {
			t.Errorf("UpdateTrunk(%+v) = %v, want registers_here_fixed", patch, err)
		}
	}

	// Another kind has no login Linx made.
	st.oneTrunkStore.t = Trunk{ID: uuid.New(), Kind: KindRegistration, Host: "sip.example.com"}
	if _, err := svc2.ResetTrunkPassword(ctx, st.oneTrunkStore.t.ID); apiCode(err) != "no_login_here" {
		t.Errorf("reset of a provider line = %v", err)
	}

	// A person's session must have confirmed it's them first.
	sess := auth.UserSession{UserID: uuid.New(), TenantID: uuid.New(), Role: auth.RoleAdmin, MFAVerified: true}
	sctx := auth.WithSession(auth.WithPrincipal(context.Background(), sess.Principal()), sess)
	if _, err := svc.CreateTrunk(sctx, TrunkInput{Name: "y", Kind: KindRegistersHere}); apiCode(err) != "confirm_required" {
		t.Error("created without confirm-it's-you")
	}
}

func TestSignedInResult(t *testing.T) {
	for _, tc := range []struct {
		t  Trunk
		ok bool
	}{
		{Trunk{Enabled: true, Status: trunkstatus.StatusRegistered}, true},
		{Trunk{Enabled: true, Status: trunkstatus.StatusUnknown}, false},
		{Trunk{Enabled: false, Status: trunkstatus.StatusRegistered}, false},
	} {
		res := SignedInResult(tc.t)
		if res.OK != tc.ok || len(res.Steps) != 1 || res.Steps[0].Name != "signed_in" || res.Steps[0].Words == "" {
			t.Errorf("SignedInResult(%+v) = %+v", tc.t, res)
		}
	}
}

// levelStore holds one call permission level.
type levelStore struct {
	Store
	l CallPermissionLevel
}

func (s *levelStore) CallPermissionLevel(context.Context, uuid.UUID, uuid.UUID) (CallPermissionLevel, error) {
	return s.l, nil
}
func (s *levelStore) UpdateCallPermissionLevel(_ context.Context, l CallPermissionLevel, _ auth.AuditEntry) (CallPermissionLevel, error) {
	s.l = l
	return l, nil
}

// Allowing calls abroad or to premium numbers is where fraud costs money:
// a session must have confirmed it's you; turning them off, or anything
// else, needn't.
func TestCostlyCategoriesNeedConfirmation(t *testing.T) {
	sess := auth.UserSession{UserID: uuid.New(), TenantID: uuid.New(), Role: auth.RoleAdmin, MFAVerified: true}
	ctx := auth.WithSession(auth.WithPrincipal(context.Background(), sess.Principal()), sess)
	st := &levelStore{l: CallPermissionLevel{ID: uuid.New(), Name: "Everyone", AllowedCategories: []string{"mobile", "international"}, Version: 1}}
	svc := &Service{Store: st, Now: time.Now}
	for _, tc := range []struct {
		categories []string
		confirm    bool
	}{
		{[]string{"mobile", "international", "landline"}, false}, // already allowed
		{[]string{"mobile"}, false},                              // turning off
		{[]string{"mobile", "premium"}, true},
		{[]string{"mobile", "international"}, true}, // back on after being turned off
	} {
		_, err := svc.UpdateCallPermissionLevel(ctx, st.l.ID, CallPermissionLevelPatch{AllowedCategories: tc.categories}, "")
		if got := apiCode(err) == "confirm_required"; got != tc.confirm {
			t.Errorf("%v: err %v, want confirmation asked: %v", tc.categories, err, tc.confirm)
		}
	}
	if _, err := svc.CreateCallPermissionLevel(ctx, CallPermissionLevelInput{Name: "Abroad", AllowedCategories: []string{"international"}}); apiCode(err) != "confirm_required" {
		t.Errorf("creating a level that calls abroad: %v", err)
	}
}
