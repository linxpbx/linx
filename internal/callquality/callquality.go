// Package callquality judges every call from the phone system's own side
// (owner, 2026-10-16: "I need a mechanism to know if a call went through
// issues even if the user didn't report it").
//
// As each phone's leg of a call hangs up, Asterisk says how its sound went
// (RTPAUDIOQOS: packets lost each way, jitter, round trip); a woken phone
// that never arrived is said by the dialplan (LinxWakeMissed). Each becomes
// one record — Good, Had problems or Bad, with the reasons in plain words —
// that an admin reads beside what the phone itself reported (Calls → Call
// quality), and a phone whose calls keep going wrong raises an admin alert.
//
// Small and short-lived like the phones' own reports (docs/RESOURCES.md):
// kept seven days, saving one clears what is older, nothing runs on a timer.
package callquality

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Verdicts.
const (
	Good     = "good"
	Problems = "problems"
	Bad      = "bad"
)

// Kinds of record.
const (
	KindCall = "call" // a phone's leg of a call, by its sound
	KindWake = "wake" // a phone woken for a call that never arrived
)

// KeptFor is how long a record is kept.
const KeptFor = 7 * 24 * time.Hour

// Record is one phone's leg of one call, judged.
type Record struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	DeviceID  uuid.UUID
	Kind      string
	CallID    string // the leg's SIP Call-ID: what the phone's own report is filed under
	StartedAt time.Time
	EndedAt   time.Time
	Verdict   string
	Reasons   []string
	Sound     Sound
}

// Sound is what Asterisk measured of a leg's sound. Loss is a share of the
// packets (0–100), jitter and round trip in milliseconds; In is what the
// phone sent Linx, Out what Linx sent the phone.
type Sound struct {
	InPackets, OutPackets int
	InLost, OutLost       float64
	JitterMs, RTTMs       float64
}

// ParseQoS reads Asterisk's RTPAUDIOQOS ("ssrc=…;lp=2;rxjitter=0.004;
// rxcount=500;txcount=480;rlp=0;rtt=0.045"): lp is what Linx lost of the
// phone's sound, rlp what the phone lost of Linx's.
func ParseQoS(value string) (Sound, bool) {
	fields := map[string]float64{}
	for _, part := range strings.Split(value, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			fields[k] = f
		}
	}
	rx, okRx := fields["rxcount"]
	tx, okTx := fields["txcount"]
	if !okRx || !okTx {
		return Sound{}, false
	}
	share := func(lost, got float64) float64 {
		if lost <= 0 || lost+got <= 0 {
			return 0
		}
		return 100 * lost / (lost + got)
	}
	return Sound{
		InPackets: int(rx), OutPackets: int(tx),
		InLost:   share(fields["lp"], rx),
		OutLost:  share(fields["rlp"], tx),
		JitterMs: 1000 * fields["rxjitter"],
		RTTMs:    1000 * fields["rtt"],
	}, true
}

// Thresholds: past the first, a call had problems; past the second, it
// was bad. A ship's or a plane's satellite link has a long round trip
// whatever is done, so delay alone never makes a call bad.
const (
	lossProblems, lossBad     = 2.0, 8.0
	jitterProblems, jitterBad = 40.0, 100.0
	rttProblems               = 400.0
	// shortestJudged is the least sound a leg needs before loss means
	// anything: a call answered and hung up at once has none to speak of.
	shortestJudged = 100
)

// Judge says how a leg's sound went, with the reasons in words an admin
// reads without knowing what jitter is.
func Judge(s Sound) (string, []string) {
	var bad, problems []string
	switch {
	case s.InPackets == 0 && s.OutPackets > shortestJudged:
		bad = append(bad, "no sound reached Linx from this phone")
	case s.OutPackets == 0 && s.InPackets > shortestJudged:
		bad = append(bad, "no sound from Linx reached this phone")
	}
	lost := func(share float64, which string) {
		switch {
		case share >= lossBad:
			bad = append(bad, fmt.Sprintf("%s lost %.0f%% of its sound", which, share))
		case share >= lossProblems:
			problems = append(problems, fmt.Sprintf("%s lost %.0f%% of its sound", which, share))
		}
	}
	if s.InPackets+s.OutPackets > shortestJudged {
		lost(s.InLost, "the way from this phone")
		lost(s.OutLost, "the way to this phone")
	}
	switch {
	case s.JitterMs >= jitterBad:
		bad = append(bad, fmt.Sprintf("its sound came in uneven bursts (%.0f ms)", s.JitterMs))
	case s.JitterMs >= jitterProblems:
		problems = append(problems, fmt.Sprintf("its sound came unevenly (%.0f ms)", s.JitterMs))
	}
	if s.RTTMs >= rttProblems {
		problems = append(problems, fmt.Sprintf("a long delay each way (%.0f ms round trip)", s.RTTMs))
	}
	switch {
	case len(bad) > 0:
		return Bad, append(bad, problems...)
	case len(problems) > 0:
		return Problems, problems
	}
	return Good, nil
}

// Store keeps the records.
type Store interface {
	// DeviceForQuality is the phone Asterisk calls sipUsername, if it is
	// one of Linx's.
	DeviceForQuality(ctx context.Context, sipUsername string) (tenant, device uuid.UUID, name string, err error)
	// SaveCallQuality keeps r and forgets what is older than before.
	SaveCallQuality(ctx context.Context, r Record, before time.Time) error
	// BadCallsSince counts a phone's bad records since a moment.
	BadCallsSince(ctx context.Context, device uuid.UUID, since time.Time) (int, error)
}

// Alerter is the admin alerts engine (internal/alert).
type Alerter interface {
	Fire(ctx context.Context, tenant uuid.UUID, key, severity, title, message, link string) error
}

// Service records the legs and wakes it is told of.
type Service struct {
	Store  Store
	Alerts Alerter
	Now    func() time.Time
	Log    *slog.Logger
}

// AlertAfter bad calls within AlertWithin on one phone raise an alert.
const (
	AlertAfter  = 3
	AlertWithin = 24 * time.Hour
)

// Leg is told of a phone's leg hanging up: channel is Asterisk's name for
// it ("PJSIP/d_7k2m9x4q-00000001"), qos its RTPAUDIOQOS.
func (s *Service) Leg(ctx context.Context, channel, callID string, started time.Time, qos string) {
	sip, ok := sipUsername(channel)
	if !ok {
		return
	}
	sound, ok := ParseQoS(qos)
	if !ok {
		return
	}
	verdict, reasons := Judge(sound)
	s.save(ctx, sip, Record{Kind: KindCall, CallID: callID, StartedAt: started, Verdict: verdict,
		Reasons: reasons, Sound: sound})
}

// WakeMissed is told of phones a push woke for a call that hadn't arrived
// when the call stopped waiting for them.
func (s *Service) WakeMissed(ctx context.Context, sipUsernames []string, waited time.Duration) {
	now := s.Now()
	for _, sip := range sipUsernames {
		s.save(ctx, sip, Record{Kind: KindWake, StartedAt: now.Add(-waited), Verdict: Bad,
			Reasons: []string{fmt.Sprintf("woken for a call, but hadn't connected after %.0f s", waited.Seconds())}})
	}
}

func (s *Service) save(ctx context.Context, sip string, r Record) {
	tenant, device, name, err := s.Store.DeviceForQuality(ctx, sip)
	if err != nil {
		return // not one of Linx's phones (a trunk's leg), or gone
	}
	now := s.Now()
	r.ID, r.TenantID, r.DeviceID, r.EndedAt = uuid.New(), tenant, device, now
	if r.StartedAt.IsZero() || r.StartedAt.After(now) {
		r.StartedAt = now
	}
	if err := s.Store.SaveCallQuality(ctx, r, now.Add(-KeptFor)); err != nil {
		s.Log.Warn("call quality not saved", "device", device, "err", err)
		return
	}
	if r.Verdict != Bad || s.Alerts == nil {
		return
	}
	n, err := s.Store.BadCallsSince(ctx, device, now.Add(-AlertWithin))
	if err != nil || n < AlertAfter {
		return
	}
	// One alert per phone (its key), raised again only once it has been
	// resolved; the alert says where to look.
	if err := s.Alerts.Fire(ctx, tenant, "call-quality:"+device.String(), "warning",
		"Calls keep going wrong on "+name,
		fmt.Sprintf("%d of the calls on %s went badly in the last day: %s.", n, name, strings.Join(r.Reasons, "; ")),
		"/admin/calls/quality"); err != nil {
		s.Log.Warn("call quality alert not raised", "device", device, "err", err)
	}
}

// sipUsername is the endpoint in a PJSIP channel's name.
func sipUsername(channel string) (string, bool) {
	rest, ok := strings.CutPrefix(channel, "PJSIP/")
	if !ok {
		return "", false
	}
	if i := strings.LastIndexByte(rest, '-'); i > 0 {
		rest = rest[:i]
	}
	return rest, rest != ""
}
