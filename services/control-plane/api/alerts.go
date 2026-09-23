package api

import (
	"context"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
)

func toQuietHours(q *alert.QuietHours) *QuietHours {
	if q == nil {
		return nil
	}
	start, end := alert.FormatClock(q.Start), alert.FormatClock(q.End)
	bypass := q.BypassCritical
	return &QuietHours{Enabled: true, Start: &start, End: &end, Timezone: &q.Timezone, BypassCritical: &bypass}
}

func toQuietHoursInput(q *QuietHours) *alert.QuietHoursInput {
	if q == nil {
		return nil
	}
	return &alert.QuietHoursInput{
		Enabled: q.Enabled, Start: deref(q.Start), End: deref(q.End), Timezone: deref(q.Timezone),
		BypassCritical: q.BypassCritical,
	}
}

func toChannelConfig(c AlertChannelConfig) alert.Config {
	return alert.Config{
		ServerURL: deref(c.ServerUrl), Topic: deref(c.Topic), AccessToken: deref(c.AccessToken),
		AppToken: deref(c.AppToken), URL: deref(c.Url), BotToken: deref(c.BotToken), ChatID: deref(c.ChatId),
	}
}

func toAlertChannel(c alert.Channel) AlertChannel {
	return AlertChannel{
		Id: c.ID, Kind: AlertChannelKind(c.Kind), Name: c.Name, MinSeverity: AlertSeverity(c.MinSeverity),
		QuietHours: toQuietHours(c.QuietHours), Enabled: c.Enabled,
		CreatedBy: c.CreatedBy, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Etag: alert.ETag(c.Version),
	}
}

func toAlert(a alert.Alert) Alert {
	out := Alert{
		Id: a.ID, Key: a.Key, Severity: AlertSeverity(a.Severity), Title: a.Title, Message: a.Message,
		Status: AlertStatus(a.Status), FirstSeenAt: a.FirstSeenAt, LastSeenAt: a.LastSeenAt, ResolvedAt: a.ResolvedAt,
	}
	if a.Link != "" {
		out.Link = &a.Link
	}
	return out
}

func (s *Server) ListAlertChannels(ctx context.Context, req ListAlertChannelsRequestObject) (ListAlertChannelsResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(c alert.Channel) uuid.UUID { return c.ID },
		func(before *uuid.UUID, limit int) ([]alert.Channel, error) { return s.alerts.List(ctx, before, limit) })
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListAlertChannelsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := AlertChannelList{Items: make([]AlertChannel, 0, len(items)), NextCursor: next}
	for _, c := range items {
		list.Items = append(list.Items, toAlertChannel(c))
	}
	return ListAlertChannels200JSONResponse(list), nil
}

func (s *Server) CreateAlertChannel(ctx context.Context, req CreateAlertChannelRequestObject) (CreateAlertChannelResponseObject, error) {
	c, secret, err := s.alerts.Create(ctx, alert.ChannelInput{
		Kind: string(req.Body.Kind), Name: req.Body.Name, Config: toChannelConfig(req.Body.Config),
		MinSeverity: string(deref(req.Body.MinSeverity)), QuietHours: toQuietHoursInput(req.Body.QuietHours),
		Enabled: req.Body.Enabled,
	})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateAlertChanneldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	out := AlertChannelCreated{AlertChannel: toAlertChannel(c)}
	if secret != "" {
		out.Secret = &secret
	}
	return CreateAlertChannel201JSONResponse(out), nil
}

func (s *Server) GetAlertChannel(ctx context.Context, req GetAlertChannelRequestObject) (GetAlertChannelResponseObject, error) {
	c, err := s.alerts.Get(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetAlertChanneldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	ac := toAlertChannel(c)
	return GetAlertChannel200JSONResponse{Body: ac, Headers: GetAlertChannel200ResponseHeaders{ETag: &ac.Etag}}, nil
}

func (s *Server) UpdateAlertChannel(ctx context.Context, req UpdateAlertChannelRequestObject) (UpdateAlertChannelResponseObject, error) {
	var cfg *alert.Config
	if req.Body.Config != nil {
		c := toChannelConfig(*req.Body.Config)
		cfg = &c
	}
	var minSeverity *string
	if req.Body.MinSeverity != nil {
		m := string(*req.Body.MinSeverity)
		minSeverity = &m
	}
	c, err := s.alerts.Update(ctx, req.Id, alert.Patch{
		Name: req.Body.Name, Config: cfg, MinSeverity: minSeverity,
		QuietHours: toQuietHoursInput(req.Body.QuietHours), Enabled: req.Body.Enabled,
	}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateAlertChanneldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	ac := toAlertChannel(c)
	return UpdateAlertChannel200JSONResponse{Body: ac, Headers: UpdateAlertChannel200ResponseHeaders{ETag: &ac.Etag}}, nil
}

func (s *Server) DeleteAlertChannel(ctx context.Context, req DeleteAlertChannelRequestObject) (DeleteAlertChannelResponseObject, error) {
	if err := s.alerts.Delete(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteAlertChanneldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteAlertChannel204Response{}, nil
}

func (s *Server) TestAlertChannel(ctx context.Context, req TestAlertChannelRequestObject) (TestAlertChannelResponseObject, error) {
	r, err := s.alerts.Test(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return TestAlertChanneldefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return TestAlertChannel200JSONResponse(AlertChannelTestResult{
		Succeeded: r.Succeeded, StatusCode: r.StatusCode, DurationMs: &r.DurationMS, Error: r.Error,
	}), nil
}

func (s *Server) ListAlerts(ctx context.Context, req ListAlertsRequestObject) (ListAlertsResponseObject, error) {
	status := string(deref(req.Params.Status))
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(a alert.Alert) uuid.UUID { return a.ID },
		func(before *uuid.UUID, limit int) ([]alert.Alert, error) {
			return s.alerts.Alerts(ctx, status, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListAlertsdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := AlertList{Items: make([]Alert, 0, len(items)), NextCursor: next}
	for _, a := range items {
		list.Items = append(list.Items, toAlert(a))
	}
	return ListAlerts200JSONResponse(list), nil
}
