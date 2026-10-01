package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
)

// Email (ADR-066): the Email card in System → Settings, and its test.

// SetEmail adds email sending.
func (s *Server) SetEmail(svc *email.Service) { s.email = svc }

func toEmail(c email.Config, st email.Status) Email {
	out := Email{Enabled: c.Enabled, Preset: c.Preset, Host: c.Host, Port: c.Port, Security: EmailSecurity(c.Security),
		Username: c.Username, FromAddress: c.FromAddress, FromName: c.FromName, PasswordSet: len(c.PasswordEnc) > 0,
		HourlyLimit: c.HourlyLimit, ArrivedAt: c.ArrivedAt, Etag: auth.ETag(c.Version)}
	out.Status.LastSentAt, out.Status.SentLastHour, out.Status.Waiting, out.Status.LastError = st.LastSentAt, st.SentLastHour, st.Waiting, st.LastError
	return out
}

func (s *Server) GetEmail(ctx context.Context, _ GetEmailRequestObject) (GetEmailResponseObject, error) {
	c, st, err := s.email.Get(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetEmaildefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toEmail(c, st)
	return GetEmail200JSONResponse{Body: out, Headers: GetEmail200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateEmail(ctx context.Context, req UpdateEmailRequestObject) (UpdateEmailResponseObject, error) {
	b := req.Body
	_, err := s.email.Update(ctx, email.Patch{
		Enabled: b.Enabled, Preset: b.Preset, Host: b.Host, Port: b.Port, Security: (*string)(b.Security), Username: b.Username,
		FromAddress: b.FromAddress, FromName: b.FromName, Password: b.Password, HourlyLimit: b.HourlyLimit, Arrived: b.Arrived,
	}, deref(req.Params.IfMatch))
	var c email.Config
	var st email.Status
	if err == nil {
		c, st, err = s.email.Get(ctx)
	}
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateEmaildefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toEmail(c, st)
	return UpdateEmail200JSONResponse{Body: out, Headers: UpdateEmail200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) TestEmail(ctx context.Context, _ TestEmailRequestObject) (TestEmailResponseObject, error) {
	fail := func(err error) (TestEmailResponseObject, error) {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return TestEmaildefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	// The test goes to the caller's own address: a person, not an API key.
	p, _ := auth.PrincipalFromContext(ctx)
	uid, err := uuid.Parse(p.ID)
	if err != nil || p.Type != auth.TypeUser {
		return fail(&apihttp.Error{Status: http.StatusForbidden, Code: "people_only",
			Detail: "Send the test signed in as a person: it goes to your own address."})
	}
	u, err := s.accounts.GetUser(ctx, uid)
	if err != nil {
		return fail(err)
	}
	r, err := s.email.Test(ctx, u.Email)
	if err != nil {
		return fail(err)
	}
	out := TestEmail200JSONResponse{To: r.To, Ok: r.Err == nil, Passed: []EmailTestPassed{}}
	for _, st := range r.Passed {
		out.Passed = append(out.Passed, EmailTestPassed(st))
	}
	if r.Err != nil {
		stage := EmailTestStage(r.Err.Stage)
		msg := r.Err.Error()
		out.Stage, out.Error = &stage, &msg
		if r.Err.Blocked {
			out.BlockedHost = &r.Err.Host
		}
	}
	return out, nil
}
