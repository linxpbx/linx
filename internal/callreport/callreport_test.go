package callreport

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memory struct{ saved []Report }

func (m *memory) SaveCallReport(_ context.Context, r Report, _ time.Time, _ int) error {
	m.saved = append(m.saved, r)
	return nil
}

func (m *memory) CallReports(context.Context, uuid.UUID, uuid.UUID) ([]Report, error) {
	return m.saved, nil
}

func TestSaveChecksWhatThePhoneSends(t *testing.T) {
	m := &memory{}
	s := Service{Store: m, Now: time.Now}
	good := Report{CallID: "abc", Direction: "outgoing", Peer: "042144444", StartedAt: time.Now(), Text: "Linx call details"}
	for name, change := range map[string]func(*Report){
		"no direction": func(r *Report) { r.Direction = "sideways" },
		"no call id":   func(r *Report) { r.CallID = " " },
		"long call id": func(r *Report) { r.CallID = strings.Repeat("x", MaxCallID+1) },
		"long peer":    func(r *Report) { r.Peer = strings.Repeat("1", MaxPeer+1) },
		"empty report": func(r *Report) { r.Text = "" },
		"too big":      func(r *Report) { r.Text = strings.Repeat("x", MaxReport+1) },
		"not text":     func(r *Report) { r.Text = "\xff\xfe" },
	} {
		r := good
		change(&r)
		if err := s.Save(context.Background(), uuid.New(), uuid.New(), r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	tenant, device := uuid.New(), uuid.New()
	if err := s.Save(context.Background(), tenant, device, good); err != nil {
		t.Fatal(err)
	}
	if len(m.saved) != 1 || m.saved[0].TenantID != tenant || m.saved[0].DeviceID != device || m.saved[0].ID == uuid.Nil {
		t.Errorf("saved %+v", m.saved)
	}
}
