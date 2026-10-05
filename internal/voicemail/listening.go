package voicemail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/ari"
	"linxpbx.com/linx/internal/auth"
)

// Listening to your own messages from a phone: *97 (Phase 2 step 9b,
// owner 2026-10-04).
//
// The dialplan hands the call to the control plane (Stasis), which answers
// it and plays. Linx keeps voicemail in its own database (ADR-069), so
// there is no mailbox of Asterisk's to play from: each message is written
// into a folder only Asterisk reads, played, and removed again — the same
// shape as the greetings people record in the browser.
//
// **Whose messages** is decided here, from the endpoint the channel
// belongs to, never from anything the phone sent or the dialplan passed:
// a phone can only ever hear its own extension's box.
//
// The keys are the three a person can remember without being told twice:
// 1 again, 2 next, 3 delete. A message heard to the end counts as heard,
// the same as it does in the web app; one skipped stays new.

// What *97 plays. Every one of these is a file in the Asterisk image
// (deploy/docker/asterisk/prompts, make prompts); TestListeningPrompts
// fails if one isn't.
const (
	promptNoBox   = "linx/vm-nobox"
	promptNone    = "linx/vm-none"
	promptIntro   = "linx/vm-intro"
	promptFrom    = "linx/vm-from"
	promptUnknown = "linx/vm-unknown"
	promptDeleted = "linx/vm-deleted"
	promptEnd     = "linx/vm-end"
	promptDigit   = "linx/digit-" // digit-0 … digit-9, digit-plus
)

// Keys a person may press.
const (
	keyAgain  = "1"
	keyNext   = "2"
	keyDelete = "3"
)

const (
	// afterMessage is how long a message waits for a key before moving on
	// by itself, so a phone put down still gets through them.
	afterMessage = 3 * time.Second
	// wholeCall ends a session that somehow never finishes, so one stuck
	// call can't hold a goroutine and a file for ever.
	wholeCall = 15 * time.Minute
	// maxDigits is how much of a caller's number is read out: enough for
	// any real number, and a cap on what a line can make Linx say.
	maxDigits = 15
)

// StasisVoicemail is the dialplan's Stasis() argument for *97.
const StasisVoicemail = "voicemail"

// ListenStore is the database access *97 needs (internal/store).
type ListenStore interface {
	// BoxForEndpoint is the voicemail box of the extension whose live
	// device has this SIP username, with the person it belongs to.
	// ErrNotFound when the device is gone, or its extension has no box.
	BoxForEndpoint(ctx context.Context, sipUsername string) (Box, *uuid.UUID, error)
	// UnheardVoicemail lists a box's messages nobody has heard, oldest
	// first and without their audio.
	UnheardVoicemail(ctx context.Context, box uuid.UUID) ([]Message, error)
	VoicemailMessage(ctx context.Context, tenant, id uuid.UUID) (Message, error)
	// HeardVoicemail marks a message heard, by this person or by nobody
	// in particular (an extension with no person).
	HeardVoicemail(ctx context.Context, tenant, id uuid.UUID, by *uuid.UUID, at time.Time) error
	DeleteVoicemail(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error
}

// Listening is *97 on this server.
type Listening struct {
	Store ListenStore
	// Dir is where the control plane writes a message out: in memory,
	// read-only to Asterisk (compose's voicemail-play).
	Dir string
	// DirForAsterisk is what Asterisk calls that same folder, which is
	// the name the message has to be played by. In Linx the two are the
	// same path in both containers (asteriskconf.PlayDir) and this can
	// be left empty; the call suite mounts a folder of its own.
	DirForAsterisk string
	// Light turns the message-waiting light off once messages are heard.
	Light *Light
	// Changed moves the Voicemail badge in the web app and the phone app.
	Changed func(tenant uuid.UUID)
	// AfterMessage is how long a message waits for a key before moving on
	// by itself (0: afterMessage).
	AfterMessage time.Duration
	Now          func() time.Time
	Log          *slog.Logger

	mu    sync.Mutex
	calls map[string]*session
}

func (l *Listening) log() *slog.Logger {
	if l.Log == nil {
		return slog.Default()
	}
	return l.Log
}

func (l *Listening) afterMessage() time.Duration {
	if l.AfterMessage > 0 {
		return l.AfterMessage
	}
	return afterMessage
}

func (l *Listening) now() time.Time {
	if l.Now == nil {
		return time.Now()
	}
	return l.Now()
}

// Stasis takes the ARI events of a call the control plane is holding.
// Anything that isn't *97 is left alone.
func (l *Listening) Stasis(ctx context.Context, c Asterisk, ev ari.Event) {
	// A playback's events carry no channel of their own: which call the
	// sound is playing into is in the playback's target.
	id := ""
	if ev.Channel != nil {
		id = ev.Channel.ID
	}
	if id == "" && ev.Playback != nil {
		id = ev.Playback.Channel()
	}
	if id == "" {
		return
	}
	switch ev.Type {
	case "StasisStart":
		if len(ev.Args) == 0 || ev.Args[0] != StasisVoicemail {
			// Not ours. Whatever put it there should take it out; Linx
			// has no other Stasis application.
			l.log().Warn("a call reached the control plane for something it doesn't do", "args", ev.Args)
			return
		}
		if ev.Channel == nil {
			return
		}
		s := &session{l: l, c: c, channel: id, events: make(chan ari.Event, 16), done: make(chan struct{})}
		l.mu.Lock()
		if l.calls == nil {
			l.calls = map[string]*session{}
		}
		if _, busy := l.calls[id]; busy {
			l.mu.Unlock()
			return
		}
		l.calls[id] = s
		l.mu.Unlock()
		go l.run(context.WithoutCancel(ctx), s, *ev.Channel)
	case "StasisEnd", "ChannelDestroyed", "ChannelHangupRequest":
		// The caller hung up (or Linx did). The session is told once; the
		// channel of events is never closed, so an event arriving at the
		// same moment has somewhere to go.
		l.mu.Lock()
		s := l.calls[id]
		delete(l.calls, id)
		l.mu.Unlock()
		if s != nil {
			s.finish()
		}
	default:
		l.mu.Lock()
		s := l.calls[id]
		l.mu.Unlock()
		if s == nil {
			return
		}
		select {
		case s.events <- ev:
		case <-s.done:
		default: // the session isn't listening for this one
		}
	}
}

// errGone is the caller hanging up: everything stops, quietly.
var errGone = errors.New("the caller hung up")

type session struct {
	l       *Listening
	c       Asterisk
	channel string
	events  chan ari.Event
	done    chan struct{}
	once    sync.Once
}

// finish says the call is over, once.
func (s *session) finish() { s.once.Do(func() { close(s.done) }) }

func (l *Listening) run(ctx context.Context, s *session, ch ari.Channel) {
	ctx, cancel := context.WithTimeout(ctx, wholeCall)
	defer cancel()
	defer func() {
		l.mu.Lock()
		if l.calls[ch.ID] == s {
			delete(l.calls, ch.ID)
		}
		l.mu.Unlock()
		s.finish()
	}()
	err := s.listen(ctx, ch)
	gone := errors.Is(err, errGone) || ari.Gone(err)
	if err != nil && !gone {
		l.log().Error("listening to voicemail from a phone didn't finish", "err", err)
	}
	if !gone {
		s.hangUp(ctx)
	}
}

func (s *session) listen(ctx context.Context, ch ari.Channel) error {
	if err := s.c.Do(ctx, "POST", "channels/"+url.PathEscape(s.channel)+"/answer", nil); err != nil {
		return err
	}
	box, owner, err := s.l.Store.BoxForEndpoint(ctx, ch.Endpoint())
	if err != nil || !box.Enabled {
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		_, _, err := s.play(ctx, promptNoBox, 0)
		return err
	}
	msgs, err := s.l.Store.UnheardVoicemail(ctx, box.ID)
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		_, _, err := s.play(ctx, promptNone, 0)
		return err
	}
	if _, _, err := s.play(ctx, promptIntro, 0); err != nil {
		return err
	}
	for i := 0; i < len(msgs); {
		key, err := s.message(ctx, box, owner, msgs[i])
		if err != nil {
			return err
		}
		if key == keyAgain {
			continue
		}
		i++
	}
	_, _, err = s.play(ctx, promptEnd, 0)
	return err
}

// message plays one message and returns the key the person pressed.
func (s *session) message(ctx context.Context, box Box, owner *uuid.UUID, m Message) (string, error) {
	key, _, err := s.play(ctx, s.whoFrom(m), 0)
	if err != nil || key != "" {
		return s.act(ctx, box, owner, m, key, err)
	}
	file, play, err := s.write(ctx, box.TenantID, m)
	if err != nil {
		// The message can't be played; say nothing wrong, go on to the
		// next. It is still there in the web app and the phone app.
		s.l.log().Error("a voicemail couldn't be written out for the phone to play", "message", m.ID, "err", err)
		return keyNext, nil
	}
	defer os.Remove(file)
	key, played, err := s.play(ctx, "sound:"+play, s.l.afterMessage())
	if err == nil && played && key != keyDelete {
		// Heard right through, so it stops being new — the same rule the
		// web app follows, and hearing it here clears the badge there.
		// A message skipped partway is still new, which is what pressing
		// 2 in the middle of one means.
		s.heard(ctx, box, owner, m)
	}
	return s.act(ctx, box, owner, m, key, err)
}

func (s *session) act(ctx context.Context, box Box, owner *uuid.UUID, m Message, key string, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if key != keyDelete {
		return key, nil
	}
	actor := "extension:" + box.ID.String()
	if owner != nil {
		actor = auth.TypeUser + ":" + owner.String()
	}
	audit := auth.AuditEntry{TenantID: &box.TenantID, Actor: actor,
		Action: "voicemail.delete", Target: "voicemail:" + m.ID.String(), Result: auth.ResultOK,
		Detail: map[string]any{"by": "phone"}}
	if err := s.l.Store.DeleteVoicemail(ctx, box.TenantID, m.ID, audit); err != nil {
		s.l.log().Error("deleting a voicemail from a phone failed", "message", m.ID, "err", err)
		return keyNext, nil
	}
	s.l.changed(box.TenantID)
	_, _, err = s.play(ctx, promptDeleted, 0)
	return keyNext, err
}

func (s *session) heard(ctx context.Context, box Box, owner *uuid.UUID, m Message) {
	if err := s.l.Store.HeardVoicemail(ctx, box.TenantID, m.ID, owner, s.l.now()); err != nil {
		s.l.log().Error("marking a voicemail heard from a phone failed", "message", m.ID, "err", err)
		return
	}
	s.l.changed(box.TenantID)
}

func (l *Listening) changed(tenant uuid.UUID) {
	if l.Changed != nil {
		l.Changed(tenant)
	}
	l.Light.Changed()
}

// whoFrom is "Message from 0 5 0 …", or "from a number that didn't show".
func (s *session) whoFrom(m Message) string {
	digits := Digits(m.CallerNumber)
	if len(digits) == 0 {
		return promptUnknown
	}
	return strings.Join(append([]string{promptFrom}, digits...), ",sound:")
}

// Digits is a caller's number as the files that read it out, one at a
// time. Anything that isn't a digit or a leading + is dropped, so nothing
// a line sends can name a file of its own.
func Digits(number string) []string {
	var out []string
	for i, r := range number {
		switch {
		case r >= '0' && r <= '9':
			out = append(out, promptDigit+string(r))
		case r == '+' && i == 0:
			out = append(out, promptDigit+"plus")
		}
		if len(out) == maxDigits {
			break
		}
	}
	return out
}

// playExt is the format Asterisk reads a written-out message in: 8 kHz
// mu-law, exactly as it was recorded and kept.
const playExt = ".ulaw"

// write puts one message where Asterisk can play it. It returns the file
// it wrote and the name Asterisk plays it by (no extension: Asterisk
// picks the format from it). The file is written beside its name and
// renamed into place, so a half-written message is never played.
func (s *session) write(ctx context.Context, tenant uuid.UUID, m Message) (file, play string, err error) {
	if s.l.Dir == "" {
		return "", "", errors.New("no folder for playing voicemail")
	}
	full, err := s.l.Store.VoicemailMessage(ctx, tenant, m.ID)
	if err != nil {
		return "", "", err
	}
	if len(full.Audio) == 0 {
		return "", "", fmt.Errorf("voicemail %s has no audio", m.ID)
	}
	file = filepath.Join(s.l.Dir, m.ID.String()+playExt)
	tmp := file + ".tmp"
	// Readable by whatever user Asterisk runs as: the folder is what
	// keeps a message private (compose's voicemail-play is 2750, so only
	// the control plane and Asterisk can even open it), not the file's
	// own mode. With 0640 the file is unreadable to Asterisk wherever
	// its uid isn't in the folder's group — which is how the call suite
	// mounts it, and how it failed there while working on a Mac, whose
	// Docker ignores uids.
	if err := os.WriteFile(tmp, full.Audio, 0o644); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return "", "", err
	}
	return file, path.Join(s.l.dirForAsterisk(), m.ID.String()), nil
}

// dirForAsterisk is what Asterisk calls the folder messages are written
// into: the same path, unless something mounted it elsewhere.
func (l *Listening) dirForAsterisk() string {
	if l.DirForAsterisk != "" {
		return l.DirForAsterisk
	}
	return l.Dir
}

// play plays media into the call and returns the key the person pressed
// (or "" if they pressed none) and whether the sound reached its end. A
// key stops it at once; after is how long to go on waiting for a key once
// it has finished.
func (s *session) play(ctx context.Context, media string, after time.Duration) (string, bool, error) {
	if !strings.HasPrefix(media, "sound:") {
		media = "sound:" + media
	}
	var p ari.Playback
	uri := "channels/" + url.PathEscape(s.channel) + "/play?media=" + url.QueryEscape(media)
	if err := s.c.Do(ctx, "POST", uri, &p); err != nil {
		return "", false, err
	}
	for {
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-s.done:
			return "", false, errGone
		case ev := <-s.events:
			switch ev.Type {
			case "ChannelDtmfReceived":
				s.stop(ctx, p.ID)
				return ev.Digit, false, nil
			case "PlaybackFinished":
				if ev.Playback != nil && ev.Playback.ID != p.ID {
					continue
				}
				key, err := s.waitForKey(ctx, after)
				return key, true, err
			}
		}
	}
}

// waitForKey is the moment after a message: a key, or nothing.
func (s *session) waitForKey(ctx context.Context, after time.Duration) (string, error) {
	if after <= 0 {
		return "", nil
	}
	timer := time.NewTimer(after)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
			return "", nil
		case <-s.done:
			return "", errGone
		case ev := <-s.events:
			if ev.Type == "ChannelDtmfReceived" {
				return ev.Digit, nil
			}
		}
	}
}

func (s *session) stop(ctx context.Context, playback string) {
	if playback == "" {
		return
	}
	if err := s.c.Do(ctx, "DELETE", "playbacks/"+url.PathEscape(playback), nil); err != nil && !ari.Gone(err) {
		s.l.log().Warn("stopping a voicemail playback failed", "err", err)
	}
}

func (s *session) hangUp(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.c.Do(ctx, "DELETE", "channels/"+url.PathEscape(s.channel), nil); err != nil && !ari.Gone(err) {
		s.l.log().Warn("hanging up after voicemail failed", "err", err)
	}
}
