package callquality

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestParseQoS(t *testing.T) {
	s, ok := ParseQoS("ssrc=1;themssrc=2;lp=10;rxjitter=0.004500;rxcount=490;txjitter=0.001;txcount=500;rlp=0;rtt=0.045000")
	if !ok {
		t.Fatal("not read")
	}
	if s.InPackets != 490 || s.OutPackets != 500 || s.InLost < 1.99 || s.InLost > 2.01 || s.OutLost != 0 ||
		s.JitterMs < 4.49 || s.JitterMs > 4.51 || s.RTTMs < 44.9 || s.RTTMs > 45.1 {
		t.Errorf("read %+v", s)
	}
	if _, ok := ParseQoS("ssrc=1;lp=0"); ok {
		t.Error("read a value with no packet counts")
	}
}

func TestJudge(t *testing.T) {
	for _, c := range []struct {
		name    string
		sound   Sound
		verdict string
		says    string
	}{
		{"a fine call", Sound{InPackets: 3000, OutPackets: 3000, JitterMs: 5, RTTMs: 40}, Good, ""},
		{"a ship's satellite: slow but whole", Sound{InPackets: 3000, OutPackets: 3000, JitterMs: 10, RTTMs: 800},
			Problems, "long delay"},
		{"some loss", Sound{InPackets: 3000, OutPackets: 3000, InLost: 3}, Problems, "from this phone lost 3%"},
		{"heavy loss", Sound{InPackets: 3000, OutPackets: 3000, OutLost: 12}, Bad, "to this phone lost 12%"},
		{"bursts", Sound{InPackets: 3000, OutPackets: 3000, JitterMs: 150}, Bad, "uneven bursts"},
		{"a silent phone", Sound{InPackets: 0, OutPackets: 2000}, Bad, "no sound reached Linx"},
		{"answered and hung up at once", Sound{InPackets: 3, OutPackets: 4}, Good, ""},
	} {
		verdict, reasons := Judge(c.sound)
		if verdict != c.verdict || !strings.Contains(strings.Join(reasons, "; "), c.says) {
			t.Errorf("%s: %s %q", c.name, verdict, reasons)
		}
	}
}

type memory struct {
	saved  []Record
	before time.Time
}

func (m *memory) DeviceForQuality(_ context.Context, sip string) (uuid.UUID, uuid.UUID, string, error) {
	if sip != "d_abc" {
		return uuid.Nil, uuid.Nil, "", io.EOF
	}
	return uuid.MustParse("00000000-0000-0000-0000-000000000001"), uuid.MustParse("00000000-0000-0000-0000-000000000002"),
		"M’s iPhone", nil
}

func (m *memory) SaveCallQuality(_ context.Context, r Record, before time.Time) error {
	m.saved = append(m.saved, r)
	m.before = before
	return nil
}

func (m *memory) BadCallsSince(_ context.Context, _ uuid.UUID, since time.Time) (n int, _ error) {
	for _, r := range m.saved {
		if r.Verdict == Bad && !r.EndedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

type alerts struct{ fired []string }

func (a *alerts) Fire(_ context.Context, _ uuid.UUID, key, _, title, _, _ string) error {
	a.fired = append(a.fired, key+" "+title)
	return nil
}

func TestService(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	m, a := &memory{}, &alerts{}
	s := Service{Store: m, Alerts: a, Now: func() time.Time { return now }, Log: slog.New(slog.DiscardHandler)}
	s.Leg(context.Background(), "PJSIP/trunk-00000001", "x", now, "rxcount=10;txcount=10")
	if len(m.saved) != 0 {
		t.Fatal("a leg that isn't a phone was kept")
	}
	s.Leg(context.Background(), "PJSIP/d_abc-00000004", "call-1", now.Add(-time.Minute),
		"lp=0;rxjitter=0.002;rxcount=3000;txcount=3000;rlp=0;rtt=0.04")
	if len(m.saved) != 1 || m.saved[0].Verdict != Good || m.saved[0].CallID != "call-1" ||
		!m.before.Equal(now.Add(-KeptFor)) {
		t.Fatalf("kept %+v", m.saved)
	}
	for range AlertAfter - 1 {
		s.WakeMissed(context.Background(), []string{"d_abc"}, 10*time.Second)
	}
	if len(a.fired) != 0 {
		t.Fatalf("alerted too soon: %q", a.fired)
	}
	s.WakeMissed(context.Background(), []string{"d_abc"}, 10*time.Second)
	if len(a.fired) != 1 || !strings.Contains(a.fired[0], "M’s iPhone") {
		t.Errorf("alerts %q", a.fired)
	}
	last := m.saved[len(m.saved)-1]
	if last.Kind != KindWake || last.Verdict != Bad || !strings.Contains(last.Reasons[0], "after 10 s") {
		t.Errorf("wake kept as %+v", last)
	}
}
