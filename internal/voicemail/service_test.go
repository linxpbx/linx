package voicemail

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// fakeVM is one tenant: Sara's box (101), Sales' box (Sara is in it),
// Bob's box (102).
type fakeVM struct {
	sara, sales, bob uuid.UUID
	boxes            map[uuid.UUID]*Box
	members          map[uuid.UUID]bool // groups Sara (the viewer) is in
	viewerExt        uuid.UUID
	messages         map[uuid.UUID]Message
	heard            map[uuid.UUID]*uuid.UUID
	greetings        map[string][]byte
	inUse            map[string]bool
	keep             int
	audits           []auth.AuditEntry
}

func newFakeVM() *fakeVM {
	f := &fakeVM{sara: uuid.New(), sales: uuid.New(), bob: uuid.New(), messages: map[uuid.UUID]Message{},
		heard: map[uuid.UUID]*uuid.UUID{}, greetings: map[string][]byte{}, inUse: map[string]bool{}, keep: 60}
	f.boxes = map[uuid.UUID]*Box{
		f.sara:  {ID: f.sara, ExtensionID: &f.sara, Owner: "Sara Haddad (101)", Enabled: true, Email: true},
		f.sales: {ID: f.sales, RingGroupID: &f.sales, Owner: "Sales", Enabled: true},
		f.bob:   {ID: f.bob, ExtensionID: &f.bob, Owner: "Bob (102)", Enabled: true, Email: true},
	}
	f.members = map[uuid.UUID]bool{f.sales: true}
	f.viewerExt = f.sara
	for _, b := range []uuid.UUID{f.sara, f.sales, f.bob} {
		id := uuid.New()
		f.messages[id] = Message{ID: id, BoxID: b, Audio: tone(1)}
	}
	return f
}

func (f *fakeVM) msgIn(box uuid.UUID) uuid.UUID {
	for id, m := range f.messages {
		if m.BoxID == box {
			return id
		}
	}
	return uuid.Nil
}

func (f *fakeVM) VoicemailBoxes(_ context.Context, _, _ uuid.UUID) ([]BoxView, error) {
	var out []BoxView
	for _, id := range []uuid.UUID{f.sara, f.sales, f.bob} {
		b := f.boxes[id]
		v := BoxView{Box: *b, Mine: b.ExtensionID != nil && *b.ExtensionID == f.viewerExt, Member: f.members[id]}
		for mid, m := range f.messages {
			if m.BoxID == id {
				v.Count++
				if f.heard[mid] == nil {
					v.New++
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeVM) VoicemailList(_ context.Context, _ uuid.UUID, boxes []uuid.UUID, _ uuid.UUID, _ int) ([]Listed, error) {
	var out []Listed
	for _, m := range f.messages {
		for _, b := range boxes {
			if m.BoxID == b {
				out = append(out, Listed{Message: m})
			}
		}
	}
	return out, nil
}

func (f *fakeVM) VoicemailMessage(_ context.Context, _, id uuid.UUID) (Message, error) {
	m, ok := f.messages[id]
	if !ok {
		return m, ErrNotFound
	}
	return m, nil
}

func (f *fakeVM) VoicemailInfo(ctx context.Context, t, id uuid.UUID) (Message, error) {
	m, err := f.VoicemailMessage(ctx, t, id)
	m.Audio = nil
	return m, err
}

func (f *fakeVM) MarkVoicemail(_ context.Context, _, id uuid.UUID, by *uuid.UUID, _ time.Time) error {
	f.heard[id] = by
	return nil
}

func (f *fakeVM) DeleteVoicemail(_ context.Context, _, id uuid.UUID, a auth.AuditEntry) error {
	delete(f.messages, id)
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeVM) VoicemailGreetings(_ context.Context, box uuid.UUID) (map[string]GreetingInfo, error) {
	out := map[string]GreetingInfo{}
	for _, k := range []string{GreetingUnavailable, GreetingClosed} {
		if a, ok := f.greetings[GreetingName(box, k)]; ok {
			out[k] = GreetingInfo{Recorded: true, InUse: f.inUse[GreetingName(box, k)], Duration: time.Duration(len(a)) * time.Second / SampleRate}
		} else {
			out[k] = GreetingInfo{}
		}
	}
	return out, nil
}

func (f *fakeVM) VoicemailEmailTo(_ context.Context, b Box) ([]string, error) {
	if b.ExtensionID == nil {
		return nil, nil
	}
	return []string{"sara@example.com"}, nil
}

func (f *fakeVM) UpdateVoicemailBox(_ context.Context, _, id uuid.UUID, p BoxPatch, a auth.AuditEntry) error {
	b := f.boxes[id]
	if p.Enabled != nil {
		b.Enabled = *p.Enabled
	}
	if p.Email != nil {
		b.Email = *p.Email
	}
	for k, own := range p.UseOwn {
		f.inUse[GreetingName(id, k)] = own
	}
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeVM) SetGreeting(_ context.Context, _, box uuid.UUID, kind string, audio []byte, _ time.Time, _ uuid.UUID, a auth.AuditEntry) error {
	f.greetings[GreetingName(box, kind)] = audio
	f.inUse[GreetingName(box, kind)] = true
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeVM) DeleteGreeting(_ context.Context, _, box uuid.UUID, kind string, _ auth.AuditEntry) error {
	delete(f.greetings, GreetingName(box, kind))
	return nil
}

func (f *fakeVM) GreetingAudio(_ context.Context, box uuid.UUID, kind string) ([]byte, error) {
	a, ok := f.greetings[GreetingName(box, kind)]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

func (f *fakeVM) VoicemailKeepDays(context.Context) (int, error) { return f.keep, nil }

func (f *fakeVM) SetVoicemailKeepDays(_ context.Context, d int, _ auth.AuditEntry) error {
	f.keep = d
	return nil
}

func (f *fakeVM) VoicemailUsage(context.Context, uuid.UUID) (Usage, error) {
	return Usage{Count: len(f.messages)}, nil
}

func (f *fakeVM) ExpireVoicemail(context.Context, time.Time) ([]uuid.UUID, error) { return nil, nil }

func code(err error) string {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestServiceWhoSeesWhat(t *testing.T) {
	f := newFakeVM()
	changes := 0
	s := &Service{Store: f, Now: time.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Changed: func(uuid.UUID) { changes++ }}
	ctx := context.Background()
	sara := Viewer{User: uuid.New()}

	boxes, items, err := s.List(ctx, sara, nil, true) // all is for admins only
	if err != nil || len(boxes) != 2 || len(items) != 2 {
		t.Fatalf("Sara's list: %v, %d boxes, %d messages; want her own and Sales'", err, len(boxes), len(items))
	}
	if n, _ := s.NewCount(ctx, sara); n != 2 {
		t.Errorf("Sara's badge = %d, want 2", n)
	}
	if _, _, err := s.List(ctx, sara, &f.bob, false); code(err) != "not_found" {
		t.Errorf("Sara listing Bob's box: %v", err)
	}
	bobs := f.msgIn(f.bob)
	if _, err := s.Audio(ctx, sara, bobs); code(err) != "not_found" {
		t.Errorf("Sara playing Bob's message: %v", err)
	}
	if err := s.Mark(ctx, sara, bobs, true); code(err) != "not_found" {
		t.Errorf("Sara marking Bob's message: %v", err)
	}
	if err := s.Delete(ctx, sara, bobs, auth.AuditEntry{}); code(err) != "not_found" {
		t.Errorf("Sara deleting Bob's message: %v", err)
	}
	if _, err := s.Settings(ctx, sara, f.bob); code(err) != "not_found" {
		t.Errorf("Sara opening Bob's box: %v", err)
	}

	// A group's message: heard by Sara, for everyone in it.
	sales := f.msgIn(f.sales)
	if err := s.Mark(ctx, sara, sales, true); err != nil || f.heard[sales] == nil || *f.heard[sales] != sara.User || changes != 1 {
		t.Errorf("marking heard: %v, %v, %d changes", err, f.heard[sales], changes)
	}
	if n, _ := s.NewCount(ctx, sara); n != 1 {
		t.Errorf("badge after hearing one = %d", n)
	}
	if err := s.Delete(ctx, sara, sales, auth.AuditEntry{}); err != nil || len(f.audits) != 1 || f.audits[0].Action != "voicemail.delete" {
		t.Errorf("deleting: %v, %+v", err, f.audits)
	}

	admin := Viewer{User: uuid.New(), Admin: true}
	f.viewerExt, f.members = uuid.Nil, nil // the admin has no box and no groups
	if boxes, _, _ := s.List(ctx, admin, nil, false); len(boxes) != 0 {
		t.Errorf("an admin's own list has %d boxes (they have none)", len(boxes))
	}
	if boxes, _, _ := s.List(ctx, admin, nil, true); len(boxes) != 3 {
		t.Errorf("an admin's All boxes has %d", len(boxes))
	}
	if _, err := s.Audio(ctx, admin, bobs); err != nil {
		t.Errorf("an admin playing Bob's message: %v", err)
	}
}

func TestServiceBoxSettings(t *testing.T) {
	f := newFakeVM()
	emailOn := false
	s := &Service{Store: f, Now: time.Now, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		EmailOn: func(context.Context, uuid.UUID) (bool, error) { return emailOn, nil }}
	ctx := context.Background()
	sara := Viewer{User: uuid.New()}
	off, on := false, true

	got, err := s.Settings(ctx, sara, f.sara)
	if err != nil || !got.CanSwitch || got.EmailTo != "sara@example.com" || got.EmailReady {
		t.Fatalf("Sara's own box: %v, %+v", err, got)
	}
	if got, _ := s.Settings(ctx, sara, f.sales); got.CanSwitch {
		t.Error("a group's member may turn its box off")
	}
	if _, err := s.Update(ctx, sara, f.sales, BoxPatch{Enabled: &off}, auth.AuditEntry{}); code(err) != "forbidden" {
		t.Errorf("a member turning the group's box off: %v", err)
	}
	if _, err := s.Update(ctx, sara, f.sara, BoxPatch{Enabled: &off}, auth.AuditEntry{}); err != nil || f.boxes[f.sara].Enabled {
		t.Errorf("Sara turning her own box off: %v", err)
	}
	// Email: a group's never; a person's once email is set up (turning it
	// off always works).
	admin := Viewer{User: uuid.New(), Admin: true}
	if _, err := s.Update(ctx, admin, f.sales, BoxPatch{Email: &on}, auth.AuditEntry{}); code(err) != "email_not_for_groups" {
		t.Errorf("emailing a group's box: %v", err)
	}
	if _, err := s.Update(ctx, sara, f.sara, BoxPatch{Email: &off}, auth.AuditEntry{}); err != nil {
		t.Errorf("turning email off: %v", err)
	}
	if _, err := s.Update(ctx, sara, f.sara, BoxPatch{Email: &on}, auth.AuditEntry{}); code(err) != "email_not_set_up" {
		t.Errorf("email on before it's set up: %v", err)
	}
	emailOn = true
	if _, err := s.Update(ctx, sara, f.sara, BoxPatch{Email: &on}, auth.AuditEntry{}); err != nil || !f.boxes[f.sara].Email {
		t.Errorf("email on: %v", err)
	}

	// Greetings: a member may record the group's; "own" needs a recording.
	if _, err := s.Update(ctx, sara, f.sales, BoxPatch{UseOwn: map[string]bool{GreetingUnavailable: true}}, auth.AuditEntry{}); code(err) != "greeting_not_recorded" {
		t.Errorf("own greeting before recording: %v", err)
	}
	if err := s.SetGreeting(ctx, sara, f.sales, GreetingClosed, WAV(tone(3)), auth.AuditEntry{}); err != nil {
		t.Fatalf("a member recording the group's greeting: %v", err)
	}
	if err := s.SetGreeting(ctx, sara, f.sales, "weekend", WAV(tone(3)), auth.AuditEntry{}); code(err) != "not_found" {
		t.Errorf("an unknown greeting: %v", err)
	}
	if err := s.SetGreeting(ctx, sara, f.bob, GreetingClosed, WAV(tone(3)), auth.AuditEntry{}); code(err) != "not_found" {
		t.Errorf("recording Bob's greeting: %v", err)
	}
	if err := s.SetGreeting(ctx, sara, f.sara, GreetingUnavailable, []byte("RIFF"), auth.AuditEntry{}); code(err) != "greeting_invalid" {
		t.Errorf("a greeting that isn't audio: %v", err)
	}
	got, _ = s.Settings(ctx, sara, f.sales)
	if g := got.Greetings[GreetingClosed]; !g.Recorded || !g.InUse || g.Duration != 3*time.Second {
		t.Errorf("after recording: %+v", g)
	}
	if _, err := s.Update(ctx, sara, f.sales, BoxPatch{UseOwn: map[string]bool{GreetingClosed: false}}, auth.AuditEntry{}); err != nil || f.inUse[GreetingName(f.sales, GreetingClosed)] {
		t.Errorf("back to Linx's own: %v", err)
	}
	if wav, err := s.Greeting(ctx, sara, f.sales, GreetingClosed); err != nil || len(wav) != 44+2*3*SampleRate {
		t.Errorf("hearing it: %v, %d bytes", err, len(wav))
	}

	for days, want := range map[int]string{6: "keep_days_invalid", 366: "keep_days_invalid", 7: "", 365: ""} {
		if err := s.SetKeepDays(ctx, days, auth.AuditEntry{}); code(err) != want {
			t.Errorf("keep %d days: %v", days, err)
		}
	}
}
