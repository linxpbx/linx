package moved

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var (
	home = Place{ServerID: "a", Domain: "pbx.old.com", LANNetworks: []string{"192.168.1.0/24"}, LANAddress: "192.168.1.212",
		PublicAddress: "198.51.100.4", FrontDoor: "pangolin"}
	rented = Place{ServerID: "b", Domain: "example.com", LANNetworks: []string{}, PublicAddress: "203.0.113.5", FrontDoor: "linx-443"}
)

func TestMoved(t *testing.T) {
	same := home
	same.PublicAddress = "198.51.100.9" // a home's address changes by itself
	if Moved(home, same) {
		t.Error("a new public address on the same server counted as a move")
	}
	if !Moved(home, rented) {
		t.Error("another server didn't count")
	}
	other := home
	other.ServerID = "c"
	if !Moved(home, other) {
		t.Error("another server with the same settings didn't count")
	}
	// Without ids (a backup from before setup made one): the settings decide.
	a, b := home, home
	a.ServerID, b.ServerID = "", ""
	b.PublicAddress = "198.51.100.9"
	if Moved(a, b) {
		t.Error("public address alone counted")
	}
	b.LANNetworks = []string{"10.0.0.0/24"}
	if !Moved(a, b) {
		t.Error("another home network didn't count")
	}
}

func ids(items []Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i], _, _ = strings.Cut(it.ID, ":")
	}
	return out
}

func TestItems(t *testing.T) {
	ucm, telnyx, wg, gxw := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	f := Facts{
		LANPeers: []Named{{ucm, "UCM landlines"}}, ByAddress: []Named{{telnyx, "Telnyx"}}, Tunnels: []Named{{wg, "Office"}},
		SignsIn:    []Named{{gxw, "Branch GXW"}},
		DeskPhones: 3, AppPhones: 2, AdminRestricted: true, AdminNetworks: []string{"192.168.1.0/24"},
		BackedUpSince: func(time.Time) bool { return false },
	}
	m := Move{DetectedAt: time.Now(), Before: home, After: rented}
	got := Items(m, f)
	want := []string{ItemOldServer, ItemLANPeer, ItemProvider, ItemTunnel, ItemDeskPhones, ItemAppPhones, ItemSignsIn,
		ItemAdminNetworks, ItemPasskeys, ItemBackups}
	if !slices.Equal(ids(got), want) {
		t.Fatalf("items %v", ids(got))
	}
	for _, it := range got {
		if it.Title == "" || it.Why == "" || it.Done {
			t.Errorf("item %+v", it)
		}
	}
	if !strings.Contains(got[2].Title, "Telnyx") || !strings.Contains(got[2].Title, "203.0.113.5") ||
		!strings.Contains(got[4].Title, "3 desk phones") || !strings.Contains(got[6].Title, "Branch GXW") ||
		!strings.Contains(got[8].Why, "pbx.old.com") {
		t.Errorf("words: %q / %q / %q / %q", got[2].Title, got[4].Title, got[6].Title, got[8].Why)
	}
	// An iPhone can't be given the new address: it is set up again, and it
	// is only ever the domain that does this to one (docs/PHASE2.md §9).
	if !strings.Contains(got[5].Title, "2 iPhones and iPads") || !strings.Contains(got[5].Why, "set each one up again") {
		t.Errorf("app phones: %q / %q", got[5].Title, got[5].Why)
	}
	sameDomain := rented
	sameDomain.Domain = home.Domain
	if got := ids(Items(Move{Before: home, After: sameDomain}, f)); slices.Contains(got, ItemAppPhones) {
		t.Errorf("app phones asked to be set up again for a home network they never used: %v", got)
	}

	// The same home network and domain on a new server: only what always applies.
	next := home
	next.ServerID = "c"
	if got := ids(Items(Move{Before: home, After: next}, f)); !slices.Equal(got, []string{ItemOldServer, ItemBackups}) {
		t.Errorf("same place, new server: %v", got)
	}

	// Ticks by hand; backups tick themselves, and can't be ticked by hand.
	m.Ticks = map[string]bool{ItemOldServer: true, ItemBackups: true}
	got = Items(m, f)
	if !got[0].Done || got[len(got)-1].Done {
		t.Errorf("ticks %+v", got)
	}
	f.BackedUpSince = func(time.Time) bool { return true }
	if got := Items(m, f); !got[len(got)-1].Done {
		t.Error("a backup since didn't tick itself")
	}
}

type fakeStore struct {
	move  *Move
	facts Facts
}

func (f *fakeStore) RecordPlace(context.Context, Place, time.Time, uuid.UUID) (*Move, error) {
	return nil, nil
}
func (f *fakeStore) CurrentMove(context.Context) (*Move, error) {
	if f.move == nil || f.move.DoneAt != nil {
		return nil, nil
	}
	m := *f.move
	m.Ticks = map[string]bool{}
	for k, v := range f.move.Ticks {
		m.Ticks[k] = v
	}
	return &m, nil
}
func (f *fakeStore) UpdateMove(_ context.Context, m Move) error      { f.move = &m; return nil }
func (f *fakeStore) Facts(context.Context, uuid.UUID) (Facts, error) { return f.facts, nil }

func TestChecklist(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	st := &fakeStore{facts: Facts{BackedUpSince: func(time.Time) bool { return true }}}
	svc := &Service{Store: st, Now: func() time.Time { return now }}
	if c, err := svc.Current(ctx, uuid.Nil); c != nil || err != nil {
		t.Fatalf("no move: %v %v", c, err)
	}
	if _, err := svc.Update(ctx, uuid.Nil, Change{}); err != ErrNoChecklist {
		t.Errorf("update without one: %v", err)
	}
	st.move = &Move{ID: uuid.New(), DetectedAt: now.Add(-time.Hour), Before: home, After: rented}
	c, err := svc.Current(ctx, uuid.Nil)
	if err != nil || c == nil || c.Left() != 2 || !svc.PasskeysMoved(ctx) {
		t.Fatalf("checklist %+v %v", c, err)
	}
	if _, err := svc.Update(ctx, uuid.Nil, Change{Ticks: map[string]bool{ItemBackups: true}}); err == nil {
		t.Error("ticked an item that ticks itself")
	}
	if _, err := svc.Update(ctx, uuid.Nil, Change{Ticks: map[string]bool{"lan_peer:nope": true}}); err == nil {
		t.Error("ticked an item that isn't there")
	}
	hide := true
	c, err = svc.Update(ctx, uuid.Nil, Change{Ticks: map[string]bool{ItemOldServer: true}, Hide: &hide})
	if err != nil || c == nil || c.Left() != 1 || c.HiddenUntil == nil || !c.HiddenUntil.Equal(now.Add(HideFor)) {
		t.Fatalf("after tick: %+v %v", c, err)
	}
	// A week later it opens again by itself.
	svc.Now = func() time.Time { return now.Add(HideFor + time.Minute) }
	if c, _ := svc.Current(ctx, uuid.Nil); c == nil || c.HiddenUntil != nil {
		t.Errorf("still hidden: %+v", c)
	}
	c, err = svc.Update(ctx, uuid.Nil, Change{Ticks: map[string]bool{ItemPasskeys: true}})
	if err != nil || c != nil || st.move.DoneAt == nil || svc.PasskeysMoved(ctx) {
		t.Errorf("all done: %+v %v %+v", c, err, st.move)
	}
}
