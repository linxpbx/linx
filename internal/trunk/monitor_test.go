package trunk

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/trunkstatus"
)

type monitorStore struct {
	trunks  []Trunk
	changes []string
}

func (s *monitorStore) AllTrunks(context.Context) ([]Trunk, error) { return s.trunks, nil }

func (s *monitorStore) SetTrunkStatus(_ context.Context, _, id uuid.UUID, status, detail string, at time.Time) (bool, error) {
	for i := range s.trunks {
		if s.trunks[i].ID == id {
			changed := s.trunks[i].Status != status
			s.trunks[i].Status, s.trunks[i].StatusDetail = status, detail
			if changed {
				s.changes = append(s.changes, s.trunks[i].Name+":"+status)
			}
			return changed, nil
		}
	}
	return false, nil
}

type fakeAlerter struct {
	open     map[string]string // key → severity
	messages map[string]string
	holdBack time.Duration
}

func (a *fakeAlerter) FireAfter(_ context.Context, _ uuid.UUID, key, severity, _, message, _ string, holdBack time.Duration) error {
	a.open[key], a.messages[key], a.holdBack = severity, message, holdBack
	return nil
}

func (a *fakeAlerter) Resolve(_ context.Context, _ uuid.UUID, key string) error {
	delete(a.open, key)
	return nil
}

func TestMonitor(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	one := 1
	primary := Trunk{ID: uuid.New(), Name: "Primary", Kind: KindRegistration, Enabled: true, OutboundPriority: &one, Status: "unknown"}
	peer := Trunk{ID: uuid.New(), Name: "UCM", Kind: KindLANPeer, Enabled: true, Status: "unknown"}
	off := Trunk{ID: uuid.New(), Name: "Off", Kind: KindLANPeer, Enabled: false, Status: "unknown"}
	st := &monitorStore{trunks: []Trunk{primary, peer, off}}
	al := &fakeAlerter{open: map[string]string{}, messages: map[string]string{}}
	m := &Monitor{Store: st, Alerts: al, Dir: dir, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}

	write := func(trunks map[string]trunkstatus.Trunk) {
		t.Helper()
		if err := trunkstatus.Write(dir, trunkstatus.File{WrittenAt: now, Trunks: trunks}); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		if err := m.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	// No report from Asterisk yet: only the disabled trunk changes.
	check()
	if strings.Join(st.changes, ",") != "Off:disabled" {
		t.Fatalf("changes = %v", st.changes)
	}

	write(map[string]trunkstatus.Trunk{
		primary.Endpoint(): {Registration: trunkstatus.RegRegistered, Contact: trunkstatus.ContactAvail},
		peer.Endpoint():    {Contact: trunkstatus.ContactAvail},
	})
	check()
	if got := strings.Join(st.changes, ","); got != "Off:disabled,Primary:registered,UCM:reachable" {
		t.Fatalf("changes = %v", got)
	}
	if len(al.open) != 0 {
		t.Fatalf("alerts open while every line works: %v", al.open)
	}

	// The only outgoing line refuses the login: critical, after 2 minutes.
	now = now.Add(time.Minute)
	write(map[string]trunkstatus.Trunk{
		primary.Endpoint(): {Registration: trunkstatus.RegRejected, Contact: trunkstatus.ContactAvail},
		peer.Endpoint():    {Contact: trunkstatus.ContactUnavail},
	})
	check()
	if al.open[DownAlertKey(primary.ID)] != "critical" || !strings.Contains(al.messages[DownAlertKey(primary.ID)], "emergency") {
		t.Errorf("primary down: alert %q %q, want critical mentioning emergency calls", al.open[DownAlertKey(primary.ID)], al.messages[DownAlertKey(primary.ID)])
	}
	if al.open[DownAlertKey(peer.ID)] != "warning" {
		t.Errorf("peer down: alert %q, want warning", al.open[DownAlertKey(peer.ID)])
	}
	if al.holdBack != DownHoldBack {
		t.Errorf("hold-back = %v, want %v", al.holdBack, DownHoldBack)
	}
	if st.trunks[0].Status != trunkstatus.StatusRejected {
		t.Errorf("primary status = %q, want rejected", st.trunks[0].Status)
	}

	// Asterisk stops reporting: statuses and alerts stay as they were.
	now = now.Add(5 * time.Minute)
	n := len(st.changes)
	check()
	if len(st.changes) != n || len(al.open) != 2 {
		t.Errorf("a stale report changed things: %v, alerts %v", st.changes[n:], al.open)
	}

	// Back up: the alerts close.
	write(map[string]trunkstatus.Trunk{
		primary.Endpoint(): {Registration: trunkstatus.RegRegistered, Contact: trunkstatus.ContactAvail},
		peer.Endpoint():    {Contact: trunkstatus.ContactAvail},
	})
	check()
	if len(al.open) != 0 {
		t.Errorf("alerts still open after the lines came back: %v", al.open)
	}
}
