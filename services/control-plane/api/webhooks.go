package api

import (
	"context"
	"encoding/base64"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/apihttp"
	"linxpbx.com/linx/internal/webhook"
)

// decodeIDCursor and encodeIDCursor wrap the "resume after this id"
// position of id-ordered lists in an opaque cursor.
func decodeIDCursor(cursor string) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, errCursorInvalid
	}
	id, err := uuid.ParseBytes(b)
	if err != nil {
		return nil, errCursorInvalid
	}
	return &id, nil
}

func encodeIDCursor(id uuid.UUID) *string {
	c := base64.RawURLEncoding.EncodeToString([]byte(id.String()))
	return &c
}

// page lists one page with list, fetching one extra item to know whether
// there is a next page.
func page[T any](limit *Limit, cursor *Cursor, id func(T) uuid.UUID, list func(before *uuid.UUID, limit int) ([]T, error)) ([]T, *string, error) {
	p := apihttp.ParsePagination(limit, cursor)
	before, err := decodeIDCursor(p.Cursor)
	if err != nil {
		return nil, nil, err
	}
	items, err := list(before, p.Limit+1)
	if err != nil {
		return nil, nil, err
	}
	if len(items) > p.Limit {
		items = items[:p.Limit]
		return items, encodeIDCursor(id(items[len(items)-1])), nil
	}
	return items, nil, nil
}

func toWebhook(e webhook.Endpoint) Webhook {
	w := Webhook{
		Id: e.ID, Url: e.URL, Description: e.Description, EventTypes: e.EventTypes, Enabled: e.Enabled,
		DisabledAt: e.DisabledAt, FailingSince: e.FailingSince, LastSuccessAt: e.LastSuccessAt,
		CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, Etag: webhook.ETag(e.Version),
	}
	if w.EventTypes == nil {
		w.EventTypes = []string{}
	}
	if e.DisabledReason != nil {
		r := WebhookDisabledReason(*e.DisabledReason)
		w.DisabledReason = &r
	}
	if len(e.PreviousSecretEnc) > 0 {
		w.PreviousSecretExpiresAt = e.PreviousSecretExpiresAt
	}
	return w
}

func toDelivery(d webhook.Delivery) WebhookDelivery {
	out := WebhookDelivery{
		Id: d.ID, WebhookId: d.EndpointID, EventId: d.EventID, EventType: d.EventType, Status: DeliveryStatus(d.Status),
		Attempts: d.Attempts, MaxAttempts: d.MaxAttempts, NextAttemptAt: d.NextAttemptAt, ReplayOf: d.ReplayOf,
		CreatedAt: d.CreatedAt, FinishedAt: d.FinishedAt,
	}
	if d.Status != webhook.StatusPending {
		out.NextAttemptAt = nil
	}
	if d.Log != nil {
		log := make([]DeliveryAttempt, 0, len(d.Log))
		for _, a := range d.Log {
			log = append(log, DeliveryAttempt{At: a.At, StatusCode: a.StatusCode, DurationMs: a.DurationMS,
				ResponseExcerpt: a.ResponseExcerpt, Error: a.Error})
		}
		out.Log = &log
	}
	return out
}

func toAllowlistEntry(e webhook.AllowlistEntry) AllowlistEntry {
	out := AllowlistEntry{Id: e.ID, Description: e.Description, CreatedBy: e.CreatedBy, CreatedAt: e.CreatedAt}
	if e.CIDR != nil {
		out.Kind, out.Value = Cidr, *e.CIDR
	} else if e.Host != nil {
		out.Kind, out.Value = Host, *e.Host
	}
	return out
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func (s *Server) ListWebhooks(ctx context.Context, req ListWebhooksRequestObject) (ListWebhooksResponseObject, error) {
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(e webhook.Endpoint) uuid.UUID { return e.ID },
		func(before *uuid.UUID, limit int) ([]webhook.Endpoint, error) {
			return s.webhooks.List(ctx, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListWebhooksdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := WebhookList{Items: make([]Webhook, 0, len(items)), NextCursor: next}
	for _, e := range items {
		list.Items = append(list.Items, toWebhook(e))
	}
	return ListWebhooks200JSONResponse(list), nil
}

func (s *Server) CreateWebhook(ctx context.Context, req CreateWebhookRequestObject) (CreateWebhookResponseObject, error) {
	e, secret, err := s.webhooks.Create(ctx, webhook.EndpointInput{
		URL: req.Body.Url, Description: deref(req.Body.Description), EventTypes: deref(req.Body.EventTypes),
		Enabled: req.Body.Enabled,
	})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateWebhookdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateWebhook201JSONResponse{Webhook: toWebhook(e), Secret: secret}, nil
}

func (s *Server) GetWebhook(ctx context.Context, req GetWebhookRequestObject) (GetWebhookResponseObject, error) {
	e, err := s.webhooks.Get(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetWebhookdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	w := toWebhook(e)
	return GetWebhook200JSONResponse{Body: w, Headers: GetWebhook200ResponseHeaders{ETag: &w.Etag}}, nil
}

func (s *Server) UpdateWebhook(ctx context.Context, req UpdateWebhookRequestObject) (UpdateWebhookResponseObject, error) {
	e, err := s.webhooks.Update(ctx, req.Id, webhook.Patch{
		URL: req.Body.Url, Description: req.Body.Description, EventTypes: req.Body.EventTypes, Enabled: req.Body.Enabled,
	}, deref(req.Params.IfMatch))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return UpdateWebhookdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	w := toWebhook(e)
	return UpdateWebhook200JSONResponse{Body: w, Headers: UpdateWebhook200ResponseHeaders{ETag: &w.Etag}}, nil
}

func (s *Server) DeleteWebhook(ctx context.Context, req DeleteWebhookRequestObject) (DeleteWebhookResponseObject, error) {
	if err := s.webhooks.Delete(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteWebhookdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteWebhook204Response{}, nil
}

func (s *Server) RotateWebhookSecret(ctx context.Context, req RotateWebhookSecretRequestObject) (RotateWebhookSecretResponseObject, error) {
	e, secret, err := s.webhooks.RotateSecret(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return RotateWebhookSecretdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return RotateWebhookSecret200JSONResponse{Webhook: toWebhook(e), Secret: secret}, nil
}

func (s *Server) TestWebhook(ctx context.Context, req TestWebhookRequestObject) (TestWebhookResponseObject, error) {
	d, err := s.webhooks.Test(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return TestWebhookdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return TestWebhook200JSONResponse(toDelivery(d)), nil
}

func (s *Server) ReplayWebhook(ctx context.Context, req ReplayWebhookRequestObject) (ReplayWebhookResponseObject, error) {
	n, err := s.webhooks.ReplayFailed(ctx, req.Id, req.Body.Since)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ReplayWebhookdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ReplayWebhook202JSONResponse{Queued: n}, nil
}

func (s *Server) ListWebhookDeliveries(ctx context.Context, req ListWebhookDeliveriesRequestObject) (ListWebhookDeliveriesResponseObject, error) {
	status := string(deref(req.Params.Status))
	items, next, err := page(req.Params.Limit, req.Params.Cursor, func(d webhook.Delivery) uuid.UUID { return d.ID },
		func(before *uuid.UUID, limit int) ([]webhook.Delivery, error) {
			return s.webhooks.Deliveries(ctx, req.Id, status, before, limit)
		})
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ListWebhookDeliveriesdefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	list := WebhookDeliveryList{Items: make([]WebhookDelivery, 0, len(items)), NextCursor: next}
	for _, d := range items {
		list.Items = append(list.Items, toDelivery(d))
	}
	return ListWebhookDeliveries200JSONResponse(list), nil
}

func (s *Server) GetWebhookDelivery(ctx context.Context, req GetWebhookDeliveryRequestObject) (GetWebhookDeliveryResponseObject, error) {
	d, err := s.webhooks.Delivery(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return GetWebhookDeliverydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return GetWebhookDelivery200JSONResponse(toDelivery(d)), nil
}

func (s *Server) ReplayWebhookDelivery(ctx context.Context, req ReplayWebhookDeliveryRequestObject) (ReplayWebhookDeliveryResponseObject, error) {
	d, err := s.webhooks.Replay(ctx, req.Id)
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return ReplayWebhookDeliverydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return ReplayWebhookDelivery202JSONResponse(toDelivery(d)), nil
}

func (s *Server) ListOutboundAllowlist(ctx context.Context, _ ListOutboundAllowlistRequestObject) (ListOutboundAllowlistResponseObject, error) {
	entries, err := s.webhooks.Store.ListAllowlist(ctx)
	if err != nil {
		return nil, err
	}
	list := AllowlistEntryList{Items: make([]AllowlistEntry, 0, len(entries))}
	for _, e := range entries {
		list.Items = append(list.Items, toAllowlistEntry(e))
	}
	return ListOutboundAllowlist200JSONResponse(list), nil
}

func (s *Server) CreateOutboundAllowlistEntry(ctx context.Context, req CreateOutboundAllowlistEntryRequestObject) (CreateOutboundAllowlistEntryResponseObject, error) {
	entry, err := s.webhooks.AddAllowlistEntry(ctx, req.Body.Value, deref(req.Body.Description))
	if err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return CreateOutboundAllowlistEntrydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return CreateOutboundAllowlistEntry201JSONResponse(toAllowlistEntry(entry)), nil
}

func (s *Server) DeleteOutboundAllowlistEntry(ctx context.Context, req DeleteOutboundAllowlistEntryRequestObject) (DeleteOutboundAllowlistEntryResponseObject, error) {
	if err := s.webhooks.DeleteAllowlistEntry(ctx, req.Id); err != nil {
		e, err := apiError(err)
		if e == nil {
			return nil, err
		}
		return DeleteOutboundAllowlistEntrydefaultApplicationProblemPlusJSONResponse{StatusCode: e.Status, Body: problem(e)}, nil
	}
	return DeleteOutboundAllowlistEntry204Response{}, nil
}
