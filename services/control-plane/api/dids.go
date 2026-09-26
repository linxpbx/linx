package api

import (
	"context"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/trunk"
)

func toDid(d trunk.DID) Did {
	out := Did{
		Id: d.ID, TrunkId: d.TrunkID, Number: d.Number, Label: d.Label,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, Etag: trunk.ETag(d.Version),
	}
	if d.ExtensionID != nil {
		out.ExtensionId = d.ExtensionID
	}
	return out
}

func (s *Server) ListTrunkDids(ctx context.Context, req ListTrunkDidsRequestObject) (ListTrunkDidsResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(d trunk.DID) uuid.UUID { return d.ID },
		func(before *uuid.UUID, limit int) ([]trunk.DID, error) {
			return s.trunks.ListDIDs(ctx, req.Id, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListTrunkDidsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := DidList{Items: make([]Did, 0, len(items)), NextCursor: next}
	for _, d := range items {
		list.Items = append(list.Items, toDid(d))
	}
	return ListTrunkDids200JSONResponse(list), nil
}

func (s *Server) CreateDid(ctx context.Context, req CreateDidRequestObject) (CreateDidResponseObject, error) {
	in := trunk.DIDInput{Number: req.Body.Number, Label: deref(req.Body.Label)}
	if req.Body.ExtensionId != nil {
		id := *req.Body.ExtensionId
		in.ExtensionID = &id
	}
	d, err := s.trunks.CreateDID(ctx, req.Id, in)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateDiddefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateDid201JSONResponse(toDid(d)), nil
}

func (s *Server) GetDid(ctx context.Context, req GetDidRequestObject) (GetDidResponseObject, error) {
	d, err := s.trunks.GetDID(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetDiddefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toDid(d)
	return GetDid200JSONResponse{Body: out, Headers: GetDid200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateDid(ctx context.Context, req UpdateDidRequestObject) (UpdateDidResponseObject, error) {
	d, err := s.trunks.UpdateDID(ctx, req.Id, trunk.DIDPatch{
		Label: req.Body.Label, ExtensionID: req.Body.ExtensionId,
	}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateDiddefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toDid(d)
	return UpdateDid200JSONResponse{Body: out, Headers: UpdateDid200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) DeleteDid(ctx context.Context, req DeleteDidRequestObject) (DeleteDidResponseObject, error) {
	if err := s.trunks.DeleteDID(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteDiddefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteDid204Response{}, nil
}

func (s *Server) ListInboundRoutes(ctx context.Context, req ListInboundRoutesRequestObject) (ListInboundRoutesResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(d trunk.DID) uuid.UUID { return d.ID },
		func(before *uuid.UUID, limit int) ([]trunk.DID, error) {
			return s.trunks.ListInboundRoutes(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListInboundRoutesdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := InboundRouteList{Items: make([]Did, 0, len(items)), NextCursor: next}
	for _, d := range items {
		list.Items = append(list.Items, toDid(d))
	}
	return ListInboundRoutes200JSONResponse(list), nil
}
