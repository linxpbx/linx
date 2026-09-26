package api

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
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
		Status: TrunkStatus(t.Status), StatusDetail: t.StatusDetail, StatusSince: t.StatusSince,
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
	out, err := s.outboundRouting(ctx, items)
	if err != nil {
		return nil, err
	}
	return GetOutboundRouting200JSONResponse(out), nil
}

func (s *Server) outboundRouting(ctx context.Context, items []trunk.Trunk) (OutboundRouting, error) {
	country, err := s.numbering.Country(ctx)
	if err != nil {
		return OutboundRouting{}, err
	}
	minutes, calls, err := s.trunks.InternationalAlert(ctx)
	if err != nil {
		return OutboundRouting{}, err
	}
	out := toOutboundRouting(country, items)
	out.InternationalAlert = InternationalAlert{Minutes: minutes, Calls: calls}
	return out, nil
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
	if a := req.Body.InternationalAlert; a != nil {
		if err := s.trunks.SetInternationalAlert(ctx, a.Minutes, a.Calls); err != nil {
			e, err := apiError(err)
			if e == nil {
				return nil, err
			}
			return SetOutboundRoutingdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
		}
	}
	items, err := s.trunks.SetOutboundOrder(ctx, order)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return SetOutboundRoutingdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out, err := s.outboundRouting(ctx, items)
	if err != nil {
		return nil, err
	}
	return SetOutboundRouting200JSONResponse(out), nil
}

func (s *Server) TestTrunk(ctx context.Context, req TestTrunkRequestObject) (TestTrunkResponseObject, error) {
	res, err := s.trunks.TestTrunk(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return TestTrunkdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	// trunkprobe.Result's JSON is the API's TrunkTest.
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var out TrunkTest
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out.Steps == nil {
		out.Steps = []struct {
			Name   TrunkTestStepsName   `json:"name"`
			Result TrunkTestStepsResult `json:"result"`
			Words  string               `json:"words"`
		}{}
	}
	return TestTrunk200JSONResponse(out), nil
}

func (s *Server) TestRoute(ctx context.Context, req TestRouteRequestObject) (TestRouteResponseObject, error) {
	fail := func(e *apihttp.Error) (TestRouteResponseObject, error) {
		return TestRoutedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("no principal on the request")
	}
	country, err := s.numbering.Country(ctx)
	if err != nil {
		return nil, err
	}
	if req.Body.From == nil || *req.Body.From == "" {
		r, err := s.numbering.Classify(ctx, country, req.Body.Number)
		if err != nil {
			return nil, err
		}
		words := fmt.Sprintf("%s: %s.", r.Kind(), r.Pretty())
		if r.Category == numbering.Invalid {
			words = fmt.Sprintf("%q isn't a number that can be called from %s.", req.Body.Number, numbering.CountryName(country, numbering.Countries[country]))
		}
		return TestRoute200JSONResponse(routeTest(numbering.Route{Result: r}, false, words)), nil
	}
	ext, err := s.numbering.ExtensionByNumber(ctx, p.TenantID, *req.Body.From)
	if errors.Is(err, pbx.ErrNotFound) {
		return fail(&apihttp.Error{Status: http.StatusUnprocessableEntity, Code: "extension_not_found", Detail: "There is no extension " + *req.Body.From + "."})
	}
	if err != nil {
		return nil, err
	}
	r, err := s.numbering.Route(ctx, ext.ID, req.Body.Number)
	if err != nil {
		return nil, err
	}
	return TestRoute200JSONResponse(routeTest(r, true, strings.TrimSpace(r.Explain(req.Body.Number, *req.Body.From, country)))), nil
}

func routeTest(r numbering.Route, decided bool, words string) RouteTest {
	out := RouteTest{Category: RouteTestCategory(r.Category), Kind: r.Kind(), E164: optString(r.E164), Region: optString(r.Region), Words: words}
	if decided {
		reason := RouteTestReason(r.Reason)
		out.Allowed, out.Reason = &r.Allowed, &reason
		lines := make([]struct {
			CallerId *string `json:"caller_id,omitempty"`
			Number   string  `json:"number"`
			Trunk    string  `json:"trunk"`
		}, 0, len(r.Lines))
		for _, l := range r.Lines {
			lines = append(lines, struct {
				CallerId *string `json:"caller_id,omitempty"`
				Number   string  `json:"number"`
				Trunk    string  `json:"trunk"`
			}{optString(l.CallerID), l.Number, l.Trunk})
		}
		out.Lines = &lines
	}
	return out
}
