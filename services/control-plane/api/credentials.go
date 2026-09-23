package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
)

// CredentialStore is the database access the API key and OAuth client
// endpoints need (internal/store implements it).
type CredentialStore interface {
	CreateCredential(ctx context.Context, c auth.Credential, audit auth.AuditEntry) error
	TenantCredential(ctx context.Context, kind string, tenant, id uuid.UUID) (auth.Credential, error)
	ListCredentials(ctx context.Context, kind string, tenant uuid.UUID, before *uuid.UUID, limit int) ([]auth.Credential, error)
	RevokeCredential(ctx context.Context, kind string, tenant, id uuid.UUID, at time.Time, audit auth.AuditEntry) (auth.Credential, error)
}

var errNoPrincipal = errors.New("no principal on the request: authentication middleware is missing")

func problem(e *apihttp.Error) Problem {
	return Problem{Type: "about:blank", Title: http.StatusText(e.Status), Status: e.Status, Code: e.Code, Detail: e.Detail}
}

func notFound(kind string) *apihttp.Error {
	what := "API key"
	if kind == auth.TypeOAuthClient {
		what = "OAuth client"
	}
	return &apihttp.Error{Status: http.StatusNotFound, Code: "not_found", Detail: "There is no " + what + " with that id."}
}

var errCursorInvalid = &apihttp.Error{Status: http.StatusBadRequest, Code: "cursor_invalid",
	Detail: "That cursor is not valid; start again without one."}

func (s *Server) createCredential(ctx context.Context, kind string, body *CredentialCreate) (auth.Credential, string, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Credential{}, "", errNoPrincipal
	}
	req := auth.CredentialRequest{Kind: kind, Name: body.Name, Scopes: body.Scopes, ExpiresAt: body.ExpiresAt}
	if body.Role != nil {
		req.Role = string(*body.Role)
	}
	if body.AllowedIps != nil {
		req.AllowedIPs = *body.AllowedIps
	}
	cred, secret, err := auth.NewCredential(p, req, s.now())
	if err != nil {
		return auth.Credential{}, "", err
	}
	audit := auth.AuditEntry{
		TenantID: &p.TenantID,
		Actor:    p.Actor(),
		IP:       auth.ClientIPFromContext(ctx),
		Action:   kind + ".create",
		Target:   kind + ":" + cred.ID.String(),
		Result:   auth.ResultOK,
		Detail: map[string]any{
			"name": cred.Name, "role": cred.Role, "scopes": cred.Scopes,
			"expires_at": cred.ExpiresAt, "allowed_ips": ipStrings(cred),
		},
	}
	if err := s.store.CreateCredential(ctx, cred, audit); err != nil {
		return auth.Credential{}, "", fmt.Errorf("creating %s: %w", kind, err)
	}
	return cred, secret, nil
}

func (s *Server) getCredential(ctx context.Context, kind string, id uuid.UUID) (auth.Credential, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Credential{}, errNoPrincipal
	}
	c, err := s.store.TenantCredential(ctx, kind, p.TenantID, id)
	if errors.Is(err, auth.ErrNotFound) {
		return auth.Credential{}, notFound(kind)
	}
	return c, err
}

func (s *Server) listCredentials(ctx context.Context, kind string, limit *Limit, cursor *Cursor) ([]auth.Credential, *string, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return nil, nil, errNoPrincipal
	}
	return page(limit, cursor, func(c auth.Credential) uuid.UUID { return c.ID },
		func(before *uuid.UUID, limit int) ([]auth.Credential, error) {
			return s.store.ListCredentials(ctx, kind, p.TenantID, before, limit)
		})
}

func (s *Server) revokeCredential(ctx context.Context, kind string, id uuid.UUID) error {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return errNoPrincipal
	}
	audit := auth.AuditEntry{
		TenantID: &p.TenantID,
		Actor:    p.Actor(),
		IP:       auth.ClientIPFromContext(ctx),
		Action:   kind + ".revoke",
		Target:   kind + ":" + id.String(),
		Result:   auth.ResultOK,
	}
	_, err := s.store.RevokeCredential(ctx, kind, p.TenantID, id, s.now(), audit)
	if errors.Is(err, auth.ErrNotFound) {
		return notFound(kind)
	}
	return err
}

// apiError splits a handler error into a caller-facing problem, or an
// unexpected error for the strict server's 500 handler.
func apiError(err error) (*apihttp.Error, error) {
	var e *apihttp.Error
	if errors.As(err, &e) {
		return e, nil
	}
	return nil, err
}

func ipStrings(c auth.Credential) []string {
	out := make([]string, 0, len(c.AllowedIPs))
	for _, p := range c.AllowedIPs {
		if p.IsSingleIP() {
			out = append(out, p.Addr().String())
		} else {
			out = append(out, p.String())
		}
	}
	return out
}

func lastUsedIP(c auth.Credential) *string {
	if c.LastUsedIP == nil {
		return nil
	}
	s := c.LastUsedIP.String()
	return &s
}

func toAPIKey(c auth.Credential) ApiKey {
	return ApiKey{
		Id:         c.ID,
		Name:       c.Name,
		Prefix:     auth.APIKeyPrefix + c.PublicID,
		Role:       Role(c.Role),
		Scopes:     c.Scopes,
		AllowedIps: ipStrings(c),
		CreatedBy:  c.CreatedBy,
		CreatedAt:  c.CreatedAt,
		ExpiresAt:  c.ExpiresAt,
		LastUsedAt: c.LastUsedAt,
		LastUsedIp: lastUsedIP(c),
		RevokedAt:  c.RevokedAt,
	}
}

func toOAuthClient(c auth.Credential) OAuthClient {
	return OAuthClient{
		Id:         c.ID,
		ClientId:   c.PublicID,
		Name:       c.Name,
		Role:       Role(c.Role),
		Scopes:     c.Scopes,
		AllowedIps: ipStrings(c),
		CreatedBy:  c.CreatedBy,
		CreatedAt:  c.CreatedAt,
		ExpiresAt:  c.ExpiresAt,
		LastUsedAt: c.LastUsedAt,
		LastUsedIp: lastUsedIP(c),
		RevokedAt:  c.RevokedAt,
	}
}

func (s *Server) ListApiKeys(ctx context.Context, req ListApiKeysRequestObject) (ListApiKeysResponseObject, error) {
	items, next, err := s.listCredentials(ctx, auth.TypeAPIKey, req.Params.Limit, req.Params.Cursor)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListApiKeysdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := ApiKeyList{Items: make([]ApiKey, 0, len(items)), NextCursor: next}
	for _, c := range items {
		list.Items = append(list.Items, toAPIKey(c))
	}
	return ListApiKeys200JSONResponse(list), nil
}

func (s *Server) CreateApiKey(ctx context.Context, req CreateApiKeyRequestObject) (CreateApiKeyResponseObject, error) {
	cred, key, err := s.createCredential(ctx, auth.TypeAPIKey, req.Body)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateApiKeydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateApiKey201JSONResponse{ApiKey: toAPIKey(cred), Key: key}, nil
}

func (s *Server) GetApiKey(ctx context.Context, req GetApiKeyRequestObject) (GetApiKeyResponseObject, error) {
	cred, err := s.getCredential(ctx, auth.TypeAPIKey, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetApiKeydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return GetApiKey200JSONResponse(toAPIKey(cred)), nil
}

func (s *Server) RevokeApiKey(ctx context.Context, req RevokeApiKeyRequestObject) (RevokeApiKeyResponseObject, error) {
	if err := s.revokeCredential(ctx, auth.TypeAPIKey, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RevokeApiKeydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RevokeApiKey204Response{}, nil
}

func (s *Server) ListOauthClients(ctx context.Context, req ListOauthClientsRequestObject) (ListOauthClientsResponseObject, error) {
	items, next, err := s.listCredentials(ctx, auth.TypeOAuthClient, req.Params.Limit, req.Params.Cursor)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListOauthClientsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := OAuthClientList{Items: make([]OAuthClient, 0, len(items)), NextCursor: next}
	for _, c := range items {
		list.Items = append(list.Items, toOAuthClient(c))
	}
	return ListOauthClients200JSONResponse(list), nil
}

func (s *Server) CreateOauthClient(ctx context.Context, req CreateOauthClientRequestObject) (CreateOauthClientResponseObject, error) {
	cred, secret, err := s.createCredential(ctx, auth.TypeOAuthClient, req.Body)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateOauthClientdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateOauthClient201JSONResponse{OauthClient: toOAuthClient(cred), ClientSecret: secret}, nil
}

func (s *Server) GetOauthClient(ctx context.Context, req GetOauthClientRequestObject) (GetOauthClientResponseObject, error) {
	cred, err := s.getCredential(ctx, auth.TypeOAuthClient, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetOauthClientdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return GetOauthClient200JSONResponse(toOAuthClient(cred)), nil
}

func (s *Server) RevokeOauthClient(ctx context.Context, req RevokeOauthClientRequestObject) (RevokeOauthClientResponseObject, error) {
	if err := s.revokeCredential(ctx, auth.TypeOAuthClient, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RevokeOauthClientdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RevokeOauthClient204Response{}, nil
}
