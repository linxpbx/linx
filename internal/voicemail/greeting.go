package voicemail

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Greetings (docs/ui/SCREENS_PHASE1F.md §12.3-12.4): a box's own
// "unavailable" and "closed" greetings, recorded in the browser. The
// browser sends plain 16-bit audio at 8 kHz (it does the resampling, so
// nothing here decodes a compressed format); Linx keeps it as mu-law like
// the messages, and copies the greetings in use into a folder Asterisk
// reads, where the dialplan plays a box's own file when it's there.

// Greeting kinds.
const (
	GreetingUnavailable = "unavailable"
	GreetingClosed      = "closed"
)

// Greeting limits: at most 30 seconds (the recording dialog stops there),
// at least half a second.
const (
	MaxGreetingSeconds = 30
	minGreetingBytes   = SampleRate / 2
	maxGreetingBytes   = MaxGreetingSeconds*SampleRate + SampleRate
	// MaxGreetingUpload is the largest WAV the upload takes: 16-bit audio
	// at 8 kHz is twice the mu-law, plus its header.
	MaxGreetingUpload = 2*maxGreetingBytes + 1024
)

// ValidGreetingKind reports whether kind is a greeting kind.
func ValidGreetingKind(kind string) bool {
	return kind == GreetingUnavailable || kind == GreetingClosed
}

// Upload mistakes, in words for the person recording.
var (
	ErrGreetingFormat = errors.New("send the greeting as a WAV file: 16-bit, one channel, 8000 samples a second")
	ErrGreetingShort  = errors.New("the greeting is shorter than half a second; record it again")
	ErrGreetingLong   = fmt.Errorf("a greeting can be at most %d seconds", MaxGreetingSeconds)
)

// GreetingFromWAV checks a browser's WAV (16-bit PCM, mono, 8 kHz) and
// returns it as mu-law.
func GreetingFromWAV(b []byte) ([]byte, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, ErrGreetingFormat
	}
	var fmtOK bool
	var data []byte
	for p := 12; p+8 <= len(b); {
		id, size := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:p+8]))
		body := b[p+8:]
		if size < 0 || size > len(body) {
			if id != "data" {
				return nil, ErrGreetingFormat
			}
			size = len(body) // a recorder that didn't know the length yet
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, ErrGreetingFormat
			}
			fmtOK = binary.LittleEndian.Uint16(body[0:]) == 1 && // PCM
				binary.LittleEndian.Uint16(body[2:]) == 1 && // mono
				binary.LittleEndian.Uint32(body[4:]) == SampleRate &&
				binary.LittleEndian.Uint16(body[14:]) == 16
		case "data":
			data = body[:size&^1]
		}
		p += 8 + size + size&1
	}
	if !fmtOK || data == nil {
		return nil, ErrGreetingFormat
	}
	n := len(data) / 2
	switch {
	case n < minGreetingBytes:
		return nil, ErrGreetingShort
	case n > maxGreetingBytes:
		return nil, ErrGreetingLong
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = linearToUlaw(int16(binary.LittleEndian.Uint16(data[2*i:])))
	}
	return out, nil
}

// linearToUlaw is G.711's mu-law compression (the inverse of
// ulawToLinear).
func linearToUlaw(s int16) byte {
	const bias, clip = 0x84, 32635
	v := int(s)
	sign := 0
	if v < 0 {
		v, sign = -v, 0x80
	}
	if v > clip {
		v = clip
	}
	v += bias
	exp := 7
	for mask := 0x4000; v&mask == 0 && exp > 0; mask >>= 1 {
		exp--
	}
	mant := (v >> (exp + 3)) & 0x0f
	return ^byte(sign | exp<<4 | mant)
}

// GreetingFile is one greeting in use, for the folder.
type GreetingFile struct {
	BoxID      uuid.UUID
	Kind       string
	RecordedAt time.Time
}

// GreetingName is a greeting's file name in the folder (the dialplan's
// linx-voicemail builds the same name).
func GreetingName(box uuid.UUID, kind string) string {
	return box.String() + "-" + kind + ".ulaw"
}

// GreetingStore is what the folder is made from (internal/store).
type GreetingStore interface {
	// GreetingsInUse lists every greeting in use, on every tenant.
	GreetingsInUse(ctx context.Context) ([]GreetingFile, error)
	// GreetingAudio returns one greeting's mu-law audio.
	GreetingAudio(ctx context.Context, box uuid.UUID, kind string) ([]byte, error)
}

// Greetings keeps the folder Asterisk reads the same as the database: a
// file for each greeting in use, its time the greeting's, nothing else.
type Greetings struct {
	Dir   string
	Store GreetingStore
	Log   *slog.Logger

	mu sync.Mutex
}

// Sync brings the folder up to date: writes what's new or changed,
// removes what's no longer in use. Each file is written next to its name
// and renamed into place, so Asterisk never plays half of one.
func (g *Greetings) Sync(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	want, err := g.Store.GreetingsInUse(ctx)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	var firstErr error
	for _, w := range want {
		name := GreetingName(w.BoxID, w.Kind)
		keep[name] = true
		path := filepath.Join(g.Dir, name)
		if info, err := os.Stat(path); err == nil && info.ModTime().Equal(w.RecordedAt.Truncate(time.Second)) {
			continue
		}
		if err := g.write(ctx, path, w); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("greeting %s: %w", name, err)
		}
	}
	entries, err := os.ReadDir(g.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if keep[e.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(g.Dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (g *Greetings) write(ctx context.Context, path string, w GreetingFile) error {
	audio, err := g.Store.GreetingAudio(ctx, w.BoxID, w.Kind)
	if err != nil {
		return err
	}
	// Not .ulaw, so the dialplan never sees it; a leftover goes at the
	// next Sync.
	tmp := strings.TrimSuffix(path, ".ulaw") + ".tmp"
	if err := os.WriteFile(tmp, audio, 0o640); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	at := w.RecordedAt.Truncate(time.Second)
	if err := os.Chtimes(tmp, at, at); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// SyncLogged is Sync for a change someone just made: a failure is logged
// (the box plays Linx's own greeting meanwhile) and tried again at the
// next change or start.
func (g *Greetings) SyncLogged(ctx context.Context) {
	if g == nil {
		return
	}
	if err := g.Sync(context.WithoutCancel(ctx)); err != nil {
		g.Log.Error("copying voicemail greetings for Asterisk failed; boxes play Linx's own greeting until it works", "err", err)
	}
}
