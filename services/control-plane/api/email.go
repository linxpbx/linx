package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/email"
)

// Email (ADR-066): the Email card in System → Settings, and its test.

// SetEmail adds email sending; webAddress (https://<domain>[:port]) is
// what links in emails start with.
func (s *Server) SetEmail(svc *email.Service, webAddress string) {
	s.email, s.webAddress = svc, webAddress
}

// inviteEmail queues u's set-password link, as asked for by People → Add or
// New invite link (docs/ui/SCREENS_PHASE1F.md §5.2). Not queued is said in
// the answer, never an error: the link itself was made and is shown.
func (s *Server) inviteEmail(ctx context.Context, u auth.User, token string) *InviteEmail {
	out := &InviteEmail{To: u.Email}
	if s.email == nil {
		msg := email.ErrOff.Detail
		out.Error = &msg
		return out
	}
	p, _ := auth.PrincipalFromContext(ctx)
	who := "An admin"
	if uid, err := uuid.Parse(p.ID); err == nil && p.Type == auth.TypeUser {
		if me, err := s.accounts.GetUser(ctx, uid); err == nil && me.Name != "" {
			who = me.Name
		}
	}
	host := strings.TrimPrefix(s.webAddress, "https://")
	c := email.Content{Subject: "Your Linx account at " + host,
		Text: fmt.Sprintf("Hello %s,\n\n%s added you to Linx, the phone system at %s.\n\n"+
			"Set up your account (the link works once, for 24 hours):\n%s/setup/%s\n\n"+
			"If you weren't expecting this, you can ignore this email.", u.Name, who, host, s.webAddress, token)}
	_, err := s.email.Enqueue(ctx, p.TenantID, email.KindInvite, []string{u.Email}, c,
		auth.AuditEntry{Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx), Action: "email.queue", Result: auth.ResultOK,
			Detail: map[string]any{"user": u.ID.String()}})
	if err != nil {
		var e *apihttp.Error
		msg := "Linx couldn't queue the email. Copy the link instead."
		if errors.As(err, &e) {
			msg = e.Detail
		}
		out.Error = &msg
		return out
	}
	out.Queued = true
	return out
}

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
