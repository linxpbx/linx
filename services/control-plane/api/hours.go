package api

import (
	"context"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"linxpbx.com/linx/internal/routing"
)

// Office hours, holidays and "When someone calls" (docs/PHASE1F.md §7,
// internal/routing).

func toSchedule(v routing.ScheduleView) Schedule {
	out := Schedule{Id: v.ID, Name: v.Name, Words: v.Words, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		Etag: routing.ETag(v.Version), Spans: make([]Span, 0, len(v.Spans)), Holidays: make([]Holiday, 0, len(v.Holidays))}
	for _, sp := range v.Spans {
		out.Spans = append(out.Spans, Span{Weekday: sp.Weekday, Opens: sp.Opens, Closes: sp.Closes})
	}
	for _, h := range v.Holidays {
		first, _ := time.Parse(time.DateOnly, h.FirstDay)
		last, _ := time.Parse(time.DateOnly, h.LastDay)
		every := h.EveryYear
		out.Holidays = append(out.Holidays, Holiday{Name: h.Name, FirstDay: openapi_types.Date{Time: first},
			LastDay: openapi_types.Date{Time: last}, EveryYear: &every})
	}
	out.Now.Open, out.Now.ChangesAt = v.Now.Open, v.Now.ChangesAt
	if v.Now.Holiday != "" {
		out.Now.Holiday = &v.Now.Holiday
	}
	out.UsedBy = make([]struct {
		Id   openapi_types.UUID `json:"id"`
		Name string             `json:"name"`
	}, 0, len(v.UsedBy))
	for _, u := range v.UsedBy {
		out.UsedBy = append(out.UsedBy, struct {
			Id   openapi_types.UUID `json:"id"`
			Name string             `json:"name"`
		}{u.ID, u.Name})
	}
	return out
}

func spansIn(in *[]Span) *[]routing.Span {
	if in == nil {
		return nil
	}
	out := make([]routing.Span, 0, len(*in))
	for _, sp := range *in {
		out = append(out, routing.Span{Weekday: sp.Weekday, Opens: sp.Opens, Closes: sp.Closes})
	}
	return &out
}

func holidaysIn(in *[]Holiday) *[]routing.Holiday {
	if in == nil {
		return nil
	}
	out := make([]routing.Holiday, 0, len(*in))
	for _, h := range *in {
		out = append(out, routing.Holiday{Name: h.Name, FirstDay: h.FirstDay.Format(time.DateOnly),
			LastDay: h.LastDay.Format(time.DateOnly), EveryYear: deref(h.EveryYear)})
	}
	return &out
}

func (s *Server) ListSchedules(ctx context.Context, _ ListSchedulesRequestObject) (ListSchedulesResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	fail := func(err error) (ListSchedulesResponseObject, error) {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListSchedulesdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	zone, err := s.routing.TimeZone(ctx)
	if err != nil {
		return fail(err)
	}
	views, err := s.routing.ListSchedules(ctx)
	if err != nil {
		return fail(err)
	}
	list := ScheduleList{TimeZone: zone, Items: make([]Schedule, 0, len(views))}
	for _, v := range views {
		list.Items = append(list.Items, toSchedule(v))
	}
	return ListSchedules200JSONResponse(list), nil
}

func (s *Server) CreateSchedule(ctx context.Context, req CreateScheduleRequestObject) (CreateScheduleResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	in := routing.ScheduleInput{Name: req.Body.Name}
	if sp := spansIn(req.Body.Spans); sp != nil {
		in.Spans = *sp
	}
	if h := holidaysIn(req.Body.Holidays); h != nil {
		in.Holidays = *h
	}
	v, err := s.routing.CreateSchedule(ctx, in)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateScheduledefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateSchedule201JSONResponse(toSchedule(v)), nil
}

func (s *Server) UpdateSchedule(ctx context.Context, req UpdateScheduleRequestObject) (UpdateScheduleResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	patch := routing.SchedulePatch{Name: req.Body.Name, Spans: spansIn(req.Body.Spans), Holidays: holidaysIn(req.Body.Holidays)}
	v, err := s.routing.UpdateSchedule(ctx, req.Id, patch, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateScheduledefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return UpdateSchedule200JSONResponse(toSchedule(v)), nil
}

func (s *Server) DeleteSchedule(ctx context.Context, req DeleteScheduleRequestObject) (DeleteScheduleResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	if err := s.routing.DeleteSchedule(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteScheduledefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteSchedule204Response{}, nil
}

func toIncoming(v routing.IncomingView) Incoming {
	out := Incoming{Id: v.ID, Kind: IncomingKind(v.Kind), LineId: v.LineID, LineName: v.LineName, LineKind: v.LineKind,
		Words: v.Words, Etag: routing.ETag(v.Version), JustRing: v.Rule == nil,
		IfNoAnswerSeconds: 30, IfNoAnswer: toDestination(routing.Destination{Kind: routing.KindMessage, Message: routing.MessageNotAvailable}, `"Not available" message`),
		AfterHours: toDestination(routing.Destination{Kind: routing.KindMessage, Message: routing.MessageClosed}, `"We're closed" message`)}
	if v.Kind == routing.IncomingNumber {
		out.Number = &v.Number
		if v.Label != "" {
			out.Label = &v.Label
		}
	}
	if v.Rings != nil {
		d := toDestination(*v.Rings, v.RingsLabel)
		out.Rings = &d
	}
	if r := v.Rule; r != nil {
		out.IfNoAnswerSeconds = r.NoAnswerSeconds
		out.IfNoAnswer = toDestination(r.NoAnswer, v.NoAnswerLabel)
		out.ScheduleId = r.ScheduleID
		out.AfterHours = toDestination(r.Closed, v.ClosedLabel)
		if r.Holiday != nil {
			d := toDestination(*r.Holiday, v.HolidayLabel)
			out.Holidays = &d
		}
	}
	return out
}

func (s *Server) ListIncoming(ctx context.Context, _ ListIncomingRequestObject) (ListIncomingResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	views, err := s.routing.ListIncoming(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListIncomingdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := IncomingList{Items: make([]Incoming, 0, len(views))}
	for _, v := range views {
		list.Items = append(list.Items, toIncoming(v))
	}
	return ListIncoming200JSONResponse(list), nil
}

func (s *Server) SetIncoming(ctx context.Context, req SetIncomingRequestObject) (SetIncomingResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	b := req.Body
	in := routing.IncomingInput{Rings: fromDestination(b.Rings), JustRing: deref(b.JustRing), NoAnswerSeconds: b.IfNoAnswerSeconds,
		NoAnswer: fromDestination(b.IfNoAnswer), ScheduleID: b.ScheduleId, Closed: fromDestination(b.AfterHours), Holiday: fromDestination(b.Holidays), Preview: deref(b.Preview)}
	v, err := s.routing.SetIncoming(ctx, req.Id, in, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return SetIncomingdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return SetIncoming200JSONResponse(toIncoming(v)), nil
}

// simSteps turns a simulation into RouteTest's steps.
func simSteps(sim routing.Simulation) *[]struct {
	Rings bool   `json:"rings"`
	Words string `json:"words"`
} {
	out := make([]struct {
		Rings bool   `json:"rings"`
		Words string `json:"words"`
	}, 0, len(sim.Steps))
	for _, st := range sim.Steps {
		out = append(out, struct {
			Rings bool   `json:"rings"`
			Words string `json:"words"`
		}{st.Rings, st.Words})
	}
	return &out
}
