package voicemail

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// Listening and settings (docs/ui/SCREENS_PHASE1F.md §12, Phase 1F step
// 14): who may hear which box, marking heard, deleting, the box's
// switches and greetings, and how long messages are kept.
//
// Who sees a box (docs/PHASE1F.md §12): a person's box, that person; a
// ring group's, its members; every box, admins (who hold users:write: an
// admin limited to the office network doesn't). Only a signed-in person,
// never an API key: the audio is people's voices.

// KeepDays limits (System → Settings).
const (
	MinKeepDays     = 7
	MaxKeepDays     = 365
	DefaultKeepDays = 60
)

// MaxList is how many messages one list returns, newest first.
const MaxList = 500

// Viewer is the signed-in person asking.
type Viewer struct {
	Tenant, User uuid.UUID
	// Admin may see and change every box.
	Admin bool
}

// ViewerFrom is the person behind p.
func ViewerFrom(p auth.Principal, user uuid.UUID) Viewer {
	return Viewer{Tenant: p.TenantID, User: user, Admin: slices.Contains(p.Scopes, "users:write")}
}

// BoxView is one box as its viewer sees it.
type BoxView struct {
	Box
	// Mine: the viewer's own box. Member: a ring group's the viewer is in.
	Mine, Member bool
	// Removed: the person is gone; the box stays until its messages
	// expire.
	Removed    bool
	Count, New int
	Bytes      int64
}

// GreetingInfo is one of a box's greetings.
type GreetingInfo struct {
	// Recorded is false when the box has only Linx's own.
	Recorded   bool
	InUse      bool
	RecordedAt time.Time
	Duration   time.Duration
}

// BoxSettings is one box's settings sheet.
type BoxSettings struct {
	BoxView
	Greetings map[string]GreetingInfo
	// EmailTo is where messages are emailed (a person's own box, to its
	// owner only).
	EmailTo string
	// EmailReady: email is set up on this server.
	EmailReady bool
	// CanSwitch: the viewer may turn the box on or off and its emails (the
	// person, or an admin); a group's members may change its greetings
	// only.
	CanSwitch bool
}

// Listed is one message in a list: no audio.
type Listed struct {
	Message
	HeardAt   *time.Time
	HeardBy   string // the person's name
	HeardByMe bool
}

// BoxPatch changes a box; nil leaves a field.
type BoxPatch struct {
	Enabled, Email *bool
	// UseOwn picks a greeting kind's recording (true) or Linx's own.
	UseOwn map[string]bool
}

// Usage is the space voicemail uses.
type Usage struct {
	Count int
	Bytes int64
}

// ServiceStore is the database access listening needs (internal/store).
type ServiceStore interface {
	VoicemailBoxes(ctx context.Context, tenant, user uuid.UUID) ([]BoxView, error)
	VoicemailList(ctx context.Context, tenant uuid.UUID, boxes []uuid.UUID, user uuid.UUID, limit int) ([]Listed, error)
	// VoicemailMessage returns one message with its audio; VoicemailInfo
	// without it.
	VoicemailMessage(ctx context.Context, tenant, id uuid.UUID) (Message, error)
	VoicemailInfo(ctx context.Context, tenant, id uuid.UUID) (Message, error)
	MarkVoicemail(ctx context.Context, tenant, id uuid.UUID, heardBy *uuid.UUID, at time.Time) error
	DeleteVoicemail(ctx context.Context, tenant, id uuid.UUID, audit auth.AuditEntry) error
	VoicemailGreetings(ctx context.Context, box uuid.UUID) (map[string]GreetingInfo, error)
	VoicemailEmailTo(ctx context.Context, box Box) ([]string, error)
	UpdateVoicemailBox(ctx context.Context, tenant, id uuid.UUID, p BoxPatch, audit auth.AuditEntry) error
	SetGreeting(ctx context.Context, tenant, box uuid.UUID, kind string, audio []byte, at time.Time, by uuid.UUID, audit auth.AuditEntry) error
	DeleteGreeting(ctx context.Context, tenant, box uuid.UUID, kind string, audit auth.AuditEntry) error
	GreetingAudio(ctx context.Context, box uuid.UUID, kind string) ([]byte, error)
	VoicemailKeepDays(ctx context.Context) (int, error)
	SetVoicemailKeepDays(ctx context.Context, days int, audit auth.AuditEntry) error
	VoicemailUsage(ctx context.Context, tenant uuid.UUID) (Usage, error)
	// Audit records an admin listening to someone else's message.
	Audit(ctx context.Context, e auth.AuditEntry) error
	// ExpireVoicemail deletes messages older than the kept days, returning
	// the tenants that lost any.
	ExpireVoicemail(ctx context.Context, now time.Time) ([]uuid.UUID, error)
}

// Service is voicemail for the API.
type Service struct {
	Store ServiceStore
	// EmailOn reports whether email is set up (nil: never).
	EmailOn func(ctx context.Context, tenant uuid.UUID) (bool, error)
	// Greetings keeps Asterisk's folder current (nil in tests).
	Greetings *Greetings
	// TimeZone is the server's, for downloads' names (nil: UTC).
	TimeZone func(ctx context.Context) (string, error)
	// Changed is told when a tenant's messages change (the badge); may be
	// nil.
	Changed func(tenant uuid.UUID)
	Now     func() time.Time
	Log     *slog.Logger
}

// Errors the API turns into answers.
var (
	errNoBox = &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There's no such voicemail box, or it isn't yours."}
	errNoMsg = &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "That voicemail is gone, or it isn't yours."}
	errOnOff = &apihttp.Error{Status: http.StatusForbidden, Code: "forbidden",
		Detail: "Only the box's person or an admin can turn it on or off, or change its emails."}
	errGroupEmail = &apihttp.Error{Status: http.StatusBadRequest, Code: "email_not_for_groups",
		Detail: "A ring group's messages aren't emailed: nobody owns its address. Its members see them in Voicemail."}
	errEmailOff = &apihttp.Error{Status: http.StatusConflict, Code: "email_not_set_up",
		Detail: "Set up email first (System → Settings)."}
	errNoRecording = &apihttp.Error{Status: http.StatusConflict, Code: "greeting_not_recorded",
		Detail: "Record your own greeting first."}
	errKeepDays = &apihttp.Error{Status: http.StatusBadRequest, Code: "keep_days_invalid",
		Detail: "Keep voicemail between 7 and 365 days."}
	errKind = &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "A greeting is unavailable or closed."}
)

func (s *Service) changed(tenant uuid.UUID) {
	if s.Changed != nil {
		s.Changed(tenant)
	}
}

// Boxes lists the boxes v may see: their own and their groups', and for
// an admin every box when all is true.
func (s *Service) Boxes(ctx context.Context, v Viewer, all bool) ([]BoxView, error) {
	boxes, err := s.Store.VoicemailBoxes(ctx, v.Tenant, v.User)
	if err != nil {
		return nil, err
	}
	out := boxes[:0]
	for _, b := range boxes {
		if b.Mine || b.Member || (all && v.Admin) {
			out = append(out, b)
		}
	}
	return out, nil
}

// box is one box v may see.
func (s *Service) box(ctx context.Context, v Viewer, id uuid.UUID) (BoxView, error) {
	boxes, err := s.Boxes(ctx, v, true)
	if err != nil {
		return BoxView{}, err
	}
	for _, b := range boxes {
		if b.ID == id {
			return b, nil
		}
	}
	return BoxView{}, errNoBox
}

// NewCount is how many messages v hasn't heard in their own and their
// groups' boxes (the sidebar badge).
func (s *Service) NewCount(ctx context.Context, v Viewer) (int, error) {
	boxes, err := s.Boxes(ctx, v, false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, b := range boxes {
		n += b.New
	}
	return n, nil
}

// List is the messages in box (one box), or in all of v's own and group
// boxes (box nil), or every box (all, admins).
func (s *Service) List(ctx context.Context, v Viewer, box *uuid.UUID, all bool) ([]BoxView, []Listed, error) {
	boxes, err := s.Boxes(ctx, v, all || box != nil)
	if err != nil {
		return nil, nil, err
	}
	var ids []uuid.UUID
	for _, b := range boxes {
		if box == nil || b.ID == *box {
			ids = append(ids, b.ID)
		}
	}
	if box != nil && len(ids) == 0 {
		return nil, nil, errNoBox
	}
	if len(ids) == 0 {
		return boxes, []Listed{}, nil
	}
	items, err := s.Store.VoicemailList(ctx, v.Tenant, ids, v.User, MaxList)
	return boxes, items, err
}

// message is message id, if v may hear it (with its audio when audio).
func (s *Service) message(ctx context.Context, v Viewer, id uuid.UUID, audio bool) (Message, BoxView, error) {
	get := s.Store.VoicemailInfo
	if audio {
		get = s.Store.VoicemailMessage
	}
	m, err := get(ctx, v.Tenant, id)
	if errors.Is(err, ErrNotFound) {
		return m, BoxView{}, errNoMsg
	}
	if err != nil {
		return m, BoxView{}, err
	}
	b, err := s.box(ctx, v, m.BoxID)
	if errors.Is(err, errNoBox) {
		return m, b, errNoMsg
	}
	return m, b, err
}

// Audio is message id with its audio, for v to play or download. An
// admin hearing a box that isn't theirs or their group's is written in
// the activity log (owner, 2026-10-02); if that can't be written, the
// audio isn't given.
func (s *Service) Audio(ctx context.Context, v Viewer, id uuid.UUID, audit auth.AuditEntry) (Message, error) {
	m, b, err := s.message(ctx, v, id, true)
	if err != nil || b.Mine || b.Member {
		return m, err
	}
	audit.Action, audit.Target = "voicemail.play", id.String()
	audit.Detail = map[string]any{"box": b.ID, "name": b.Owner, "received_at": m.ReceivedAt}
	if err := s.Store.Audit(ctx, audit); err != nil {
		return Message{}, err
	}
	return m, nil
}

// FileName is m's name as a download: when it was left, in the server's
// time zone ("voicemail-2026-10-02-1042.wav").
func (s *Service) FileName(ctx context.Context, m Message) string {
	zone := "UTC"
	if s.TimeZone != nil {
		if z, err := s.TimeZone(ctx); err == nil {
			zone = z
		}
	}
	return File(Message{ReceivedAt: m.ReceivedAt}, zone).Name
}

// Mark marks message id heard (by v) or new again.
func (s *Service) Mark(ctx context.Context, v Viewer, id uuid.UUID, heard bool) error {
	if _, _, err := s.message(ctx, v, id, false); err != nil {
		return err
	}
	var by *uuid.UUID
	if heard {
		by = &v.User
	}
	if err := s.Store.MarkVoicemail(ctx, v.Tenant, id, by, s.Now()); err != nil {
		return err
	}
	s.changed(v.Tenant)
	return nil
}

// Delete deletes message id for everyone who sees its box.
func (s *Service) Delete(ctx context.Context, v Viewer, id uuid.UUID, audit auth.AuditEntry) error {
	m, b, err := s.message(ctx, v, id, false)
	if err != nil {
		return err
	}
	audit.Action, audit.Target = "voicemail.delete", id.String()
	audit.Detail = map[string]any{"box": b.ID, "received_at": m.ReceivedAt}
	if err := s.Store.DeleteVoicemail(ctx, v.Tenant, id, audit); err != nil {
		if errors.Is(err, ErrNotFound) {
			return errNoMsg
		}
		return err
	}
	s.changed(v.Tenant)
	return nil
}

// Settings is box id's settings sheet.
func (s *Service) Settings(ctx context.Context, v Viewer, id uuid.UUID) (BoxSettings, error) {
	b, err := s.box(ctx, v, id)
	if err != nil {
		return BoxSettings{}, err
	}
	out := BoxSettings{BoxView: b, CanSwitch: v.Admin || b.Mine}
	if out.Greetings, err = s.Store.VoicemailGreetings(ctx, id); err != nil {
		return out, err
	}
	if b.ExtensionID != nil {
		to, err := s.Store.VoicemailEmailTo(ctx, b.Box)
		if err != nil {
			return out, err
		}
		if len(to) > 0 {
			out.EmailTo = to[0]
		}
	}
	if s.EmailOn != nil {
		if out.EmailReady, err = s.EmailOn(ctx, v.Tenant); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Update changes box id's switches and which greetings it plays.
func (s *Service) Update(ctx context.Context, v Viewer, id uuid.UUID, p BoxPatch, audit auth.AuditEntry) (BoxSettings, error) {
	cur, err := s.Settings(ctx, v, id)
	if err != nil {
		return cur, err
	}
	if (p.Enabled != nil || p.Email != nil) && !cur.CanSwitch {
		return cur, errOnOff
	}
	if p.Email != nil && *p.Email {
		if cur.RingGroupID != nil {
			return cur, errGroupEmail
		}
		if !cur.EmailReady && !cur.Email {
			return cur, errEmailOff
		}
	}
	for kind, own := range p.UseOwn {
		if !ValidGreetingKind(kind) {
			return cur, errKind
		}
		if own && !cur.Greetings[kind].Recorded {
			return cur, errNoRecording
		}
	}
	audit.Action, audit.Target = "voicemail_box.update", id.String()
	detail := map[string]any{}
	if p.Enabled != nil {
		detail["enabled"] = *p.Enabled
	}
	if p.Email != nil {
		detail["email"] = *p.Email
	}
	for kind, own := range p.UseOwn {
		detail["greeting_"+kind] = map[bool]string{true: "own", false: "linx"}[own]
	}
	audit.Detail = detail
	if err := s.Store.UpdateVoicemailBox(ctx, v.Tenant, id, p, audit); err != nil {
		return cur, err
	}
	if len(p.UseOwn) > 0 {
		s.Greetings.SyncLogged(ctx)
	}
	return s.Settings(ctx, v, id)
}

// SetGreeting keeps a new recording of box id's greeting kind (a WAV from
// the browser) and starts playing it.
func (s *Service) SetGreeting(ctx context.Context, v Viewer, id uuid.UUID, kind string, wav []byte, audit auth.AuditEntry) error {
	if !ValidGreetingKind(kind) {
		return errKind
	}
	if _, err := s.box(ctx, v, id); err != nil {
		return err
	}
	audio, err := GreetingFromWAV(wav)
	if err != nil {
		return &apihttp.Error{Status: http.StatusBadRequest, Code: "greeting_invalid", Detail: upperFirst(err.Error()) + "."}
	}
	audit.Action, audit.Target = "voicemail_greeting.record", id.String()
	audit.Detail = map[string]any{"kind": kind, "seconds": int(GreetingDuration(audio).Seconds())}
	if err := s.Store.SetGreeting(ctx, v.Tenant, id, kind, audio, s.Now(), v.User, audit); err != nil {
		return err
	}
	s.Greetings.SyncLogged(ctx)
	return nil
}

// DeleteGreeting forgets box id's recording of kind: Linx's own plays.
func (s *Service) DeleteGreeting(ctx context.Context, v Viewer, id uuid.UUID, kind string, audit auth.AuditEntry) error {
	if !ValidGreetingKind(kind) {
		return errKind
	}
	if _, err := s.box(ctx, v, id); err != nil {
		return err
	}
	audit.Action, audit.Target = "voicemail_greeting.delete", id.String()
	audit.Detail = map[string]any{"kind": kind}
	if err := s.Store.DeleteGreeting(ctx, v.Tenant, id, kind, audit); err != nil {
		return err
	}
	s.Greetings.SyncLogged(ctx)
	return nil
}

// Greeting is box id's recording of kind as a WAV, for its sheet's play
// button.
func (s *Service) Greeting(ctx context.Context, v Viewer, id uuid.UUID, kind string) ([]byte, error) {
	if !ValidGreetingKind(kind) {
		return nil, errKind
	}
	if _, err := s.box(ctx, v, id); err != nil {
		return nil, err
	}
	audio, err := s.Store.GreetingAudio(ctx, id, kind)
	if errors.Is(err, ErrNotFound) {
		return nil, errNoRecording
	}
	if err != nil {
		return nil, err
	}
	return GreetingWAV(audio), nil
}

// KeepDays is how long messages are kept, with the space they use.
func (s *Service) KeepDays(ctx context.Context, tenant uuid.UUID) (int, Usage, error) {
	days, err := s.Store.VoicemailKeepDays(ctx)
	if err != nil {
		return 0, Usage{}, err
	}
	u, err := s.Store.VoicemailUsage(ctx, tenant)
	return days, u, err
}

// SetKeepDays changes how long messages are kept; older ones go at the
// next clean-up (within the hour).
func (s *Service) SetKeepDays(ctx context.Context, days int, audit auth.AuditEntry) error {
	if days < MinKeepDays || days > MaxKeepDays {
		return errKeepDays
	}
	audit.Action, audit.Target = "voicemail.keep_days", "settings"
	audit.Detail = map[string]any{"days": days}
	return s.Store.SetVoicemailKeepDays(ctx, days, audit)
}

// expireEvery is how often old messages are deleted: a single indexed
// DELETE, so a message lives at most this long past its days.
const expireEvery = time.Hour

// RunExpiry deletes old messages now and every expireEvery until ctx
// ends; it also kicks the greeting folder into shape at start.
func (s *Service) RunExpiry(ctx context.Context) {
	t := time.NewTicker(expireEvery)
	defer t.Stop()
	for {
		tenants, err := s.Store.ExpireVoicemail(ctx, s.Now())
		if err != nil && ctx.Err() == nil {
			s.Log.Error("deleting old voicemail failed; trying again in an hour", "err", err)
		}
		for _, tenant := range tenants {
			s.changed(tenant)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}
