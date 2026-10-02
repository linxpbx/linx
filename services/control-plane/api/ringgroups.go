package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/routing"
)

// SetRouting gives the server the ring group service (ADR-068).
func (s *Server) SetRouting(svc *routing.Service) {
	s.routing = svc
}

var errNoRouting = errors.New("routing service not set")

func toDestination(d routing.Destination, label string) Destination {
	out := Destination{Kind: DestinationKind(d.Kind), ExtensionId: d.ExtensionID, RingGroupId: d.RingGroupID}
	if d.Message != "" {
		m := DestinationMessage(d.Message)
		out.Message = &m
	}
	if label != "" {
		out.Label = &label
	}
	return out
}

func fromDestination(d *Destination) *routing.Destination {
	if d == nil {
		return nil
	}
	out := &routing.Destination{Kind: string(d.Kind), ExtensionID: d.ExtensionId, RingGroupID: d.RingGroupId}
	if d.Message != nil {
		out.Message = string(*d.Message)
	}
	return out
}

func toRingGroup(v routing.View) RingGroup {
	out := RingGroup{
		Id: v.ID, Name: v.Name, Strategy: RingGroupStrategy(v.Strategy), RingSeconds: v.RingSeconds, TurnSeconds: v.TurnSeconds,
		IfNoAnswer: toDestination(v.NoAnswer, v.NoAnswerLabel), Words: v.Words,
		Members: make([]RingGroupMember, 0, len(v.Members)), CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, Etag: routing.ETag(v.Version),
	}
	out.UsedBy = make([]struct {
		How  RingGroupUsedByHow  `json:"how"`
		Id   uuid.UUID           `json:"id"`
		Kind RingGroupUsedByKind `json:"kind"`
		Name string              `json:"name"`
	}, 0, len(v.UsedBy))
	if v.Number != "" {
		out.Number = &v.Number
	}
	for _, m := range v.Members {
		out.Members = append(out.Members, RingGroupMember{ExtensionId: m.ExtensionID, Number: m.Number, DisplayName: m.DisplayName, CanRing: m.CanRing})
	}
	for _, u := range v.UsedBy {
		out.UsedBy = append(out.UsedBy, struct {
			How  RingGroupUsedByHow  `json:"how"`
			Id   uuid.UUID           `json:"id"`
			Kind RingGroupUsedByKind `json:"kind"`
			Name string              `json:"name"`
		}{RingGroupUsedByHow(u.How), u.ID, RingGroupUsedByKind(u.Kind), u.Name})
	}
	return out
}

func (s *Server) ListRingGroups(ctx context.Context, _ ListRingGroupsRequestObject) (ListRingGroupsResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	views, err := s.routing.ListRingGroups(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListRingGroupsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := RingGroupList{Items: make([]RingGroup, 0, len(views))}
	for _, v := range views {
		list.Items = append(list.Items, toRingGroup(v))
	}
	return ListRingGroups200JSONResponse(list), nil
}

func (s *Server) CreateRingGroup(ctx context.Context, req CreateRingGroupRequestObject) (CreateRingGroupResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	in := routing.Input{Name: req.Body.Name, Number: deref(req.Body.Number), RingSeconds: req.Body.RingSeconds,
		TurnSeconds: req.Body.TurnSeconds, MemberIDs: req.Body.MemberIds, NoAnswer: fromDestination(req.Body.IfNoAnswer)}
	if req.Body.Strategy != nil {
		in.Strategy = string(*req.Body.Strategy)
	}
	v, err := s.routing.CreateRingGroup(ctx, in)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateRingGroupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateRingGroup201JSONResponse(toRingGroup(v)), nil
}

func (s *Server) GetRingGroup(ctx context.Context, req GetRingGroupRequestObject) (GetRingGroupResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	v, err := s.routing.GetRingGroup(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetRingGroupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toRingGroup(v)
	return GetRingGroup200JSONResponse{Body: out, Headers: GetRingGroup200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateRingGroup(ctx context.Context, req UpdateRingGroupRequestObject) (UpdateRingGroupResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	patch := routing.Patch{Name: req.Body.Name, Number: req.Body.Number, RingSeconds: req.Body.RingSeconds,
		TurnSeconds: req.Body.TurnSeconds, MemberIDs: req.Body.MemberIds, NoAnswer: fromDestination(req.Body.IfNoAnswer)}
	if req.Body.Strategy != nil {
		st := string(*req.Body.Strategy)
		patch.Strategy = &st
	}
	v, err := s.routing.UpdateRingGroup(ctx, req.Id, patch, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateRingGroupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toRingGroup(v)
	return UpdateRingGroup200JSONResponse{Body: out, Headers: UpdateRingGroup200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) DeleteRingGroup(ctx context.Context, req DeleteRingGroupRequestObject) (DeleteRingGroupResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	if err := s.routing.DeleteRingGroup(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteRingGroupdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteRingGroup204Response{}, nil
}
