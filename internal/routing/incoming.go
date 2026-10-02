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
)

// "When someone calls" (docs/PHASE1F.md §7, migration 0033): for each phone
// number, and each line's calls for none of its numbers, who rings during
// office hours, what happens if nobody answers, and what happens outside
// office hours and on holidays. And the call simulator, which walks the
// same database function real calls use.

var errIncomingChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This number was changed since you read it. Fetch it again and retry."}

func incomingNotFound() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no phone number or line with that id."}
}

// DefaultNoAnswerSeconds is how long a person rings before "if nobody
// answers", in a rule; without one an extension rings 30 seconds (the
// dialplan's own default).
const (
	DefaultNoAnswerSeconds = 25
	plainExtensionSeconds  = 30
)

// IncomingView is one number or line as the Incoming screen shows it.
type IncomingView struct {
	Incoming
	RingsLabel, NoAnswerLabel, ClosedLabel, HolidayLabel string
	Words                                                string
}

func incomingName(in Incoming) string {
	if in.Kind == IncomingLine {
		return "Other calls on " + in.LineName
	}
	return in.Number
}

// incomingUses lists the numbers and lines that send calls to group.
func incomingUses(list []Incoming, group uuid.UUID) []UsedBy {
	var out []UsedBy
	is := func(d *Destination) bool {
		return d != nil && d.Kind == KindRingGroup && d.RingGroupID != nil && *d.RingGroupID == group
	}
	for _, in := range list {
		add := func(how string) {
			out = append(out, UsedBy{Kind: in.Kind, ID: in.ID, Name: incomingName(in), How: how})
		}
		if is(in.Rings) {
			add("rings")
		}
		if in.Rule == nil {
			continue
		}
		if in.Rings != nil && in.Rings.Kind == KindExtension && is(&in.Rule.NoAnswer) {
			add("if_nobody_answers")
		}
		if in.Rule.ScheduleID != nil {
			if is(&in.Rule.Closed) {
				add("after_hours")
			}
			if is(in.Rule.Holiday) {
				add("holidays")
			}
		}
	}
	return out
}

type lookups struct {
	groups    map[uuid.UUID]RingGroup
	exts      map[uuid.UUID]ExtensionRef
	schedules map[uuid.UUID]Schedule
}

func (s *Service) lookups(ctx context.Context, tenant uuid.UUID, list []Incoming) (lookups, error) {
	l := lookups{groups: map[uuid.UUID]RingGroup{}, exts: map[uuid.UUID]ExtensionRef{}, schedules: map[uuid.UUID]Schedule{}}
	groups, err := s.Store.RingGroups(ctx, tenant)
	if err != nil {
		return l, err
	}
	var ids []uuid.UUID
	for _, g := range groups {
		l.groups[g.ID] = g
		if g.NoAnswer.ExtensionID != nil {
			ids = append(ids, *g.NoAnswer.ExtensionID)
		}
	}
	for _, in := range list {
		for _, d := range in.destinations() {
			if d.ExtensionID != nil {
				ids = append(ids, *d.ExtensionID)
			}
		}
	}
	if len(ids) > 0 {
		exts, err := s.Store.Extensions(ctx, tenant, ids)
		if err != nil {
			return l, err
		}
		for _, e := range exts {
			l.exts[e.ID] = e
		}
	}
	schedules, err := s.Rules.OfficeHours(ctx, tenant)
	if err != nil {
		return l, err
	}
	for _, sc := range schedules {
		l.schedules[sc.ID] = sc
	}
	return l, nil
}

func (in Incoming) destinations() []*Destination {
	out := []*Destination{in.Rings}
	if in.Rule != nil {
		out = append(out, &in.Rule.NoAnswer, &in.Rule.Closed, in.Rule.Holiday)
	}
	return slices.DeleteFunc(out, func(d *Destination) bool { return d == nil })
}

// ListIncoming returns every number and line with its sentence.
func (s *Service) ListIncoming(ctx context.Context) ([]IncomingView, error) {
	if err := s.rules(); err != nil {
		return nil, err
	}
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.Rules.IncomingList(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	l, err := s.lookups(ctx, p.TenantID, list)
	if err != nil {
		return nil, err
	}
	out := make([]IncomingView, 0, len(list))
	for _, in := range list {
		out = append(out, l.view(in))
	}
	return out, nil
}

func (l lookups) view(in Incoming) IncomingView {
	v := IncomingView{Incoming: in, Words: l.words(in)}
	if in.Rings != nil {
		v.RingsLabel = DestinationLabel(*in.Rings, l.groups, l.exts)
	}
	if in.Rule != nil {
		v.NoAnswerLabel = DestinationLabel(in.Rule.NoAnswer, l.groups, l.exts)
		v.ClosedLabel = DestinationLabel(in.Rule.Closed, l.groups, l.exts)
		if in.Rule.Holiday != nil {
			v.HolidayLabel = DestinationLabel(*in.Rule.Holiday, l.groups, l.exts)
		}
	}
	return v
}

// words is the sentence saying what a caller to in gets (the Incoming
// list, the wizard and, later, Routing changes): "Calls to 042000100 ring
// Sales (all at once) Mon–Fri 08:00–17:00. If nobody answers, the call
// goes to Sara Haddad (101). At other times and on holidays, callers hear
// "We're closed"."
func (l lookups) words(in Incoming) string {
	subject := "Calls to " + in.Number
	if in.Kind == IncomingLine {
		subject = "Calls on " + in.LineName + " for none of its numbers"
	}
	var hours string
	if in.Rule != nil && in.Rule.ScheduleID != nil {
		if sc, ok := l.schedules[*in.Rule.ScheduleID]; ok {
			hours = HoursWords(sc.Spans)
		}
	}
	var b strings.Builder
	who := ""
	var group *RingGroup
	if in.Rings != nil {
		switch in.Rings.Kind {
		case KindExtension:
			if in.Rings.ExtensionID != nil {
				if e, ok := l.exts[*in.Rings.ExtensionID]; ok {
					who = fmt.Sprintf("%s (%s)", e.DisplayName, e.Number)
				}
			}
		case KindRingGroup:
			if in.Rings.RingGroupID != nil {
				if g, ok := l.groups[*in.Rings.RingGroupID]; ok {
					group = &g
					how := "all at once"
					if g.Strategy == StrategyInTurn {
						how = "one after another"
					}
					who = fmt.Sprintf("%s (%s)", g.Name, how)
				}
			}
		}
	}
	switch {
	case who == "" && hours != "":
		fmt.Fprintf(&b, "%s ring nobody %s: callers hear the number isn't in use.", subject, hours)
	case who == "":
		fmt.Fprintf(&b, "%s ring nobody: callers hear the number isn't in use.", subject)
	case hours != "":
		fmt.Fprintf(&b, "%s ring %s %s.", subject, who, hours)
	default:
		fmt.Fprintf(&b, "%s ring %s.", subject, who)
	}
	switch {
	case group != nil:
		fmt.Fprintf(&b, " If nobody answers, %s", destinationWords(group.NoAnswer, l.groups, l.exts))
	case who != "" && in.Rule != nil:
		fmt.Fprintf(&b, " If nobody answers in %d seconds, %s", in.Rule.NoAnswerSeconds, destinationWords(in.Rule.NoAnswer, l.groups, l.exts))
	case who != "":
		fmt.Fprintf(&b, ` If nobody answers in %d seconds, callers hear "not available".`, plainExtensionSeconds)
	}
	if hours != "" {
		closed := destinationWords(in.Rule.Closed, l.groups, l.exts)
		if in.Rule.Holiday == nil {
			fmt.Fprintf(&b, " At other times and on holidays, %s", closed)
		} else {
			fmt.Fprintf(&b, " At other times, %s On holidays, %s", closed, destinationWords(*in.Rule.Holiday, l.groups, l.exts))
		}
	}
	return b.String()
}

// IncomingInput is a whole "When someone calls". JustRing sets only Rings,
// all the time (the quick change); otherwise nil fields take the
// defaults: 25 seconds, then "not available"; no office hours; "We're
// closed" outside them; holidays as outside them.
type IncomingInput struct {
	Rings           *Destination
	JustRing        bool
	NoAnswerSeconds *int
	NoAnswer        *Destination
	ScheduleID      *uuid.UUID
	Closed          *Destination
	Holiday         *Destination
	// Preview checks it and returns it with its sentence, unsaved.
	Preview bool
}

// SetIncoming replaces id's "When someone calls" (id is a number's or a
// line's). ifMatch, when not empty, must match its current ETag.
func (s *Service) SetIncoming(ctx context.Context, id uuid.UUID, in IncomingInput, ifMatch string) (IncomingView, error) {
	if err := s.rules(); err != nil {
		return IncomingView{}, err
	}
	p, err := principal(ctx)
	if err != nil {
		return IncomingView{}, err
	}
	list, err := s.Rules.IncomingList(ctx, p.TenantID)
	if err != nil {
		return IncomingView{}, err
	}
	i := slices.IndexFunc(list, func(x Incoming) bool { return x.ID == id })
	if i < 0 {
		return IncomingView{}, incomingNotFound()
	}
	cur := list[i]
	if ifMatch != "" && !matchETag(ifMatch, cur.Version) {
		return IncomingView{}, errIncomingChanged
	}
	cur.Rings, cur.Rule = in.Rings, nil
	if !in.JustRing {
		r := Rule{NoAnswerSeconds: DefaultNoAnswerSeconds, NoAnswer: Destination{Kind: KindMessage, Message: MessageNotAvailable},
			ScheduleID: in.ScheduleID, Closed: Destination{Kind: KindMessage, Message: MessageClosed}, Holiday: in.Holiday}
		if in.NoAnswerSeconds != nil {
			r.NoAnswerSeconds = *in.NoAnswerSeconds
		}
		if in.NoAnswer != nil {
			r.NoAnswer = *in.NoAnswer
		}
		if in.Closed != nil {
			r.Closed = *in.Closed
		}
		if r.ScheduleID == nil {
			r.Closed, r.Holiday = Destination{Kind: KindMessage, Message: MessageClosed}, nil
		}
		cur.Rule = &r
	}
	l, err := s.lookups(ctx, p.TenantID, nil)
	if err != nil {
		return IncomingView{}, err
	}
	if err := s.checkIncoming(ctx, p.TenantID, &cur, l); err != nil {
		return IncomingView{}, err
	}
	if in.Preview {
		return l.view(cur), nil
	}
	_, a, err := audit(ctx, "incoming.set", cur.Kind+":"+id.String())
	if err != nil {
		return IncomingView{}, err
	}
	a.Detail = map[string]any{"name": incomingName(cur), "words": l.words(cur)}
	if err := s.Rules.SetIncoming(ctx, cur, a); err != nil {
		switch {
		case errors.Is(err, ErrVersionChanged):
			return IncomingView{}, errIncomingChanged
		case errors.Is(err, ErrNotFound):
			return IncomingView{}, invalid("destination_invalid", "That ring group or office hours doesn't exist any more.")
		case errors.Is(err, ErrExtensionNotFound):
			return IncomingView{}, invalid("extension_not_found", "That extension doesn't exist any more.")
		}
		return IncomingView{}, err
	}
	list, err = s.Rules.IncomingList(ctx, p.TenantID)
	if err != nil {
		return IncomingView{}, err
	}
	for _, x := range list {
		if x.ID == id {
			return l.view(x), nil
		}
	}
	return IncomingView{}, incomingNotFound()
}

func (s *Service) checkIncoming(ctx context.Context, tenant uuid.UUID, in *Incoming, l lookups) error {
	var ids []uuid.UUID
	for _, d := range in.destinations() {
		if d.ExtensionID != nil {
			ids = append(ids, *d.ExtensionID)
		}
	}
	if len(ids) > 0 {
		exts, err := s.Store.Extensions(ctx, tenant, ids)
		if err != nil {
			return err
		}
		for _, e := range exts {
			l.exts[e.ID] = e
		}
	}
	check := func(d *Destination, what string) error {
		switch d.Kind {
		case KindExtension:
			if d.ExtensionID == nil {
				return invalid("destination_invalid", "Choose which person "+what+".")
			}
			if _, ok := l.exts[*d.ExtensionID]; !ok {
				return invalid("extension_not_found", "That extension doesn't exist.")
			}
			d.RingGroupID, d.Message = nil, ""
		case KindRingGroup:
			if d.RingGroupID == nil {
				return invalid("destination_invalid", "Choose which ring group "+what+".")
			}
			if _, ok := l.groups[*d.RingGroupID]; !ok {
				return invalid("ring_group_not_found", "That ring group doesn't exist.")
			}
			d.ExtensionID, d.Message = nil, ""
		case KindMessage:
			if d.Message != MessageNotAvailable && d.Message != MessageClosed {
				return invalid("destination_invalid", `The message can be "not-available" or "closed".`)
			}
			d.ExtensionID, d.RingGroupID = nil, nil
		default:
			return invalid("destination_invalid", "Calls can go to a person, a ring group or a message.")
		}
		return nil
	}
	if in.Rings != nil {
		if in.Rings.Kind == KindMessage {
			return invalid("destination_invalid", "Calls ring a person or a ring group. For nobody, leave it empty.")
		}
		if err := check(in.Rings, "it rings"); err != nil {
			return err
		}
	}
	if in.Rule == nil {
		return nil
	}
	r := in.Rule
	if r.NoAnswerSeconds < 5 || r.NoAnswerSeconds > 300 {
		return invalid("no_answer_seconds_invalid", "Ring for 5 to 300 seconds.")
	}
	if err := check(&r.NoAnswer, "gets unanswered calls"); err != nil {
		return err
	}
	if r.ScheduleID != nil {
		if _, ok := l.schedules[*r.ScheduleID]; !ok {
			return invalid("schedule_not_found", "Those office hours don't exist.")
		}
	}
	if err := check(&r.Closed, "gets calls outside office hours"); err != nil {
		return err
	}
	if r.Holiday != nil {
		if err := check(r.Holiday, "gets calls on holidays"); err != nil {
			return err
		}
	}
	return nil
}

// SimStep is one step of a simulated call. Rings: it rings someone, and
// the next step is what happens when nobody answers.
type SimStep struct {
	Words string
	Rings bool
}

// Simulation is where a call goes, step by step.
type Simulation struct {
	// When: for a number with office hours, the moment and whether it's
	// open ("Friday 2 Oct 20:00 (Asia/Dubai time): outside office hours").
	When  string
	Steps []SimStep
}

// maxPlaces is the dialplan's cap: a call stops after 10 places.
const maxPlaces = 10

// SimulateNumber walks a call to one of your phone numbers at at.
func (s *Service) SimulateNumber(ctx context.Context, number string, at time.Time) (Simulation, bool, error) {
	if err := s.rules(); err != nil {
		return Simulation{}, false, err
	}
	p, err := principal(ctx)
	if err != nil {
		return Simulation{}, false, err
	}
	list, err := s.Rules.IncomingList(ctx, p.TenantID)
	if err != nil {
		return Simulation{}, false, err
	}
	i := slices.IndexFunc(list, func(x Incoming) bool { return x.Kind == IncomingNumber && x.Number == number })
	if i < 0 {
		return Simulation{}, false, nil
	}
	in := list[i]
	sim, err := s.simulate(ctx, p.TenantID, "d:"+in.ID.String(), "", at)
	if err != nil {
		return Simulation{}, true, err
	}
	if in.Rule != nil && in.Rule.ScheduleID != nil {
		when, err := s.when(ctx, p.TenantID, *in.Rule.ScheduleID, at)
		if err != nil {
			return Simulation{}, true, err
		}
		sim.When = when
	}
	return sim, true, nil
}

// SimulateInternal walks a call from extension caller to number, if number
// is an extension's or a ring group's (false otherwise).
func (s *Service) SimulateInternal(ctx context.Context, caller, number string) (Simulation, bool, error) {
	if err := s.rules(); err != nil {
		return Simulation{}, false, err
	}
	p, err := principal(ctx)
	if err != nil {
		return Simulation{}, false, err
	}
	at := s.Now()
	first, err := s.Rules.RouteStep(ctx, "n:"+number, caller, at)
	if err != nil || first == nil {
		return Simulation{}, false, err
	}
	if caller == number {
		return Simulation{Steps: []SimStep{{Words: `That's the caller's own number: they hear "not available".`}}}, true, nil
	}
	sim, err := s.simulate(ctx, p.TenantID, "n:"+number, caller, at)
	return sim, true, err
}

func (s *Service) when(ctx context.Context, tenant, schedule uuid.UUID, at time.Time) (string, error) {
	zone, err := s.Rules.TimeZone(ctx)
	if err != nil {
		return "", err
	}
	states, err := s.Rules.OfficeHoursStates(ctx, tenant, at)
	if err != nil {
		return "", err
	}
	local := at
	if loc, err := time.LoadLocation(zone); err == nil {
		local = at.In(loc)
	}
	head := fmt.Sprintf("%s (%s time)", local.Format("Monday 2 Jan 15:04"), zone)
	st := states[schedule]
	switch {
	case st.Holiday != "":
		return fmt.Sprintf("%s: %s, a holiday", head, st.Holiday), nil
	case st.Open:
		return head + ": during office hours", nil
	default:
		return head + ": outside office hours", nil
	}
}

func (s *Service) simulate(ctx context.Context, tenant uuid.UUID, dest, caller string, at time.Time) (Simulation, error) {
	sim := Simulation{Steps: []SimStep{}}
	groups, err := s.Store.RingGroups(ctx, tenant)
	if err != nil {
		return sim, err
	}
	byID := map[uuid.UUID]RingGroup{}
	byNumber := map[string]RingGroup{}
	for _, g := range groups {
		byID[g.ID] = g
		if g.Number != "" {
			byNumber[g.Number] = g
		}
	}
	places := 1
	for range 200 {
		st, err := s.Rules.RouteStep(ctx, dest, caller, at)
		if err != nil {
			return sim, err
		}
		if st == nil {
			sim.Steps = append(sim.Steps, SimStep{Words: messageWords("not-in-use")})
			return sim, nil
		}
		if st.Action == "message" {
			sim.Steps = append(sim.Steps, SimStep{Words: messageWords(st.Targets)})
			return sim, nil
		}
		kind, arg, _ := strings.Cut(dest, ":")
		var group *RingGroup
		switch kind {
		case "g":
			id, _, _ := strings.Cut(arg, ":")
			if u, err := uuid.Parse(id); err == nil {
				if g, ok := byID[u]; ok {
					group = &g
				}
			}
		case "n":
			if g, ok := byNumber[arg]; ok {
				group = &g
			}
		}
		if st.Action == "dial" {
			words, err := s.dialWords(ctx, tenant, st, group, caller)
			if err != nil {
				return sim, err
			}
			sim.Steps = append(sim.Steps, SimStep{Words: words, Rings: true})
		} else if kind != "d" && kind != "l" {
			sim.Steps = append(sim.Steps, SimStep{Words: nobodyWords(ctx, s, tenant, group, st.Label)})
		}
		dest = st.Next
		if st.Counts == 1 {
			places++
			if places > maxPlaces {
				sim.Steps = append(sim.Steps, SimStep{Words: fmt.Sprintf(`Stopped after %d places (calls going round in circles end here): %s`,
					maxPlaces, lowerFirst(messageWords(MessageNotAvailable)))})
				return sim, nil
			}
		}
	}
	return sim, nil
}

func messageWords(name string) string {
	switch name {
	case MessageClosed:
		return `Plays "We're closed", then hangs up.`
	case MessageNotAvailable:
		return `Plays "Nobody can take your call right now", then hangs up.`
	default:
		return `Plays "That number isn't in use", then hangs up.`
	}
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func nobodyWords(ctx context.Context, s *Service, tenant uuid.UUID, group *RingGroup, label string) string {
	if group != nil {
		return fmt.Sprintf("Nobody in %s can ring right now (no phone or browser connected), so the call goes straight on.", group.Name)
	}
	if exts, err := s.Rules.ExtensionsByNumber(ctx, tenant, []string{label}); err == nil && len(exts) == 1 {
		return fmt.Sprintf("%s (%s) has no phone or browser connected, so the call goes straight on.", exts[0].DisplayName, exts[0].Number)
	}
	return "Nobody can ring there right now, so the call goes straight on."
}

// dialWords says who a step rings: "Rings Sara Haddad (101) for 25
// seconds.", "Rings Sales: Aisha and Sara together, for 25 seconds (Omar:
// can't ring right now, skipped).", "Rings Sara Haddad (Sales, one after
// another) for 15 seconds."
func (s *Service) dialWords(ctx context.Context, tenant uuid.UUID, st *Step, group *RingGroup, caller string) (string, error) {
	var users []string
	for _, t := range strings.Split(st.Targets, "&") {
		if u, ok := strings.CutPrefix(t, "PJSIP/"); ok {
			users = append(users, u)
		}
	}
	targets, err := s.Rules.RingTargets(ctx, users)
	if err != nil {
		return "", err
	}
	var names []string
	ringing := map[string]bool{}
	for _, t := range targets {
		if !ringing[t.Number] {
			ringing[t.Number] = true
			names = append(names, t.DisplayName)
		}
	}
	if len(names) == 0 {
		names = []string{"a phone"}
	}
	if group == nil {
		if exts, err := s.Rules.ExtensionsByNumber(ctx, tenant, []string{st.Label}); err == nil && len(exts) == 1 {
			return fmt.Sprintf("Rings %s (%s) for %d seconds.", exts[0].DisplayName, exts[0].Number, st.Seconds), nil
		}
		return fmt.Sprintf("Rings %s for %d seconds.", listNames(names, ""), st.Seconds), nil
	}
	if group.Strategy == StrategyInTurn {
		return fmt.Sprintf("Rings %s (%s, one after another) for %d seconds.", listNames(names, ""), group.Name, st.Seconds), nil
	}
	var skipped []string
	for _, m := range group.Members {
		if !ringing[m.Number] && m.Number != caller {
			skipped = append(skipped, m.DisplayName)
		}
	}
	together := " together"
	if len(names) == 1 {
		together = ""
	}
	words := fmt.Sprintf("Rings %s: %s%s, for %d seconds", group.Name, listNames(names, ""), together, st.Seconds)
	if len(skipped) > 0 {
		words += fmt.Sprintf(" (%s: can't ring right now, skipped)", listNames(skipped, ""))
	}
	return words + ".", nil
}
