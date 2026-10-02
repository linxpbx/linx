package routing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/numbering"
)

// Service is what the API's ring group endpoints do.
type Service struct {
	Store Store
	// Rules is office hours, "When someone calls" and the simulator; nil
	// leaves them out (ring groups alone).
	Rules RulesStore
	// Changes is undo for routing (changes.go); nil leaves it out.
	Changes ChangeStore
	Now     func() time.Time
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

var errChanged = &apihttp.Error{Status: http.StatusPreconditionFailed, Code: "etag_mismatch",
	Detail: "This ring group was changed since you read it. Fetch it again and retry."}

func notFound() *apihttp.Error {
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no ring group with that id."}
}

func invalid(code, detail string) *apihttp.Error {
	return &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: code, Detail: detail}
}

func principal(ctx context.Context) (auth.Principal, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, errNoPrincipal
	}
	return p, nil
}

func audit(ctx context.Context, action, target string) (auth.Principal, auth.AuditEntry, error) {
	p, err := principal(ctx)
	if err != nil {
		return auth.Principal{}, auth.AuditEntry{}, err
	}
	return p, auth.AuditEntry{
		TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
		Action: action, Target: target, Result: auth.ResultOK,
	}, nil
}

// ETag is a ring group's version as an HTTP entity tag.
func ETag(version int) string { return `"` + strconv.Itoa(version) + `"` }

func matchETag(ifMatch string, version int) bool {
	for _, tag := range strings.Split(ifMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || tag == ETag(version) {
			return true
		}
	}
	return false
}

var numberPattern = regexp.MustCompile(`^[0-9]{2,6}$`)

// View is a ring group as the screens show it: with where its unanswered
// calls go in words, what sends calls to it, and the sentence saying what
// a caller gets.
type View struct {
	RingGroup
	NoAnswerLabel string
	UsedBy        []UsedBy
	Words         string
}

// UsedBy is one place that sends calls to a ring group (or follows a
// schedule).
type UsedBy struct {
	Kind string // KindRingGroup, IncomingNumber or IncomingLine
	ID   uuid.UUID
	Name string
	How  string // "if_nobody_answers", "rings", "after_hours", "holidays" ("schedule" for a schedule)
}

// ListRingGroups returns every ring group, by name.
func (s *Service) ListRingGroups(ctx context.Context) ([]View, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.Store.RingGroups(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	return s.views(ctx, p.TenantID, groups)
}

// GetRingGroup returns one ring group.
func (s *Service) GetRingGroup(ctx context.Context, id uuid.UUID) (View, error) {
	p, err := principal(ctx)
	if err != nil {
		return View{}, err
	}
	groups, err := s.Store.RingGroups(ctx, p.TenantID)
	if err != nil {
		return View{}, err
	}
	views, err := s.views(ctx, p.TenantID, groups)
	if err != nil {
		return View{}, err
	}
	for _, v := range views {
		if v.ID == id {
			return v, nil
		}
	}
	return View{}, notFound()
}

func (s *Service) views(ctx context.Context, tenant uuid.UUID, groups []RingGroup) ([]View, error) {
	var ids []uuid.UUID
	for _, g := range groups {
		if g.NoAnswer.ExtensionID != nil {
			ids = append(ids, *g.NoAnswer.ExtensionID)
		}
	}
	exts := map[uuid.UUID]ExtensionRef{}
	if len(ids) > 0 {
		list, err := s.Store.Extensions(ctx, tenant, ids)
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			exts[e.ID] = e
		}
	}
	byID := map[uuid.UUID]RingGroup{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	var incoming []Incoming
	if s.Rules != nil {
		var err error
		if incoming, err = s.Rules.IncomingList(ctx, tenant); err != nil {
			return nil, err
		}
	}
	out := make([]View, 0, len(groups))
	for _, g := range groups {
		v := View{RingGroup: g, UsedBy: []UsedBy{}}
		v.NoAnswerLabel = DestinationLabel(g.NoAnswer, byID, exts)
		v.Words = Words(g, destinationWords(g.NoAnswer, byID, exts))
		for _, o := range groups {
			// Another group sending its unanswered calls here or to this
			// group's voicemail (its own voicemail is part of it).
			if o.ID != g.ID && o.NoAnswer.RingGroupID != nil && *o.NoAnswer.RingGroupID == g.ID {
				v.UsedBy = append(v.UsedBy, UsedBy{Kind: KindRingGroup, ID: o.ID, Name: o.Name, How: "if_nobody_answers"})
			}
		}
		v.UsedBy = append(v.UsedBy, incomingUses(incoming, g.ID)...)
		out = append(out, v)
	}
	return out, nil
}

// DestinationLabel is a destination in a few words, for lists and the
// picker: "Sara Haddad (101)", "Sales", "Voicemail for Sales", `"Not
// available" message`.
func DestinationLabel(d Destination, groups map[uuid.UUID]RingGroup, exts map[uuid.UUID]ExtensionRef) string {
	switch d.Kind {
	case KindVoicemail:
		if owner := voicemailOwner(d, groups, exts); owner != "" {
			if voicemailOff(d, groups, exts) {
				return "Voicemail for " + owner + " (off)"
			}
			return "Voicemail for " + owner
		}
	case KindExtension:
		if d.ExtensionID != nil {
			if e, ok := exts[*d.ExtensionID]; ok {
				return fmt.Sprintf("%s (%s)", e.DisplayName, e.Number)
			}
		}
		return `"Not available" message`
	case KindRingGroup:
		if d.RingGroupID != nil {
			if g, ok := groups[*d.RingGroupID]; ok {
				return g.Name
			}
		}
	case KindMessage:
		if d.Message == MessageClosed {
			return `"We're closed" message`
		}
	}
	return `"Not available" message`
}

// voicemailOwner is whose box d is ("Sara Haddad (101)", "Sales"), or ""
// when they're gone.
func voicemailOwner(d Destination, groups map[uuid.UUID]RingGroup, exts map[uuid.UUID]ExtensionRef) string {
	if d.ExtensionID != nil {
		if e, ok := exts[*d.ExtensionID]; ok {
			return fmt.Sprintf("%s (%s)", e.DisplayName, e.Number)
		}
	}
	if d.RingGroupID != nil {
		if g, ok := groups[*d.RingGroupID]; ok {
			return g.Name
		}
	}
	return ""
}

// voicemailOff reports whether box d is turned off (callers hear "not
// available" instead, migration 0034's voicemail_dest).
func voicemailOff(d Destination, groups map[uuid.UUID]RingGroup, exts map[uuid.UUID]ExtensionRef) bool {
	if d.ExtensionID != nil {
		return exts[*d.ExtensionID].VoicemailOff
	}
	if d.RingGroupID != nil {
		return groups[*d.RingGroupID].VoicemailOff
	}
	return false
}

func destinationWords(d Destination, groups map[uuid.UUID]RingGroup, exts map[uuid.UUID]ExtensionRef) string {
	switch d.Kind {
	case KindVoicemail:
		if owner := voicemailOwner(d, groups, exts); owner != "" {
			if voicemailOff(d, groups, exts) {
				return `callers hear "not available" (the voicemail for ` + owner + " is off)."
			}
			return "callers can leave a voicemail for " + owner + "."
		}
	case KindExtension, KindRingGroup:
		if label := DestinationLabel(d, groups, exts); label != `"Not available" message` {
			return "the call goes to " + label + "."
		}
	case KindMessage:
		if d.Message == MessageClosed {
			return `callers hear "We're closed".`
		}
	}
	return `callers hear "not available".`
}

// Words is the sentence saying what a caller gets from g (the same one on
// the list, the detail sheet and, later, the simulator); then is where
// unanswered calls go, in words ending with a full stop.
func Words(g RingGroup, then string) string {
	names := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		names = append(names, m.DisplayName)
	}
	if len(names) == 0 {
		return "Nobody is in this group, so " + then
	}
	if g.Strategy == StrategyInTurn && len(names) > 1 {
		return fmt.Sprintf("Calls ring %s, %d seconds each. If nobody answers, %s",
			listNames(names, ", then "), g.TurnSeconds, then)
	}
	together := " together"
	if len(names) == 1 {
		together = ""
	}
	seconds := g.RingSeconds
	if g.Strategy == StrategyInTurn {
		seconds = g.TurnSeconds
	}
	return fmt.Sprintf("Calls ring %s%s. If nobody answers in %d seconds, %s", listNames(names, ""), together, seconds, then)
}

// listNames joins names as "A, B and C" (or with sep: "A, then B, then
// C"), at most four of them: "A, B, C and 3 more".
func listNames(names []string, sep string) string {
	if sep != "" {
		if len(names) > 4 {
			return strings.Join(names[:4], sep) + fmt.Sprintf(" and %d more", len(names)-4)
		}
		return strings.Join(names, sep)
	}
	switch {
	case len(names) == 1:
		return names[0]
	case len(names) > 4:
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}

// Input is a new ring group.
type Input struct {
	Name        string
	Number      string // "" for none
	Strategy    string // "" means all at once
	RingSeconds *int
	TurnSeconds *int
	MemberIDs   []uuid.UUID
	NoAnswer    *Destination // nil means the group's own voicemail
}

// Patch is a JSON Merge Patch of a ring group; nil fields stay as they are.
type Patch struct {
	Name        *string
	Number      *string // "" removes it
	Strategy    *string
	RingSeconds *int
	TurnSeconds *int
	MemberIDs   *[]uuid.UUID
	NoAnswer    *Destination
}

// CreateRingGroup adds a ring group.
func (s *Service) CreateRingGroup(ctx context.Context, in Input) (View, error) {
	p, err := principal(ctx)
	if err != nil {
		return View{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return View{}, err
	}
	now := s.Now().UTC()
	g := RingGroup{ID: id, TenantID: p.TenantID, Name: strings.TrimSpace(in.Name), Number: in.Number,
		Strategy: in.Strategy, RingSeconds: 25, TurnSeconds: 15, Version: 1, CreatedAt: now, UpdatedAt: now,
		NoAnswer: Destination{Kind: KindVoicemail, RingGroupID: &id}}
	if g.Strategy == "" {
		g.Strategy = StrategyAll
	}
	if in.RingSeconds != nil {
		g.RingSeconds = *in.RingSeconds
	}
	if in.TurnSeconds != nil {
		g.TurnSeconds = *in.TurnSeconds
	}
	if in.NoAnswer != nil {
		g.NoAnswer = *in.NoAnswer
	}
	all, err := s.Store.RingGroups(ctx, p.TenantID)
	if err != nil {
		return View{}, err
	}
	if len(all) >= MaxRingGroups {
		return View{}, invalid("too_many", fmt.Sprintf("A server can have up to %d ring groups.", MaxRingGroups))
	}
	if err := s.check(ctx, &g, in.MemberIDs, all); err != nil {
		return View{}, err
	}
	_, a, err := audit(ctx, "ring_group.create", "ring_group:"+id.String())
	if err != nil {
		return View{}, err
	}
	a.Detail = auditDetail(g)
	if err := s.Store.CreateRingGroup(ctx, g, a); err != nil {
		return View{}, storeError(err, g)
	}
	return s.GetRingGroup(ctx, id)
}

// UpdateRingGroup applies patch. ifMatch, when not empty, must match the
// group's current ETag (412 otherwise).
func (s *Service) UpdateRingGroup(ctx context.Context, id uuid.UUID, patch Patch, ifMatch string) (View, error) {
	p, err := principal(ctx)
	if err != nil {
		return View{}, err
	}
	all, err := s.Store.RingGroups(ctx, p.TenantID)
	if err != nil {
		return View{}, err
	}
	i := slices.IndexFunc(all, func(g RingGroup) bool { return g.ID == id })
	if i < 0 {
		return View{}, notFound()
	}
	g := all[i]
	if ifMatch != "" && !matchETag(ifMatch, g.Version) {
		return View{}, errChanged
	}
	if patch.Name != nil {
		g.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.Number != nil {
		g.Number = *patch.Number
	}
	if patch.Strategy != nil {
		g.Strategy = *patch.Strategy
	}
	if patch.RingSeconds != nil {
		g.RingSeconds = *patch.RingSeconds
	}
	if patch.TurnSeconds != nil {
		g.TurnSeconds = *patch.TurnSeconds
	}
	if patch.NoAnswer != nil {
		g.NoAnswer = *patch.NoAnswer
	}
	members := make([]uuid.UUID, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, m.ExtensionID)
	}
	if patch.MemberIDs != nil {
		members = *patch.MemberIDs
	}
	if err := s.check(ctx, &g, members, all); err != nil {
		return View{}, err
	}
	g.UpdatedAt = s.Now().UTC()
	_, a, err := audit(ctx, "ring_group.update", "ring_group:"+id.String())
	if err != nil {
		return View{}, err
	}
	a.Detail = auditDetail(g)
	if err := s.Store.UpdateRingGroup(ctx, g, a); err != nil {
		if errors.Is(err, ErrVersionChanged) {
			return View{}, errChanged
		}
		if errors.Is(err, ErrNotFound) {
			return View{}, notFound()
		}
		return View{}, storeError(err, g)
	}
	return s.GetRingGroup(ctx, id)
}

// DeleteRingGroup removes a ring group nothing sends calls to.
func (s *Service) DeleteRingGroup(ctx context.Context, id uuid.UUID) error {
	p, a, err := audit(ctx, "ring_group.delete", "ring_group:"+id.String())
	if err != nil {
		return err
	}
	err = s.Store.DeleteRingGroup(ctx, p.TenantID, id, a)
	if errors.Is(err, ErrNotFound) {
		return notFound()
	}
	if errors.Is(err, ErrInUse) {
		return &apihttp.Error{Status: http.StatusConflict, Code: "ring_group_in_use",
			Detail: "Another ring group or a phone number sends calls here or to this group's voicemail. Choose where they go instead first."}
	}
	return err
}

// check validates g (and its members, which it fills in) against the
// other ring groups.
func (s *Service) check(ctx context.Context, g *RingGroup, memberIDs []uuid.UUID, all []RingGroup) error {
	if n := len([]rune(g.Name)); n < 1 || n > 60 {
		return invalid("name_invalid", "Give it a name of 1 to 60 characters.")
	}
	if g.Number != "" && !numberPattern.MatchString(g.Number) {
		return invalid("number_invalid", "A ring group's number is 2 to 6 digits.")
	}
	if g.Strategy != StrategyAll && g.Strategy != StrategyInTurn {
		return invalid("strategy_invalid", `How it rings is "all" (all at once) or "in_turn" (one after another).`)
	}
	if g.RingSeconds < 5 || g.RingSeconds > 300 {
		return invalid("ring_seconds_invalid", "Ring for 5 to 300 seconds.")
	}
	if g.TurnSeconds < 5 || g.TurnSeconds > 120 {
		return invalid("turn_seconds_invalid", "Ring each person for 5 to 120 seconds.")
	}
	if len(memberIDs) == 0 {
		return invalid("members_missing", "Choose at least one person to ring.")
	}
	if len(memberIDs) > MaxMembers {
		return invalid("members_too_many", fmt.Sprintf("A ring group can have up to %d people.", MaxMembers))
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range memberIDs {
		if seen[id] {
			return invalid("members_duplicate", "Each person can be in a ring group once.")
		}
		seen[id] = true
	}
	ids := slices.Clone(memberIDs)
	if g.NoAnswer.ExtensionID != nil {
		ids = append(ids, *g.NoAnswer.ExtensionID)
	}
	exts, err := s.Store.Extensions(ctx, g.TenantID, ids)
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]ExtensionRef{}
	for _, e := range exts {
		byID[e.ID] = e
	}
	g.Members = make([]Member, 0, len(memberIDs))
	for _, id := range memberIDs {
		e, ok := byID[id]
		if !ok {
			return invalid("extension_not_found", "One of those people's extensions doesn't exist any more.")
		}
		g.Members = append(g.Members, Member{ExtensionID: id, Number: e.Number, DisplayName: e.DisplayName})
	}
	return checkDestination(g, byID, all)
}

func checkDestination(g *RingGroup, exts map[uuid.UUID]ExtensionRef, all []RingGroup) error {
	d := &g.NoAnswer
	switch d.Kind {
	case KindExtension:
		if d.ExtensionID == nil {
			return invalid("destination_invalid", "Choose which extension unanswered calls go to.")
		}
		if _, ok := exts[*d.ExtensionID]; !ok {
			return invalid("extension_not_found", "That extension doesn't exist.")
		}
		d.RingGroupID, d.Message = nil, ""
	case KindRingGroup:
		if d.RingGroupID == nil {
			return invalid("destination_invalid", "Choose which ring group unanswered calls go to.")
		}
		if err := checkLoop(g, *d.RingGroupID, all); err != nil {
			return err
		}
		d.ExtensionID, d.Message = nil, ""
	case KindVoicemail:
		// No owner: the group's own box.
		if d.ExtensionID == nil && d.RingGroupID == nil {
			d.RingGroupID = &g.ID
		}
		switch {
		case d.ExtensionID != nil && d.RingGroupID != nil:
			return invalid("destination_invalid", "A voicemail box is a person's or a ring group's, not both.")
		case d.ExtensionID != nil:
			if _, ok := exts[*d.ExtensionID]; !ok {
				return invalid("extension_not_found", "That extension doesn't exist.")
			}
		case *d.RingGroupID != g.ID && !slices.ContainsFunc(all, func(o RingGroup) bool { return o.ID == *d.RingGroupID }):
			return invalid("ring_group_not_found", "That ring group doesn't exist.")
		}
		d.Message = ""
	case KindMessage:
		if d.Message != MessageNotAvailable && d.Message != MessageClosed {
			return invalid("destination_invalid", `The message can be "not-available" or "closed".`)
		}
		d.ExtensionID, d.RingGroupID = nil, nil
	default:
		return invalid("destination_invalid", "Unanswered calls can go to an extension, a ring group, a voicemail box or a message.")
	}
	return nil
}

// checkLoop refuses a "next" that leads back to g, following each
// group's own unanswered calls (the only way one group reaches another).
// The dialplan's 10-step cap still ends a loop made some other way.
func checkLoop(g *RingGroup, next uuid.UUID, all []RingGroup) error {
	byID := map[uuid.UUID]RingGroup{}
	for _, o := range all {
		byID[o.ID] = o
	}
	if next == g.ID {
		return invalid("routing_loop", "A ring group can't send its unanswered calls to itself.")
	}
	at := next
	for range len(all) + 1 {
		o, ok := byID[at]
		if !ok {
			if at == next {
				return invalid("ring_group_not_found", "That ring group doesn't exist.")
			}
			return nil
		}
		if o.NoAnswer.Kind != KindRingGroup || o.NoAnswer.RingGroupID == nil {
			return nil
		}
		if *o.NoAnswer.RingGroupID == g.ID {
			return invalid("routing_loop", fmt.Sprintf("%s already sends its unanswered calls here, so calls would go round in circles.", o.Name))
		}
		at = *o.NoAnswer.RingGroupID
	}
	return nil
}

func storeError(err error, g RingGroup) error {
	if errors.Is(err, ErrDuplicateName) {
		return &apihttp.Error{Status: http.StatusConflict, Code: "name_duplicate",
			Detail: fmt.Sprintf("A ring group named %q already exists.", g.Name)}
	}
	if errors.Is(err, ErrNumberTaken) {
		return &apihttp.Error{Status: http.StatusConflict, Code: "number_duplicate",
			Detail: fmt.Sprintf("Number %s is already an extension's or another ring group's.", g.Number)}
	}
	if r, ok := errors.AsType[*ReservedNumberError](err); ok {
		return invalid("number_reserved", numbering.ClashText(r.Number, r.Reason, r.Country))
	}
	if errors.Is(err, ErrExtensionNotFound) {
		return invalid("extension_not_found", "One of those extensions doesn't exist any more.")
	}
	if errors.Is(err, ErrNotFound) {
		return invalid("ring_group_not_found", "That ring group doesn't exist.")
	}
	return err
}

func auditDetail(g RingGroup) map[string]any {
	members := make([]string, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, m.Number)
	}
	d := map[string]any{"name": g.Name, "number": g.Number, "strategy": g.Strategy, "members": members,
		"no_answer": g.NoAnswer.Kind}
	switch {
	case g.NoAnswer.ExtensionID != nil:
		d["no_answer_extension_id"] = *g.NoAnswer.ExtensionID
	case g.NoAnswer.RingGroupID != nil:
		d["no_answer_ring_group_id"] = *g.NoAnswer.RingGroupID
	}
	return d
}

// ETagValue is the view's ETag.
func (v View) ETagValue() string { return ETag(v.Version) }
