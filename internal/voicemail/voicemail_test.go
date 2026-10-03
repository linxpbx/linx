package voicemail

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
)

type fakeStore struct {
	boxes   map[uuid.UUID]Box
	exts    map[string]uuid.UUID
	added   []Message
	events  []map[string]any
	emailTo []string
	failAdd error
}

func (f *fakeStore) VoicemailBox(_ context.Context, id uuid.UUID) (Box, error) {
	b, ok := f.boxes[id]
	if !ok {
		return Box{}, ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) CallerExtension(_ context.Context, _ uuid.UUID, number string) (*uuid.UUID, string, error) {
	if id, ok := f.exts[number]; ok {
		return &id, "Aisha Khan", nil
	}
	return nil, "", nil
}

func (f *fakeStore) AddVoicemail(_ context.Context, m Message, ev map[string]any) (bool, error) {
	if f.failAdd != nil {
		return false, f.failAdd
	}
	for _, o := range f.added {
		if o.Source == m.Source {
			return false, nil
		}
	}
	f.added = append(f.added, m)
	f.events = append(f.events, ev)
	return true, nil
}

func (f *fakeStore) VoicemailEmailTo(context.Context, Box) ([]string, error) { return f.emailTo, nil }

func (f *fakeStore) VoicemailMessage(_ context.Context, _, id uuid.UUID) (Message, error) {
	for _, m := range f.added {
		if m.ID == id {
			return m, nil
		}
	}
	return Message{}, ErrNotFound
}

func (f *fakeStore) TimeZone(context.Context) (string, error) { return "Asia/Dubai", nil }

type fakeMail struct {
	sent []email.Content
	to   [][]string
}

func (f *fakeMail) Enqueue(_ context.Context, _ uuid.UUID, kind string, to []string, c email.Content, _ auth.AuditEntry) (uuid.UUID, error) {
	if kind != email.KindVoicemail {
		panic(kind)
	}
	f.sent, f.to = append(f.sent, c), append(f.to, to)
	return uuid.New(), nil
}

var testNow = time.Date(2026, 10, 2, 6, 42, 0, 0, time.UTC)

func setup(t *testing.T) (*Importer, *fakeStore, *fakeMail, uuid.UUID) {
	t.Helper()
	box, ext := uuid.New(), uuid.New()
	st := &fakeStore{boxes: map[uuid.UUID]Box{box: {ID: box, TenantID: uuid.New(), ExtensionID: &box, Owner: "Sara Haddad (101)",
		Enabled: true, Email: true}}, exts: map[string]uuid.UUID{"103": ext}, emailTo: []string{"sara@example.com"}}
	mail := &fakeMail{}
	im := &Importer{Dir: t.TempDir(), Store: st, Mail: mail, WebAddress: "https://pbx.example.com", Now: func() time.Time { return testNow },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return im, st, mail, box
}

func write(t *testing.T, dir, name string, data []byte, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	at := testNow.Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func files(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestImportFromALine(t *testing.T) {
	im, st, mail, box := setup(t)
	start := testNow.Add(-50 * time.Second).Unix()
	write(t, im.Dir, "1727850000.42.ulaw", make([]byte, 42*SampleRate), 0)
	write(t, im.Dir, "1727850000.42.txt", []byte("v1|"+box.String()+"||0501234567|Ahmed Ali|"+strconv.FormatInt(start, 10)), 0)
	if err := im.ImportAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(st.added) != 1 {
		t.Fatalf("added %d messages", len(st.added))
	}
	m := st.added[0]
	if m.BoxID != box || m.CallerNumber != "0501234567" || m.CallerName != "Ahmed Ali" || m.CallerExtensionID != nil ||
		m.Duration != 42*time.Second || m.ReceivedAt.Unix() != start || m.Source != "1727850000.42" {
		t.Errorf("message = %+v", m)
	}
	if got := files(t, im.Dir); len(got) != 0 {
		t.Errorf("files left: %v", got)
	}
	ev := st.events[0]
	if ev["duration_seconds"] != 42 || ev["box"].(map[string]any)["kind"] != "extension" {
		t.Errorf("event = %v", ev)
	}
	if len(mail.sent) != 1 || mail.to[0][0] != "sara@example.com" {
		t.Fatalf("emails = %+v", mail.sent)
	}
	c := mail.sent[0]
	if c.Subject != "Voicemail from Ahmed Ali (0501234567) (0:42)" || c.Voicemail == nil || *c.Voicemail != m.ID {
		t.Errorf("email = %+v", c)
	}
	if !strings.Contains(c.Text, "called you at 10:41 and left a 42-second message") || !strings.Contains(c.Text, "https://pbx.example.com/voicemail") {
		t.Errorf("text = %q", c.Text)
	}
	// Once more (the files came back): stored once.
	write(t, im.Dir, "1727850000.42.ulaw", make([]byte, 42*SampleRate), 0)
	write(t, im.Dir, "1727850000.42.txt", []byte("v1|"+box.String()+"||0501234567|Ahmed Ali|"+strconv.FormatInt(start, 10)), 0)
	if err := im.ImportAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(st.added) != 1 || len(mail.sent) != 1 {
		t.Errorf("stored or emailed twice")
	}
}

func TestImportFromAPhone(t *testing.T) {
	im, st, _, box := setup(t)
	write(t, im.Dir, "1727850001.7.ulaw", make([]byte, 3*SampleRate), 0)
	// A phone's own name never reaches the note's text unfiltered; the
	// person's name comes from their extension.
	write(t, im.Dir, "1727850001.7.txt", []byte("v1|"+box.String()+"|103|103|Aisha|1727850000\n"), 0)
	if err := im.ImportAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(st.added) != 1 || st.added[0].CallerExtensionID == nil || st.added[0].CallerName != "Aisha Khan" {
		t.Fatalf("added = %+v", st.added)
	}
	// A start a day off: the time it arrived instead.
	if !st.added[0].ReceivedAt.Equal(testNow) {
		t.Errorf("received at %v", st.added[0].ReceivedAt)
	}
	if got := Caller(st.added[0]); got != "Aisha Khan (103)" {
		t.Errorf("caller = %q", got)
	}
}

// An analog line's busy tone (docs/PBX.md §4): the note says how much of
// the end is tone, and that's cut off; all tone leaves nothing.
func TestImportCutsTheBusyTone(t *testing.T) {
	im, st, _, box := setup(t)
	audio := make([]byte, 5*SampleRate+2550*SampleRate/1000)
	for i := range audio {
		audio[i] = byte(i)
	}
	write(t, im.Dir, "3.1.ulaw", audio, 0)
	write(t, im.Dir, "3.1.txt", []byte("v1|"+box.String()+"||0501234567|x|1727850000|2550\n"), 0)
	// The caller hung up before saying anything.
	write(t, im.Dir, "3.2.ulaw", make([]byte, 3*SampleRate), 0)
	write(t, im.Dir, "3.2.txt", []byte("v1|"+box.String()+"||0501234567|x|1727850000|2550\n"), 0)
	// The last field empty: the caller hung up on a line that signals it.
	write(t, im.Dir, "3.3.ulaw", make([]byte, 3*SampleRate), 0)
	write(t, im.Dir, "3.3.txt", []byte("v1|"+box.String()+"||0501234567|x|1727850000|\n"), 0)
	if err := im.ImportAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(st.added) != 2 {
		t.Fatalf("added %d messages, want 2", len(st.added))
	}
	for _, m := range st.added {
		switch m.Source {
		case "3.1":
			if m.Duration != 5*time.Second || !bytes.Equal(m.Audio, audio[:5*SampleRate]) {
				t.Errorf("3.1 kept %v, want the first 5 s", m.Duration)
			}
		case "3.3":
			if m.Duration != 3*time.Second {
				t.Errorf("3.3 kept %v, want all 3 s", m.Duration)
			}
		default:
			t.Errorf("kept %s", m.Source)
		}
	}
	if got := files(t, im.Dir); len(got) != 0 {
		t.Errorf("files left: %v", got)
	}
}

func TestImportRefuses(t *testing.T) {
	im, st, mail, box := setup(t)
	note := func(rest string) []byte { return []byte("v1|" + box.String() + "|" + rest) }
	// Shorter than a second: a hang-up at the tone.
	write(t, im.Dir, "1.1.ulaw", make([]byte, SampleRate-1), 0)
	write(t, im.Dir, "1.1.txt", note("||0501|x|1727850000"), 0)
	// Longer than Linx records.
	write(t, im.Dir, "1.2.ulaw", make([]byte, maxBytes+1), 0)
	write(t, im.Dir, "1.2.txt", note("||0501|x|1727850000"), 0)
	// A note that isn't plain.
	write(t, im.Dir, "1.3.ulaw", make([]byte, 2*SampleRate), 0)
	write(t, im.Dir, "1.3.txt", note("||0501|x<script>|1727850000"), 0)
	write(t, im.Dir, "1.4.ulaw", make([]byte, 2*SampleRate), 0)
	write(t, im.Dir, "1.4.txt", []byte("v2|whatever"), 0)
	// More busy tone to cut than the dialplan ever hears, or not a number.
	write(t, im.Dir, "1.8.ulaw", make([]byte, 20*SampleRate), 0)
	write(t, im.Dir, "1.8.txt", note("||0501|x|1727850000|10001"), 0)
	write(t, im.Dir, "1.9.ulaw", make([]byte, 20*SampleRate), 0)
	write(t, im.Dir, "1.9.txt", note("||0501|x|1727850000|-5"), 0)
	// A box that doesn't exist.
	write(t, im.Dir, "1.5.ulaw", make([]byte, 2*SampleRate), 0)
	write(t, im.Dir, "1.5.txt", []byte("v1|"+uuid.NewString()+"||0501|x|1727850000"), 0)
	// Still being recorded (no note yet, fresh): left alone. Cut off long
	// ago, and a stranger: removed.
	write(t, im.Dir, "1.6.ulaw", make([]byte, 2*SampleRate), time.Minute)
	write(t, im.Dir, "1.7.ulaw", make([]byte, 2*SampleRate), time.Hour)
	write(t, im.Dir, "notes.txt", []byte("hi"), time.Hour)
	if err := im.ImportAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(st.added) != 0 || len(mail.sent) != 0 {
		t.Errorf("added %+v", st.added)
	}
	if got := files(t, im.Dir); len(got) != 1 || got[0] != "1.6.ulaw" {
		t.Errorf("files left = %v, want only the one being recorded", got)
	}
}

func TestImportKeepsFilesWhenTheDatabaseFails(t *testing.T) {
	im, st, _, box := setup(t)
	st.failAdd = context.DeadlineExceeded
	write(t, im.Dir, "2.1.ulaw", make([]byte, 2*SampleRate), 0)
	write(t, im.Dir, "2.1.txt", []byte("v1|"+box.String()+"||0501|x|1727850000"), 0)
	if err := im.ImportAll(t.Context()); err == nil {
		t.Fatal("no error")
	}
	if got := files(t, im.Dir); len(got) != 2 {
		t.Errorf("files = %v, want both kept for the next try", got)
	}
}

func TestNoEmailForAGroupOrWhenOff(t *testing.T) {
	im, st, mail, box := setup(t)
	b := st.boxes[box]
	b.Email = false
	st.boxes[box] = b
	group := uuid.New()
	st.boxes[group] = Box{ID: group, TenantID: b.TenantID, RingGroupID: &group, Owner: "Sales", Enabled: true, Email: true}
	write(t, im.Dir, "3.1.ulaw", make([]byte, 2*SampleRate), 0)
	write(t, im.Dir, "3.1.txt", []byte("v1|"+box.String()+"||0501|x|1727850000"), 0)
	write(t, im.Dir, "3.2.ulaw", make([]byte, 2*SampleRate), 0)
	write(t, im.Dir, "3.2.txt", []byte("v1|"+group.String()+"||0501|x|1727850000"), 0)
	if err := im.ImportAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(st.added) != 2 || len(mail.sent) != 0 {
		t.Errorf("added %d, emailed %d", len(st.added), len(mail.sent))
	}
}

func TestKickNeverBlocks(t *testing.T) {
	im, _, _, _ := setup(t)
	for range 5 {
		im.Kick()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { im.Run(ctx); close(done) }()
	cancel()
	<-done
}

func TestWAV(t *testing.T) {
	w := WAV([]byte{0xff, 0x7f, 0x00, 0x80})
	if len(w) != 44+8 || string(w[0:4]) != "RIFF" || string(w[8:16]) != "WAVEfmt " || string(w[36:40]) != "data" {
		t.Fatalf("header = %q", w[:44])
	}
	if binary.LittleEndian.Uint32(w[24:]) != 8000 || binary.LittleEndian.Uint32(w[40:]) != 8 {
		t.Errorf("rate or size wrong")
	}
	samples := []int16{
		int16(binary.LittleEndian.Uint16(w[44:])), int16(binary.LittleEndian.Uint16(w[46:])),
		int16(binary.LittleEndian.Uint16(w[48:])), int16(binary.LittleEndian.Uint16(w[50:])),
	}
	// G.711: 0xff and 0x7f are silence, 0x00 and 0x80 the loudest.
	if samples[0] != 0 || samples[1] != 0 || samples[2] != -32124 || samples[3] != 32124 {
		t.Errorf("samples = %v", samples)
	}
}

func TestAttachment(t *testing.T) {
	_, st, _, box := setup(t)
	id := uuid.New()
	st.added = []Message{{ID: id, TenantID: st.boxes[box].TenantID, ReceivedAt: testNow, Audio: make([]byte, SampleRate)}}
	f, found, err := Attachment(st)(t.Context(), uuid.Nil, id)
	if err != nil || !found || f.Name != "voicemail-2026-10-02-1042.wav" || f.ContentType != "audio/wav" || len(f.Data) != 44+2*SampleRate {
		t.Errorf("file = %s %s %d, %v %v", f.Name, f.ContentType, len(f.Data), found, err)
	}
	if _, found, err := Attachment(st)(t.Context(), uuid.Nil, uuid.New()); found || err != nil {
		t.Errorf("deleted message: found %v, %v", found, err)
	}
}
