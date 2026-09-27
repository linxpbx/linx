package api

import (
	"context"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
)

func toUser(u auth.User) User {
	out := User{
		Id: u.ID, Email: u.Email, Name: u.Name, Role: Role(u.Role), MfaEnabled: u.MFAEnabled,
		Passkeys: &u.PasskeyCount, HasPassword: ptr(u.HasPassword()), PasswordOnly: ptr(!u.HasSecondStep()),
		Disabled: u.DisabledAt != nil, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt, Etag: auth.ETag(u.Version),
	}
	if u.ExtensionID != nil {
		out.ExtensionId = u.ExtensionID
	}
	return out
}

func (s *Server) ListUsers(ctx context.Context, req ListUsersRequestObject) (ListUsersResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(u auth.User) uuid.UUID { return u.ID },
		func(before *uuid.UUID, limit int) ([]auth.User, error) {
			return s.accounts.ListUsers(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListUsersdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := UserList{Items: make([]User, 0, len(items)), NextCursor: next}
	for _, u := range items {
		list.Items = append(list.Items, toUser(u))
	}
	return ListUsers200JSONResponse(list), nil
}

func (s *Server) CreateUser(ctx context.Context, req CreateUserRequestObject) (CreateUserResponseObject, error) {
	u, token, err := s.accounts.CreateUser(ctx, auth.UserInput{
		Email: req.Body.Email, Name: req.Body.Name, Role: string(req.Body.Role), ExtensionID: req.Body.ExtensionId,
	})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateUserdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateUser201JSONResponse{User: toUser(u), SetupLinkToken: token}, nil
}

func (s *Server) GetUser(ctx context.Context, req GetUserRequestObject) (GetUserResponseObject, error) {
	u, err := s.accounts.GetUser(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetUserdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toUser(u)
	return GetUser200JSONResponse{Body: out, Headers: GetUser200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateUser(ctx context.Context, req UpdateUserRequestObject) (UpdateUserResponseObject, error) {
	patch := auth.UserPatch{Name: req.Body.Name, ExtensionID: req.Body.ExtensionId, Disabled: req.Body.Disabled}
	if req.Body.Role != nil {
		role := string(*req.Body.Role)
		patch.Role = &role
	}
	u, err := s.accounts.UpdateUser(ctx, req.Id, patch, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateUserdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toUser(u)
	return UpdateUser200JSONResponse{Body: out, Headers: UpdateUser200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) ResetUserMfa(ctx context.Context, req ResetUserMfaRequestObject) (ResetUserMfaResponseObject, error) {
	u, err := s.accounts.ResetMFA(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ResetUserMfadefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ResetUserMfa200JSONResponse(toUser(u)), nil
}

func (s *Server) DisableUser(ctx context.Context, req DisableUserRequestObject) (DisableUserResponseObject, error) {
	if _, err := s.accounts.DisableUser(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DisableUserdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DisableUser204Response{}, nil
}

func (s *Server) CreateUserSetupLink(ctx context.Context, req CreateUserSetupLinkRequestObject) (CreateUserSetupLinkResponseObject, error) {
	token, err := s.accounts.CreateSetupLink(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateUserSetupLinkdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateUserSetupLink200JSONResponse{SetupLinkToken: token}, nil
}

func (s *Server) ChangeMyPassword(ctx context.Context, req ChangeMyPasswordRequestObject) (ChangeMyPasswordResponseObject, error) {
	if err := s.accounts.ChangePassword(ctx, deref(req.Body.CurrentPassword), req.Body.NewPassword); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ChangeMyPassworddefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ChangeMyPassword204Response{}, nil
}

func (s *Server) BeginMyMfaEnrollment(ctx context.Context, _ BeginMyMfaEnrollmentRequestObject) (BeginMyMfaEnrollmentResponseObject, error) {
	secret, uri, err := s.accounts.BeginMFAEnrollment(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return BeginMyMfaEnrollmentdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return BeginMyMfaEnrollment200JSONResponse{Secret: secret, OtpauthUrl: uri}, nil
}

func (s *Server) ConfirmMyMfaEnrollment(ctx context.Context, req ConfirmMyMfaEnrollmentRequestObject) (ConfirmMyMfaEnrollmentResponseObject, error) {
	codes, err := s.accounts.ConfirmMFAEnrollment(ctx, req.Body.Code)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ConfirmMyMfaEnrollmentdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ConfirmMyMfaEnrollment200JSONResponse{RecoveryCodes: codes}, nil
}

func ptr[T any](v T) *T { return &v }

// ToPasskey is a passkey as the API shows it: never its key or counter.
func ToPasskey(p auth.Passkey) Passkey {
	return Passkey{Id: p.ID, Name: p.Name, Synced: p.BackupState, CreatedAt: p.CreatedAt, LastUsedAt: p.LastUsedAt}
}

func (s *Server) ListMyPasskeys(ctx context.Context, _ ListMyPasskeysRequestObject) (ListMyPasskeysResponseObject, error) {
	keys, err := s.accounts.ListPasskeys(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListMyPasskeysdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := PasskeyList{Items: make([]Passkey, 0, len(keys))}
	for _, k := range keys {
		list.Items = append(list.Items, ToPasskey(k))
	}
	return ListMyPasskeys200JSONResponse(list), nil
}

func (s *Server) RenameMyPasskey(ctx context.Context, req RenameMyPasskeyRequestObject) (RenameMyPasskeyResponseObject, error) {
	p, err := s.accounts.RenamePasskey(ctx, req.Id, req.Body.Name)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RenameMyPasskeydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RenameMyPasskey200JSONResponse(ToPasskey(p)), nil
}

func (s *Server) RemoveMyPasskey(ctx context.Context, req RemoveMyPasskeyRequestObject) (RemoveMyPasskeyResponseObject, error) {
	if err := s.accounts.RemovePasskey(ctx, req.Id, deref(req.Params.AcceptPasswordOnly)); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RemoveMyPasskeydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RemoveMyPasskey204Response{}, nil
}

func (s *Server) AcceptPasswordOnly(ctx context.Context, _ AcceptPasswordOnlyRequestObject) (AcceptPasswordOnlyResponseObject, error) {
	if err := s.accounts.AcceptPasswordOnly(ctx); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return AcceptPasswordOnlydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return AcceptPasswordOnly204Response{}, nil
}
