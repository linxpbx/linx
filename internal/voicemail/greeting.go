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
// browser sends plain 16-bit audio at 16 kHz (it does the resampling, so
// nothing here decodes a compressed format); Linx keeps it as it came,
// which is Asterisk's slin16 (as clear as Linx's own messages, owner
// 2026-10-02), and copies the greetings in use into a folder Asterisk
// reads, where the dialplan plays a box's own file when it's there.

// Greeting kinds.
const (
	GreetingUnavailable = "unavailable"
	GreetingClosed      = "closed"
)

// GreetingRate is a greeting's samples a second: 16-bit, one channel, so
// 32 KB a second.
const GreetingRate = 16000

// Greeting limits: at most 30 seconds (the recording dialog stops there,
// a second more is allowed), at least half a second.
const (
	MaxGreetingSeconds = 30
	minGreetingBytes   = GreetingRate // half a second, 2 bytes a sample
	maxGreetingBytes   = 2 * GreetingRate * (MaxGreetingSeconds + 1)
	// MaxGreetingUpload is the largest WAV the upload takes: the audio and
	// its header.
	MaxGreetingUpload = maxGreetingBytes + 1024
)

// ValidGreetingKind reports whether kind is a greeting kind.
func ValidGreetingKind(kind string) bool {
	return kind == GreetingUnavailable || kind == GreetingClosed
}

// Upload mistakes, in words for the person recording.
var (
	ErrGreetingFormat = errors.New("send the greeting as a WAV file: 16-bit, one channel, 16000 samples a second")
	ErrGreetingShort  = errors.New("the greeting is shorter than half a second; record it again")
	ErrGreetingLong   = fmt.Errorf("a greeting can be at most %d seconds", MaxGreetingSeconds)
)

// GreetingFromWAV checks a browser's WAV (16-bit PCM, mono, 16 kHz) and
// returns its audio: little-endian 16-bit samples, Asterisk's slin16.
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
				binary.LittleEndian.Uint32(body[4:]) == GreetingRate &&
				binary.LittleEndian.Uint16(body[14:]) == 16
		case "data":
			data = body[:size&^1]
		}
		p += 8 + size + size&1
	}
	if !fmtOK || data == nil {
		return nil, ErrGreetingFormat
	}
	switch {
	case len(data) < minGreetingBytes:
		return nil, ErrGreetingShort
	case len(data) > maxGreetingBytes:
		return nil, ErrGreetingLong
	}
	return append([]byte(nil), data...), nil
}

// GreetingDuration is how long a greeting's audio plays.
func GreetingDuration(audio []byte) time.Duration {
	return time.Duration(len(audio)) * time.Second / (2 * GreetingRate)
}

// GreetingWAV is a greeting's audio as a WAV file, for its play button.
func GreetingWAV(audio []byte) []byte {
	out := make([]byte, 44+len(audio))
	copy(out[0:], "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(audio)))
	copy(out[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], 1) // mono
	binary.LittleEndian.PutUint32(out[24:], GreetingRate)
	binary.LittleEndian.PutUint32(out[28:], GreetingRate*2)
	binary.LittleEndian.PutUint16(out[32:], 2)
	binary.LittleEndian.PutUint16(out[34:], 16)
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(audio)))
	copy(out[44:], audio)
	return out
}

// GreetingFile is one greeting in use, for the folder.
type GreetingFile struct {
	BoxID      uuid.UUID
	Kind       string
	RecordedAt time.Time
}

// GreetingName is a greeting's file name in the folder (the dialplan's
// linx-voicemail builds the same name; .sln16 tells Asterisk the format).
func GreetingName(box uuid.UUID, kind string) string {
	return box.String() + "-" + kind + ".sln16"
}

// GreetingStore is what the folder is made from (internal/store).
type GreetingStore interface {
	// GreetingsInUse lists every greeting in use, on every tenant.
	GreetingsInUse(ctx context.Context) ([]GreetingFile, error)
	// GreetingAudio returns one greeting's audio (slin16).
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
	// Not .sln16, so the dialplan never sees it; a leftover goes at the
	// next Sync.
	tmp := strings.TrimSuffix(path, ".sln16") + ".tmp"
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
