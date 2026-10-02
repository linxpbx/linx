package api

import (
	"context"

	"linxpbx.com/linx/internal/routing"
)

// Undo for call routing (ADR-071): System → Routing changes and "Saved.
// Undo".

func toItemChanges(list []routing.ItemChange) []RoutingItemChange {
	out := make([]RoutingItemChange, 0, len(list))
	for _, c := range list {
		ic := RoutingItemChange{Kind: RoutingItemChangeKind(c.Kind), Name: c.Name}
		if c.Before != "" {
			ic.Before = &c.Before
		}
		if c.After != "" {
			ic.After = &c.After
		}
		out = append(out, ic)
	}
	return out
}

func (s *Server) ListRoutingChanges(ctx context.Context, _ ListRoutingChangesRequestObject) (ListRoutingChangesResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	views, err := s.routing.ListChanges(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListRoutingChangesdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := RoutingChangeList{Items: make([]RoutingChange, 0, len(views))}
	for _, v := range views {
		c := RoutingChange{Id: v.ID, At: v.At, Actor: v.Actor, ActorName: v.ActorName, Kind: Change,
			Summary: v.Summary, PutBackAt: v.PutBackAt}
		switch v.Action {
		case "routing.undo":
			c.Kind = Undo
		case "routing.put_back":
			c.Kind = PutBack
		}
		if v.Changes != nil {
			changes := toItemChanges(v.Changes)
			c.Changes = &changes
		}
		list.Items = append(list.Items, c)
	}
	return ListRoutingChanges200JSONResponse(list), nil
}

func (s *Server) PreviewRoutingPutBack(ctx context.Context, req PreviewRoutingPutBackRequestObject) (PreviewRoutingPutBackResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	pv, err := s.routing.PreviewPutBack(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return PreviewRoutingPutBackdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return PreviewRoutingPutBack200JSONResponse{Changes: toItemChanges(pv.Changes), NeedsConfirm: pv.NeedConfirm,
		ChangesOutgoing: pv.Outgoing}, nil
}

func (s *Server) PutRoutingBack(ctx context.Context, req PutRoutingBackRequestObject) (PutRoutingBackResponseObject, error) {
	if s.routing == nil {
		return nil, errNoRouting
	}
	undo := req.Body != nil && req.Body.Undo != nil && *req.Body.Undo
	id, err := s.routing.PutBack(ctx, req.Id, undo)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return PutRoutingBackdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return PutRoutingBack201JSONResponse{Id: id}, nil
}
