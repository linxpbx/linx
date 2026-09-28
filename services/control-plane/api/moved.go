package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/moved"
)

// SetMoved connects "Moved to a new place?" (docs/INSTALL.md §8).
func (s *Server) SetMoved(svc *moved.Service) { s.moved = svc }

// movedState is the checklist in the API's shape.
func movedState(c *moved.Checklist) (MovedChecklistState, error) {
	if c == nil {
		return MovedChecklistState{}, nil
	}
	for _, p := range []*moved.Place{&c.Before, &c.After} {
		if p.LANNetworks == nil {
			p.LANNetworks = []string{}
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return MovedChecklistState{}, err
	}
	var out MovedChecklist
	if err := json.Unmarshal(b, &out); err != nil {
		return MovedChecklistState{}, err
	}
	return MovedChecklistState{Checklist: &out}, nil
}

func (s *Server) GetMovedChecklist(ctx context.Context, _ GetMovedChecklistRequestObject) (GetMovedChecklistResponseObject, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	if s.moved == nil {
		return GetMovedChecklist200JSONResponse{}, nil
	}
	c, err := s.moved.Current(ctx, p.TenantID)
	if err != nil {
		return nil, err
	}
	out, err := movedState(c)
	if err != nil {
		return nil, err
	}
	return GetMovedChecklist200JSONResponse(out), nil
}

func (s *Server) UpdateMovedChecklist(ctx context.Context, req UpdateMovedChecklistRequestObject) (UpdateMovedChecklistResponseObject, error) {
	fail := func(e *apihttp.Error) (UpdateMovedChecklistResponseObject, error) {
		return UpdateMovedChecklistdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errNoPrincipal
	}
	noChecklist := &apihttp.Error{Status: http.StatusNotFound, Code: "no_checklist", Detail: "There's no moved-server checklist."}
	if s.moved == nil {
		return fail(noChecklist)
	}
	ch := moved.Change{Hide: req.Body.Hide}
	if req.Body.Ticks != nil {
		ch.Ticks = *req.Body.Ticks
	}
	c, err := s.moved.Update(ctx, p.TenantID, ch)
	var unknown moved.ErrUnknownItem
	switch {
	case errors.Is(err, moved.ErrNoChecklist):
		return fail(noChecklist)
	case errors.As(err, &unknown):
		return fail(&apihttp.Error{Status: http.StatusUnprocessableEntity, Code: "unknown_item", Detail: "That isn't an item on the list that can be ticked."})
	case err != nil:
		return nil, err
	}
	if s.writeAudit != nil {
		detail := map[string]any{"ticks": ch.Ticks}
		if ch.Hide != nil {
			detail["hide"] = *ch.Hide
		}
		_ = s.writeAudit(context.WithoutCancel(ctx), auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx),
			Action: "settings.moved_checklist", Target: "moved_checklist", Result: auth.ResultOK, Detail: detail})
	}
	out, err := movedState(c)
	if err != nil {
		return nil, err
	}
	return UpdateMovedChecklist200JSONResponse(out), nil
}
