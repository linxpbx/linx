// Package voicemail takes the messages callers leave (ADR-069,
// docs/PHASE1F.md §8). The dialplan (internal/asteriskconf's
// linx-voicemail) plays the greeting and records into a folder only
// Asterisk and the control plane share; when the call ends it writes a
// one-line note next to the recording and tells the control plane over
// ARI (a LinxVoicemail user event). The Importer then checks both files,
// keeps the message in the database (so backups include it), deletes the
// files, queues the voicemail.created webhook and emails the box's owner.
// Files left while the control plane was restarting are taken when it
// starts: no polling.
package voicemail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
)

// Limits (docs/PHASE1F.md §8).
const (
	// MaxSeconds is how long a message can be (the dialplan's Record).
	MaxSeconds = 180
	// minBytes: anything shorter than a second is a caller hanging up at
	// the tone, not a message.
	minBytes = SampleRate
	// maxBytes allows a little over MaxSeconds; more isn't a recording
	// Linx's dialplan made.
	maxBytes = (MaxSeconds + 5) * SampleRate
	// staleAfter: a recording with no note this old was cut off (Asterisk
	// stopped mid-message), and anything else this old isn't Linx's.
	staleAfter = 10 * time.Minute
	maxNote    = 512
)

// EventName is the dialplan's UserEvent once a message's files are
// complete.
const EventName = "LinxVoicemail"

// sourcePattern is a recording's name: Asterisk's call id (UNIQUEID).
var sourcePattern = regexp.MustCompile(`^[0-9]{1,12}\.[0-9]{1,10}$`)

// ErrNotFound is a box or message that doesn't exist.
var ErrNotFound = errors.New("not found")

// Box is one voicemail box: a person's or a ring group's.
type Box struct {
	ID, TenantID uuid.UUID
	ExtensionID  *uuid.UUID
	RingGroupID  *uuid.UUID
	// Owner in words: "Sara Haddad (101)", "Sales".
	Owner          string
	Enabled, Email bool
}

// Message is one voicemail.
type Message struct {
	ID, TenantID, BoxID uuid.UUID
	// Source is the recording's name (Asterisk's call id): a message is
	// stored once even if its files outlive a restart.
	Source string
	// From a line: the caller's number and name as the dialplan cleaned
	// them. From a phone: CallerExtensionID, and its name.
	CallerNumber      string
	CallerName        string
	CallerExtensionID *uuid.UUID
	ReceivedAt        time.Time
	Duration          time.Duration
	// Audio is 8 kHz mu-law, as recorded.
	Audio     []byte
	CreatedAt time.Time
}

// Store is the database access voicemail needs (internal/store).
type Store interface {
	// VoicemailBox returns box id, ErrNotFound if there's none.
	VoicemailBox(ctx context.Context, id uuid.UUID) (Box, error)
	// CallerExtension is the tenant's live extension with this number
	// (nil when none) and its person's name.
	CallerExtension(ctx context.Context, tenant uuid.UUID, number string) (*uuid.UUID, string, error)
	// AddVoicemail stores m with its voicemail.created event (data); false
	// when m.Source was stored already.
	AddVoicemail(ctx context.Context, m Message, event map[string]any) (bool, error)
	// VoicemailEmailTo is where box's new messages are emailed: the
	// addresses of the box owner's own active accounts.
	VoicemailEmailTo(ctx context.Context, box Box) ([]string, error)
	// VoicemailMessage returns one message with its audio, ErrNotFound
	// when it's gone.
	VoicemailMessage(ctx context.Context, tenant, id uuid.UUID) (Message, error)
	// TimeZone is the server's (emails say the time in it).
	TimeZone(ctx context.Context) (string, error)
}

// Mailer is how a new message is emailed (internal/email).
type Mailer interface {
	Enqueue(ctx context.Context, tenant uuid.UUID, kind string, to []string, c email.Content, audit auth.AuditEntry) (uuid.UUID, error)
}

// Importer takes recorded messages from Dir into the database.
type Importer struct {
	Dir   string
	Store Store
	// Mail is nil for no emails.
	Mail Mailer
	// WebAddress is the web app's address, for the email's link ("" for
	// none).
	WebAddress string
	// Arrived, if set, is told when a message is kept, so the box owner's
	// phones can show a quiet notification (internal/push).
	Arrived func(ctx context.Context, extension uuid.UUID, from string)
	// Changed is told when a tenant gets a message (the badge); may be
	// nil.
	Changed func(tenant uuid.UUID)
	Now     func() time.Time
	Log     *slog.Logger

	once sync.Once
	kick chan struct{}
}

func (im *Importer) init() {
	im.once.Do(func() { im.kick = make(chan struct{}, 1) })
}

// Kick asks Run to look at the folder now (the dialplan's event). Never
// blocks.
func (im *Importer) Kick() {
	im.init()
	select {
	case im.kick <- struct{}{}:
	default:
	}
}

// Run imports what's waiting, then again whenever Kick is called, until
// ctx ends.
func (im *Importer) Run(ctx context.Context) {
	im.init()
	for {
		if err := im.ImportAll(ctx); err != nil && ctx.Err() == nil {
			im.Log.Error("taking voicemail from Asterisk failed; trying again with the next message", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-im.kick:
		}
	}
}

// ImportAll takes every complete message in Dir and clears out what's
// stale.
func (im *Importer) ImportAll(ctx context.Context) error {
	entries, err := os.ReadDir(im.Dir)
	if err != nil {
		return err
	}
	now := im.Now()
	notes := map[string]bool{}
	for _, e := range entries {
		if src, ok := strings.CutSuffix(e.Name(), ".txt"); ok && sourcePattern.MatchString(src) {
			notes[src] = true
		}
	}
	var firstErr error
	for _, e := range entries {
		name := e.Name()
		src, ext := strings.TrimSuffix(name, filepath.Ext(name)), filepath.Ext(name)
		switch {
		case ext == ".txt" && notes[src]:
			if err := im.importOne(ctx, src); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("message %s: %w", src, err)
			}
		case ext == ".ulaw" && notes[src]:
			// Taken with its note.
		default:
			// Still being recorded, or left behind.
			info, err := e.Info()
			if err == nil && now.Sub(info.ModTime()) > staleAfter {
				im.Log.Warn("removing a voicemail file that was never finished", "file", name)
				im.remove(name)
			}
		}
	}
	return firstErr
}

func (im *Importer) remove(names ...string) {
	for _, n := range names {
		if err := os.Remove(filepath.Join(im.Dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			im.Log.Error("removing a voicemail file failed", "file", n, "err", err)
		}
	}
}

// note is the dialplan's line next to a recording:
// v1|<box>|<calling extension, or "" from a line>|<number>|<name>|<start, Unix seconds>[|<busy tone ms>]
// The last field (since the busy tone, docs/PBX.md §4; a note without it
// is from before) is how much of the recording's end is an analog line's
// busy tone, empty when the caller simply hung up.
type note struct {
	box             uuid.UUID
	callerExtension string
	number, name    string
	start           time.Time
	busyTone        time.Duration
}

// maxBusyTone is the most busy tone a note can ask to cut: more than the
// dialplan ever hears before it stops (asteriskconf.BusyTone.TrimMs).
const maxBusyTone = 10 * time.Second

var (
	numberPattern = regexp.MustCompile(`^\+?[0-9]{0,20}$`)
	extPattern    = regexp.MustCompile(`^([0-9]{2,6})?$`)
	namePattern   = regexp.MustCompile(`^[A-Za-z0-9 .]{0,40}$`)
)

func parseNote(b []byte) (note, error) {
	f := strings.Split(strings.TrimRight(string(b), "\r\n"), "|")
	if len(f) != 6 && len(f) != 7 || f[0] != "v1" {
		return note{}, errors.New("the note isn't Linx's")
	}
	var n note
	var err error
	if n.box, err = uuid.Parse(f[1]); err != nil {
		return note{}, errors.New("the note's box isn't an id")
	}
	if !extPattern.MatchString(f[2]) || !numberPattern.MatchString(f[3]) || !namePattern.MatchString(f[4]) {
		return note{}, errors.New("the note's caller isn't plain")
	}
	n.callerExtension, n.number, n.name = f[2], f[3], strings.TrimSpace(f[4])
	sec, err := strconv.ParseInt(f[5], 10, 64)
	if err != nil {
		return note{}, errors.New("the note's time isn't a time")
	}
	n.start = time.Unix(sec, 0).UTC()
	if len(f) == 7 && f[6] != "" {
		ms, err := strconv.Atoi(f[6])
		if err != nil || ms < 0 || time.Duration(ms)*time.Millisecond > maxBusyTone {
			return note{}, errors.New("the note's busy tone isn't a length")
		}
		n.busyTone = time.Duration(ms) * time.Millisecond
	}
	return n, nil
}

// readLimited reads path, refusing more than limit bytes.
func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("longer than %d bytes", limit)
	}
	return b, nil
}

// importOne takes message src. A mistake in the files drops them (they
// won't get better); a database error leaves them for the next try.
func (im *Importer) importOne(ctx context.Context, src string) error {
	drop := func(why string) error {
		im.Log.Warn("voicemail not kept", "message", src, "why", why)
		im.remove(src+".ulaw", src+".txt")
		return nil
	}
	raw, err := readLimited(filepath.Join(im.Dir, src+".txt"), maxNote)
	if err != nil {
		return drop("its note can't be read: " + err.Error())
	}
	n, err := parseNote(raw)
	if err != nil {
		return drop(err.Error())
	}
	audio, err := readLimited(filepath.Join(im.Dir, src+".ulaw"), maxBytes)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Stored already (deleting stopped halfway), or nothing recorded.
		im.remove(src + ".txt")
		return nil
	case err != nil:
		return drop("the recording can't be read: " + err.Error())
	}
	// The line's busy tone after the caller hung up isn't the message.
	audio = audio[:max(0, len(audio)-int(n.busyTone*SampleRate/time.Second))]
	if len(audio) < minBytes {
		im.Log.Info("voicemail shorter than a second, not kept", "message", src)
		im.remove(src+".ulaw", src+".txt")
		return nil
	}
	box, err := im.Store.VoicemailBox(ctx, n.box)
	if errors.Is(err, ErrNotFound) {
		return drop("its voicemail box doesn't exist")
	}
	if err != nil {
		return err
	}
	now := im.Now().UTC()
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	m := Message{ID: id, TenantID: box.TenantID, BoxID: box.ID, Source: src, CallerNumber: n.number, CallerName: n.name,
		ReceivedAt: n.start, Duration: time.Duration(len(audio)) * time.Second / SampleRate, Audio: audio, CreatedAt: now}
	// The dialplan's start, unless it's nonsense (a clock far off).
	if m.ReceivedAt.After(now) || now.Sub(m.ReceivedAt) > 24*time.Hour {
		m.ReceivedAt = now
	}
	if n.callerExtension != "" {
		ext, name, err := im.Store.CallerExtension(ctx, box.TenantID, n.callerExtension)
		if err != nil {
			return err
		}
		if ext != nil {
			m.CallerExtensionID, m.CallerName, m.CallerNumber = ext, name, n.callerExtension
		}
	}
	added, err := im.Store.AddVoicemail(ctx, m, eventData(box, m))
	if err != nil {
		return err
	}
	im.remove(src+".ulaw", src+".txt")
	if added {
		im.Log.Info("voicemail kept", "message", m.ID, "box", box.ID, "seconds", int(m.Duration.Seconds()))
		if im.Changed != nil {
			im.Changed(box.TenantID)
		}
		if im.Arrived != nil && box.ExtensionID != nil {
			im.Arrived(ctx, *box.ExtensionID, m.CallerNumber)
		}
		im.mail(ctx, box, m)
	}
	return nil
}

// eventData is voicemail.created's data (docs/API.md §4): no audio, which
// the API serves to those allowed to hear it.
func eventData(box Box, m Message) map[string]any {
	b := map[string]any{"id": box.ID}
	if box.ExtensionID != nil {
		b["kind"], b["extension_id"] = "extension", *box.ExtensionID
	} else if box.RingGroupID != nil {
		b["kind"], b["ring_group_id"] = "ring_group", *box.RingGroupID
	}
	from := map[string]any{"number": m.CallerNumber, "name": m.CallerName}
	if m.CallerExtensionID != nil {
		from["extension_id"] = *m.CallerExtensionID
	}
	return map[string]any{"id": m.ID, "box": b, "from": from, "received_at": m.ReceivedAt,
		"duration_seconds": int(m.Duration.Round(time.Second).Seconds())}
}

// mail emails m to its box's owner, when the box says so and email is set
// up. A failure here doesn't lose the message: it's stored already.
func (im *Importer) mail(ctx context.Context, box Box, m Message) {
	if im.Mail == nil || !box.Email || box.ExtensionID == nil {
		return
	}
	to, err := im.Store.VoicemailEmailTo(ctx, box)
	if err != nil {
		im.Log.Error("finding where to email a voicemail failed", "message", m.ID, "err", err)
		return
	}
	if len(to) == 0 {
		return
	}
	zone, err := im.Store.TimeZone(ctx)
	if err != nil {
		zone = "UTC"
	}
	c := Content(m, zone, im.WebAddress, im.Now())
	_, err = im.Mail.Enqueue(ctx, box.TenantID, email.KindVoicemail, to, c,
		auth.AuditEntry{Actor: "system", Action: "email.queue", Result: auth.ResultOK,
			Detail: map[string]any{"voicemail_id": m.ID}})
	if err != nil && !errors.Is(err, email.ErrOff) {
		im.Log.Error("queueing a voicemail email failed", "message", m.ID, "err", err)
	}
}

// Caller is who left m, in words: "Aisha (103)", "050 123 4567", "Ahmed
// Ali (+971501234567)", "a withheld number".
func Caller(m Message) string {
	switch {
	case m.CallerExtensionID != nil:
		return fmt.Sprintf("%s (%s)", m.CallerName, m.CallerNumber)
	case m.CallerNumber != "" && m.CallerName != "" && m.CallerName != m.CallerNumber:
		return fmt.Sprintf("%s (%s)", m.CallerName, m.CallerNumber)
	case m.CallerNumber != "":
		return m.CallerNumber
	case m.CallerName != "":
		return m.CallerName
	}
	return "a withheld number"
}

// Length is a message's length as m:ss ("0:42").
func Length(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Content is m's email (docs/ui/SCREENS_PHASE1F.md §5.3): who and how
// long in the subject, the audio attached when it's sent. The day is
// said when it isn't now's.
func Content(m Message, zone, web string, now time.Time) email.Content {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	who := Caller(m)
	if !utf8.ValidString(who) || strings.ContainsAny(who, "\r\n") {
		who = "a caller"
	}
	at := m.ReceivedAt.In(loc)
	when := at.Format("15:04")
	if day := at.Format("Mon 2 Jan"); day != now.In(loc).Format("Mon 2 Jan") {
		when += " on " + day
	}
	secs := int(m.Duration.Round(time.Second).Seconds())
	text := fmt.Sprintf("%s called you at %s and left a %d-second message. It's attached.", who, when, secs)
	if web != "" {
		text += "\n\nListen in Linx: " + strings.TrimRight(web, "/") + "/voicemail"
	}
	id := m.ID
	return email.Content{
		Subject:   fmt.Sprintf("Voicemail from %s (%s)", who, Length(m.Duration)),
		Text:      text,
		Voicemail: &id,
	}
}

// File is m as an email attachment: a WAV named for when it was left.
func File(m Message, zone string) email.File {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	return email.File{Name: "voicemail-" + m.ReceivedAt.In(loc).Format("2006-01-02-1504") + ".wav",
		ContentType: "audio/wav", Data: WAV(m.Audio)}
}

// Attachment is internal/email's Voicemail hook: the message as a file,
// or found false once it's deleted.
func Attachment(st Store) func(ctx context.Context, tenant, id uuid.UUID) (email.File, bool, error) {
	return func(ctx context.Context, tenant, id uuid.UUID) (email.File, bool, error) {
		m, err := st.VoicemailMessage(ctx, tenant, id)
		if errors.Is(err, ErrNotFound) {
			return email.File{}, false, nil
		}
		if err != nil {
			return email.File{}, false, err
		}
		zone, err := st.TimeZone(ctx)
		if err != nil {
			zone = "UTC"
		}
		return File(m, zone), true, nil
	}
}
