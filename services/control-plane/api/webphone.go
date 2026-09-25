package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/turn"
)

// WebPhonePath is POST /me/web-phone's full path.
const WebPhonePath = "/api/v1/me/web-phone"

// SIPPath is where browsers open their phone line's websocket (the control
// plane's /sip relay, docs/WEB.md §5), on the page's own origin.
const SIPPath = "/sip"

var errNotSignedInPerson = &apihttp.Error{Status: http.StatusBadRequest, Code: "not_a_session",
	Detail: "This only works for a signed-in browser session."}

var errSignInUnfinished = &apihttp.Error{Status: http.StatusForbidden, Code: "sign_in_unfinished",
	Detail: "Finish signing in (your authenticator code) first."}

var errNoRelay = &apihttp.Error{Status: http.StatusServiceUnavailable, Code: "relay_unavailable",
	Detail: "Calls from the browser aren't set up on this server."}

// signedInPerson is the caller as a person who has finished signing in.
func signedInPerson(ctx context.Context) (auth.Principal, uuid.UUID, *apihttp.Error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok || p.Type != auth.TypeUser {
		return p, uuid.Nil, errNotSignedInPerson
	}
	if p.Pending {
		return p, uuid.Nil, errSignInUnfinished
	}
	id, err := uuid.Parse(p.ID)
	if err != nil {
		return p, uuid.Nil, errNotSignedInPerson
	}
	return p, id, nil
}

func turnCredentials(c turn.Credentials) TurnCredentials {
	return TurnCredentials{Urls: c.URLs, Username: c.Username, Credential: c.Password, ExpiresAt: c.ExpiresAt}
}

func (s *Server) IssueMyWebPhone(ctx context.Context, _ IssueMyWebPhoneRequestObject) (IssueMyWebPhoneResponseObject, error) {
	fail := func(e *apihttp.Error) (IssueMyWebPhoneResponseObject, error) {
		return IssueMyWebPhonedefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	_, uid, perr := signedInPerson(ctx)
	if perr != nil {
		return fail(perr)
	}
	sess, ok := auth.SessionFromContext(ctx)
	if !ok {
		return fail(errNotSignedInPerson)
	}
	if s.turn == nil {
		return fail(errNoRelay)
	}
	u, err := s.accounts.GetUser(ctx, uid)
	if err != nil {
		if e, err := apiError(err); e != nil {
			return fail(e)
		} else {
			return nil, err
		}
	}
	line, err := s.pbx.IssueWebPhone(ctx, sess, u.ExtensionID)
	if err != nil {
		if e, err := apiError(err); e != nil {
			return fail(e)
		} else {
			return nil, err
		}
	}
	return IssueMyWebPhone200JSONResponse{
		DeviceId:      line.Device.ID,
		SipUsername:   line.Device.SIPUsername,
		Password:      line.Password,
		SipUri:        "sip:" + line.Device.SIPUsername + "@" + s.pbx.SIPServer(),
		WebsocketPath: SIPPath,
		DisplayName:   line.Extension.DisplayName,
		Extension:     line.Extension.Number,
		Turn:          turnCredentials(s.turn.Issue(uid)),
	}, nil
}

func (s *Server) GetMyTurnCredentials(ctx context.Context, _ GetMyTurnCredentialsRequestObject) (GetMyTurnCredentialsResponseObject, error) {
	fail := func(e *apihttp.Error) (GetMyTurnCredentialsResponseObject, error) {
		return GetMyTurnCredentialsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	_, uid, perr := signedInPerson(ctx)
	if perr != nil {
		return fail(perr)
	}
	if s.turn == nil {
		return fail(errNoRelay)
	}
	return GetMyTurnCredentials200JSONResponse(turnCredentials(s.turn.Issue(uid))), nil
}
