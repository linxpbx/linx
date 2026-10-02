package api

import (
	"context"
	"errors"
	"net/http"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/callhistory"
)

// SetCallHistory gives the server call history (ADR-070, Phase 1F step
// 15).
func (s *Server) SetCallHistory(svc *callhistory.Service) {
	s.history = svc
}

var errNoCallHistory = errors.New("call history service not set")

var errBadCursor = &apihttp.Error{Status: http.StatusBadRequest, Code: "before_invalid", Detail: "before must be a page's next value."}

func toCallRecord(l callhistory.Listed, placedByMe *bool) CallRecord {
	out := CallRecord{Id: l.ID, Direction: CallRecordDirection(l.Direction), StartedAt: l.StartedAt, AnsweredAt: l.AnsweredAt,
		EndedAt: l.EndedAt, TalkSeconds: l.TalkSeconds, Result: CallRecordResult(l.Result), Missed: l.Missed,
		RangUnanswered: l.RangUnanswered, PlacedByMe: placedByMe,
		From: CallHistoryParty{Number: l.FromNumber, Name: l.FromName, ExtensionId: l.FromExtensionID},
		To:   CallHistoryParty{Number: l.ToNumber, Name: l.ToName, ExtensionId: l.ToExtensionID},
		Line: optString(l.TrunkName), RingGroup: optString(l.RingGroupName), AnsweredBy: optString(l.AnsweredByName),
		AnsweredByExtensionId: l.AnsweredByExtensionID, Steps: l.Words()}
	if l.VoicemailBoxName != "" {
		vm := CallHistoryVoicemail{Box: l.VoicemailBoxName, Id: l.VoicemailID, BoxId: l.VoicemailBoxID}
		if l.VoicemailID != nil {
			ms := int(l.VoicemailDuration.Milliseconds())
			vm.DurationMs = &ms
		}
		out.Voicemail = &vm
	}
	return out
}

func (s *Server) callPage(ctx context.Context, items []callhistory.Listed, next *callhistory.Cursor, mine *callhistory.Filter) (CallHistoryPage, error) {
	days, err := s.history.KeepDays(ctx)
	if err != nil {
		return CallHistoryPage{}, err
	}
	out := CallHistoryPage{Items: make([]CallRecord, 0, len(items)), KeepDays: days}
	for _, l := range items {
		var placed *bool
		if mine != nil {
			p := l.FromExtensionID != nil && mine.Party != nil && *l.FromExtensionID == *mine.Party
			placed = &p
		}
		out.Items = append(out.Items, toCallRecord(l, placed))
	}
	if next != nil {
		n := next.String()
		out.Next = &n
	}
	return out, nil
}

func cursor(before *string) (*callhistory.Cursor, *apihttp.Error) {
	if before == nil || *before == "" {
		return nil, nil
	}
	c, err := callhistory.ParseCursor(*before)
	if err != nil {
		return nil, errBadCursor
	}
	return &c, nil
}

func (s *Server) ListMyCalls(ctx context.Context, req ListMyCallsRequestObject) (ListMyCallsResponseObject, error) {
	fail := func(e *apihttp.Error) (ListMyCallsResponseObject, error) {
		return ListMyCallsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	if s.history == nil {
		return nil, errNoCallHistory
	}
	p, uid, perr := signedInPerson(ctx)
	if perr != nil {
		return fail(perr)
	}
	before, perr := cursor(req.Params.Before)
	if perr != nil {
		return fail(perr)
	}
	f := callhistory.Filter{Missed: deref(req.Params.Missed), Number: deref(req.Params.Number), Before: before, Limit: deref(req.Params.Limit)}
	ext, err := s.history.Store.UserExtension(ctx, uid)
	if err != nil {
		return nil, err
	}
	f.Party = ext
	items, next, err := s.history.Mine(ctx, p.TenantID, uid, f)
	if err != nil {
		return nil, err
	}
	out, err := s.callPage(ctx, items, next, &f)
	if err != nil {
		return nil, err
	}
	return ListMyCalls200JSONResponse(out), nil
}

func (s *Server) GetMyMissedCalls(ctx context.Context, _ GetMyMissedCallsRequestObject) (GetMyMissedCallsResponseObject, error) {
	if s.history == nil {
		return nil, errNoCallHistory
	}
	_, uid, perr := signedInPerson(ctx)
	if perr != nil {
		return GetMyMissedCallsdefaultApplicationProblemPlusJSONResponse{StatusCode: perr.Status, Body: problem(perr)}, nil
	}
	n, err := s.history.MissedCount(ctx, uid)
	if err != nil {
		return nil, err
	}
	return GetMyMissedCalls200JSONResponse{Missed: n}, nil
}

func (s *Server) ClearMyMissedCalls(ctx context.Context, _ ClearMyMissedCallsRequestObject) (ClearMyMissedCallsResponseObject, error) {
	if s.history == nil {
		return nil, errNoCallHistory
	}
	_, uid, perr := signedInPerson(ctx)
	if perr != nil {
		return ClearMyMissedCallsdefaultApplicationProblemPlusJSONResponse{StatusCode: perr.Status, Body: problem(perr)}, nil
	}
	if err := s.history.Seen(ctx, uid); err != nil {
		return nil, err
	}
	return ClearMyMissedCalls204Response{}, nil
}

func (s *Server) ListCalls(ctx context.Context, req ListCallsRequestObject) (ListCallsResponseObject, error) {
	if s.history == nil {
		return nil, errNoCallHistory
	}
	before, perr := cursor(req.Params.Before)
	if perr != nil {
		return ListCallsdefaultApplicationProblemPlusJSONResponse{StatusCode: perr.Status, Body: problem(perr)}, nil
	}
	p, _ := auth.PrincipalFromContext(ctx)
	f := callhistory.Filter{Party: req.Params.ExtensionId, Missed: deref(req.Params.Missed), Number: deref(req.Params.Number),
		From: req.Params.From, To: req.Params.To, Before: before, Limit: deref(req.Params.Limit)}
	items, next, err := s.history.All(ctx, p.TenantID, f)
	if err != nil {
		return nil, err
	}
	out, err := s.callPage(ctx, items, next, nil)
	if err != nil {
		return nil, err
	}
	return ListCalls200JSONResponse(out), nil
}

func (s *Server) callHistorySettings(ctx context.Context) (CallHistorySettings, error) {
	p, _ := auth.PrincipalFromContext(ctx)
	days, err := s.history.KeepDays(ctx)
	if err != nil {
		return CallHistorySettings{}, err
	}
	u, err := s.history.Store.CallHistoryUsage(ctx, p.TenantID)
	return CallHistorySettings{KeepDays: days, Calls: u.Calls, Bytes: u.Bytes}, err
}

func (s *Server) GetCallHistorySettings(ctx context.Context, _ GetCallHistorySettingsRequestObject) (GetCallHistorySettingsResponseObject, error) {
	if s.history == nil {
		return nil, errNoCallHistory
	}
	out, err := s.callHistorySettings(ctx)
	if err != nil {
		return nil, err
	}
	return GetCallHistorySettings200JSONResponse(out), nil
}

func (s *Server) UpdateCallHistorySettings(ctx context.Context, req UpdateCallHistorySettingsRequestObject) (UpdateCallHistorySettingsResponseObject, error) {
	if s.history == nil {
		return nil, errNoCallHistory
	}
	p, _ := auth.PrincipalFromContext(ctx)
	audit := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx), Result: auth.ResultOK}
	if err := s.history.SetKeepDays(ctx, req.Body.KeepDays, audit); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateCallHistorySettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out, err := s.callHistorySettings(ctx)
	if err != nil {
		return nil, err
	}
	return UpdateCallHistorySettings200JSONResponse(out), nil
}
