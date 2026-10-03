package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/push"
)

// Ringing the app on a sleeping phone (ADR-074, docs/PHASE2.md §5): the
// Apple key a system admin sets, and the tokens each phone sends about
// itself.

var errNoPush = errors.New("the push gateway is not configured on this server")

// SetPush gives the server the gateway that rings app phones.
func (s *Server) SetPush(g *push.Gateway) { s.push = g }

func toAppPush(s push.Settings, st push.Stats) AppPush {
	sent, failed := 0, 0
	for _, n := range st.Sent {
		sent += n
	}
	for _, n := range st.Failed {
		failed += n
	}
	out := AppPush{
		Enabled: s.Enabled, TeamId: s.TeamID, KeyId: s.KeyID, BundleId: s.BundleID,
		Environment: AppPushEnvironment(s.Environment), WaitMs: s.WaitMS, KeySet: s.HasKey,
		Etag: fmt.Sprintf(`"%d"`, s.Version),
	}
	out.Status.Sent = sent
	out.Status.Failed = failed
	out.Status.DeadTokens = st.Dead
	out.Status.Woken = st.Woke
	out.Status.SlowestWakeSeconds = float32(st.SlowestWake)
	if st.Woke > 0 {
		average := float32(st.WokeSeconds / float64(st.Woke))
		out.Status.AverageWakeSeconds = &average
	}
	return out
}

func (s *Server) GetAppPush(ctx context.Context, _ GetAppPushRequestObject) (GetAppPushResponseObject, error) {
	if s.push == nil {
		return nil, errNoPush
	}
	p, _ := auth.PrincipalFromContext(ctx)
	settings, err := s.push.Settings(ctx, &p)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetAppPushdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := toAppPush(settings, s.push.Snapshot())
	return GetAppPush200JSONResponse{Body: out, Headers: GetAppPush200ResponseHeaders{ETag: &out.Etag}}, nil
}

func (s *Server) UpdateAppPush(ctx context.Context, req UpdateAppPushRequestObject) (UpdateAppPushResponseObject, error) {
	fail := func(err error) (UpdateAppPushResponseObject, error) {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateAppPushdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	if s.push == nil {
		return nil, errNoPush
	}
	p, _ := auth.PrincipalFromContext(ctx)
	current, err := s.push.Settings(ctx, &p)
	if err != nil {
		return fail(err)
	}
	// A merge patch: what isn't in the body stays as it is.
	in := push.Input{
		Enabled: current.Enabled, TeamID: current.TeamID, KeyID: current.KeyID, BundleID: current.BundleID,
		Environment: current.Environment, WaitMS: current.WaitMS,
	}
	b := req.Body
	if b.Enabled != nil {
		in.Enabled = *b.Enabled
	}
	if b.TeamId != nil {
		in.TeamID = *b.TeamId
	}
	if b.KeyId != nil {
		in.KeyID = *b.KeyId
	}
	if b.BundleId != nil {
		in.BundleID = *b.BundleId
	}
	if b.Environment != nil {
		in.Environment = string(*b.Environment)
	}
	if b.WaitMs != nil {
		in.WaitMS = *b.WaitMs
	}
	if b.Key != nil {
		in.Key = []byte(*b.Key)
	}
	saved, err := s.push.Save(ctx, in, deref(req.Params.IfMatch))
	if err != nil {
		return fail(err)
	}
	out := toAppPush(saved, s.push.Snapshot())
	return UpdateAppPush200JSONResponse{Body: out, Headers: UpdateAppPush200ResponseHeaders{ETag: &out.Etag}}, nil
}

// SetMyPhonePush is the app saying where Apple can reach it. Only a set-up
// phone may, with its own device token, and it may only ever speak for
// itself.
func (s *Server) SetMyPhonePush(ctx context.Context, req SetMyPhonePushRequestObject) (SetMyPhonePushResponseObject, error) {
	fail := func(e *apihttp.Error) (SetMyPhonePushResponseObject, error) {
		return SetMyPhonePushdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	if s.push == nil {
		return nil, errNoPush
	}
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok || p.DeviceID == nil {
		return fail(&apihttp.Error{Status: http.StatusBadRequest, Code: "not_a_phone",
			Detail: "This only works for the Linx app on a phone that has been set up."})
	}
	err := s.push.SaveTokens(ctx, *p.DeviceID, deref(req.Body.VoipToken), deref(req.Body.AlertToken),
		string(req.Body.Environment))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	return SetMyPhonePush204Response{}, nil
}
