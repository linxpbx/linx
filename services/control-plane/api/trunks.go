package api

import (
	"cmp"
	"context"
	"net/http"
	"slices"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/trunk"
)

var invalidWireGuardProfileID = &apihttp.Error{Status: http.StatusUnprocessableEntity, Code: "wireguard_profile_id_invalid",
	Detail: "That isn't a valid WireGuard profile id."}

func toTrunkCodecs(codecs []string) []TrunkCodec {
	out := make([]TrunkCodec, len(codecs))
	for i, c := range codecs {
		out[i] = TrunkCodec(c)
	}
	return out
}

func fromTrunkCodecs(codecs *[]TrunkCodec) []string {
	if codecs == nil {
		return nil
	}
	out := make([]string, len(*codecs))
	for i, c := range *codecs {
		out[i] = string(c)
	}
	return out
}

func toTrunk(t trunk.Trunk) Trunk {
	out := Trunk{
		Id: t.ID, Name: t.Name, Kind: TrunkKind(t.Kind), Host: t.Host, Port: t.Port,
		Transport: TrunkTransport(t.Transport), MediaEncryption: TrunkMediaEncryption(t.MediaEncryption),
		CertTrust: TrunkCertTrust(t.CertTrust), DialFormat: TrunkDialFormat(t.DialFormat), Codecs: toTrunkCodecs(t.Codecs),
		MaxCalls: t.MaxCalls, OutboundPriority: t.OutboundPriority, Unencrypted: t.Unencrypted(),
		UnencryptedConfirmedAt: t.UnencryptedConfirmedAt, Enabled: t.Enabled,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, Etag: trunk.ETag(t.Version),
	}
	if t.Template != "" {
		out.Template = &t.Template
	}
	if t.PinnedCertificate != "" {
		out.PinnedCertificate = &t.PinnedCertificate
	}
	if t.Username != "" {
		out.Username = &t.Username
	}
	if t.CallerIDNumber != "" {
		out.CallerIdNumber = &t.CallerIDNumber
	}
	if t.WireGuardProfileID != nil {
		out.WireguardProfileId = t.WireGuardProfileID
	}
	if t.UnencryptedConfirmedBy != "" {
		out.UnencryptedConfirmedBy = &t.UnencryptedConfirmedBy
	}
	return out
}

func (s *Server) ListTrunks(ctx context.Context, req ListTrunksRequestObject) (ListTrunksResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(t trunk.Trunk) uuid.UUID { return t.ID },
		func(before *uuid.UUID, limit int) ([]trunk.Trunk, error) {
			return s.trunks.ListTrunks(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListTrunksdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := TrunkList{Items: make([]Trunk, 0, len(items)), NextCursor: next}
	for _, t := range items {
		list.Items = append(list.Items, toTrunk(t))
	}
	return ListTrunks200JSONResponse(list), nil
}

func (s *Server) CreateTrunk(ctx context.Context, req CreateTrunkRequestObject) (CreateTrunkResponseObject, error) {
	in := trunk.TrunkInput{
		Name: req.Body.Name, Kind: string(req.Body.Kind), Host: req.Body.Host, Port: req.Body.Port,
		Username: deref(req.Body.Username), Password: deref(req.Body.Password),
		CallerIDNumber: deref(req.Body.CallerIdNumber), MaxCalls: req.Body.MaxCalls,
		ConfirmUnencrypted: deref(req.Body.ConfirmUnencrypted), Enabled: req.Body.Enabled,
		Codecs: fromTrunkCodecs(req.Body.Codecs),
	}
	if req.Body.Template != nil {
		in.Template = *req.Body.Template
	}
	if req.Body.Transport != nil {
		in.Transport = string(*req.Body.Transport)
	}
	if req.Body.MediaEncryption != nil {
		in.MediaEncryption = string(*req.Body.MediaEncryption)
	}
	if req.Body.CertTrust != nil {
		in.CertTrust = string(*req.Body.CertTrust)
	}
	if req.Body.PinnedCertificate != nil {
		in.PinnedCertificate = *req.Body.PinnedCertificate
	}
	if req.Body.DialFormat != nil {
		in.DialFormat = string(*req.Body.DialFormat)
	}
	if req.Body.WireguardProfileId != nil {
		id := *req.Body.WireguardProfileId
		in.WireGuardProfileID = &id
	}
	t, err := s.trunks.CreateTrunk(ctx, in)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateTrunkdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateTrunk201JSONResponse(toTrunk(t)), nil
}

func (s *Server) GetTrunk(ctx context.Context, req GetTrunkRequestObject) (GetTrunkResponseObject, error) {
	t, err := s.trunks.GetTrunk(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetTrunkdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toTrunk(t)
	return GetTrunk200JSONResponse{Body: out, Headers: GetTrunk200ResponseHeaders{ETag: &out.Etag}}, nil
}

// wireguardProfilePatch turns TrunkPatch's empty-string-clears convention
// into the **uuid.UUID tri-state trunk.TrunkPatch needs: nil (not sent),
// pointer-to-nil (clear), or pointer-to-value.
func wireguardProfilePatch(s *string) (**uuid.UUID, error) {
	if s == nil {
		return nil, nil
	}
	if *s == "" {
		var cleared *uuid.UUID
		return &cleared, nil
	}
	id, err := uuid.Parse(*s)
	if err != nil {
		return nil, err
	}
	value := &id
	return &value, nil
}

func (s *Server) UpdateTrunk(ctx context.Context, req UpdateTrunkRequestObject) (UpdateTrunkResponseObject, error) {
	wgProfile, err := wireguardProfilePatch(req.Body.WireguardProfileId)
	if err != nil {
		return UpdateTrunkdefaultApplicationProblemPlusJSONResponse{StatusCode: invalidWireGuardProfileID.Status, Body: problem(invalidWireGuardProfileID)}, nil
	}
	patch := trunk.TrunkPatch{
		Name: req.Body.Name, Host: req.Body.Host, Port: req.Body.Port, Username: req.Body.Username,
		Password: req.Body.Password, CallerIDNumber: req.Body.CallerIdNumber, MaxCalls: req.Body.MaxCalls,
		PinnedCertificate: req.Body.PinnedCertificate, ConfirmUnencrypted: req.Body.ConfirmUnencrypted,
		Enabled: req.Body.Enabled, Codecs: fromTrunkCodecs(req.Body.Codecs), WireGuardProfileID: wgProfile,
	}
	if req.Body.Transport != nil {
		v := string(*req.Body.Transport)
		patch.Transport = &v
	}
	if req.Body.MediaEncryption != nil {
		v := string(*req.Body.MediaEncryption)
		patch.MediaEncryption = &v
	}
	if req.Body.CertTrust != nil {
		v := string(*req.Body.CertTrust)
		patch.CertTrust = &v
	}
	if req.Body.DialFormat != nil {
		v := string(*req.Body.DialFormat)
		patch.DialFormat = &v
	}
	t, err := s.trunks.UpdateTrunk(ctx, req.Id, patch, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateTrunkdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toTrunk(t)
	return UpdateTrunk200JSONResponse{Body: out, Headers: UpdateTrunk200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) DeleteTrunk(ctx context.Context, req DeleteTrunkRequestObject) (DeleteTrunkResponseObject, error) {
	if err := s.trunks.DeleteTrunk(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteTrunkdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteTrunk204Response{}, nil
}

func (s *Server) GetOutboundRouting(ctx context.Context, _ GetOutboundRoutingRequestObject) (GetOutboundRoutingResponseObject, error) {
	items, err := s.trunks.ListTrunks(ctx, nil, apihttp.MaxPageLimit)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetOutboundRoutingdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	country, err := s.numbering.Country(ctx)
	if err != nil {
		return nil, err
	}
	return GetOutboundRouting200JSONResponse(toOutboundRouting(country, items)), nil
}

func toOutboundRouting(country string, items []trunk.Trunk) OutboundRouting {
	ordered := make([]trunk.Trunk, 0, len(items))
	for _, t := range items {
		if t.OutboundPriority != nil {
			ordered = append(ordered, t)
		}
	}
	slices.SortFunc(ordered, func(a, b trunk.Trunk) int { return cmp.Compare(*a.OutboundPriority, *b.OutboundPriority) })
	out := OutboundRouting{Country: country, Trunks: make([]Trunk, 0, len(ordered))}
	for _, t := range ordered {
		out.Trunks = append(out.Trunks, toTrunk(t))
	}
	return out
}

func (s *Server) SetOutboundRouting(ctx context.Context, req SetOutboundRoutingRequestObject) (SetOutboundRoutingResponseObject, error) {
	order := make([]uuid.UUID, len(req.Body.Order))
	copy(order, req.Body.Order)
	items, err := s.trunks.SetOutboundOrder(ctx, order)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return SetOutboundRoutingdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	country, err := s.numbering.Country(ctx)
	if err != nil {
		return nil, err
	}
	return SetOutboundRouting200JSONResponse(toOutboundRouting(country, items)), nil
}
