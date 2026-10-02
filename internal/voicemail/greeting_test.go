package voicemail

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

func tone(seconds float64) []byte {
	b := make([]byte, int(seconds*SampleRate))
	for i := range b {
		b[i] = byte(i%100 + 130) // not 0x7f (-0 comes back as +0)
	}
	return b
}

// speech is seconds of 16 kHz 16-bit audio, a greeting as the browser
// sends it (in GreetingWAV's file).
func speech(seconds float64) []byte {
	b := make([]byte, 2*int(seconds*GreetingRate))
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func TestGreetingFromWAV(t *testing.T) {
	audio := speech(2)
	got, err := GreetingFromWAV(GreetingWAV(audio))
	if err != nil || string(got) != string(audio) {
		t.Fatalf("a 2-second greeting: %v (same audio: %v)", err, string(got) == string(audio))
	}

	// Another chunk before the audio, and a data length the recorder
	// didn't know yet.
	w := GreetingWAV(audio)
	list := append([]byte("LIST\x04\x00\x00\x00INFO"), w[36:]...)
	odd := append(append([]byte{}, w[:36]...), list...)
	if got, err := GreetingFromWAV(odd); err != nil || len(got) != len(audio) {
		t.Errorf("with a LIST chunk: %v, %d samples", err, len(got))
	}
	unknown := append([]byte{}, w...)
	binary.LittleEndian.PutUint32(unknown[40:], 0xffffffff)
	if got, err := GreetingFromWAV(unknown); err != nil || len(got) != len(audio) {
		t.Errorf("with an unknown length: %v, %d samples", err, len(got))
	}

	wrongRate := append([]byte{}, w...)
	binary.LittleEndian.PutUint32(wrongRate[24:], 8000)
	stereo := append([]byte{}, w...)
	binary.LittleEndian.PutUint16(stereo[22:], 2)
	for name, c := range map[string]struct {
		b    []byte
		want error
	}{
		"not a WAV":    {[]byte("hello, this is not audio"), ErrGreetingFormat},
		"8 kHz":        {wrongRate, ErrGreetingFormat},
		"stereo":       {stereo, ErrGreetingFormat},
		"no audio":     {w[:36], ErrGreetingFormat},
		"too short":    {GreetingWAV(speech(0.3)), ErrGreetingShort},
		"too long":     {GreetingWAV(speech(MaxGreetingSeconds + 2)), ErrGreetingLong},
		"just 30 s":    {GreetingWAV(speech(MaxGreetingSeconds)), nil},
		"half-sample":  {append(GreetingWAV(speech(1)), 7), nil},
		"empty":        {nil, ErrGreetingFormat},
		"cut in fmt":   {w[:20], ErrGreetingFormat},
		"short header": {w[:11], ErrGreetingFormat},
	} {
		if _, err := GreetingFromWAV(c.b); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if len(GreetingWAV(speech(MaxGreetingSeconds+1))) > MaxGreetingUpload {
		t.Error("the upload limit is smaller than the longest greeting")
	}
	if d := GreetingDuration(speech(2)); d != 2*time.Second {
		t.Errorf("a 2-second greeting lasts %v", d)
	}
}

type greetingStore struct {
	files []GreetingFile
	audio map[string][]byte
	reads int
}

func (g *greetingStore) GreetingsInUse(context.Context) ([]GreetingFile, error) { return g.files, nil }

func (g *greetingStore) GreetingAudio(_ context.Context, box uuid.UUID, kind string) ([]byte, error) {
	g.reads++
	a, ok := g.audio[GreetingName(box, kind)]
	if !ok {
		return nil, ErrNotFound
	}
	return a, nil
}

func TestGreetingsSync(t *testing.T) {
	dir := t.TempDir()
	box, other := uuid.New(), uuid.New()
	at := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	st := &greetingStore{
		files: []GreetingFile{{BoxID: box, Kind: GreetingUnavailable, RecordedAt: at}, {BoxID: box, Kind: GreetingClosed, RecordedAt: at}},
		audio: map[string][]byte{GreetingName(box, GreetingUnavailable): []byte("hello"), GreetingName(box, GreetingClosed): []byte("closed")},
	}
	g := &Greetings{Dir: dir, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Left over: a greeting no longer in use, a half-written copy.
	for _, n := range []string{GreetingName(other, GreetingUnavailable), "x.tmp"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("folder has %d files, want the two in use", len(entries))
	}
	b, _ := os.ReadFile(filepath.Join(dir, GreetingName(box, GreetingUnavailable)))
	if string(b) != "hello" {
		t.Errorf("greeting = %q", b)
	}

	// Nothing changed: nothing read again.
	st.reads = 0
	if err := g.Sync(context.Background()); err != nil || st.reads != 0 {
		t.Errorf("second Sync: %v, %d reads", err, st.reads)
	}

	// Recorded again: copied again. Back to Linx's own: removed.
	st.files = []GreetingFile{{BoxID: box, Kind: GreetingUnavailable, RecordedAt: at.Add(time.Minute)}}
	st.audio[GreetingName(box, GreetingUnavailable)] = []byte("hello again")
	if err := g.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, GreetingName(box, GreetingUnavailable)))
	if string(b) != "hello again" {
		t.Errorf("after recording again: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, GreetingName(box, GreetingClosed))); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a greeting no longer in use is still there: %v", err)
	}

	var none *Greetings
	none.SyncLogged(context.Background()) // no folder: nothing happens
}
