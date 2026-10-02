package routing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// Undo for call routing (ADR-071, docs/PHASE1F.md §9): every routing
// change keeps the routing as it was just before it (internal/store,
// migration 0038), the last 50. "Saved. Undo" and System → Routing
// changes put one back, as a new change, so that can be undone too.
// What a version says is shown with the same sentences the screens use.

// Item is one thing routing is made of, in words: a ring group, a
// schedule, a number or line, the outgoing lines, a calling level.
type Item struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Words string `json:"words"`
}

// Item kinds (besides IncomingNumber and IncomingLine).
const (
	ItemRingGroup = "ring_group"
	ItemSchedule  = "schedule"
	ItemOutgoing  = "outgoing"
	ItemLevel     = "calling_level"
)

// Change is one kept routing change.
type Change struct {
	ID        uuid.UUID
	At        time.Time
	Actor     string
	ActorName string // a person's name; "" for a key, an app or the server
	Action    string
	Target    string
	// A put-back: which change's version, and when that change was.
	PutBackOf *uuid.UUID
	PutBackAt *time.Time
	// The routing just before and after it, in words; nil when the
	// server couldn't say.
	BeforeWords, AfterWords []Item
}

// Outgoing is the outgoing part of routing.
type Outgoing struct {
	Lines  []string // the lines outgoing calls try, in order
	Levels []Level
}

// Level is what one calling level allows.
type Level struct {
	ID               uuid.UUID
	Name             string
	Categories       []string
	WithholdCallerID bool
}

// StateStore reads the routing (as it is, or a version of it).
type StateStore interface {
	Store
	RulesStore
	OutgoingRouting(ctx context.Context, tenant uuid.UUID) (Outgoing, error)
}

// ChangeStore keeps routing changes (internal/store).
type ChangeStore interface {
	StateStore
	// RoutingChanges returns every kept change, newest first.
	RoutingChanges(ctx context.Context, tenant uuid.UUID) ([]Change, error)
	// WithRoutingBefore runs fn on the routing as it was just before
	// change id, as it would be put back now; nothing is kept.
	WithRoutingBefore(ctx context.Context, tenant, id uuid.UUID, fn func(StateStore) error) error
	// PutRoutingBack puts it back as a new change; with ifLatest only
	// while id is the newest change (ErrVersionChanged).
	PutRoutingBack(ctx context.Context, tenant, id uuid.UUID, ifLatest bool, audit auth.AuditEntry) (uuid.UUID, error)
}

// PutBackError is a version that can't be put back as things are now.
type PutBackError struct{ Detail string }

func (e *PutBackError) Error() string { return "routing version can't be put back: " + e.Detail }

// --- Telling the request which change it made ------------------------------

type noteKey struct{}

type note struct{ id uuid.UUID }

// NoteChange tells the request (ChangeHeader) that it made change id.
func NoteChange(ctx context.Context, id uuid.UUID) {
	if n, ok := ctx.Value(noteKey{}).(*note); ok {
		n.id = id
	}
}

// ChangeHeaderName carries the routing change a request made, for "Saved.
// Undo".
const ChangeHeaderName = "Linx-Routing-Change"

// ChangeHeader adds ChangeHeaderName to a successful response whose
// request changed routing.
func ChangeHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := &note{}
		next.ServeHTTP(&noteWriter{ResponseWriter: w, note: n}, r.WithContext(context.WithValue(r.Context(), noteKey{}, n)))
	})
}

type noteWriter struct {
	http.ResponseWriter
	note  *note
	wrote bool
}

func (w *noteWriter) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		if status < 300 && w.note.id != uuid.Nil {
			w.Header().Set(ChangeHeaderName, w.note.id.String())
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *noteWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func (w *noteWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// --- The routing in words -------------------------------------------------

func (s *Service) changes() error {
	if s.Changes == nil {
		return errors.New("routing changes aren't set up")
	}
	return nil
}

// Words is the routing st reads for tenant, in words (internal/store
// keeps it with each change).
func (s *Service) Words(ctx context.Context, st StateStore, tenant uuid.UUID) ([]Item, error) {
	if p, ok := auth.PrincipalFromContext(ctx); !ok || p.TenantID != tenant {
		ctx = auth.WithPrincipal(ctx, auth.SystemPrincipal(tenant))
	}
	items, _, err := s.items(ctx, st)
	return items, err
}

// items is the routing st reads, in words, in the screens' order: ring
// groups, office hours, numbers and lines, outgoing calls.
func (s *Service) items(ctx context.Context, st StateStore) ([]Item, Outgoing, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, Outgoing{}, err
	}
	sv := &Service{Store: st, Rules: st, Now: s.Now}
	groups, err := sv.ListRingGroups(ctx)
	if err != nil {
		return nil, Outgoing{}, err
	}
	schedules, err := sv.ListSchedules(ctx)
	if err != nil {
		return nil, Outgoing{}, err
	}
	incoming, err := sv.ListIncoming(ctx)
	if err != nil {
		return nil, Outgoing{}, err
	}
	out, err := st.OutgoingRouting(ctx, p.TenantID)
	if err != nil {
		return nil, Outgoing{}, err
	}
	items := []Item{}
	for _, g := range groups {
		w := g.Words
		if g.Number != "" {
			w = "Number " + g.Number + ". " + w
		}
		items = append(items, Item{Key: "g:" + g.ID.String(), Kind: ItemRingGroup, Name: g.Name, Words: w})
	}
	for _, sc := range schedules {
		items = append(items, Item{Key: "s:" + sc.ID.String(), Kind: ItemSchedule, Name: sc.Name, Words: scheduleWords(sc.Schedule)})
	}
	for _, in := range incoming {
		key := "d:"
		if in.Kind == IncomingLine {
			key = "l:"
		}
		items = append(items, Item{Key: key + in.ID.String(), Kind: in.Kind, Name: incomingName(in.Incoming), Words: in.Words})
	}
	lines := "Outgoing calls can't go out: no line is used for them."
	if len(out.Lines) > 0 {
		lines = "Outgoing calls go out on " + strings.Join(out.Lines, ", then ") + "."
	}
	items = append(items, Item{Key: "o:lines", Kind: ItemOutgoing, Name: "Outgoing calls", Words: lines})
	for _, l := range out.Levels {
		items = append(items, Item{Key: "c:" + l.ID.String(), Kind: ItemLevel, Name: l.Name, Words: levelWords(l)})
	}
	return items, out, nil
}

// scheduleWords: "Mon–Fri 08:00–17:00. Closed on Eid al-Fitr (2027-03-20
// to 2027-03-22), National Day (12-02, every year)."
func scheduleWords(sc Schedule) string {
	w := "Open " + HoursWords(sc.Spans) + "."
	if HoursWords(sc.Spans) == "Never open" {
		w = "Never open."
	}
	if len(sc.Holidays) == 0 {
		return w + " No holidays."
	}
	days := make([]string, 0, len(sc.Holidays))
	for _, h := range sc.Holidays {
		when := h.FirstDay
		if h.LastDay != h.FirstDay {
			when += " to " + h.LastDay
		}
		if h.EveryYear {
			when += ", every year"
		}
		days = append(days, fmt.Sprintf("%s (%s)", h.Name, when))
	}
	return w + " Closed on " + strings.Join(days, ", ") + "."
}

// categoryWords are the categories as the Outgoing calls screen names
// them (web/src/lib/categories.ts), in its order.
var categoryWords = []struct{ key, words string }{
	{"landline", "local numbers"}, {"service", "service numbers"}, {"mobile", "mobiles"},
	{"national", "other cities"}, {"toll_free", "free numbers"}, {"shared_cost", "shared-cost numbers"},
	{"international", "abroad"}, {"premium", "premium-rate numbers"},
}

func levelWords(l Level) string {
	var can []string
	for _, c := range categoryWords {
		if slices.Contains(l.Categories, c.key) {
			can = append(can, c.words)
		}
	}
	w := "Can call emergency numbers only."
	if len(can) > 0 {
		w = "Can call " + listNames(can, "") + "."
	}
	if l.WithholdCallerID {
		return w + " The number is hidden."
	}
	return w
}

// ItemChange is one item's words before and after ("" where it isn't).
type ItemChange struct {
	Kind, Name    string
	Before, After string
}

// Diff lists what differs between two versions, in after's order, then
// what only before had.
func Diff(before, after []Item) []ItemChange {
	was := map[string]Item{}
	for _, it := range before {
		was[it.Key] = it
	}
	out := []ItemChange{}
	seen := map[string]bool{}
	for _, it := range after {
		seen[it.Key] = true
		b, ok := was[it.Key]
		switch {
		case !ok:
			out = append(out, ItemChange{Kind: it.Kind, Name: it.Name, After: it.Words})
		case b.Words != it.Words || b.Name != it.Name:
			name := it.Name
			if b.Name != it.Name {
				name = b.Name + " → " + it.Name
			}
			out = append(out, ItemChange{Kind: it.Kind, Name: name, Before: b.Words, After: it.Words})
		}
	}
	for _, it := range before {
		if !seen[it.Key] {
			out = append(out, ItemChange{Kind: it.Kind, Name: it.Name, Before: it.Words})
		}
	}
	return out
}

var kindWords = map[string]string{
	ItemRingGroup: "ring group", ItemSchedule: "office hours", IncomingNumber: "number", IncomingLine: "line",
	ItemOutgoing: "outgoing calls", ItemLevel: "calling level",
}

// Summary says a change in a few words: "Changed Sales", "Added the ring
// group Support", "Changed Sales, 042000102 and 2 more".
func Summary(changes []ItemChange) string {
	if len(changes) == 0 {
		return "Nothing changed"
	}
	if len(changes) == 1 {
		c := changes[0]
		switch {
		case c.Before == "":
			return "Added the " + kindWords[c.Kind] + " " + c.Name
		case c.After == "":
			return "Removed the " + kindWords[c.Kind] + " " + c.Name
		}
		return "Changed " + c.Name
	}
	names := make([]string, 0, len(changes))
	for _, c := range changes {
		names = append(names, c.Name)
	}
	return "Changed " + listNames(names, "")
}

// --- Routing changes ------------------------------------------------------

// ChangeView is one change as System → Routing changes shows it.
type ChangeView struct {
	Change
	Summary string
	// Changes: what it changed; nil when it wasn't kept in words.
	Changes []ItemChange
}

// ListChanges returns the kept changes, newest first, each with what it
// changed; a change that changed nothing is left out.
func (s *Service) ListChanges(ctx context.Context) ([]ChangeView, error) {
	if err := s.changes(); err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.Changes.RoutingChanges(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	out := []ChangeView{}
	for _, c := range list {
		v := ChangeView{Change: c}
		if c.BeforeWords == nil || c.AfterWords == nil {
			v.Summary = "Changed the routing"
			out = append(out, v)
			continue
		}
		v.Changes = Diff(c.BeforeWords, c.AfterWords)
		if len(v.Changes) == 0 {
			continue
		}
		v.Summary = Summary(v.Changes)
		out = append(out, v)
	}
	return out, nil
}

// PutBackPreview is what putting change id's version back would change
// now, and whether it needs a fresh "confirm it's you".
type PutBackPreview struct {
	Changes     []ItemChange
	NeedConfirm bool
	// Outgoing: it changes outgoing calls (needs trunks:write too).
	Outgoing bool
}

// costly are the calling categories where phone fraud costs money:
// turning one on needs "confirm it's you" (internal/trunk).
var costly = []string{"international", "premium"}

// preview compares the routing now with change id's version.
func (s *Service) preview(ctx context.Context, tenant, id uuid.UUID) (PutBackPreview, error) {
	now, nowOut, err := s.items(ctx, s.Changes)
	if err != nil {
		return PutBackPreview{}, err
	}
	var then []Item
	var thenOut Outgoing
	if err := s.Changes.WithRoutingBefore(ctx, tenant, id, func(st StateStore) error {
		var err error
		then, thenOut, err = s.items(ctx, st)
		return err
	}); err != nil {
		if errors.Is(err, ErrNotFound) {
			return PutBackPreview{}, changeNotFound()
		}
		return PutBackPreview{}, err
	}
	pv := PutBackPreview{Changes: Diff(now, then)}
	for _, c := range pv.Changes {
		if c.Kind == ItemOutgoing || c.Kind == ItemLevel {
			pv.Outgoing = true
		}
	}
	allowed := map[uuid.UUID][]string{}
	for _, l := range nowOut.Levels {
		allowed[l.ID] = l.Categories
	}
	for _, l := range thenOut.Levels {
		for _, c := range costly {
			if slices.Contains(l.Categories, c) && !slices.Contains(allowed[l.ID], c) {
				pv.NeedConfirm = true
			}
		}
	}
	return pv, nil
}

func changeNotFound() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found",
		Detail: "That routing change isn't kept any more (only the last 50 are)."}
}

// PreviewPutBack says what putting change id's version back would change.
func (s *Service) PreviewPutBack(ctx context.Context, id uuid.UUID) (PutBackPreview, error) {
	if err := s.changes(); err != nil {
		return PutBackPreview{}, err
	}
	p, err := principal(ctx)
	if err != nil {
		return PutBackPreview{}, err
	}
	pv, err := s.preview(ctx, p.TenantID, id)
	return pv, putBackAPIError(err)
}

// PutBack puts the routing back as it was just before change id, as a
// new change it returns. undo ("Saved. Undo") only while id is still the
// newest change, so it never undoes someone else's later change too.
// Turning on calls abroad or premium numbers needs "confirm it's you",
// and changing outgoing calls trunks:write, as when changed directly.
func (s *Service) PutBack(ctx context.Context, id uuid.UUID, undo bool) (uuid.UUID, error) {
	if err := s.changes(); err != nil {
		return uuid.Nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	pv, err := s.preview(ctx, p.TenantID, id)
	if err != nil {
		return uuid.Nil, putBackAPIError(err)
	}
	if pv.Outgoing && !p.Has("trunks:write") {
		return uuid.Nil, &apihttp.Error{Status: http.StatusForbidden, Code: "insufficient_scope",
			Detail: "This version changes outgoing calls, which needs the trunks:write permission too."}
	}
	if pv.NeedConfirm {
		if err := auth.RequireConfirmed(ctx, s.Now()); err != nil {
			return uuid.Nil, err
		}
	}
	action := "routing.put_back"
	if undo {
		action = "routing.undo"
	}
	_, a, err := audit(ctx, action, "routing_change:"+id.String())
	if err != nil {
		return uuid.Nil, err
	}
	change, err := s.Changes.PutRoutingBack(ctx, p.TenantID, id, undo, a)
	return change, putBackAPIError(err)
}

func putBackAPIError(err error) error {
	var pb *PutBackError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return changeNotFound()
	case errors.Is(err, ErrVersionChanged):
		return &apihttp.Error{Status: http.StatusConflict, Code: "routing_changed",
			Detail: "Routing was changed again since. See System → Routing changes to put back the version you want."}
	case errors.As(err, &pb):
		return &apihttp.Error{Status: http.StatusConflict, Code: "cannot_put_back", Detail: pb.Detail}
	}
	return err
}
