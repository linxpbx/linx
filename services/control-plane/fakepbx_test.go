package main

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"linxpbx.com/linx/internal/auth"
	"linxpbx.com/linx/internal/numbering"
	"linxpbx.com/linx/internal/pbx"
)

// fakePbxStore is an in-memory pbx.Store for the API tests. Real Postgres
// behaviour (optimistic concurrency, the extension-delete transaction,
// events) is tested in internal/store (make test-docker).
type fakePbxStore struct {
	mu         sync.Mutex
	extensions map[uuid.UUID]pbx.Extension
	devices    map[uuid.UUID]pbx.Device
	audits     []auth.AuditEntry
	// sessionLive stands in for migration 0013's device_live view: which
	// sessions' web devices are still live (nil: all).
	sessionLive func(session uuid.UUID) bool
}

var _ pbx.Store = (*fakePbxStore)(nil)

func newFakePbxStore() *fakePbxStore {
	return &fakePbxStore{extensions: map[uuid.UUID]pbx.Extension{}, devices: map[uuid.UUID]pbx.Device{}}
}

// reservedNumber stands in for migration 0016's extension-number check,
// for two of the UAE's cases.
func reservedNumber(number string) error {
	switch {
	case strings.HasPrefix(number, "0"):
		return &pbx.ReservedNumberError{Number: number, Reason: numbering.ClashNationalPrefix, Country: "AE"}
	case number == "999":
		return &pbx.ReservedNumberError{Number: number, Reason: numbering.ClashEmergency, Country: "AE"}
	}
	return nil
}

func (f *fakePbxStore) CreateExtension(_ context.Context, e pbx.Extension, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := reservedNumber(e.Number); err != nil {
		return err
	}
	for _, x := range f.extensions {
		if x.TenantID == e.TenantID && x.Number == e.Number && x.DeletedAt == nil {
			return pbx.ErrDuplicate
		}
	}
	f.extensions[e.ID] = e
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakePbxStore) Extension(_ context.Context, tenant, id uuid.UUID) (pbx.Extension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.extensions[id]
	if !ok || e.TenantID != tenant || e.DeletedAt != nil {
		return pbx.Extension{}, pbx.ErrNotFound
	}
	return e, nil
}

func (f *fakePbxStore) ListExtensions(_ context.Context, tenant uuid.UUID, before *uuid.UUID, limit int) ([]pbx.Extension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []pbx.Extension{}
	for _, e := range f.extensions {
		if e.TenantID == tenant && e.DeletedAt == nil && (before == nil || compareUUID(e.ID, *before) < 0) {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b pbx.Extension) int { return -compareUUID(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakePbxStore) UpdateExtension(_ context.Context, e pbx.Extension, a auth.AuditEntry) (pbx.Extension, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.extensions[e.ID]
	if !ok || cur.TenantID != e.TenantID || cur.DeletedAt != nil {
		return pbx.Extension{}, pbx.ErrNotFound
	}
	if cur.Version != e.Version {
		return pbx.Extension{}, pbx.ErrVersionChanged
	}
	if cur.Number != e.Number {
		if err := reservedNumber(e.Number); err != nil {
			return pbx.Extension{}, err
		}
	}
	for _, x := range f.extensions {
		if x.ID != e.ID && x.TenantID == e.TenantID && x.Number == e.Number && x.DeletedAt == nil {
			return pbx.Extension{}, pbx.ErrDuplicate
		}
	}
	e.Version++
	f.extensions[e.ID] = e
	f.audits = append(f.audits, a)
	return e, nil
}

func (f *fakePbxStore) DeleteExtension(_ context.Context, tenant, id uuid.UUID, at time.Time, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.extensions[id]
	if !ok || e.TenantID != tenant || e.DeletedAt != nil {
		return pbx.ErrNotFound
	}
	e.DeletedAt, e.Enabled, e.UpdatedAt = &at, false, at
	f.extensions[id] = e
	for did, d := range f.devices {
		if d.ExtensionID == id && d.Enabled {
			d.Enabled, d.UpdatedAt = false, at
			d.Version++
			f.devices[did] = d
		}
	}
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakePbxStore) CreateDevice(_ context.Context, d pbx.Device, a auth.AuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devices[d.ID] = d
	f.audits = append(f.audits, a)
	return nil
}

func (f *fakePbxStore) Device(_ context.Context, tenant, id uuid.UUID) (pbx.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[id]
	if !ok || d.TenantID != tenant {
		return pbx.Device{}, pbx.ErrNotFound
	}
	return d, nil
}

func (f *fakePbxStore) ListDevicesByExtension(_ context.Context, tenant, extension uuid.UUID, before *uuid.UUID, limit int) ([]pbx.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []pbx.Device{}
	for _, d := range f.devices {
		if d.TenantID == tenant && d.ExtensionID == extension && (before == nil || compareUUID(d.ID, *before) < 0) {
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b pbx.Device) int { return -compareUUID(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakePbxStore) UpdateDevice(_ context.Context, d pbx.Device, a auth.AuditEntry) (pbx.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.devices[d.ID]
	if !ok || cur.TenantID != d.TenantID {
		return pbx.Device{}, pbx.ErrNotFound
	}
	if cur.RevokedAt != nil {
		return pbx.Device{}, pbx.ErrRevoked
	}
	if cur.Version != d.Version {
		return pbx.Device{}, pbx.ErrVersionChanged
	}
	d.Version++
	f.devices[d.ID] = d
	f.audits = append(f.audits, a)
	return d, nil
}

func (f *fakePbxStore) RevokeDevice(_ context.Context, tenant, id uuid.UUID, at time.Time, a auth.AuditEntry) (pbx.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[id]
	if !ok || d.TenantID != tenant {
		return pbx.Device{}, pbx.ErrNotFound
	}
	if d.RevokedAt == nil {
		d.Enabled, d.RevokedAt, d.UpdatedAt = false, &at, at
		d.Version++
		f.devices[id] = d
	}
	f.audits = append(f.audits, a)
	return d, nil
}

func (f *fakePbxStore) IssueWebDevice(_ context.Context, d pbx.Device, digest func(string) string, a auth.AuditEntry) (pbx.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, cur := range f.devices {
		if cur.UserSessionID == nil || *cur.UserSessionID != *d.UserSessionID || cur.RevokedAt != nil {
			continue
		}
		if cur.ExtensionID == d.ExtensionID && cur.Enabled {
			cur.DigestHash, cur.UpdatedAt = digest(cur.SIPUsername), d.UpdatedAt
			cur.Version++
			f.devices[id] = cur
			f.audits = append(f.audits, a)
			return cur, nil
		}
		at := d.UpdatedAt
		cur.Enabled, cur.RevokedAt = false, &at
		f.devices[id] = cur
	}
	d.DigestHash = digest(d.SIPUsername)
	f.devices[d.ID] = d
	f.audits = append(f.audits, a)
	return d, nil
}

func (f *fakePbxStore) WebDeviceForSession(_ context.Context, session uuid.UUID) (pbx.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devices {
		if d.UserSessionID != nil && *d.UserSessionID == session && d.RevokedAt == nil {
			return d, nil
		}
	}
	return pbx.Device{}, pbx.ErrNotFound
}

func (f *fakePbxStore) RevokeDeadWebDevices(_ context.Context, at time.Time) ([]pbx.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pbx.Device
	for id, d := range f.devices {
		if d.Kind != pbx.KindWeb || d.RevokedAt != nil || f.sessionLive == nil || f.sessionLive(*d.UserSessionID) {
			continue
		}
		d.Enabled, d.RevokedAt = false, &at
		d.Version++
		f.devices[id] = d
		out = append(out, d)
	}
	return out, nil
}

func (f *fakePbxStore) auditActions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.audits))
	for _, a := range f.audits {
		out = append(out, a.Action)
	}
	return out
}
