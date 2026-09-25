package pbx

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTeamStatuses(t *testing.T) {
	t0 := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	members := []TeamMember{
		{Extension: "101", Name: "sara Haddad", Online: true, Presence: PresenceAvailable},
		{Extension: "102", Name: "Daniel Reyes", Online: true, Presence: PresenceAvailable},
		{Extension: "103", Name: "Aisha Rahman", Online: true, Presence: PresenceAvailable},
		{Extension: "104", Name: "Yusuf Nasser", Online: true, Presence: PresenceAway},
		{Extension: "105", Name: "Chen Wei", Online: false, Presence: PresenceAway},
		{Extension: "106", Name: "Dana Dnd", Online: true, Presence: PresenceDND},
		{Extension: "107", Name: "Reception", Online: true},
		{Extension: "108", Name: "Echo Tester", Online: true, Presence: PresenceDND},
		{Extension: "109", Name: "Ben Callee", Online: true, Presence: PresenceAway},
	}
	calls := []ActiveCall{
		{From: CallParty{Extension: "101"}, To: "109", State: CallAnswered, StartedAt: t0, AnsweredAt: &t1, AnsweredBy: &CallParty{Extension: "109"}},
		{From: CallParty{Extension: "103"}, To: "102", State: CallRinging, StartedAt: t0},
		{From: CallParty{Extension: "108"}, To: EchoTestNumber, State: CallSystem, StartedAt: t0},
		// A second caller ringing someone already on a call leaves them on it.
		{From: CallParty{Extension: "107"}, To: "101", State: CallRinging, StartedAt: t1},
	}
	want := map[string]struct {
		status string
		since  *time.Time
	}{
		"101": {TeamOnCall, &t1},
		"109": {TeamOnCall, &t1},
		"102": {TeamRinging, &t0},
		"103": {TeamOnCall, &t0},
		"104": {TeamAway, nil},
		"105": {TeamOffline, nil},
		"106": {TeamDND, nil},
		"107": {TeamOnCall, &t1},
		"108": {TeamOnCall, &t0},
	}
	got := TeamStatuses(members, calls)
	if len(got) != len(members) {
		t.Fatalf("%d rows, want %d", len(got), len(members))
	}
	var order []string
	for _, s := range got {
		order = append(order, s.Extension)
		w := want[s.Extension]
		if s.Status != w.status {
			t.Errorf("%s: status %s, want %s", s.Extension, s.Status, w.status)
		}
		if (s.Since == nil) != (w.since == nil) || (s.Since != nil && !s.Since.Equal(*w.since)) {
			t.Errorf("%s: since %v, want %v", s.Extension, s.Since, w.since)
		}
	}
	// By name, ignoring case ("sara Haddad" sorts among the S's).
	if got, want := strings.Join(order, ","), "103,109,105,106,102,108,107,101,104"; got != want {
		t.Errorf("order %s, want %s", got, want)
	}
}

type fakeTeamStore struct{ presence string }

func (f *fakeTeamStore) TeamMembers(context.Context, uuid.UUID) ([]TeamMember, error) {
	return nil, nil
}
func (f *fakeTeamStore) SetPresence(_ context.Context, _, _ uuid.UUID, p string) error {
	f.presence = p
	return nil
}

func TestSetPresence(t *testing.T) {
	st := &fakeTeamStore{}
	team := &Team{Store: st}
	if err := team.SetPresence(context.Background(), uuid.New(), uuid.New(), "busy"); !errors.Is(err, ErrPresenceInvalid) {
		t.Errorf("unknown status: %v", err)
	}
	if err := team.SetPresence(context.Background(), uuid.New(), uuid.New(), PresenceDND); err != nil || st.presence != PresenceDND {
		t.Errorf("dnd: %v, stored %q", err, st.presence)
	}
}
