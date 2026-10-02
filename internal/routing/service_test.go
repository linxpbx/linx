package routing

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

type fakeStore struct {
	groups []RingGroup
	exts   []ExtensionRef
}

func (f *fakeStore) RingGroups(_ context.Context, tenant uuid.UUID) ([]RingGroup, error) {
	out := []RingGroup{}
	for _, g := range f.groups {
		if g.TenantID == tenant {
			out = append(out, g)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateRingGroup(_ context.Context, g RingGroup, _ auth.AuditEntry) error {
	for _, o := range f.groups {
		if o.Name == g.Name {
			return ErrDuplicateName
		}
	}
	f.groups = append(f.groups, g)
	return nil
}

func (f *fakeStore) UpdateRingGroup(_ context.Context, g RingGroup, _ auth.AuditEntry) error {
	for i, o := range f.groups {
		if o.ID == g.ID {
			if o.Version != g.Version {
				return ErrVersionChanged
			}
			g.Version++
			f.groups[i] = g
			return nil
		}
	}
	return ErrNotFound
}

func (f *fakeStore) DeleteRingGroup(_ context.Context, _, id uuid.UUID, _ auth.AuditEntry) error {
	for _, o := range f.groups {
		if o.ID != id && o.NoAnswer.RingGroupID != nil && *o.NoAnswer.RingGroupID == id {
			return ErrInUse
		}
	}
	f.groups = slices.DeleteFunc(f.groups, func(g RingGroup) bool { return g.ID == id })
	return nil
}

func (f *fakeStore) Extensions(_ context.Context, _ uuid.UUID, ids []uuid.UUID) ([]ExtensionRef, error) {
	var out []ExtensionRef
	for _, e := range f.exts {
		if slices.Contains(ids, e.ID) {
			out = append(out, e)
		}
	}
	return out, nil
}

func testCtx(tenant uuid.UUID) context.Context {
	return auth.WithPrincipal(context.Background(), auth.Principal{
		Type: auth.TypeSystem, ID: "cli", TenantID: tenant, Role: auth.RoleSystemAdmin, Scopes: auth.Scopes,
	})
}

func errCode(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func setup() (*Service, *fakeStore, context.Context, []uuid.UUID) {
	st := &fakeStore{}
	var ids []uuid.UUID
	for i, name := range []string{"Mohammed", "Sara", "Omar"} {
		id := uuid.New()
		ids = append(ids, id)
		st.exts = append(st.exts, ExtensionRef{ID: id, Number: []string{"100", "101", "102"}[i], DisplayName: name})
	}
	return &Service{Store: st, Now: time.Now}, st, testCtx(uuid.New()), ids
}

func TestCreateRingGroupDefaults(t *testing.T) {
	svc, _, ctx, ids := setup()
	v, err := svc.CreateRingGroup(ctx, Input{Name: " Sales ", Number: "600", MemberIDs: ids})
	if err != nil {
		t.Fatalf("CreateRingGroup: %v", err)
	}
	// Unanswered calls: the group's own voicemail box.
	if v.Name != "Sales" || v.Strategy != StrategyAll || v.RingSeconds != 25 || v.NoAnswer.Kind != KindVoicemail ||
		v.NoAnswer.RingGroupID == nil || *v.NoAnswer.RingGroupID != v.ID || len(v.Members) != 3 || len(v.UsedBy) != 0 {
		t.Errorf("group = %+v", v)
	}
	if v.NoAnswerLabel != "Voicemail for Sales" {
		t.Errorf("label = %q", v.NoAnswerLabel)
	}
	want := `Calls ring Mohammed, Sara and Omar together. If nobody answers in 25 seconds, callers can leave a voicemail for Sales.`
	if v.Words != want {
		t.Errorf("words = %q\nwant    %q", v.Words, want)
	}
}

func TestCreateRingGroupRefuses(t *testing.T) {
	svc, _, ctx, ids := setup()
	five := 4
	for _, tc := range []struct {
		in   Input
		code string
	}{
		{Input{Name: "", MemberIDs: ids}, "name_invalid"},
		{Input{Name: "A", Number: "6a", MemberIDs: ids}, "number_invalid"},
		{Input{Name: "A", Strategy: "random", MemberIDs: ids}, "strategy_invalid"},
		{Input{Name: "A", RingSeconds: &five, MemberIDs: ids}, "ring_seconds_invalid"},
		{Input{Name: "A"}, "members_missing"},
		{Input{Name: "A", MemberIDs: []uuid.UUID{ids[0], ids[0]}}, "members_duplicate"},
		{Input{Name: "A", MemberIDs: []uuid.UUID{uuid.New()}}, "extension_not_found"},
		{Input{Name: "A", MemberIDs: ids, NoAnswer: &Destination{Kind: KindMessage, Message: "hello"}}, "destination_invalid"},
		{Input{Name: "A", MemberIDs: ids, NoAnswer: &Destination{Kind: KindRingGroup, RingGroupID: ptr(uuid.New())}}, "ring_group_not_found"},
	} {
		if _, err := svc.CreateRingGroup(ctx, tc.in); errCode(err) != tc.code {
			t.Errorf("%+v: err = %v, want %s", tc.in, err, tc.code)
		}
	}
}

func ptr[T any](v T) *T { return &v }

// TestRingGroupLoopRefused: Sales → Support → Sales would go round in
// circles; the screens grey it out and the API refuses it.
func TestRingGroupLoopRefused(t *testing.T) {
	svc, _, ctx, ids := setup()
	sales, err := svc.CreateRingGroup(ctx, Input{Name: "Sales", MemberIDs: ids[:1]})
	if err != nil {
		t.Fatal(err)
	}
	support, err := svc.CreateRingGroup(ctx, Input{Name: "Support", MemberIDs: ids[1:], Strategy: StrategyInTurn,
		NoAnswer: &Destination{Kind: KindRingGroup, RingGroupID: &sales.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `Calls ring Sara, then Omar, 15 seconds each. If nobody answers, the call goes to Sales.`; support.Words != want {
		t.Errorf("words = %q", support.Words)
	}
	_, err = svc.UpdateRingGroup(ctx, sales.ID, Patch{NoAnswer: &Destination{Kind: KindRingGroup, RingGroupID: &support.ID}}, "")
	if errCode(err) != "routing_loop" {
		t.Errorf("Sales → Support: err = %v, want routing_loop", err)
	}
	_, err = svc.UpdateRingGroup(ctx, sales.ID, Patch{NoAnswer: &Destination{Kind: KindRingGroup, RingGroupID: &sales.ID}}, "")
	if errCode(err) != "routing_loop" {
		t.Errorf("Sales → Sales: err = %v, want routing_loop", err)
	}
	got, err := svc.GetRingGroup(ctx, sales.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.UsedBy) != 1 || got.UsedBy[0].Name != "Support" {
		t.Errorf("Sales used by %+v", got.UsedBy)
	}
	if err := svc.DeleteRingGroup(ctx, sales.ID); errCode(err) != "ring_group_in_use" {
		t.Errorf("delete while used: %v", err)
	}
	_, err = svc.UpdateRingGroup(ctx, support.ID, Patch{NoAnswer: &Destination{Kind: KindExtension, ExtensionID: &ids[0]}}, support.ETagValue())
	if err != nil {
		t.Fatalf("moving Support's unanswered calls: %v", err)
	}
	if err := svc.DeleteRingGroup(ctx, sales.ID); err != nil {
		t.Errorf("delete once unused: %v", err)
	}
}

func TestUpdateRingGroupETag(t *testing.T) {
	svc, _, ctx, ids := setup()
	g, err := svc.CreateRingGroup(ctx, Input{Name: "Sales", MemberIDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateRingGroup(ctx, g.ID, Patch{Name: ptr("Sales team")}, `"7"`); errCode(err) != "etag_mismatch" {
		t.Errorf("stale etag: %v", err)
	}
	got, err := svc.UpdateRingGroup(ctx, g.ID, Patch{MemberIDs: &[]uuid.UUID{ids[2], ids[0]}}, g.ETagValue())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 || got.Members[0].DisplayName != "Omar" {
		t.Errorf("members = %+v", got.Members)
	}
}

func TestWords(t *testing.T) {
	g := RingGroup{Strategy: StrategyAll, RingSeconds: 20, Members: []Member{{DisplayName: "Sara"}}}
	if got := Words(g, "the call goes to Sales."); got != "Calls ring Sara. If nobody answers in 20 seconds, the call goes to Sales." {
		t.Errorf("one person: %q", got)
	}
	for _, n := range []string{"B", "C", "D", "E", "F"} {
		g.Members = append(g.Members, Member{DisplayName: n})
	}
	if got := Words(g, "x."); got != "Calls ring Sara, B, C and 3 more together. If nobody answers in 20 seconds, x." {
		t.Errorf("six people: %q", got)
	}
	g.Members = nil
	if got := Words(g, `callers hear "not available".`); got != `Nobody is in this group, so callers hear "not available".` {
		t.Errorf("nobody: %q", got)
	}
}
