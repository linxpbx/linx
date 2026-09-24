package api

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/pbx"
)

// sipPort is the PJSIP TLS transport's bind port (docs/PBX.md §2), the same
// value internal/asteriskconf.Config.SIPPort renders.
const sipPort = 5061

func toDevice(d pbx.Device) Device {
	out := Device{
		Id: d.ID, ExtensionId: d.ExtensionID, Name: d.Name, Kind: DeviceKind(d.Kind), SipUsername: d.SIPUsername,
		Enabled: d.Enabled, LastRegisteredAt: d.LastRegisteredAt, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		Etag: pbx.ETag(d.Version),
	}
	if d.LastRegisteredFrom != nil {
		s := d.LastRegisteredFrom.String()
		out.LastRegisteredFrom = &s
	}
	return out
}

func (s *Server) toCredentials(c pbx.Credentials) DeviceCredentials {
	d := toDevice(c.Device)
	server := s.pbx.SIPServer()
	settings := fmt.Sprintf(
		"Server: %s\nPort: %d\nTransport: TLS\nUsername: %s\nPassword: %s\nEncrypted audio: required (SRTP)",
		server, sipPort, d.SipUsername, c.Password)
	return DeviceCredentials{
		Device: d, Password: c.Password, Server: server, Port: sipPort, Transport: "tls", SettingsText: settings,
	}
}

func (s *Server) ListExtensionDevices(ctx context.Context, req ListExtensionDevicesRequestObject) (ListExtensionDevicesResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(d pbx.Device) uuid.UUID { return d.ID },
		func(before *uuid.UUID, limit int) ([]pbx.Device, error) {
			return s.pbx.ListDevices(ctx, req.Id, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListExtensionDevicesdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := DeviceList{Items: make([]Device, 0, len(items)), NextCursor: next}
	for _, d := range items {
		list.Items = append(list.Items, toDevice(d))
	}
	return ListExtensionDevices200JSONResponse(list), nil
}

func (s *Server) CreateDevice(ctx context.Context, req CreateDeviceRequestObject) (CreateDeviceResponseObject, error) {
	var kind string
	if req.Body.Kind != nil {
		kind = string(*req.Body.Kind)
	}
	c, err := s.pbx.CreateDevice(ctx, req.Id, pbx.DeviceInput{Name: req.Body.Name, Kind: kind, Enabled: req.Body.Enabled})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateDevicedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateDevice201JSONResponse(s.toCredentials(c)), nil
}

func (s *Server) GetDevice(ctx context.Context, req GetDeviceRequestObject) (GetDeviceResponseObject, error) {
	d, err := s.pbx.GetDevice(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetDevicedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toDevice(d)
	return GetDevice200JSONResponse{Body: out, Headers: GetDevice200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateDevice(ctx context.Context, req UpdateDeviceRequestObject) (UpdateDeviceResponseObject, error) {
	d, err := s.pbx.UpdateDevice(ctx, req.Id, pbx.DevicePatch{Name: req.Body.Name, Enabled: req.Body.Enabled}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateDevicedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toDevice(d)
	return UpdateDevice200JSONResponse{Body: out, Headers: UpdateDevice200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) RevokeDevice(ctx context.Context, req RevokeDeviceRequestObject) (RevokeDeviceResponseObject, error) {
	if err := s.pbx.RevokeDevice(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RevokeDevicedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RevokeDevice204Response{}, nil
}

func (s *Server) ResetDevicePassword(ctx context.Context, req ResetDevicePasswordRequestObject) (ResetDevicePasswordResponseObject, error) {
	c, err := s.pbx.ResetDevicePassword(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ResetDevicePassworddefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ResetDevicePassword200JSONResponse(s.toCredentials(c)), nil
}
