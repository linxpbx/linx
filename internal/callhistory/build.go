// Package callhistory turns Asterisk's call records into call history
// (ADR-070, docs/PHASE1F.md §9): one line per call, in plain words, and
// who each call belongs to.
//
// Asterisk adds a row to asterisk.cdr (migration 0037) for each pair of
// channels in a call: the caller and each phone or line it rang (the "leg"
// rows, with a dstchannel), and the caller alone wherever Linx itself
// answered (a message, voicemail, the echo test). linkedid ties them
// together; the caller's own rows are those whose uniqueid is the
// linkedid. The dialplan adds three things (internal/asteriskconf): what a
// phone dialled (linx_dialled), the number a call from a line was for
// (linx_did), and the place each ringing step was (linx_step, migration
// 0032's text: n:<number>, e:<number>, g:<id>[:<turn>], d:<id>, l:<id>).
package callhistory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/numbering"
)

// Row is one row of asterisk.cdr. Times are UTC; a zero Answered (Asterisk
// writes 1970-01-01) means that pair never answered.
type Row struct {
	ID          int64
	Started     time.Time
	Answered    time.Time
	Ended       time.Time
	CLID        string
	Src         string
	Dst         string
	DContext    string
	Channel     string
	DstChannel  string
	Duration    int
	BillSec     int
	Disposition string
	UniqueID    string
	LinkedID    string
	Dialled     string
	DID         string
	Step        string
}

// Results (call_record.result). Missed: a phone rang and nobody answered
// (for the person who called, the screens say "No answer").
const (
	ResultAnswered     = "answered"
	ResultMissed       = "missed"
	ResultVoicemail    = "voicemail"
	ResultNotAvailable = "not_available"
	ResultNotInUse     = "not_in_use"
	ResultClosed       = "closed"
	ResultNotPermitted = "not_permitted"
	ResultNoLines      = "no_lines"
	ResultLimit        = "limit_reached"
	ResultEchoTest     = "echo_test"
	ResultBusy         = "busy"
	ResultFailed       = "failed"
)

// Directions (call_record.direction), as in call events.
const (
	DirectionInternal = "internal"
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// Step kinds.
const (
	StepRing      = "ring"      // phones rang: a person, or a ring group's members
	StepLine      = "line"      // an outgoing call tried a line
	StepVoicemail = "voicemail" // reached a voicemail box
	StepMessage   = "message"   // Linx played a message and hung up
	StepEcho      = "echo"      // the echo test
)

// Step is one part of a call's way through Linx, kept as call_record.steps.
type Step struct {
	Kind string `json:"kind"`
	// Ring: the ring group (empty for a person), one after another or all
	// at once, the people whose phones rang, how long, who answered. No
	// People: nothing could ring (Target says who).
	Group      string   `json:"group,omitempty"`
	InTurn     bool     `json:"in_turn,omitempty"`
	Target     string   `json:"target,omitempty"`
	People     []string `json:"people,omitempty"`
	Seconds    int      `json:"seconds,omitempty"`
	AnsweredBy string   `json:"answered_by,omitempty"`
	Busy       bool     `json:"busy,omitempty"`
	// Line: its name and what happened ("answered", "no_answer", "busy",
	// "failed": down or full).
	Line    string `json:"line,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	// Voicemail: whose box.
	Box string `json:"box,omitempty"`
	// Message: which (not-available, not-in-use, closed, not-permitted,
	// no-lines, limit).
	Message string `json:"message,omitempty"`
}

// Party is a person a call belongs to (call_record_party).
type Party struct {
	ExtensionID uuid.UUID
	Missed      bool
}

// Call is one call, as call_record keeps it.
type Call struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	LinkedID       string
	Direction      string
	StartedAt      time.Time
	AnsweredAt     *time.Time
	EndedAt        time.Time
	TalkSeconds    int
	Result         string
	RangUnanswered bool

	FromNumber      string
	FromName        string
	FromExtensionID *uuid.UUID
	ToNumber        string
	ToName          string
	ToExtensionID   *uuid.UUID

	TrunkID       *uuid.UUID
	TrunkName     string
	RingGroupID   *uuid.UUID
	RingGroupName string

	AnsweredByExtensionID *uuid.UUID
	AnsweredByName        string

	VoicemailSource  string
	VoicemailBoxName string

	Steps   []Step
	Parties []Party
}

// Person is an extension and its person's name.
type Person struct {
	ExtensionID uuid.UUID
	TenantID    uuid.UUID
	Number      string
	Name        string
}

// Label is "Sara Haddad (101)".
func (p Person) Label() string {
	if p.Name == "" {
		return p.Number
	}
	return fmt.Sprintf("%s (%s)", p.Name, p.Number)
}

// Group is a ring group.
type Group struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
	InTurn   bool
}

// Line is a trunk.
type Line struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
}

// Box is a voicemail box and its owner: a person (ExtensionID set) or a
// ring group.
type Box struct {
	Name        string
	ExtensionID *uuid.UUID
}

// ErrNotFound is what Lookup returns for something that isn't there (any
// more).
var ErrNotFound = errors.New("not found")

// Lookup resolves what the rows name, as things are now: the history is
// made moments after the call, and kept in words from then on.
type Lookup interface {
	DeviceOwner(ctx context.Context, endpoint string) (Person, error)
	ExtensionByNumber(ctx context.Context, tenant uuid.UUID, number string) (Person, error)
	RingGroup(ctx context.Context, id uuid.UUID) (Group, error)
	RingGroupByNumber(ctx context.Context, tenant uuid.UUID, number string) (Group, error)
	Trunk(ctx context.Context, id uuid.UUID) (Line, error)
	VoicemailBox(ctx context.Context, id uuid.UUID) (Box, error)
	Country(ctx context.Context) (string, error)
	DefaultTenant(ctx context.Context) (uuid.UUID, error)
}

const trunkPrefix = "trunk-"

// endpoint is the PJSIP endpoint in a channel name: "PJSIP/d_x-0000002a"
// is d_x, "PJSIP/trunk-<uuid>-00000003" is trunk-<uuid>.
func endpoint(channel string) string {
	rest, ok := strings.CutPrefix(channel, "PJSIP/")
	if !ok {
		return ""
	}
	if i := strings.LastIndexByte(rest, '-'); i > 0 {
		return rest[:i]
	}
	return rest
}

func trunkOf(endpoint string) (uuid.UUID, bool) {
	rest, ok := strings.CutPrefix(endpoint, trunkPrefix)
	if !ok {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(rest)
	return id, err == nil
}

// callerName is the name in a CLID ("\"Evil Caller\" <+97150…>"), as the
// dialplan cleaned it.
func callerName(clid string) string {
	if !strings.HasPrefix(clid, `"`) {
		return ""
	}
	name, _, ok := strings.Cut(clid[1:], `"`)
	if !ok {
		return ""
	}
	return name
}

func zeroTime(t time.Time) bool { return t.IsZero() || t.Year() < 2000 }

// messageResult maps linx-messages' extensions to results.
var messageResult = map[string]string{
	"not-available": ResultNotAvailable,
	"not-in-use":    ResultNotInUse,
	"closed":        ResultClosed,
	"not-permitted": ResultNotPermitted,
	"no-lines":      ResultNoLines,
	"limit":         ResultLimit,
}

// step text's kinds.
func splitStep(s string) (kind, arg, turn string) {
	parts := strings.SplitN(s, ":", 3)
	kind = parts[0]
	if len(parts) > 1 {
		arg = parts[1]
	}
	if len(parts) > 2 {
		turn = parts[2]
	}
	return
}

// rung is a person whose phones rang in a step, from when.
type rung struct {
	at     time.Time
	number string
	label  string
}

// ringStep is a step being put together from its leg rows.
type ringStep struct {
	key     string
	step    Step
	target  *Person
	first   time.Time
	last    time.Time
	people  map[uuid.UUID]bool
	rung    []rung
	allBusy bool
}

// Build makes one call from its rows (any order). It never fails for
// something missing (a removed person or line reads "a removed …");
// only the lookups' own errors stop it.
func Build(ctx context.Context, lk Lookup, rows []Row) (Call, error) {
	if len(rows) == 0 {
		return Call{}, errors.New("no rows")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	c := Call{LinkedID: rows[0].LinkedID, Direction: DirectionInternal, StartedAt: rows[0].Started, EndedAt: rows[0].Ended}
	var own []Row // the caller's rows
	for _, r := range rows {
		if r.Started.Before(c.StartedAt) {
			c.StartedAt = r.Started
		}
		if r.Ended.After(c.EndedAt) {
			c.EndedAt = r.Ended
		}
		if r.UniqueID == r.LinkedID {
			own = append(own, r)
		}
	}
	if len(own) == 0 {
		own = rows
	}
	home, err := lk.Country(ctx)
	if err != nil {
		return c, err
	}
	if home == "" {
		home = numbering.DefaultCountry
	}
	e164 := func(n string) string {
		if r := numbering.Classify(home, n); r.E164 != "" {
			return r.E164
		}
		return n
	}

	parties := map[uuid.UUID]bool{} // extension → missed
	addParty := func(id uuid.UUID, missed bool) {
		if was, ok := parties[id]; ok {
			missed = was && missed
		}
		parties[id] = missed
	}

	// Who called.
	first := own[0]
	var caller *Person
	if id, ok := trunkOf(endpoint(first.Channel)); ok {
		c.Direction = DirectionInbound
		c.TrunkID = &id
		line, err := lk.Trunk(ctx, id)
		switch {
		case err == nil:
			c.TenantID, c.TrunkName = line.TenantID, line.Name
		case errors.Is(err, ErrNotFound):
			c.TrunkName = "a removed line"
		default:
			return c, err
		}
		c.FromNumber = e164(first.Src)
		if name := callerName(first.CLID); name != "" && name != first.Src {
			c.FromName = name
		}
		for _, r := range own {
			if r.DID != "" {
				c.ToNumber = e164(r.DID)
				break
			}
		}
	} else {
		p, err := lk.DeviceOwner(ctx, endpoint(first.Channel))
		switch {
		case err == nil:
			caller = &p
			c.TenantID = p.TenantID
			c.FromExtensionID, c.FromNumber, c.FromName = &p.ExtensionID, p.Number, p.Name
		case errors.Is(err, ErrNotFound):
			c.FromNumber = first.Src
			c.FromName = callerName(first.CLID)
		default:
			return c, err
		}
	}
	if c.TenantID == uuid.Nil {
		if c.TenantID, err = lk.DefaultTenant(ctx); err != nil {
			return c, err
		}
	}

	dialled := ""
	for _, r := range own {
		if r.Dialled != "" {
			dialled = r.Dialled
			break
		}
	}
	if dialled == "" && c.Direction != DirectionInbound {
		dialled = first.Dst // the echo test
	}

	// The legs, in order: ring steps and lines.
	var steps []*ringStep
	var lines []Step
	answered := false
	rang := false
	var answer Row
	stepRows := map[string]bool{}
	for _, r := range own {
		if r.DstChannel == "" {
			continue
		}
		ep := endpoint(r.DstChannel)
		if id, ok := trunkOf(ep); ok {
			c.Direction = DirectionOutbound
			c.TrunkID = &id
			name := "a removed line"
			if line, err := lk.Trunk(ctx, id); err == nil {
				name = line.Name
			} else if !errors.Is(err, ErrNotFound) {
				return c, err
			}
			c.TrunkName = name
			outcome := "failed"
			switch r.Disposition {
			case "ANSWERED":
				outcome = "answered"
			case "NO ANSWER":
				outcome = "no_answer"
			case "BUSY":
				outcome = "busy"
			}
			lines = append(lines, Step{Kind: StepLine, Line: name, Outcome: outcome})
			if r.Disposition == "ANSWERED" && !answered {
				answered, answer = true, r
			}
			continue
		}
		rang = true
		stepRows[r.Step] = true
		key, err := stepKey(ctx, lk, c.TenantID, r.Step)
		if err != nil {
			return c, err
		}
		var cur *ringStep
		if n := len(steps); n > 0 && steps[n-1].key == key {
			cur = steps[n-1]
		} else {
			cur, err = newRingStep(ctx, lk, c.TenantID, key)
			if err != nil {
				return c, err
			}
			cur.first = r.Started
			steps = append(steps, cur)
		}
		if r.Ended.After(cur.last) {
			cur.last = r.Ended
		}
		cur.allBusy = cur.allBusy && r.Disposition == "BUSY"
		p, err := lk.DeviceOwner(ctx, ep)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return c, err
		}
		if err == nil && !cur.people[p.ExtensionID] {
			cur.people[p.ExtensionID] = true
			cur.rung = append(cur.rung, rung{at: r.Started, number: p.Number, label: p.Label()})
			addParty(p.ExtensionID, true)
		}
		if r.Disposition == "ANSWERED" && !answered {
			answered, answer = true, r
			if err == nil {
				id := p.ExtensionID
				c.AnsweredByExtensionID, c.AnsweredByName = &id, p.Label()
				cur.step.AnsweredBy = p.Label()
			}
		}
	}

	// Where it ended: the caller's last row without a leg.
	var end *Row
	for i := len(own) - 1; i >= 0; i-- {
		if own[i].DstChannel == "" {
			end = &own[i]
			break
		}
	}
	// A step the call reached where nothing could ring (no phone
	// connected) only shows in that row's linx_step.
	if end != nil && end.Step != "" && !stepRows[end.Step] {
		key, err := stepKey(ctx, lk, c.TenantID, end.Step)
		if err != nil {
			return c, err
		}
		if n := len(steps); n == 0 || steps[n-1].key != key {
			s, err := newRingStep(ctx, lk, c.TenantID, key)
			if err != nil {
				return c, err
			}
			if s.target != nil || s.step.Group != "" {
				s.allBusy = false
				steps = append(steps, s)
			}
		}
	}

	for _, s := range steps {
		if s.target != nil {
			addParty(s.target.ExtensionID, true)
		}
		// In the order they rang; those rung together by number (Asterisk
		// writes their rows in no set order).
		sort.SliceStable(s.rung, func(i, j int) bool {
			if !s.rung[i].at.Equal(s.rung[j].at) {
				return s.rung[i].at.Before(s.rung[j].at)
			}
			return s.rung[i].number < s.rung[j].number
		})
		for _, r := range s.rung {
			s.step.People = append(s.step.People, r.label)
		}
		if len(s.step.People) > 0 {
			s.step.Seconds = int(s.last.Sub(s.first).Round(time.Second) / time.Second)
			s.step.Busy = s.allBusy && s.step.AnsweredBy == ""
		}
		c.Steps = append(c.Steps, s.step)
		if c.RingGroupID == nil && s.step.Group != "" {
			if id, err := uuid.Parse(strings.TrimPrefix(s.key, "g:")); err == nil {
				c.RingGroupID, c.RingGroupName = &id, s.step.Group
			}
		}
	}
	// The dialplan tries the next line only when one is down or full, so
	// every line before the last tried failed, whatever Asterisk wrote
	// (a line that times out is "NO ANSWER" in its records).
	// Ending on "no lines" means the last one failed too.
	tried := len(lines) - 1
	if end != nil && end.DContext == "linx-messages" && end.Dst == "no-lines" {
		tried = len(lines)
	}
	for i := 0; i < tried; i++ {
		lines[i].Outcome = "failed"
	}
	c.Steps = append(c.Steps, lines...)

	// The result.
	switch {
	case answered:
		c.Result = ResultAnswered
		at := answer.Answered
		if zeroTime(at) {
			at = answer.Started
		}
		c.AnsweredAt = &at
		c.TalkSeconds = answer.BillSec
		if c.Direction == DirectionOutbound && c.AnsweredByName == "" {
			c.AnsweredByName = c.ToNumber
		}
	case end != nil && end.DContext == "linx-voicemail":
		c.Result = ResultVoicemail
		c.VoicemailSource = end.UniqueID
		box := Step{Kind: StepVoicemail, Box: "a removed voicemail box"}
		if id, err := uuid.Parse(end.Dst); err == nil {
			b, err := lk.VoicemailBox(ctx, id)
			switch {
			case err == nil:
				box.Box = b.Name
				if b.ExtensionID != nil {
					addParty(*b.ExtensionID, true)
				}
			case !errors.Is(err, ErrNotFound):
				return c, err
			}
		}
		c.VoicemailBoxName = box.Box
		c.Steps = append(c.Steps, box)
	case end != nil && end.DContext == "linx-messages":
		c.Result = messageResult[end.Dst]
		if c.Result == "" {
			c.Result = ResultNotAvailable
		}
		c.Steps = append(c.Steps, Step{Kind: StepMessage, Message: end.Dst})
		if c.Result == ResultNotPermitted || c.Result == ResultNoLines || c.Result == ResultLimit {
			c.Direction = DirectionOutbound
		}
	case end != nil && end.Dst == "*43":
		c.Result = ResultEchoTest
		c.Steps = append(c.Steps, Step{Kind: StepEcho})
	case len(lines) > 0 && !rang:
		c.Result = ResultMissed
		switch lines[len(lines)-1].Outcome {
		case "busy":
			c.Result = ResultBusy
		case "failed":
			c.Result = ResultFailed
		}
	default:
		c.Result = ResultMissed
	}
	if rang && !answered {
		c.RangUnanswered = true
		if c.Result != ResultVoicemail {
			c.Result = ResultMissed
			if allBusy(steps) {
				c.Result = ResultBusy
			}
		}
	}
	for _, r := range own {
		if r.DContext == "linx-outbound" {
			c.Direction = DirectionOutbound
		}
	}

	// What was called.
	switch c.Direction {
	case DirectionOutbound:
		if dialled != "" {
			c.ToNumber = e164(dialled)
			if c.Result == ResultAnswered && c.AnsweredByName == "" {
				c.AnsweredByName = c.ToNumber
			}
		}
	case DirectionInternal:
		c.ToNumber = dialled
		if dialled != "" && dialled != "*43" {
			if p, err := lk.ExtensionByNumber(ctx, c.TenantID, dialled); err == nil {
				id := p.ExtensionID
				c.ToExtensionID, c.ToName = &id, p.Name
				if p.ExtensionID != c.ptrFrom() {
					addParty(p.ExtensionID, !answered)
				}
			} else if !errors.Is(err, ErrNotFound) {
				return c, err
			} else if g, err := lk.RingGroupByNumber(ctx, c.TenantID, dialled); err == nil {
				c.ToName = g.Name
			} else if !errors.Is(err, ErrNotFound) {
				return c, err
			}
		}
	}

	// Who it belongs to: the caller never missed their own call, whoever
	// answered didn't, and nobody did once someone answered.
	if c.AnsweredByExtensionID != nil {
		parties[*c.AnsweredByExtensionID] = false
	}
	if caller != nil {
		parties[caller.ExtensionID] = false
	}
	for id, missed := range parties {
		c.Parties = append(c.Parties, Party{ExtensionID: id, Missed: missed && !answered})
	}
	sort.Slice(c.Parties, func(i, j int) bool { return c.Parties[i].ExtensionID.String() < c.Parties[j].ExtensionID.String() })
	return c, nil
}

func (c Call) ptrFrom() uuid.UUID {
	if c.FromExtensionID == nil {
		return uuid.Nil
	}
	return *c.FromExtensionID
}

func allBusy(steps []*ringStep) bool {
	some := false
	for _, s := range steps {
		if len(s.step.People) == 0 {
			continue
		}
		if !s.allBusy {
			return false
		}
		some = true
	}
	return some
}

// stepKey is what makes consecutive rows one step: a ring group's id
// (one after another rings it turn by turn), else the step text.
func stepKey(ctx context.Context, lk Lookup, tenant uuid.UUID, step string) (string, error) {
	kind, arg, _ := splitStep(step)
	switch kind {
	case "g":
		return "g:" + arg, nil
	case "n":
		g, err := lk.RingGroupByNumber(ctx, tenant, arg)
		if err == nil {
			return "g:" + g.ID.String(), nil
		}
		if !errors.Is(err, ErrNotFound) {
			return "", err
		}
		return "e:" + arg, nil
	}
	return step, nil
}

func newRingStep(ctx context.Context, lk Lookup, tenant uuid.UUID, key string) (*ringStep, error) {
	s := &ringStep{key: key, step: Step{Kind: StepRing}, people: map[uuid.UUID]bool{}, allBusy: true}
	kind, arg, _ := splitStep(key)
	switch kind {
	case "g":
		id, err := uuid.Parse(arg)
		if err != nil {
			return s, nil
		}
		g, err := lk.RingGroup(ctx, id)
		switch {
		case err == nil:
			s.step.Group, s.step.InTurn = g.Name, g.InTurn
		case errors.Is(err, ErrNotFound):
			s.step.Group = "a removed ring group"
		default:
			return nil, err
		}
	case "e":
		p, err := lk.ExtensionByNumber(ctx, tenant, arg)
		switch {
		case err == nil:
			s.target = &p
			s.step.Target = p.Label()
		case !errors.Is(err, ErrNotFound):
			return nil, err
		}
	}
	// d:<id> and l:<id> (a number's or a line's own "ring this person",
	// migration 0033): whose phones rang says who.
	return s, nil
}
