package api

import (
	"context"
	"errors"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/helpanswers"
)

// Help's written answers (docs/HELP.md §4): the setting in System →
// Settings, and its Test. Asking is hand-written, since the answer streams
// (services/control-plane/help.go).

// SetHelpAnswers adds the written-answers setting.
func (s *Server) SetHelpAnswers(svc *helpanswers.Service) { s.helpAnswers = svc }

func toHelpAnswers(c helpanswers.Config, used int) HelpAnswers {
	return HelpAnswers{Enabled: c.Enabled, Provider: HelpAnswersProvider(c.Provider), BaseUrl: c.BaseURL, Model: c.Model,
		ApiKeySet: len(c.APIKeyEnc) > 0, PersonDailyLimit: c.PersonDailyLimit, ServerDailyLimit: c.ServerDailyLimit,
		UsedToday: used, Etag: auth.ETag(c.Version)}
}

func (s *Server) GetHelpAnswers(ctx context.Context, _ GetHelpAnswersRequestObject) (GetHelpAnswersResponseObject, error) {
	c, used, err := s.helpAnswers.Get(ctx)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetHelpAnswersdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toHelpAnswers(c, used)
	return GetHelpAnswers200JSONResponse{Body: out, Headers: GetHelpAnswers200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateHelpAnswers(ctx context.Context, req UpdateHelpAnswersRequestObject) (UpdateHelpAnswersResponseObject, error) {
	b := req.Body
	c, err := s.helpAnswers.Update(ctx, helpanswers.Patch{
		Enabled: b.Enabled, Provider: (*string)(b.Provider), BaseURL: b.BaseUrl, Model: b.Model, APIKey: b.ApiKey,
		PersonDailyLimit: b.PersonDailyLimit, ServerDailyLimit: b.ServerDailyLimit,
	}, deref(req.Params.IfMatch))
	var used int
	if err == nil {
		_, used, err = s.helpAnswers.Get(ctx)
	}
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateHelpAnswersdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toHelpAnswers(c, used)
	return UpdateHelpAnswers200JSONResponse{Body: out, Headers: UpdateHelpAnswers200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) TestHelpAnswers(ctx context.Context, req TestHelpAnswersRequestObject) (TestHelpAnswersResponseObject, error) {
	var q string
	if req.Body != nil {
		q = deref(req.Body.Question)
	}
	text, a, err := s.helpAnswers.Test(ctx, q)
	out := HelpAnswersTest{Question: q, By: a.By, Guides: []HelpGuideRef{}}
	if out.Question == "" {
		out.Question = helpanswers.DefaultTestQuestion
	}
	var f *helpanswers.Failure
	switch {
	case errors.As(err, &f):
		msg := f.Full()
		out.Error = &msg
	case err != nil:
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return TestHelpAnswersdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	default:
		out.Ok, out.Answer = true, &text
		for _, g := range a.Guides {
			out.Guides = append(out.Guides, HelpGuideRef{Name: g.Name, Title: g.Title})
		}
	}
	return TestHelpAnswers200JSONResponse(out), nil
}
