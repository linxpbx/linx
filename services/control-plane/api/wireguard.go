package api

import (
	"context"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/trunk"
)

func toWireguardProfile(w trunk.WireGuardProfile) WireguardProfile {
	return WireguardProfile{
		Id: w.ID, Name: w.Name, Address: w.Address, PublicKey: w.PublicKey, PeerPublicKey: w.PeerPublicKey,
		PeerEndpointHost: w.PeerEndpointHost, PeerEndpointPort: w.PeerEndpointPort, PersistentKeepalive: w.PersistentKeepalive,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, Etag: trunk.ETag(w.Version),
	}
}

func (s *Server) ListWireguardProfiles(ctx context.Context, req ListWireguardProfilesRequestObject) (ListWireguardProfilesResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(w trunk.WireGuardProfile) uuid.UUID { return w.ID },
		func(before *uuid.UUID, limit int) ([]trunk.WireGuardProfile, error) {
			return s.trunks.ListWireGuardProfiles(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListWireguardProfilesdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := WireguardProfileList{Items: make([]WireguardProfile, 0, len(items)), NextCursor: next}
	for _, w := range items {
		list.Items = append(list.Items, toWireguardProfile(w))
	}
	return ListWireguardProfiles200JSONResponse(list), nil
}

func (s *Server) CreateWireguardProfile(ctx context.Context, req CreateWireguardProfileRequestObject) (CreateWireguardProfileResponseObject, error) {
	in := trunk.WireGuardProfileInput{Name: req.Body.Name, Config: deref(req.Body.Config), Fields: trunk.WireGuardFields{
		PrivateKey: deref(req.Body.PrivateKey), Address: deref(req.Body.Address), PeerPublicKey: deref(req.Body.PeerPublicKey),
		PeerEndpointHost: deref(req.Body.PeerEndpointHost), PeerEndpointPort: req.Body.PeerEndpointPort,
		PresharedKey: deref(req.Body.PresharedKey), PersistentKeepalive: req.Body.PersistentKeepalive,
	}}
	w, err := s.trunks.CreateWireGuardProfile(ctx, in)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateWireguardProfiledefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateWireguardProfile201JSONResponse(toWireguardProfile(w)), nil
}

func (s *Server) GetWireguardProfile(ctx context.Context, req GetWireguardProfileRequestObject) (GetWireguardProfileResponseObject, error) {
	w, err := s.trunks.GetWireGuardProfile(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetWireguardProfiledefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toWireguardProfile(w)
	return GetWireguardProfile200JSONResponse{Body: out, Headers: GetWireguardProfile200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateWireguardProfile(ctx context.Context, req UpdateWireguardProfileRequestObject) (UpdateWireguardProfileResponseObject, error) {
	w, err := s.trunks.UpdateWireGuardProfile(ctx, req.Id, trunk.WireGuardProfilePatch{
		Name: req.Body.Name, PeerEndpointHost: req.Body.PeerEndpointHost, PeerEndpointPort: req.Body.PeerEndpointPort,
		PersistentKeepalive: req.Body.PersistentKeepalive,
	}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateWireguardProfiledefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toWireguardProfile(w)
	return UpdateWireguardProfile200JSONResponse{Body: out, Headers: UpdateWireguardProfile200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) DeleteWireguardProfile(ctx context.Context, req DeleteWireguardProfileRequestObject) (DeleteWireguardProfileResponseObject, error) {
	if err := s.trunks.DeleteWireGuardProfile(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteWireguardProfiledefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteWireguardProfile204Response{}, nil
}
