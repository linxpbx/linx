package main

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/alert"
	"linxpbx.com/linx/internal/auth"
)

// fakeAlertStore is an in-memory alert.Store for the API tests. The
// worker queries (scan, claim, flush, cleanup) are tested against real
// Postgres in internal/store (make test-docker).
type fakeAlertStore struct {
	mu         sync.Mutex
	channels   map[uuid.UUID]alert.Channel
	alerts     map[uuid.UUID]alert.Alert
	deliveries map[uuid.UUID]alert.Delivery
	audits     []auth.AuditEntry
	events     []struct {
		tenant uuid.UUID
		typ    string
		data   map[string]any
	}
}

var _ alert.Store = (*fakeAlertStore)(nil)

func newFakeAlertStore() *fakeAlertStore {
	return &fakeAlertStore{
		channels:   map[uuid.UUID]alert.Channel{},
		alerts:     map[uuid.UUID]alert.Alert{},
		deliveries: map[uuid.UUID]alert.Delivery{},
	}
}

func (f *fakeAlertStore) CreateChannel(_ context.Context, c alert.Channel, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[c.ID] = c
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeAlertStore) Channel(_ context.Context, tenant, id uuid.UUID) (alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.channels[id]
	if !ok || c.TenantID != tenant {
		return alert.Channel{}, alert.ErrNotFound
	}
	return c, nil
}

func (f *fakeAlertStore) ListChannels(_ context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Channel{}
	for _, c := range f.channels {
		if c.TenantID == tenant && (before == nil || compareUUID(c.ID, *before) < 0) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b alert.Channel) int { return -compareUUID(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeAlertStore) EnabledChannels(_ context.Context, tenant uuid.UUID) ([]alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Channel{}
	for _, c := range f.channels {
		if c.TenantID == tenant && c.Enabled {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *fakeAlertStore) UpdateChannel(_ context.Context, c alert.Channel, a auth.AuditEntry) (alert.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.channels[c.ID]
	if !ok || cur.TenantID != c.TenantID {
		return alert.Channel{}, alert.ErrNotFound
	}
	if cur.Version != c.Version {
		return alert.Channel{}, alert.ErrVersionChanged
	}
	c.Version++
	f.channels[c.ID] = c
	f.audits = append(f.audits, a)
	return c, nil
}

func (f *fakeAlertStore) DeleteChannel(_ context.Context, tenant, id uuid.UUID, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.channels[id]; !ok || c.TenantID != tenant {
		return alert.ErrNotFound
	}
	delete(f.channels, id)
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakeAlertStore) Fire(_ context.Context, tenant uuid.UUID, key, severity, title, message, link string, now time.Time) (alert.Alert, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.alerts {
		if a.TenantID == tenant && a.Key == key && a.Status == alert.StatusOpen {
			a.LastSeenAt, a.Severity, a.Title, a.Message, a.Link = now, severity, title, message, link
			f.alerts[i] = a
			return a, false, nil
		}
	}
	id := uuid.Must(uuid.NewV7())
	a := alert.Alert{ID: id, TenantID: tenant, Key: key, Severity: severity, Title: title, Message: message, Link: link,
		Status: alert.StatusOpen, FirstSeenAt: now, LastSeenAt: now, StableSince: now}
	f.alerts[id] = a
	return a, true, nil
}

func (f *fakeAlertStore) Resolve(_ context.Context, tenant uuid.UUID, key string, now time.Time) (alert.Alert, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.alerts {
		if a.TenantID == tenant && a.Key == key && a.Status == alert.StatusOpen {
			a.Status, a.ResolvedAt = alert.StatusResolved, &now
			f.alerts[i] = a
			return a, true, nil
		}
	}
	return alert.Alert{}, false, nil
}

func (f *fakeAlertStore) ListAlerts(_ context.Context, tenant uuid.UUID, status string, before *uuid.UUID, limit int) ([]alert.Alert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []alert.Alert{}
	for _, a := range f.alerts {
		if a.TenantID == tenant && (status == "" || a.Status == status) && (before == nil || compareUUID(a.ID, *before) < 0) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b alert.Alert) int { return -compareUUID(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeAlertStore) DueToNotify(context.Context, time.Time, int) ([]alert.Alert, error) {
	return nil, nil
}
func (f *fakeAlertStore) DueForReminder(context.Context, time.Time, int) ([]alert.Alert, error) {
	return nil, nil
}
func (f *fakeAlertStore) DueForResolvedNotice(context.Context, int) ([]alert.Alert, error) {
	return nil, nil
}

func (f *fakeAlertStore) Notify(_ context.Context, a alert.Alert, kind string, due, held []uuid.UUID, now time.Time, maxAttempts int, ev *alert.WebhookEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ev != nil {
		f.events = append(f.events, struct {
			tenant uuid.UUID
			typ    string
			data   map[string]any
		}{a.TenantID, ev.Type, ev.Data})
	}
	return nil
}

func (f *fakeAlertStore) HeldChannels(context.Context) ([]alert.Channel, error)      { return nil, nil }
func (f *fakeAlertStore) FlushHeld(context.Context, uuid.UUID, time.Time, int) error { return nil }
func (f *fakeAlertStore) ClaimAlertDeliveries(context.Context, time.Time, time.Time, int) ([]alert.Job, error) {
	return nil, nil
}
func (f *fakeAlertStore) RecordAlertAttempt(context.Context, alert.Outcome) error { return nil }
func (f *fakeAlertStore) CleanupAlerts(context.Context, time.Time) error          { return nil }

func (f *fakeAlertStore) auditActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.audits))
	for _, a := range f.audits {
		out = append(out, a.Action)
	}
	return out
}
