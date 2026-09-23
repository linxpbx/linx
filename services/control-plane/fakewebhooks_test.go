package main

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/webhook"
)

// fakeWebhookStore is an in-memory webhook.Store for the API tests. The
// worker queries (dispatch, claim, cleanup, replay-all) are tested against
// real Postgres in internal/store (make test-docker).
type fakeWebhookStore struct {
	mu         sync.Mutex
	endpoints  map[uuid.UUID]webhook.Endpoint
	events     map[uuid.UUID]webhook.Event
	deliveries map[uuid.UUID]webhook.Delivery
	allowlist  []webhook.AllowlistEntry
	audits     []auth.AuditEntry
}

var _ webhook.Store = (*fakeWebhookStore)(nil)

func newFakeWebhookStore() *fakeWebhookStore {
	return &fakeWebhookStore{
		endpoints:  map[uuid.UUID]webhook.Endpoint{},
		events:     map[uuid.UUID]webhook.Event{},
		deliveries: map[uuid.UUID]webhook.Delivery{},
	}
}

func (f *fakeWebhookStore) CreateEndpoint(_ context.Context, e webhook.Endpoint, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpoints[e.ID] = e
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeWebhookStore) Endpoint(_ context.Context, tenant, id uuid.UUID) (webhook.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.endpoints[id]
	if !ok || e.TenantID != tenant {
		return webhook.Endpoint{}, webhook.ErrNotFound
	}
	return e, nil
}

func (f *fakeWebhookStore) ListEndpoints(_ context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]webhook.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []webhook.Endpoint{}
	for _, e := range f.endpoints {
		if e.TenantID == tenant && (before == nil || compareUUID(e.ID, *before) < 0) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b webhook.Endpoint) int { return -compareUUID(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeWebhookStore) UpdateEndpoint(_ context.Context, e webhook.Endpoint, a auth.AuditEntry) (webhook.Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.endpoints[e.ID]
	if !ok || cur.TenantID != e.TenantID {
		return webhook.Endpoint{}, webhook.ErrNotFound
	}
	if cur.Version != e.Version {
		return webhook.Endpoint{}, webhook.ErrVersionChanged
	}
	e.Version++
	f.endpoints[e.ID] = e
	f.audits = append(f.audits, a)
	return e, nil
}

func (f *fakeWebhookStore) DeleteEndpoint(_ context.Context, tenant, id uuid.UUID, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e, ok := f.endpoints[id]; !ok || e.TenantID != tenant {
		return webhook.ErrNotFound
	}
	delete(f.endpoints, id)
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeWebhookStore) CreateTestDelivery(_ context.Context, ev webhook.Event, d webhook.Delivery, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[ev.ID] = ev
	f.deliveries[d.ID] = d
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeWebhookStore) Delivery(_ context.Context, tenant, id uuid.UUID) (webhook.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deliveries[id]
	if !ok || d.TenantID != tenant {
		return webhook.Delivery{}, webhook.ErrNotFound
	}
	d.Log = slices.Clone(d.Log)
	if d.Log == nil {
		d.Log = []webhook.Attempt{}
	}
	return d, nil
}

func (f *fakeWebhookStore) ListDeliveries(_ context.Context, tenant, endpoint uuid.UUID, status string, before *uuid.UUID, limit int) ([]webhook.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []webhook.Delivery{}
	for _, d := range f.deliveries {
		if d.TenantID == tenant && d.EndpointID == endpoint && (status == "" || d.Status == status) &&
			(before == nil || compareUUID(d.ID, *before) < 0) {
			d.Log = nil
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b webhook.Delivery) int { return -compareUUID(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeWebhookStore) ReplayDelivery(_ context.Context, _ webhook.Delivery, d webhook.Delivery, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries[d.ID] = d
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeWebhookStore) ReplayFailed(context.Context, webhook.Endpoint, time.Time, time.Time, int, int, auth.AuditEntry) (int, error) {
	return 0, nil
}

func (f *fakeWebhookStore) DispatchEvents(context.Context, time.Time, int, int) (int, error) {
	return 0, nil
}

func (f *fakeWebhookStore) ClaimDeliveries(context.Context, time.Time, time.Time, int) ([]webhook.Job, error) {
	return nil, nil
}

func (f *fakeWebhookStore) RecordAttempt(_ context.Context, o webhook.Outcome, _ time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.deliveries[o.DeliveryID]
	if !ok {
		return "", nil
	}
	d.Log = append(d.Log, o.Attempt)
	d.Attempts, d.NextAttemptAt = o.Attempts, o.NextAttemptAt
	if d.Status == webhook.StatusPending {
		d.Status = o.Status
	}
	if o.Status != webhook.StatusPending {
		d.FinishedAt = &o.Attempt.At
	}
	f.deliveries[d.ID] = d
	return "", nil
}

func (f *fakeWebhookStore) Cleanup(context.Context, time.Time, time.Time) error { return nil }

func (f *fakeWebhookStore) ListAllowlist(context.Context) ([]webhook.AllowlistEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.allowlist), nil
}

func (f *fakeWebhookStore) CreateAllowlistEntry(_ context.Context, e webhook.AllowlistEntry, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.allowlist {
		if (x.CIDR != nil && e.CIDR != nil && *x.CIDR == *e.CIDR) || (x.Host != nil && e.Host != nil && *x.Host == *e.Host) {
			return webhook.ErrDuplicate
		}
	}
	f.allowlist = append(f.allowlist, e)
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeWebhookStore) DeleteAllowlistEntry(_ context.Context, id uuid.UUID, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := slices.IndexFunc(f.allowlist, func(e webhook.AllowlistEntry) bool { return e.ID == id })
	if i < 0 {
		return webhook.ErrNotFound
	}
	f.allowlist = slices.Delete(f.allowlist, i, i+1)
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeWebhookStore) auditActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.audits))
	for _, a := range f.audits {
		out = append(out, a.Action)
	}
	return out
}
