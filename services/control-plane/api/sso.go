package api

import (
	"context"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/sso"
)

// Company sign-in (ADR-052, docs/ADMIN.md §6, §9): providers, and people's
// linked accounts. Starting a flow and the provider's callback set cookies,
// so they're hand-written (services/control-plane/sso.go).

func (s *Server) toSsoProvider(p sso.Provider) SsoProvider {
	return SsoProvider{
		Id: p.ID, Kind: SsoProviderKind(p.Kind), Name: p.Name, Issuer: p.Issuer, ClientId: p.ClientID,
		ClientSecretSet: len(p.ClientSecretEnc) > 0, Enabled: p.Enabled, Shown: p.Shown, Position: p.Position,
		RedirectUri: s.sso.RedirectURI(), CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Etag: auth.ETag(p.Version),
	}
}

func (s *Server) ListSsoProviders(ctx context.Context, _ ListSsoProvidersRequestObject) (ListSsoProvidersResponseObject, error) {
	all, err := s.sso.List(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListSsoProvidersdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := SsoProviderList{Items: make([]SsoProvider, 0, len(all)), RedirectUri: s.sso.RedirectURI()}
	for _, p := range all {
		list.Items = append(list.Items, s.toSsoProvider(p))
	}
	return ListSsoProviders200JSONResponse(list), nil
}

func (s *Server) CreateSsoProvider(ctx context.Context, req CreateSsoProviderRequestObject) (CreateSsoProviderResponseObject, error) {
	b := req.Body
	p, err := s.sso.Create(ctx, sso.ProviderInput{
		Kind: string(b.Kind), Name: deref(b.Name), Issuer: deref(b.Issuer), ClientID: b.ClientId, ClientSecret: deref(b.ClientSecret),
		Enabled: b.Enabled, Shown: b.Shown, Position: b.Position,
	})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateSsoProviderdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := s.toSsoProvider(p)
	return CreateSsoProvider201JSONResponse{Body: out, Headers: CreateSsoProvider201ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) GetSsoProvider(ctx context.Context, req GetSsoProviderRequestObject) (GetSsoProviderResponseObject, error) {
	p, err := s.sso.Get(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetSsoProviderdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := s.toSsoProvider(p)
	return GetSsoProvider200JSONResponse{Body: out, Headers: GetSsoProvider200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateSsoProvider(ctx context.Context, req UpdateSsoProviderRequestObject) (UpdateSsoProviderResponseObject, error) {
	b := req.Body
	p, err := s.sso.Update(ctx, req.Id, sso.ProviderPatch{
		Name: b.Name, Issuer: b.Issuer, ClientID: b.ClientId, ClientSecret: b.ClientSecret,
		Enabled: b.Enabled, Shown: b.Shown, Position: b.Position,
	}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateSsoProviderdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := s.toSsoProvider(p)
	return UpdateSsoProvider200JSONResponse{Body: out, Headers: UpdateSsoProvider200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) DeleteSsoProvider(ctx context.Context, req DeleteSsoProviderRequestObject) (DeleteSsoProviderResponseObject, error) {
	if err := s.sso.Delete(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteSsoProviderdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteSsoProvider204Response{}, nil
}

func toCompanyLinks(links []auth.CompanyLinkInfo) []CompanyLink {
	out := make([]CompanyLink, 0, len(links))
	for _, l := range links {
		out = append(out, CompanyLink{Id: l.ID, ProviderId: l.ProviderID, ProviderName: l.ProviderName, Email: l.Email,
			CreatedAt: l.CreatedAt, LastUsedAt: l.LastUsedAt})
	}
	return out
}

// ToCompanyButtons renders sign-in buttons (also for the hand-written
// GET /sign-in-options).
func ToCompanyButtons(bs []sso.SignInButton) []CompanyButton {
	out := make([]CompanyButton, 0, len(bs))
	for _, b := range bs {
		out = append(out, CompanyButton{Id: b.ID, Kind: SsoProviderKind(b.Kind), Name: b.Name})
	}
	return out
}

func (s *Server) ListMyCompanyLinks(ctx context.Context, _ ListMyCompanyLinksRequestObject) (ListMyCompanyLinksResponseObject, error) {
	links, err := s.accounts.MyCompanyLinks(ctx)
	var available []sso.SignInButton
	if err == nil {
		available, err = s.sso.Linkable(ctx)
	}
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListMyCompanyLinksdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ListMyCompanyLinks200JSONResponse{Items: toCompanyLinks(links), Available: ToCompanyButtons(available)}, nil
}

func (s *Server) UnlinkMyCompanyAccount(ctx context.Context, req UnlinkMyCompanyAccountRequestObject) (UnlinkMyCompanyAccountResponseObject, error) {
	if err := s.accounts.UnlinkMyCompany(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UnlinkMyCompanyAccountdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return UnlinkMyCompanyAccount204Response{}, nil
}

func (s *Server) ListUserCompanyLinks(ctx context.Context, req ListUserCompanyLinksRequestObject) (ListUserCompanyLinksResponseObject, error) {
	links, err := s.accounts.UserCompanyLinks(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListUserCompanyLinksdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ListUserCompanyLinks200JSONResponse{Items: toCompanyLinks(links)}, nil
}

func (s *Server) UnlinkUserCompanyAccount(ctx context.Context, req UnlinkUserCompanyAccountRequestObject) (UnlinkUserCompanyAccountResponseObject, error) {
	if err := s.accounts.UnlinkUserCompany(ctx, req.Id, req.LinkId); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UnlinkUserCompanyAccountdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return UnlinkUserCompanyAccount204Response{}, nil
}
