package api

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/voicemail"
)

// SetVoicemail gives the server voicemail listening and settings
// (ADR-069, Phase 1F step 14).
func (s *Server) SetVoicemail(svc *voicemail.Service) {
	s.voicemail = svc
}

var errNoVoicemail = errors.New("voicemail service not set")

// voicemailViewer is the signed-in person asking, with an audit entry
// for their changes. API keys get not_a_session: the audio is people's
// voices (docs/PHASE1F.md §12).
func (s *Server) voicemailViewer(ctx context.Context) (voicemail.Viewer, auth.AuditEntry, *apihttp.Error, error) {
	if s.voicemail == nil {
		return voicemail.Viewer{}, auth.AuditEntry{}, nil, errNoVoicemail
	}
	p, uid, perr := signedInPerson(ctx)
	if perr != nil {
		return voicemail.Viewer{}, auth.AuditEntry{}, perr, nil
	}
	return voicemail.ViewerFrom(p, uid), auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(),
		IP: auth.ClientIPFromContext(ctx), Result: auth.ResultOK}, nil, nil
}

func toVoicemailBox(b voicemail.BoxView) VoicemailBox {
	out := VoicemailBox{Id: b.ID, Owner: b.Owner, ExtensionId: b.ExtensionID, RingGroupId: b.RingGroupID,
		Mine: b.Mine, Member: b.Member, Removed: b.Removed, Enabled: b.Enabled, Email: b.Email,
		Messages: b.Count, New: b.New, Bytes: b.Bytes, Kind: VoicemailBoxKindPerson}
	if b.RingGroupID != nil {
		out.Kind = VoicemailBoxKindRingGroup
	}
	return out
}

func toVoicemailGreeting(g voicemail.GreetingInfo) VoicemailGreeting {
	out := VoicemailGreeting{Recorded: g.Recorded, InUse: g.Recorded && g.InUse}
	if g.Recorded {
		at, ms := g.RecordedAt, int(g.Duration.Milliseconds())
		out.RecordedAt, out.DurationMs = &at, &ms
	}
	return out
}

func toVoicemailBoxSettings(b voicemail.BoxSettings) VoicemailBoxSettings {
	out := VoicemailBoxSettings{Box: toVoicemailBox(b.BoxView), EmailReady: b.EmailReady, CanSwitch: b.CanSwitch}
	out.Greetings.Unavailable = toVoicemailGreeting(b.Greetings[voicemail.GreetingUnavailable])
	out.Greetings.Closed = toVoicemailGreeting(b.Greetings[voicemail.GreetingClosed])
	if b.EmailTo != "" {
		out.EmailTo = &b.EmailTo
	}
	return out
}

func (s *Server) ListVoicemail(ctx context.Context, req ListVoicemailRequestObject) (ListVoicemailResponseObject, error) {
	fail := func(e *apihttp.Error) (ListVoicemailResponseObject, error) {
		return ListVoicemaildefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	v, _, perr, err := s.voicemailViewer(ctx)
	if perr != nil || err != nil {
		if perr != nil {
			return fail(perr)
		}
		return nil, err
	}
	all := req.Params.All != nil && *req.Params.All
	boxes, items, err := s.voicemail.List(ctx, v, req.Params.Box, all)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	days, err := s.voicemail.Store.VoicemailKeepDays(ctx)
	if err != nil {
		return nil, err
	}
	out := VoicemailList{Boxes: make([]VoicemailBox, 0, len(boxes)), Items: make([]VoicemailMessage, 0, len(items)), KeepDays: days}
	for _, b := range boxes {
		out.Boxes = append(out.Boxes, toVoicemailBox(b))
	}
	for _, m := range items {
		item := VoicemailMessage{Id: m.ID, BoxId: m.BoxID, CallerNumber: m.CallerNumber, CallerName: m.CallerName,
			CallerExtensionId: m.CallerExtensionID, ReceivedAt: m.ReceivedAt, DurationMs: int(m.Duration.Milliseconds()),
			HeardAt: m.HeardAt, HeardByMe: m.HeardByMe}
		if m.HeardBy != "" {
			by := m.HeardBy
			item.HeardBy = &by
		}
		out.Items = append(out.Items, item)
	}
	return ListVoicemail200JSONResponse(out), nil
}

func (s *Server) UpdateVoicemail(ctx context.Context, req UpdateVoicemailRequestObject) (UpdateVoicemailResponseObject, error) {
	fail := func(e *apihttp.Error) (UpdateVoicemailResponseObject, error) {
		return UpdateVoicemaildefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	v, _, perr, err := s.voicemailViewer(ctx)
	if perr != nil {
		return fail(perr)
	}
	if err != nil {
		return nil, err
	}
	if err := s.voicemail.Mark(ctx, v, uuid.UUID(req.Id), req.Body.Heard); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	return UpdateVoicemail204Response{}, nil
}

func (s *Server) DeleteVoicemail(ctx context.Context, req DeleteVoicemailRequestObject) (DeleteVoicemailResponseObject, error) {
	fail := func(e *apihttp.Error) (DeleteVoicemailResponseObject, error) {
		return DeleteVoicemaildefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	v, audit, perr, err := s.voicemailViewer(ctx)
	if perr != nil {
		return fail(perr)
	}
	if err != nil {
		return nil, err
	}
	if err := s.voicemail.Delete(ctx, v, uuid.UUID(req.Id), audit); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	return DeleteVoicemail204Response{}, nil
}

func (s *Server) GetMyVoicemailCount(ctx context.Context, _ GetMyVoicemailCountRequestObject) (GetMyVoicemailCountResponseObject, error) {
	v, _, perr, err := s.voicemailViewer(ctx)
	if perr != nil {
		return GetMyVoicemailCountdefaultApplicationProblemPlusJSONResponse{StatusCode: perr.Status, Body: problem(perr)}, nil
	}
	if err != nil {
		return nil, err
	}
	n, err := s.voicemail.NewCount(ctx, v)
	if err != nil {
		return nil, err
	}
	return GetMyVoicemailCount200JSONResponse{New: n}, nil
}

func (s *Server) GetVoicemailBox(ctx context.Context, req GetVoicemailBoxRequestObject) (GetVoicemailBoxResponseObject, error) {
	fail := func(e *apihttp.Error) (GetVoicemailBoxResponseObject, error) {
		return GetVoicemailBoxdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	v, _, perr, err := s.voicemailViewer(ctx)
	if perr != nil {
		return fail(perr)
	}
	if err != nil {
		return nil, err
	}
	b, err := s.voicemail.Settings(ctx, v, uuid.UUID(req.Id))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	return GetVoicemailBox200JSONResponse(toVoicemailBoxSettings(b)), nil
}

func (s *Server) UpdateVoicemailBox(ctx context.Context, req UpdateVoicemailBoxRequestObject) (UpdateVoicemailBoxResponseObject, error) {
	fail := func(e *apihttp.Error) (UpdateVoicemailBoxResponseObject, error) {
		return UpdateVoicemailBoxdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	v, audit, perr, err := s.voicemailViewer(ctx)
	if perr != nil {
		return fail(perr)
	}
	if err != nil {
		return nil, err
	}
	p := voicemail.BoxPatch{Enabled: req.Body.Enabled, Email: req.Body.Email, UseOwn: map[string]bool{}}
	if g := req.Body.Greetings; g != nil {
		if g.Unavailable != nil {
			p.UseOwn[voicemail.GreetingUnavailable] = *g.Unavailable == VoicemailBoxPatchGreetingsUnavailableOwn
		}
		if g.Closed != nil {
			p.UseOwn[voicemail.GreetingClosed] = *g.Closed == VoicemailBoxPatchGreetingsClosedOwn
		}
	}
	b, err := s.voicemail.Update(ctx, v, uuid.UUID(req.Id), p, audit)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	return UpdateVoicemailBox200JSONResponse(toVoicemailBoxSettings(b)), nil
}

func (s *Server) DeleteVoicemailGreeting(ctx context.Context, req DeleteVoicemailGreetingRequestObject) (DeleteVoicemailGreetingResponseObject, error) {
	fail := func(e *apihttp.Error) (DeleteVoicemailGreetingResponseObject, error) {
		return DeleteVoicemailGreetingdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	v, audit, perr, err := s.voicemailViewer(ctx)
	if perr != nil {
		return fail(perr)
	}
	if err != nil {
		return nil, err
	}
	if err := s.voicemail.DeleteGreeting(ctx, v, uuid.UUID(req.Id), req.Kind, audit); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return fail(e)
	}
	return DeleteVoicemailGreeting204Response{}, nil
}

func (s *Server) voicemailSettings(ctx context.Context) (VoicemailSettings, error) {
	p, _ := auth.PrincipalFromContext(ctx)
	days, u, err := s.voicemail.KeepDays(ctx, p.TenantID)
	return VoicemailSettings{KeepDays: days, Messages: u.Count, Bytes: u.Bytes}, err
}

func (s *Server) GetVoicemailSettings(ctx context.Context, _ GetVoicemailSettingsRequestObject) (GetVoicemailSettingsResponseObject, error) {
	if s.voicemail == nil {
		return nil, errNoVoicemail
	}
	out, err := s.voicemailSettings(ctx)
	if err != nil {
		return nil, err
	}
	return GetVoicemailSettings200JSONResponse(out), nil
}

func (s *Server) UpdateVoicemailSettings(ctx context.Context, req UpdateVoicemailSettingsRequestObject) (UpdateVoicemailSettingsResponseObject, error) {
	if s.voicemail == nil {
		return nil, errNoVoicemail
	}
	p, _ := auth.PrincipalFromContext(ctx)
	audit := auth.AuditEntry{TenantID: &p.TenantID, Actor: p.Actor(), IP: auth.ClientIPFromContext(ctx), Result: auth.ResultOK}
	if err := s.voicemail.SetKeepDays(ctx, req.Body.KeepDays, audit); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateVoicemailSettingsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out, err := s.voicemailSettings(ctx)
	if err != nil {
		return nil, err
	}
	return UpdateVoicemailSettings200JSONResponse(out), nil
}
