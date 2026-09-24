package api

import (
	"context"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/pbx"
)

func toExtension(e pbx.Extension) Extension {
	out := Extension{
		Id: e.ID, Number: e.Number, DisplayName: e.DisplayName, Enabled: e.Enabled,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, Etag: pbx.ETag(e.Version),
	}
	if e.Email != "" {
		out.Email = &e.Email
	}
	return out
}

func (s *Server) ListExtensions(ctx context.Context, req ListExtensionsRequestObject) (ListExtensionsResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(e pbx.Extension) uuid.UUID { return e.ID },
		func(before *uuid.UUID, limit int) ([]pbx.Extension, error) {
			return s.pbx.ListExtensions(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListExtensionsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := ExtensionList{Items: make([]Extension, 0, len(items)), NextCursor: next}
	for _, e := range items {
		list.Items = append(list.Items, toExtension(e))
	}
	return ListExtensions200JSONResponse(list), nil
}

func (s *Server) CreateExtension(ctx context.Context, req CreateExtensionRequestObject) (CreateExtensionResponseObject, error) {
	e, err := s.pbx.CreateExtension(ctx, pbx.ExtensionInput{
		Number: req.Body.Number, DisplayName: req.Body.DisplayName, Email: deref(req.Body.Email), Enabled: req.Body.Enabled,
	})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateExtensiondefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateExtension201JSONResponse(toExtension(e)), nil
}

func (s *Server) GetExtension(ctx context.Context, req GetExtensionRequestObject) (GetExtensionResponseObject, error) {
	e, err := s.pbx.GetExtension(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetExtensiondefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toExtension(e)
	return GetExtension200JSONResponse{Body: out, Headers: GetExtension200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateExtension(ctx context.Context, req UpdateExtensionRequestObject) (UpdateExtensionResponseObject, error) {
	e, err := s.pbx.UpdateExtension(ctx, req.Id, pbx.ExtensionPatch{
		Number: req.Body.Number, DisplayName: req.Body.DisplayName, Email: req.Body.Email, Enabled: req.Body.Enabled,
	}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateExtensiondefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toExtension(e)
	return UpdateExtension200JSONResponse{Body: out, Headers: UpdateExtension200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) DeleteExtension(ctx context.Context, req DeleteExtensionRequestObject) (DeleteExtensionResponseObject, error) {
	if err := s.pbx.DeleteExtension(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteExtensiondefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteExtension204Response{}, nil
}
